// HTTP-surface tests for internal/kb: collection CRUD, ownership isolation
// (two live sessions), multipart limits (413/415), embeddings failures
// (502/503), batch capping and scored search — all against a canned
// /v1/embeddings stub behind the real SSRF-guarded transport.
package kb

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgo/internal/auth"
	"chatgo/internal/config"
	"chatgo/internal/store"
)

// ---------- canned embeddings stub ----------

// testWords are the feature dimensions of the deterministic test embedding.
var testWords = []string{"golang", "pasta", "kubernetes"}

func testEmbed(text string) []float32 {
	lower := strings.ToLower(text)
	v := make([]float32, len(testWords)+1)
	for i, w := range testWords {
		v[i] = float32(strings.Count(lower, w))
	}
	v[len(v)-1] = 1 // constant bias so zero-feature texts stay comparable
	return v
}

type embedStub struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	failStatus int      // non-zero → reply with this status (embed failure tests)
	batches    []int    // len(input) per received batch
	modelsSeen []string // "model" field per batch
	authSeen   []string
}

func newEmbedStub(t *testing.T) *embedStub {
	t.Helper()
	s := &embedStub{t: t}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *embedStub) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/embeddings" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if st := s.status(); st != 0 {
		// Deliberately leaky body: the client must never echo it back.
		http.Error(w, "upstream blew up secret-key-123", st)
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	var in struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	s.record(len(in.Input), in.Model, r.Header.Get("Authorization"))
	data := make([]map[string]any, 0, len(in.Input))
	for i, txt := range in.Input {
		data = append(data, map[string]any{"index": i, "embedding": testEmbed(txt)})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (s *embedStub) status() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failStatus
}

func (s *embedStub) setFail(st int) {
	s.mu.Lock()
	s.failStatus = st
	s.mu.Unlock()
}

func (s *embedStub) record(n int, model, auth string) {
	s.mu.Lock()
	s.batches = append(s.batches, n)
	s.modelsSeen = append(s.modelsSeen, model)
	s.authSeen = append(s.authSeen, auth)
	s.mu.Unlock()
}

func (s *embedStub) snapshot() (batches []int, models []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.batches...), append([]string(nil), s.modelsSeen...)
}

// ---------- env ----------

type testEnv struct {
	t     *testing.T
	Cfg   *config.Config
	Store *store.Store
	Auth  *auth.Auth
	Srv   *httptest.Server
	Embed *embedStub
	UserA string
	UserB string
	tokA  string
	tokB  string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	name := "file:mem_kb_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared"
	st, err := store.Open(name, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	secret := hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cfg := &config.Config{SessionSecret: secret, AllowLoopback: true, DataDir: t.TempDir()}
	a, err := auth.New(cfg, st)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}

	e := &testEnv{t: t, Cfg: cfg, Store: st, Auth: a, Embed: newEmbedStub(t)}
	e.UserA = e.seedUser("a@x.dev")
	e.UserB = e.seedUser("b@x.dev")
	e.tokA = e.seedSession(e.UserA, "tok-a-"+t.Name())
	e.tokB = e.seedSession(e.UserB, "tok-b-"+t.Name())

	// Provider offering an embed-named model, pointed at the stub.
	if err := st.Providers.Insert(ctx, &store.Provider{
		ID: "p-embed", Name: "stub", BaseURL: e.Embed.srv.URL + "/v1", APIKey: "sk-stub", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Models.ReplaceForProvider(ctx, "p-embed", []store.ModelRow{
		{ModelID: "gpt-4o", Enabled: true},
		{ModelID: "text-embedding-3-small", Display: "Embed 3 Small", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	Register(mux, &Deps{Cfg: cfg, Store: st, Auth: a})
	srv := httptest.NewServer(a.Require(mux))
	t.Cleanup(srv.Close)
	e.Srv = srv
	return e
}

func (e *testEnv) seedUser(email string) string {
	e.t.Helper()
	u := &store.User{Email: email, Name: email, Role: store.RoleUser}
	if err := e.Store.Users.Insert(context.Background(), u); err != nil {
		e.t.Fatal(err)
	}
	return u.ID
}

func (e *testEnv) seedSession(userID, token string) string {
	e.t.Helper()
	now := time.Now().Unix()
	s := &store.Session{Token: token, UserID: userID, CreatedAt: now, ExpiresAt: now + 3600, LastSeenAt: now}
	if err := e.Store.Sessions.Create(context.Background(), s); err != nil {
		e.t.Fatal(err)
	}
	return token
}

// req performs one request; token=="" means anonymous.
func (e *testEnv) req(method, path, token string, body io.Reader, ctype string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.Srv.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Cookie", auth.CookieName+"="+token)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func (e *testEnv) json(method, path, token string, body any) (int, map[string]any, []byte) {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	resp := e.req(method, path, token, rdr, "application/json")
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, raw
}

// upload posts multipart files (name→content) to /api/kb/{id}/files.
func (e *testEnv) upload(token, collID string, files ...[2]string) (int, map[string]any, []byte) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range files {
		fw, err := mw.CreateFormFile("file", f[0])
		if err != nil {
			e.t.Fatal(err)
		}
		if _, err := fw.Write([]byte(f[1])); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		e.t.Fatal(err)
	}
	resp := e.req("POST", "/api/kb/"+collID+"/files", token, &buf, mw.FormDataContentType())
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, raw
}

func (e *testEnv) createCollection(token, name string) string {
	e.t.Helper()
	st, m, raw := e.json("POST", "/api/kb", token, map[string]string{"name": name})
	if st != http.StatusCreated {
		e.t.Fatalf("create: status %d body %q", st, raw)
	}
	return m["id"].(string)
}

// ---------- tests ----------

func TestCollectionCRUDAndOwnership(t *testing.T) {
	e := newTestEnv(t)

	id := e.createCollection(e.tokA, "Notes")

	// List: A sees it, B does not.
	if st, m, _ := e.json("GET", "/api/kb", e.tokA, nil); st != 200 || m["total"].(float64) != 1 {
		t.Fatalf("A list: %d %v", st, m)
	}
	if st, m, _ := e.json("GET", "/api/kb", e.tokB, nil); st != 200 || m["total"].(float64) != 0 {
		t.Fatalf("B must see zero collections: %d %v", st, m)
	}

	// B cannot read/delete A's collection (404 — no existence leak).
	if st, _, _ := e.json("GET", "/api/kb/"+id, e.tokB, nil); st != 404 {
		t.Fatalf("B GET: %d, want 404", st)
	}
	if st, _, _ := e.json("DELETE", "/api/kb/"+id, e.tokB, nil); st != 404 {
		t.Fatalf("B DELETE: %d, want 404", st)
	}
	if st, _, _ := e.json("GET", "/api/kb/"+id, e.tokA, nil); st != 200 {
		t.Fatalf("A GET after B's delete attempt: %d, want 200", st)
	}
	// B cannot ingest into or search A's collection.
	if st, _, _ := e.upload(e.tokB, id, [2]string{"x.txt", "hello"}); st != 404 {
		t.Fatalf("B upload into A's collection: %d, want 404", st)
	}
	if st, _, _ := e.json("POST", "/api/kb/"+id+"/search", e.tokB, map[string]any{"q": "x"}); st != 404 {
		t.Fatalf("B search of A's collection: %d, want 404", st)
	}
	// Anonymous → 401.
	if st, _, _ := e.json("GET", "/api/kb", "", nil); st != 401 {
		t.Fatalf("anonymous: %d, want 401", st)
	}

	// Bad name → 400.
	if st, _, _ := e.json("POST", "/api/kb", e.tokA, map[string]string{"name": "  "}); st != 400 {
		t.Fatalf("blank name: %d, want 400", st)
	}

	// A deletes → 204, then 404 on re-GET.
	if st, _, _ := e.json("DELETE", "/api/kb/"+id, e.tokA, nil); st != 204 {
		t.Fatalf("A DELETE: %d, want 204", st)
	}
	if st, _, _ := e.json("GET", "/api/kb/"+id, e.tokA, nil); st != 404 {
		t.Fatalf("GET after delete: %d, want 404", st)
	}
}

func TestUploadSearchFlow(t *testing.T) {
	e := newTestEnv(t)
	id := e.createCollection(e.tokA, "Mixed")

	st, m, raw := e.upload(e.tokA, id,
		[2]string{"notes.md", "Golang error wrapping with fmt.Errorf and %w verbs. Golang rocks."},
		[2]string{"recipe.txt", "carbonara pasta with guanciale and pecorino romano pasta"},
	)
	if st != http.StatusCreated {
		t.Fatalf("upload: %d %q", st, raw)
	}
	items := m["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	fileID := items[0].(map[string]any)["id"].(string)

	// File list + per-collection GET agree.
	st, m, _ = e.json("GET", "/api/kb/"+id+"/files", e.tokA, nil)
	if st != 200 || len(m["items"].([]any)) != 2 {
		t.Fatalf("file list: %d %v", st, m)
	}

	// Delete one file → 204 and gone from the list.
	if st, _, _ := e.json("DELETE", "/api/kb/"+id+"/files/"+fileID, e.tokA, nil); st != 204 {
		t.Fatalf("file delete: %d", st)
	}
	// Deleting a foreign file id from own collection → 404.
	if st, _, _ := e.json("DELETE", "/api/kb/"+id+"/files/nope", e.tokA, nil); st != 404 {
		t.Fatalf("file delete unknown: %d, want 404", st)
	}

	// Search: "golang" now only lives in the deleted notes.md.
	st, m, raw = e.json("POST", "/api/kb/"+id+"/search", e.tokA, map[string]any{"q": "golang", "top_k": 3})
	if st != 200 {
		t.Fatalf("search: %d %q", st, raw)
	}
	hits := m["items"].([]any)
	if len(hits) != 1 {
		t.Fatalf("hits = %d (one remaining file), want 1: %v", len(hits), hits)
	}
	hit0 := hits[0].(map[string]any)
	if hit0["file_name"] != "recipe.txt" {
		t.Fatalf("hit = %v", hit0)
	}

	// Re-verify ranking with both files in a fresh collection.
	id2 := e.createCollection(e.tokA, "Rank")
	e.upload(e.tokA, id2,
		[2]string{"a.txt", "pasta carbonara recipe"},
		[2]string{"b.txt", "golang modules and golang tooling"},
	)
	_, m, _ = e.json("POST", "/api/kb/"+id2+"/search", e.tokA, map[string]any{"q": "golang", "top_k": 1})
	hits = m["items"].([]any)
	if len(hits) != 1 || hits[0].(map[string]any)["file_name"] != "b.txt" {
		t.Fatalf("top hit = %v, want b.txt", hits)
	}
	// top_k clamping + validation.
	if st, _, _ := e.json("POST", "/api/kb/"+id2+"/search", e.tokA, map[string]any{"q": ""}); st != 400 {
		t.Fatalf("empty q: %d, want 400", st)
	}
	_, m, _ = e.json("POST", "/api/kb/"+id2+"/search", e.tokA, map[string]any{"q": "pasta", "top_k": 999})
	if _, ok := m["items"]; !ok {
		t.Fatalf("clamped search shape: %v", m)
	}
}

func TestUploadLimitsAndRejections(t *testing.T) {
	e := newTestEnv(t)
	id := e.createCollection(e.tokA, "Limits")

	// >5 MiB text → 413.
	big := strings.Repeat("word ", 6<<20/5+1) // ~6 MiB
	if st, _, _ := e.upload(e.tokA, id, [2]string{"big.txt", big}); st != 413 {
		t.Fatalf("6MiB upload: %d, want 413", st)
	}
	// Disallowed extension → 415.
	if st, _, raw := e.upload(e.tokA, id, [2]string{"evil.zip", "hello"}); st != 415 {
		t.Fatalf(".zip: %d %q, want 415", st, raw)
	}
	// Text extension but binary bytes → 415 (gzip magic + NULs).
	gz := "\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\xff\x00binary\x00junk\x00" + strings.Repeat("\x00", 40)
	if st, _, raw := e.upload(e.tokA, id, [2]string{"binary.txt", gz}); st != 415 {
		t.Fatalf("binary .txt: %d %q, want 415", st, raw)
	}
	// Valid UTF-8 text with high bytes still accepted.
	if st, _, raw := e.upload(e.tokA, id, [2]string{"umlaut.md", "Grünstraße — ouch über"}); st != 201 {
		t.Fatalf("utf8 text: %d %q, want 201", st, raw)
	}
}

func TestEmbedFailureSurfaces502(t *testing.T) {
	e := newTestEnv(t)
	id := e.createCollection(e.tokA, "Boom")
	e.Embed.setFail(http.StatusInternalServerError)

	st, m, raw := e.upload(e.tokA, id, [2]string{"doc.md", "golang notes"})
	if st != 502 {
		t.Fatalf("embed 500: status %d body %q, want 502", st, raw)
	}
	msg, _ := m["error"].(string)
	if msg == "" {
		t.Fatalf("missing error envelope: %q", raw)
	}
	for _, leak := range []string{"secret-key", "127.0.0.1", "sk-stub", "localhost"} {
		if strings.Contains(strings.ToLower(msg), leak) {
			t.Fatalf("error message leaks %q: %q", leak, msg)
		}
	}
	// Nothing persisted on embed failure (atomic per-file ingest).
	if _, m, _ := e.json("GET", "/api/kb/"+id+"/files", e.tokA, nil); len(m["items"].([]any)) != 0 {
		t.Fatalf("failed ingest left rows behind: %v", m)
	}
	// Search embeds the QUERY → also 502 when the upstream is down.
	if st, _, _ := e.json("POST", "/api/kb/"+id+"/search", e.tokA, map[string]any{"q": "golang"}); st != 502 {
		t.Fatalf("search with dead embedder: %d, want 502", st)
	}
}

func TestNoEmbedModelIs503(t *testing.T) {
	e := newTestEnv(t)
	id := e.createCollection(e.tokA, "NoModel")
	ctx := context.Background()
	if err := e.Store.Providers.Delete(ctx, "p-embed"); err != nil {
		t.Fatal(err)
	}
	st, m, raw := e.upload(e.tokA, id, [2]string{"doc.md", "golang notes"})
	if st != 503 {
		t.Fatalf("no embed model: %d %q, want 503", st, raw)
	}
	if msg, _ := m["error"].(string); !strings.Contains(msg, "no embeddings model configured") {
		t.Fatalf("message = %q, want toast-worthy config guidance", msg)
	}
}

func TestEmbedBatchesCappedAt64(t *testing.T) {
	e := newTestEnv(t)
	id := e.createCollection(e.tokA, "Bulk")
	// 70 full chunks ⇒ 70*800 − 69*80 = 50,480 words (≈300 KB, under the cap).
	var sb strings.Builder
	for i := 0; i < 70*800-69*80; i++ {
		fmt.Fprintf(&sb, "w%d ", i)
	}
	st, _, raw := e.upload(e.tokA, id, [2]string{"bulk.txt", sb.String()})
	if st != http.StatusCreated {
		t.Fatalf("bulk upload: %d %q", st, raw)
	}
	batches, models := e.Embed.snapshot()
	want := []int{64, 6}
	if len(batches) != len(want) {
		t.Fatalf("batches = %v, want %v", batches, want)
	}
	for i := range want {
		if batches[i] != want[i] {
			t.Fatalf("batch %d = %d, want %d (%v)", i, batches[i], want[i], batches)
		}
	}
	for _, mdl := range models {
		if mdl != "text-embedding-3-small" {
			t.Fatalf("embed model used = %q", mdl)
		}
	}
}

func TestEmbedModelSelection(t *testing.T) {
	ctx := context.Background()
	name := "file:mem_kb_sel_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	st, err := store.Open(name, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	d := &Deps{Cfg: &config.Config{TaskModel: "task-p:chat-model"}, Store: st}

	// Nothing configured → ErrNoEmbedModel.
	if _, err := d.pickEmbedModel(ctx); err == nil || err != ErrNoEmbedModel {
		t.Fatalf("empty: %v, want ErrNoEmbedModel", err)
	}

	insert := func(id string, sort int) {
		if err := st.Providers.Insert(ctx, &store.Provider{ID: id, Name: id, BaseURL: "http://127.0.0.1:1/v1", Enabled: true, Sort: sort}); err != nil {
			t.Fatal(err)
		}
	}
	insert("task-p", 5)
	insert("early-p", 1)
	if err := st.Models.ReplaceForProvider(ctx, "early-p", []store.ModelRow{{ModelID: "early-embed-v1", Enabled: true}}); err != nil {
		t.Fatal(err)
	}

	// Task provider has no embed model → falls to first (sort order) provider.
	got, err := d.pickEmbedModel(ctx)
	if err != nil || got.prov.ID != "early-p" || got.model != "early-embed-v1" {
		t.Fatalf("fallback: %+v %v", got, err)
	}

	// Syncing an embed model onto the task provider flips priority to it.
	if err := st.Models.ReplaceForProvider(ctx, "task-p", []store.ModelRow{
		{ModelID: "gpt-4o", Enabled: true},
		{ModelID: "bge-embed-base", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	got, err = d.pickEmbedModel(ctx)
	if err != nil || got.prov.ID != "task-p" || got.model != "bge-embed-base" {
		t.Fatalf("task priority: %+v %v", got, err)
	}

	// Explicit setting override wins even for a non-"embed" model id.
	if err := st.Models.ReplaceForProvider(ctx, "early-p", []store.ModelRow{{ModelID: "m3-vectors", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Settings.Set(ctx, settingEmbedModel, "early-p:m3-vectors"); err != nil {
		t.Fatal(err)
	}
	got, err = d.pickEmbedModel(ctx)
	if err != nil || got.prov.ID != "early-p" || got.model != "m3-vectors" {
		t.Fatalf("override: %+v %v", got, err)
	}

	// Disabled providers never qualify — even via the explicit override:
	// early-p goes dark and priority falls back to the task provider.
	row, err := st.Providers.Get(ctx, "early-p")
	if err != nil {
		t.Fatal(err)
	}
	row.Enabled = false
	if err := st.Providers.Update(ctx, row); err != nil {
		t.Fatal(err)
	}
	got, err = d.pickEmbedModel(ctx)
	if err != nil || got.prov.ID != "task-p" || got.model != "bge-embed-base" {
		t.Fatalf("override on disabled provider: %+v %v, want task-p fallback", got, err)
	}
	row, err = st.Providers.Get(ctx, "task-p")
	if err != nil {
		t.Fatal(err)
	}
	row.Enabled = false
	if err := st.Providers.Update(ctx, row); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pickEmbedModel(ctx); err != ErrNoEmbedModel {
		t.Fatalf("all-disabled: %v, want ErrNoEmbedModel", err)
	}
}

// SECREV4 M3b (kb half): a rotated SESSION_SECRET leaves stored provider keys
// undecryptable. The scan paths skip such rows like disabled ones (an empty
// bearer is worse than no provider), and an explicit kb_embed_model override
// answers the honest 503 instead of dialing unauthenticated — there is no
// fallback for an override, the vector space pins the model.
func TestEmbedSkipsUnreadableKeys(t *testing.T) {
	ctx := context.Background()
	name := "file:mem_kb_unreadable_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	st, err := store.Open(name, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.Providers.Insert(ctx, &store.Provider{ID: "kb-p", Name: "kb-p",
		BaseURL: "http://127.0.0.1:1/v1", APIKey: "sk-kb", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.Models.ReplaceForProvider(ctx, "kb-p",
		[]store.ModelRow{{ModelID: "bge-embed-base", Enabled: true}}); err != nil {
		t.Fatal(err)
	}

	// Foreign-secret handle on the SAME DB: provider rows read unreadable.
	st2, err := store.Open(name, []byte("ffffffffffffffffffffffffffffffff"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	d := &Deps{Cfg: &config.Config{}, Store: st2}

	if _, err := d.pickEmbedModel(ctx); err != ErrNoEmbedModel {
		t.Fatalf("scan path: %v, want ErrNoEmbedModel (unreadable row skipped)", err)
	}

	if err := st2.Settings.Set(ctx, settingEmbedModel, "kb-p:bge-embed-base"); err != nil {
		t.Fatal(err)
	}
	_, err = d.pickEmbedModel(ctx)
	var he httpError
	if !errors.As(err, &he) || he.status != http.StatusServiceUnavailable ||
		!strings.Contains(he.msg, "can't be decrypted") {
		t.Fatalf("override path: %v, want honest 503 key-unreadable error", err)
	}
}

// ---------- spend rate limits (SECREV-V2 L7-M1) ----------

// TestSpendRateLimitsPerUser: upload (5/min), search (20/min) and collection
// create (10/min) are per-user token buckets on the shared auth limiter;
// overflow answers the house 429 envelope WITHOUT reaching the embeddings
// provider, and other users' buckets are untouched.
func TestSpendRateLimitsPerUser(t *testing.T) {
	e := newTestEnv(t)
	idA := e.createCollection(e.tokA, "Limits") // 1 create of A's 10/min

	// Upload: exactly uploadPerMin (5) admitted, the 6th is 429.
	for i := 0; i < uploadPerMin; i++ {
		if st, _, raw := e.upload(e.tokA, idA, [2]string{fmt.Sprintf("f%d.txt", i), "golang notes"}); st != http.StatusCreated {
			t.Fatalf("upload %d: %d %q, want 201", i+1, st, raw)
		}
	}
	batchesBefore, _ := e.Embed.snapshot()
	st, m, raw := e.upload(e.tokA, idA, [2]string{"late.txt", "golang notes"})
	if st != http.StatusTooManyRequests {
		t.Fatalf("upload %d: %d %q, want 429", uploadPerMin+1, st, raw)
	}
	if msg, _ := m["error"].(string); msg == "" {
		t.Fatalf("429 missing house envelope: %q", raw)
	}
	if batchesAfter, _ := e.Embed.snapshot(); len(batchesAfter) != len(batchesBefore) {
		t.Fatal("429'd upload still reached the embeddings provider")
	}

	// Search: exactly searchPerMin (20) admitted, then 429.
	for i := 0; i < searchPerMin; i++ {
		if st, _, raw := e.json("POST", "/api/kb/"+idA+"/search", e.tokA, map[string]any{"q": "golang"}); st != http.StatusOK {
			t.Fatalf("search %d: %d %q, want 200", i+1, st, raw)
		}
	}
	st, m, raw = e.json("POST", "/api/kb/"+idA+"/search", e.tokA, map[string]any{"q": "golang"})
	if st != http.StatusTooManyRequests {
		t.Fatalf("search %d: %d %q, want 429", searchPerMin+1, st, raw)
	}
	if msg, _ := m["error"].(string); msg == "" {
		t.Fatalf("search 429 missing house envelope: %q", raw)
	}

	// Create: A already spent 1 token, so 9 more pass and the 11th is 429.
	for i := 0; i < createPerMin-1; i++ {
		if st, _, raw := e.json("POST", "/api/kb", e.tokA, map[string]string{"name": fmt.Sprintf("c%d", i)}); st != http.StatusCreated {
			t.Fatalf("create %d: %d %q, want 201", i+2, st, raw)
		}
	}
	if st, _, raw := e.json("POST", "/api/kb", e.tokA, map[string]string{"name": "one-too-many"}); st != http.StatusTooManyRequests {
		t.Fatalf("create %d: %d %q, want 429", createPerMin+1, st, raw)
	}

	// Per-user isolation: B is untouched by A's flood.
	idB := e.createCollection(e.tokB, "B's")
	if st, _, raw := e.upload(e.tokB, idB, [2]string{"b.txt", "pasta pasta"}); st != http.StatusCreated {
		t.Fatalf("B upload after A's flood: %d %q, want 201", st, raw)
	}
	if st, _, raw := e.json("POST", "/api/kb/"+idB+"/search", e.tokB, map[string]any{"q": "pasta"}); st != http.StatusOK {
		t.Fatalf("B search after A's flood: %d %q, want 200", st, raw)
	}
}
