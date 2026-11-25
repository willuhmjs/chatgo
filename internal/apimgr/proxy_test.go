package apimgr

// Security regression tests for POST /v1/chat/completions (proxy.go):
//   - role gate on BOTH auth paths (session fallback + Bearer ck_ token):
//     pending / demoted accounts must get 403, never provider access.
//   - the explicit "provider:model" form must be validated against the admin
//     models cache (enabled=1), so never-synced or admin-disabled models
//     cannot be forwarded upstream (404 / 403).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgo/internal/auth"
	"chatgo/internal/store"
)

// seedModels caches provider p's model rows (synced-cache state).
func seedModels(t *testing.T, d *Deps, p *store.Provider, rows ...store.ModelRow) {
	t.Helper()
	if err := d.Store.Models.ReplaceForProvider(context.Background(), p.ID, rows); err != nil {
		t.Fatal(err)
	}
}

// sessionRequest creates a live session for u and attaches its cookie to r,
// mirroring the real policy layer (Auth.Attach) that serves the /v1 session
// fallback path.
func sessionRequest(t *testing.T, d *Deps, r *http.Request, u *store.User) *http.Request {
	t.Helper()
	tok, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if err := d.Store.Sessions.Create(context.Background(), &store.Session{
		Token: tok, UserID: u.ID, CreatedAt: now, ExpiresAt: now + 3600, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tok})
	return r
}

// serveAttached runs the proxy handler behind Auth.Attach (session path).
func serveAttached(t *testing.T, d *Deps, h *proxyH, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	d.Auth.Attach(http.HandlerFunc(h.handle)).ServeHTTP(rec, r)
	return rec
}

func mustForbidden(t *testing.T, rec *httptest.ResponseRecorder, wantBody string, fake *fakeClient) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), wantBody) {
		t.Errorf("body = %s, want to contain %q", rec.Body, wantBody)
	}
	if fake.gotModel != "" {
		t.Errorf("request reached upstream (model=%q) despite 403", fake.gotModel)
	}
}

// 1. Pending session user via the Attach fallback path → 403, no upstream.
func TestV1ProxyPendingSessionForbidden(t *testing.T) {
	d, fake := testDeps(t)
	p := seedProvider(t, d, "openai")
	seedModels(t, d, p, store.ModelRow{ModelID: "gpt-4o", Enabled: true})
	fake.completeResp = "must not run"

	pending := mustUser(t, d.Store, "pend@example.com", store.RolePending)
	h := &proxyH{d: d}
	rec := serveAttached(t, d, h, sessionRequest(t, d,
		jsonReq(`{"model":"openai:gpt-4o","messages":[{"role":"user","content":"hi"}]}`), pending))
	mustForbidden(t, rec, "account not activated", fake)

	// An activated session user on the same path still works (no regression).
	active := mustUser(t, d.Store, "act@example.com", store.RoleUser)
	rec = serveAttached(t, d, h, sessionRequest(t, d,
		jsonReq(`{"model":"openai:gpt-4o","messages":[{"role":"user","content":"hi"}]}`), active))
	if rec.Code != http.StatusOK || fake.gotModel != "gpt-4o" {
		t.Fatalf("activated session: status=%d got=%q body=%s", rec.Code, fake.gotModel, rec.Body)
	}
}

// 2. Token whose owner was demoted after issuance → 403 (role checked live).
func TestV1ProxyDemotedTokenForbidden(t *testing.T) {
	d, fake := testDeps(t)
	p := seedProvider(t, d, "openai")
	seedModels(t, d, p, store.ModelRow{ModelID: "gpt-4o", Enabled: true})
	fake.completeResp = "must not run"

	admin := mustUser(t, d.Store, "adm@example.com", store.RoleAdmin)
	key := mintToken(t, d, admin)
	ctx := context.Background()
	if err := d.Store.Users.UpdateRole(ctx, admin.ID, store.RolePending); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h := &proxyH{d: d}
	h.handle(rec, proxyReq(`{"model":"openai:gpt-4o","messages":[{"role":"user","content":"hi"}]}`, key))
	mustForbidden(t, rec, "account not activated", fake)
}

// 3. Admin disabled the model in the cache (enabled=0) → 403, even via prov:id.
func TestV1ProxyDisabledCacheModelForbidden(t *testing.T) {
	d, fake := testDeps(t)
	p := seedProvider(t, d, "openai")
	seedModels(t, d, p,
		store.ModelRow{ModelID: "gpt-4o", Enabled: false}, // admin flipped off
		store.ModelRow{ModelID: "gpt-mini", Enabled: true})
	fake.completeResp = "ok"

	u := mustUser(t, d.Store, "dc@example.com", store.RoleUser)
	key := mintToken(t, d, u)
	rec := httptest.NewRecorder()
	(&proxyH{d: d}).handle(rec, proxyReq(
		`{"model":"openai:gpt-4o","messages":[{"role":"user","content":"hi"}]}`, key))
	mustForbidden(t, rec, "model not available", fake)

	// The enabled sibling model still resolves.
	rec = httptest.NewRecorder()
	(&proxyH{d: d}).handle(rec, proxyReq(
		`{"model":"openai:gpt-mini","messages":[{"role":"user","content":"hi"}]}`, key))
	if rec.Code != http.StatusOK || fake.gotModel != "gpt-mini" {
		t.Fatalf("enabled sibling: status=%d got=%q body=%s", rec.Code, fake.gotModel, rec.Body)
	}
}

// 4. Never-synced model / provider with empty cache / unknown provider → 404.
func TestV1ProxyNeverSyncedModelNotFound(t *testing.T) {
	d, fake := testDeps(t)
	p := seedProvider(t, d, "openai")
	seedModels(t, d, p, store.ModelRow{ModelID: "gpt-4o", Enabled: true})
	seedProvider(t, d, "emptyprov") // never synced: no rows in models cache
	fake.completeResp = "must not run"

	u := mustUser(t, d.Store, "ns@example.com", store.RoleUser)
	key := mintToken(t, d, u)
	h := &proxyH{d: d}

	for _, model := range []string{"openai:gpt-5", "emptyprov:anything", "nosuchprov:gpt-4o"} {
		rec := httptest.NewRecorder()
		h.handle(rec, proxyReq(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`, key))
		if rec.Code != http.StatusNotFound {
			t.Errorf("model %q: status = %d, want 404, body %s", model, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), `"model_not_found"`) {
			t.Errorf("model %q: want model_not_found envelope, got %s", model, rec.Body)
		}
	}
	if fake.gotModel != "" {
		t.Errorf("hidden model reached upstream (model=%q)", fake.gotModel)
	}
}

// 5. Happy path (provider enabled + exact model cached enabled=1) still 200.
func TestV1ProxyEnabledCacheModelHappy(t *testing.T) {
	d, fake := testDeps(t)
	p := seedProvider(t, d, "openai")
	seedModels(t, d, p, store.ModelRow{ModelID: "gpt-4o", Display: "GPT-4o", ContextLen: 128, Enabled: true})
	fake.completeResp = "hello!"

	u := mustUser(t, d.Store, "hp@example.com", store.RoleUser)
	key := mintToken(t, d, u)
	rec := httptest.NewRecorder()
	(&proxyH{d: d}).handle(rec, proxyReq(
		`{"model":"openai:gpt-4o","messages":[{"role":"user","content":"hi"}]}`, key))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if fake.gotModel != "gpt-4o" {
		t.Errorf("upstream model = %q, want gpt-4o", fake.gotModel)
	}
	var resp chatCompletion
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hello!" {
		t.Errorf("choices = %+v", resp.Choices)
	}
}

// 6. Per-model admin gating (models.min_role='admin') must hold on the /v1
// proxy too: a plain user's ck_ token gets 404 ("hidden", mirroring
// GET /api/models) on both the explicit and bare id, while an admin's token
// resolves normally.
func TestV1ProxyAdminOnlyModelHiddenFromUsers(t *testing.T) {
	d, fake := testDeps(t)
	p := seedProvider(t, d, "openai")
	seedModels(t, d, p,
		store.ModelRow{ModelID: "gpt-4o", Enabled: true},
		store.ModelRow{ModelID: "gpt-4o-adm", Enabled: true, MinRole: "admin"})
	fake.completeResp = "ok"

	u := mustUser(t, d.Store, "gate@example.com", store.RoleUser)
	key := mintToken(t, d, u)
	h := &proxyH{d: d}

	for _, model := range []string{"openai:gpt-4o-adm", "gpt-4o-adm"} {
		fake.gotModel = ""
		rec := httptest.NewRecorder()
		h.handle(rec, proxyReq(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`, key))
		if rec.Code != http.StatusNotFound {
			t.Errorf("model %q: status = %d, want 404, body %s", model, rec.Code, rec.Body)
		}
		if fake.gotModel != "" {
			t.Errorf("gated model %q reached upstream (model=%q)", model, fake.gotModel)
		}
	}

	// Admin's token resolves the same model.
	admin := mustUser(t, d.Store, "gateadm@example.com", store.RoleAdmin)
	akey := mintToken(t, d, admin)
	rec := httptest.NewRecorder()
	h.handle(rec, proxyReq(`{"model":"openai:gpt-4o-adm","messages":[{"role":"user","content":"hi"}]}`, akey))
	if rec.Code != http.StatusOK || fake.gotModel != "gpt-4o-adm" {
		t.Fatalf("admin gated call: status=%d got=%q body=%s", rec.Code, fake.gotModel, rec.Body)
	}
}

// idsFrom parses a GET /v1/models response into its id set.
func idsFrom(t *testing.T, body []byte) map[string]bool {
	t.Helper()
	var out struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode /v1/models: %v (%s)", err, body)
	}
	if out.Object != "list" {
		t.Errorf("object = %q, want list", out.Object)
	}
	ids := map[string]bool{}
	for _, m := range out.Data {
		if m.Object != "model" {
			t.Errorf("entry object = %q, want model", m.Object)
		}
		ids[m.ID] = true
	}
	return ids
}

// 7. GET /v1/models: anon 401; users see "prov:model" + unique bare ids of
// visible models only (admin-gated and disabled rows hidden); admins see all.
func TestV1ModelsListingScopedToRole(t *testing.T) {
	d, _ := testDeps(t)
	p := seedProvider(t, d, "openai")
	seedModels(t, d, p,
		store.ModelRow{ModelID: "gpt-4o", Enabled: true},
		store.ModelRow{ModelID: "gpt-4o-adm", Enabled: true, MinRole: "admin"},
		store.ModelRow{ModelID: "gpt-old", Enabled: false}) // admin disabled
	p2 := seedProvider(t, d, "mirror")
	seedModels(t, d, p2, store.ModelRow{ModelID: "gpt-4o", Enabled: true}) // collides bare-id
	h := &proxyH{d: d}

	// Anonymous → 401 (authenticate refuses before any listing).
	rec := httptest.NewRecorder()
	h.handleModels(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon status = %d, want 401", rec.Code)
	}

	u := mustUser(t, d.Store, "lst@example.com", store.RoleUser)
	key := mintToken(t, d, u)
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	h.handleModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("user status = %d, body %s", rec.Code, rec.Body)
	}
	ids := idsFrom(t, rec.Body.Bytes())
	for want := range map[string]bool{
		"openai:gpt-4o": true, "mirror:gpt-4o": true, // prov forms always present
	} {
		if !ids[want] {
			t.Errorf("missing %q in user listing: %s", want, rec.Body)
		}
	}
	for no := range map[string]bool{
		"gpt-4o-adm": true, "openai:gpt-4o-adm": true, // admin-gated hidden
		"gpt-old": true, "openai:gpt-old": true, // disabled hidden
		"gpt-4o": true, // bare-id ambiguous (2 providers) → hidden
	} {
		if ids[no] {
			t.Errorf("user listing leaked %q: %s", no, rec.Body)
		}
	}

	admin := mustUser(t, d.Store, "lstadm@example.com", store.RoleAdmin)
	akey := mintToken(t, d, admin)
	rec = httptest.NewRecorder()
	areq := httptest.NewRequest("GET", "/v1/models", nil)
	areq.Header.Set("Authorization", "Bearer "+akey)
	h.handleModels(rec, areq)
	ids = idsFrom(t, rec.Body.Bytes())
	if !ids["openai:gpt-4o-adm"] || !ids["gpt-4o-adm"] {
		t.Errorf("admin listing missing gated model ids: %s", rec.Body)
	}
}
