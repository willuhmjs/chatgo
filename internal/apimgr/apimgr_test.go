package apimgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgo/internal/auth"
	"chatgo/internal/config"
	"chatgo/internal/providers"
	"chatgo/internal/store"
)

// ---------- fixtures ----------

type fakeClient struct {
	modelsResp []providers.Model
	modelsErr  error

	completeResp string
	completeErr  error
	gotModel     string
	gotMsgs      []providers.ChatMessage

	chunks    []providers.Chunk
	streamErr error
	gotOpts   providers.StreamOpts
}

func (f *fakeClient) ListModels(ctx context.Context, baseURL, apiKey string) ([]providers.Model, error) {
	return f.modelsResp, f.modelsErr
}

func (f *fakeClient) Complete(ctx context.Context, p providers.Provider, model string,
	msgs []providers.ChatMessage) (string, error) {
	f.gotModel = model
	f.gotMsgs = msgs
	return f.completeResp, f.completeErr
}

func (f *fakeClient) StreamChat(ctx context.Context, p providers.Provider,
	opts providers.StreamOpts) (<-chan providers.Chunk, error) {
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	f.gotOpts = opts
	ch := make(chan providers.Chunk, len(f.chunks))
	for _, c := range f.chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

func testDeps(t *testing.T) (*Deps, *fakeClient) {
	t.Helper()
	name := "file:mem_apimgr_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	s, err := store.Open(name, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	cfg := &config.Config{Registration: "approve"}
	a, err := auth.New(cfg, s)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	fake := &fakeClient{}
	return &Deps{Cfg: cfg, Store: s, Auth: a, Providers: fake}, fake
}

func mustUser(t *testing.T, s *store.Store, email, role string) *store.User {
	t.Helper()
	u := &store.User{Email: email, Name: "T", Role: role}
	if err := s.Users.Insert(context.Background(), u); err != nil {
		t.Fatalf("Users.Insert: %v", err)
	}
	return u
}

// mintToken creates a real ck_ token for u and returns the plaintext.
func mintToken(t *testing.T, d *Deps, u *store.User) string {
	t.Helper()
	plain, err := newTokenSecret()
	if err != nil {
		t.Fatal(err)
	}
	tok := &store.APIToken{UserID: u.ID, Name: "test", KeyHash: store.HashToken(plain)}
	if err := d.Store.Tokens.Create(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	return plain
}

func jsonReq(body string) *http.Request {
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// ---------- tokens ----------

func TestTokenCreateListDelete(t *testing.T) {
	d, _ := testDeps(t)
	u := mustUser(t, d.Store, "api@example.com", store.RoleUser)
	h := &tokensH{d: d}

	rec := httptest.NewRecorder()
	h.create(rec, jsonReq(`{}`), u.ID, "ci")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", rec.Code, rec.Body)
	}
	var created tokenJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Key, "ck_") || len(created.Key) != 35 { // ck_ + 32 hex
		t.Fatalf("token = %q, want ck_+32hex", created.Key)
	}
	if strings.Contains(rec.Body.String(), "key_hash") {
		t.Error("create response leaked key_hash")
	}

	// Hash lookup resolves the owner (and stamps last_used via repo).
	lu, _, err := d.Store.Tokens.HashLookup(context.Background(), store.HashToken(created.Key))
	if err != nil || lu.ID != u.ID {
		t.Fatalf("HashLookup = %v, %v", lu, err)
	}

	// List never carries a key (nothing sensitive is even stored).
	rec = httptest.NewRecorder()
	h.list(rec, jsonReq(""), u.ID)
	if !strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "[") {
		t.Fatalf("list should be a bare array: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"key"`) || strings.Contains(rec.Body.String(), "key_hash") {
		t.Errorf("list leaked key material: %s", rec.Body)
	}

	rec = httptest.NewRecorder()
	h.del(rec, jsonReq(""), created.ID, u.ID)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.del(rec, jsonReq(""), created.ID, u.ID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("re-delete status = %d, want 404", rec.Code)
	}
}

func TestRequireAdminGuards(t *testing.T) {
	d, _ := testDeps(t)
	h := &adminH{d: d}
	rec := httptest.NewRecorder()
	h.handleGet(rec, httptest.NewRequest("GET", "/api/settings", nil)) // anonymous ctx
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /api/settings = %d, want 401", rec.Code)
	}
}

// ---------- proxy ----------

func seedProvider(t *testing.T, d *Deps, name string) *store.Provider {
	t.Helper()
	p := &store.Provider{Name: name, BaseURL: "https://up.test/v1", APIKey: "sk-x", Enabled: true}
	if err := d.Store.Providers.Insert(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func proxyReq(body, token string) *http.Request {
	r := jsonReq(body)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func TestProxyNonStreamHappy(t *testing.T) {
	d, fake := testDeps(t)
	u := mustUser(t, d.Store, "p@example.com", store.RoleUser)
	p := seedProvider(t, d, "openai")
	if err := d.Store.Models.ReplaceForProvider(context.Background(), p.ID, []store.ModelRow{{ModelID: "gpt-4o", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	key := mintToken(t, d, u)
	fake.completeResp = "hello!"

	h := &proxyH{d: d}
	rec := httptest.NewRecorder()
	h.handle(rec, proxyReq(
		`{"model":"openai:gpt-4o","messages":[{"role":"user","content":"hi"}],"temperature":0.5}`, key))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var resp chatCompletion
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.ID, "chatcmpl-") || resp.Object != "chat.completion" {
		t.Errorf("resp id/object = %q/%q", resp.ID, resp.Object)
	}
	if resp.Model != "openai:gpt-4o" {
		t.Errorf("echoed model = %q", resp.Model)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hello!" ||
		resp.Choices[0].Message.Role != "assistant" || resp.Choices[0].FinishReason != "stop" {
		t.Errorf("choices = %+v", resp.Choices)
	}
	if fake.gotModel != "gpt-4o" || len(fake.gotMsgs) != 1 || fake.gotMsgs[0].Content != "hi" {
		t.Errorf("upstream call model=%q msgs=%+v", fake.gotModel, fake.gotMsgs)
	}

	// Bad / missing keys 401 (checked inside the handler, task §5).
	rec = httptest.NewRecorder()
	h.handle(rec, proxyReq(`{"model":"openai:gpt-4o","messages":[{"role":"user","content":"x"}]}`, "ck_deadbeef"))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad token status = %d, want 401", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.handle(rec, jsonReq(`{"model":"x","messages":[{"role":"user","content":"x"}]}`))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d, want 401", rec.Code)
	}
}

func TestProxyModelResolution(t *testing.T) {
	d, fake := testDeps(t)
	u := mustUser(t, d.Store, "m@example.com", store.RoleUser)
	key := mintToken(t, d, u)
	p := seedProvider(t, d, "lora")
	ctx := context.Background()
	if err := d.Store.Models.ReplaceForProvider(ctx, p.ID,
		[]store.ModelRow{{ModelID: "solo-model", Display: "Solo", ContextLen: 128, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	fake.completeResp = "ok"
	h := &proxyH{d: d}

	// Bare unique model id resolves via the enabled-models cache.
	rec := httptest.NewRecorder()
	h.handle(rec, proxyReq(`{"model":"solo-model","messages":[{"role":"user","content":"x"}]}`, key))
	if rec.Code != http.StatusOK || fake.gotModel != "solo-model" {
		t.Fatalf("bare-id resolve: status=%d got=%q body=%s", rec.Code, fake.gotModel, rec.Body)
	}

	// Unknown model → 400 (OpenAI error envelope).
	rec = httptest.NewRecorder()
	h.handle(rec, proxyReq(`{"model":"nope","messages":[{"role":"user","content":"x"}]}`, key))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("unknown model: status=%d body=%s", rec.Code, rec.Body)
	}

	// Ambiguous bare id (second provider, same model id) → 400.
	p2 := seedProvider(t, d, "lora2")
	if err := d.Store.Models.ReplaceForProvider(ctx, p2.ID,
		[]store.ModelRow{{ModelID: "solo-model", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.handle(rec, proxyReq(`{"model":"solo-model","messages":[{"role":"user","content":"x"}]}`, key))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ambiguous") {
		t.Fatalf("ambiguous model: status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestProxyStreamChunkShape(t *testing.T) {
	d, fake := testDeps(t)
	u := mustUser(t, d.Store, "s@example.com", store.RoleUser)
	p := seedProvider(t, d, "openai")
	if err := d.Store.Models.ReplaceForProvider(context.Background(), p.ID, []store.ModelRow{{ModelID: "gpt-4o", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	key := mintToken(t, d, u)
	fake.chunks = []providers.Chunk{
		{Content: "Hel"}, {Content: "lo"},
		{FinishReason: "stop", Usage: &providers.Usage{In: 3, Out: 2}},
	}

	h := &proxyH{d: d}
	rec := httptest.NewRecorder()
	h.handle(rec, proxyReq(
		`{"model":"openai:gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`, key))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}

	body := strings.TrimSuffix(rec.Body.String(), "\n\n")
	frames := strings.Split(body, "\n\n")
	last := frames[len(frames)-1]
	if last != "data: [DONE]" {
		t.Fatalf("last frame = %q, want data: [DONE]", last)
	}
	frames = frames[:len(frames)-1]
	if len(frames) != 4 { // role + Hel + lo + finish(stop)
		t.Fatalf("frames = %d (%s)", len(frames), body)
	}
	var text strings.Builder
	var first, fin streamChunk
	for i, fr := range frames {
		if !strings.HasPrefix(fr, "data: ") || strings.HasPrefix(fr, "event:") {
			t.Fatalf("frame %d malformed: %q", i, fr) // OpenAI chunks carry NO event: field
		}
		var c streamChunk
		if err := json.Unmarshal([]byte(strings.TrimPrefix(fr, "data: ")), &c); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if c.Object != "chat.completion.chunk" {
			t.Errorf("frame %d object = %q", i, c.Object)
		}
		if len(c.Choices) != 1 {
			t.Fatalf("frame %d choices = %+v", i, c.Choices)
		}
		if i == 0 {
			first = c
			if c.Choices[0].Delta.Role != "assistant" {
				t.Errorf("first delta should announce role, got %+v", c.Choices[0].Delta)
			}
		} else {
			text.WriteString(c.Choices[0].Delta.Content)
		}
		fin = c
	}
	if first.ID != fin.ID {
		t.Error("chunk ids differ across a stream")
	}
	if text.String() != "Hello" {
		t.Errorf("assembled stream = %q", text.String())
	}
	if fin.Choices[0].FinishReason == nil || *fin.Choices[0].FinishReason != "stop" {
		t.Errorf("last frame finish = %v", fin.Choices[0].FinishReason)
	}
	if fin.Usage == nil || fin.Usage.PromptTokens != 3 || fin.Usage.CompletionTokens != 2 ||
		fin.Usage.TotalTokens != 5 {
		t.Errorf("last frame usage = %+v", fin.Usage)
	}
}

func TestProxyStreamUpstreamFail(t *testing.T) {
	d, fake := testDeps(t)
	u := mustUser(t, d.Store, "sf@example.com", store.RoleUser)
	p := seedProvider(t, d, "openai")
	if err := d.Store.Models.ReplaceForProvider(context.Background(), p.ID, []store.ModelRow{{ModelID: "gpt-4o", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	key := mintToken(t, d, u)
	fake.streamErr = fmt.Errorf("boom")

	rec := httptest.NewRecorder()
	(&proxyH{d: d}).handle(rec, proxyReq(
		`{"model":"openai:gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`, key))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("pre-stream failure must be JSON, got %q", ct)
	}
}

// ---------- provider sync ----------

func TestProviderSyncPersistsContextLen(t *testing.T) {
	d, fake := testDeps(t)
	p := seedProvider(t, d, "syncme")
	fake.modelsResp = []providers.Model{
		{ID: "gpt-x", Display: "GPT X", ContextLen: 4096},
		{ID: "gpt-y", ContextLen: 0},
	}

	rec := httptest.NewRecorder()
	h := &providersH{d: d}
	h.sync(rec, httptest.NewRequest("POST", "/api/providers/"+p.ID+"/sync", nil), p.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d body %s", rec.Code, rec.Body)
	}
	var out map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["count"] != 2 {
		t.Fatalf("count = %v", out)
	}
	rows, err := d.Store.Models.ForProvider(context.Background(), p.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ForProvider = %+v, %v", rows, err)
	}
	if rows[0].ModelID != "gpt-x" || rows[0].ContextLen != 4096 || !rows[0].Enabled {
		t.Errorf("cached row = %+v", rows[0])
	}
	if rows[1].Display != "gpt-y" { // Display defaults to model id in ReplaceForProvider
		t.Errorf("default display = %q", rows[1].Display)
	}

	// Upstream failure → 502, cache untouched.
	fake.modelsResp = nil
	fake.modelsErr = fmt.Errorf("upstream down")
	if err := d.Store.Models.ReplaceForProvider(context.Background(), p.ID,
		[]store.ModelRow{{ModelID: "gpt-x", ContextLen: 4096, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.sync(rec, httptest.NewRequest("POST", "/api/providers/"+p.ID+"/sync", nil), p.ID)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("failed sync status = %d", rec.Code)
	}
	if rows, _ := d.Store.Models.ForProvider(context.Background(), p.ID); len(rows) != 1 {
		t.Errorf("failed sync clobbered the cache: %+v", rows)
	}
}

// ---------- settings ----------

func TestSettingsAllowlistRejects(t *testing.T) {
	d, _ := testDeps(t)
	h := &adminH{d: d}
	ctx := context.Background()

	// Unknown key → 400 and NOTHING applied (atomic validate-first).
	rec := httptest.NewRecorder()
	req := jsonReq(`{"title":"ok","evil":"nope"}`)
	req.Method = "PUT"
	h.put(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown key status = %d body %s", rec.Code, rec.Body)
	}
	if v, _ := d.Store.Settings.Get(ctx, "title"); v != nil {
		t.Error("partial apply after rejected batch")
	}

	// Valid keys → 200 + persisted; oversized value → 400.
	rec = httptest.NewRecorder()
	req = jsonReq(`{"title":"My ChatGo","registration":"open","search_url":"https://sx.internal"}`)
	req.Method = "PUT"
	h.put(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid put status = %d body %s", rec.Code, rec.Body)
	}
	if v, _ := d.Store.Settings.Get(ctx, "title"); v == nil || *v != "My ChatGo" {
		t.Errorf("title not stored: %v", v)
	}
	big := `{"system_prompt":"` + strings.Repeat("x", maxSettingValue+1) + `"}`
	rec = httptest.NewRecorder()
	req = jsonReq(big)
	req.Method = "PUT"
	h.put(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversize status = %d", rec.Code)
	}
	// Bad enum value.
	rec = httptest.NewRecorder()
	req = jsonReq(`{"registration":"yolo"}`)
	req.Method = "PUT"
	h.put(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad enum status = %d", rec.Code)
	}

	// GET: key-ish rows masked, per-user "me." rows hidden.
	if err := d.Store.Settings.Set(ctx, "me.u1.theme", "dark"); err != nil {
		t.Fatal(err)
	}
	if err := d.Store.Settings.Set(ctx, "legacy_api_key", "sk-super-secret-9999"); err != nil {
		t.Fatal(err)
	}
	view, err := settingsView(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if view["legacy_api_key"] != "…9999" {
		t.Errorf("secret not masked: %q", view["legacy_api_key"])
	}
	if _, ok := view["me.u1.theme"]; ok {
		t.Error("admin settings leaked a per-user row")
	}
}

// ---------- users ----------

func TestUserApproveAndLastAdminGuard(t *testing.T) {
	d, _ := testDeps(t)
	h := &usersH{d: d}
	ctx := context.Background()
	admin := mustUser(t, d.Store, "admin@example.com", store.RoleAdmin)
	pending := mustUser(t, d.Store, "new@example.com", store.RolePending)

	// approve pending → user.
	rec := httptest.NewRecorder()
	h.approve(rec, httptest.NewRequest("POST", "/api/users/"+pending.ID+"/approve", nil), pending.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status = %d body %s", rec.Code, rec.Body)
	}
	var got userJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Role != store.RoleUser {
		t.Errorf("role after approve = %q", got.Role)
	}
	// Second approve → 409.
	rec = httptest.NewRecorder()
	h.approve(rec, httptest.NewRequest("POST", "/api/users/"+pending.ID+"/approve", nil), pending.ID)
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-approve status = %d, want 409", rec.Code)
	}

	// Last-admin guard: sole admin demotion (self or otherwise) → 400.
	rec = httptest.NewRecorder()
	h.setRole(rec, httptest.NewRequest("PUT", "/api/users/"+admin.ID+"/role", nil), admin.ID, store.RoleUser)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("last-admin demote status = %d, want 400", rec.Code)
	}
	// With a second admin, demotion is allowed.
	mustUser(t, d.Store, "admin2@example.com", store.RoleAdmin)
	rec = httptest.NewRecorder()
	h.setRole(rec, httptest.NewRequest("PUT", "/api/users/"+admin.ID+"/role", nil), admin.ID, store.RoleUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("demote with 2 admins status = %d body %s", rec.Code, rec.Body)
	}
	if u, _ := d.Store.Users.GetByID(ctx, admin.ID); u.Role != store.RoleUser {
		t.Errorf("role = %q", u.Role)
	}
	// Unknown role string is rejected by handleRole's validator; setRole core
	// trusts it. Exercise the wire path for an unknown role:
	rec = httptest.NewRecorder()
	roleReq := jsonReq(`{"role":"wizard"}`)
	roleReq.Method = "PUT"
	h.handleRole(rec, roleReq)
	// handleRole can't resolve an admin caller (no session attached in unit
	// tests) — 401 proves the guard runs before validation.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous handleRole = %d, want 401", rec.Code)
	}
}

// ---------- provider display-name cap (L8-M2) ----------

func TestProviderNameCap(t *testing.T) {
	d, _ := testDeps(t)
	h := &providersH{d: d}
	admin := mustUser(t, d.Store, "padmin@example.com", store.RoleAdmin)
	ctx := context.Background()

	body := func(m any) string {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	over := strings.Repeat("日", MaxProviderNameLen+1) // 201 multibyte runes
	okName := strings.Repeat("日", MaxProviderNameLen) // 200 multibyte runes

	// Create with an over-cap name → 400 (house-style envelope).
	rec := httptest.NewRecorder()
	h.create(rec, jsonReq(body(map[string]string{"name": over, "base_url": "https://cap.test/v1"})), admin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("over-cap create status = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("over-cap create body not an error envelope: %s", rec.Body)
	}

	// Exactly 200 multibyte runes → 201, stored intact, audit detail intact
	// (600B < the 1KB audit detail cap).
	rec = httptest.NewRecorder()
	h.create(rec, jsonReq(body(map[string]string{"name": okName, "base_url": "https://cap.test/v1"})), admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("200-rune create status = %d, want 201 (body %s)", rec.Code, rec.Body)
	}
	var created providerJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Name != okName {
		t.Errorf("stored name = %q, want the 200-rune name", created.Name)
	}
	var detail string
	if err := d.Store.DB.QueryRow(
		`SELECT detail FROM audit_log WHERE action = 'provider.create' ORDER BY id DESC LIMIT 1`).
		Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if detail != okName {
		t.Errorf("audit detail = %q, want the 200-rune name whole", detail)
	}

	// Update with an over-cap name → 400 and the stored row untouched.
	rec = httptest.NewRecorder()
	upReq := httptest.NewRequest("PUT", "/api/providers/"+created.ID, strings.NewReader(body(map[string]string{"name": over})))
	h.update(rec, upReq, created.ID, admin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("over-cap update status = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
	if p, err := d.Store.Providers.Get(ctx, created.ID); err != nil || p.Name != okName {
		t.Errorf("name after rejected update = %q, %v", p.Name, err)
	}

	// Update to a valid shorter name → 200 + persisted.
	rec = httptest.NewRecorder()
	upReq = httptest.NewRequest("PUT", "/api/providers/"+created.ID, strings.NewReader(body(map[string]string{"name": "Small"})))
	h.update(rec, upReq, created.ID, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid update status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if p, _ := d.Store.Providers.Get(ctx, created.ID); p.Name != "Small" {
		t.Errorf("name after update = %q, want Small", p.Name)
	}

	// Patch without a name keeps it (PUT partial semantics, empty name is
	// "absent" not "oversized").
	rec = httptest.NewRecorder()
	upReq = httptest.NewRequest("PUT", "/api/providers/"+created.ID, strings.NewReader(body(map[string]any{"sort": 3})))
	h.update(rec, upReq, created.ID, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("nameless patch status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if p, _ := d.Store.Providers.Get(ctx, created.ID); p.Name != "Small" {
		t.Errorf("name after nameless patch = %q, want Small", p.Name)
	}
}
