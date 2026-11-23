package apimgr

// Tests for GET/PUT /api/me/settings and the instance_setup_done admin key
// (onboarding wizard loop fix). Lives in its own file to keep ownership
// boundaries from colliding with apimgr_test.go, and builds its own Deps so
// it does not lean on that file's helpers either.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgo/internal/auth"
	"chatgo/internal/config"
	"chatgo/internal/store"
)

type meSettingsEnvT struct {
	D       *Deps
	handler http.Handler
	cookie  string
	userID  string
}

// newMeSettingsEnv wires the me-settings + admin settings routes behind the
// real Require middleware with a live session cookie for an admin account
// (admin so one env covers both /api/settings and /api/me/settings).
func newMeSettingsEnv(t *testing.T) *meSettingsEnvT {
	t.Helper()
	name := "file:mem_mesettings_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	s, err := store.Open(name, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	cfg := &config.Config{Registration: "open"}
	a, err := auth.New(cfg, s)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}

	u := &store.User{Email: "setup@example.com", Name: "S", Role: store.RoleAdmin}
	if err := s.Users.Insert(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	sess, err := a.CreateSession(context.Background(), u.ID, "test", "192.0.2.9")
	if err != nil {
		t.Fatal(err)
	}

	d := &Deps{Cfg: cfg, Store: s, Auth: a}
	hnd := &adminH{d: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/me/settings", hnd.handleMeGet)
	mux.HandleFunc("PUT /api/me/settings", hnd.handleMePut)
	mux.HandleFunc("GET /api/settings", hnd.handleGet)
	mux.HandleFunc("PUT /api/settings", hnd.handlePut)
	return &meSettingsEnvT{D: d, handler: a.Require(mux), cookie: sess.Token, userID: u.ID}
}

func (e *meSettingsEnvT) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: e.cookie})
	req.RemoteAddr = "192.0.2.9:4321"
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	return m
}

// decodeAnyMap decodes a GET /api/me/settings response. Unlike decodeMap it
// tolerates the computed `search_enabled` JSON boolean riding beside the
// bare-string stored prefs (a top-level sibling, not a me-setting value).
func decodeAnyMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	return m
}

// searchFlag asserts GET /api/me/settings carried search_enabled as a real
// JSON boolean (never the old "true"/"false" string) and returns it.
func searchFlag(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	v, ok := decodeAnyMap(t, rec)["search_enabled"].(bool)
	if !ok {
		t.Fatalf("search_enabled is not a JSON boolean in %s", rec.Body)
	}
	return v
}

// PUT setup_done+accent is accepted, stored namespaced as me.<uid>.<key>, and
// GET /api/me/settings strips the namespace back to bare keys (this is the
// round-trip App.svelte's nudge reads).
func TestMeSettingsSetupDoneAccentRoundTrip(t *testing.T) {
	env := newMeSettingsEnv(t)

	rec := env.do(t, http.MethodPut, "/api/me/settings",
		`{"setup_done":"true","accent":"#0a84ff","theme":"dark"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200 (body %q)", rec.Code, rec.Body)
	}
	saved := decodeMap(t, rec)
	if saved["setup_done"] != "true" || saved["accent"] != "#0a84ff" || saved["theme"] != "dark" {
		t.Fatalf("PUT response = %v, want all three keys echoed bare", saved)
	}

	// Raw rows carry the me.<uid>. namespace…
	all, err := env.D.Store.Settings.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if all["me."+env.userID+".setup_done"] != "true" || all["me."+env.userID+".accent"] != "#0a84ff" {
		t.Fatalf("raw rows not namespaced: %v", all)
	}

	// …and GET returns them stripped (decodeAnyMap: the response also carries
	// the computed boolean search_enabled sibling).
	rec = env.do(t, http.MethodGet, "/api/me/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d (body %q)", rec.Code, rec.Body)
	}
	got := decodeAnyMap(t, rec)
	if got["setup_done"] != "true" || got["accent"] != "#0a84ff" || got["theme"] != "dark" {
		t.Fatalf("GET round-trip = %v", got)
	}
	for k := range got {
		if strings.HasPrefix(k, "me.") {
			t.Errorf("GET leaked namespace prefix: %q", k)
		}
	}
	// me.* rows stay invisible to the admin listing.
	rec = env.do(t, http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET = %d (body %q)", rec.Code, rec.Body)
	}
	for k := range decodeMap(t, rec) {
		if strings.HasPrefix(k, "me.") {
			t.Errorf("admin settings leaked per-user row %q", k)
		}
	}
}

// The allowlist still bites: unknown keys and non-string values are 400, and
// the oversized-value cap is untouched.
func TestMeSettingsRejectsUnknownAndBadValues(t *testing.T) {
	env := newMeSettingsEnv(t)

	rec := env.do(t, http.MethodPut, "/api/me/settings", `{"setup_done":"true","evil":"x"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown setting key evil") {
		t.Fatalf("unknown key: %d %q, want 400 'unknown setting key evil'", rec.Code, rec.Body)
	}

	rec = env.do(t, http.MethodPut, "/api/me/settings", `{"accent":42}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "must be a string") {
		t.Fatalf("non-string: %d %q, want 400 'must be a string'", rec.Code, rec.Body)
	}

	rec = env.do(t, http.MethodPut, "/api/me/settings", `{"theme":"`+strings.Repeat("x", 9<<10)+`"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "too large") {
		t.Fatalf("oversized: %d %q, want 400 'too large'", rec.Code, rec.Body)
	}

	// The single-key rejections left nothing behind (a mixed valid+invalid
	// batch may partially persist by pre-existing design — handleMePut
	// validates per key as it writes, unrelated to the setup_done fix).
	for k := range decodeAnyMap(t, env.do(t, http.MethodGet, "/api/me/settings", "")) {
		if k == "accent" || k == "theme" {
			t.Errorf("rejected write for %q persisted anyway", k)
		}
	}
}

// The admin global allowlist accepts instance_setup_done (wizard writes it
// alongside the per-user setup_done) and still rejects unknown globals.
func TestAdminSettingsInstanceSetupDone(t *testing.T) {
	env := newMeSettingsEnv(t)

	rec := env.do(t, http.MethodPut, "/api/settings", `{"instance_setup_done":"true"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT instance_setup_done = %d, want 200 (body %q)", rec.Code, rec.Body)
	}
	if got := decodeMap(t, rec)["instance_setup_done"]; got != "true" {
		t.Fatalf("admin GET view = %q, want \"true\"", got)
	}

	rec = env.do(t, http.MethodPut, "/api/settings", `{"instance_setup_don":"true"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("typo'd key: %d %q, want 400", rec.Code, rec.Body)
	}
}

// kb_embed_model (L4-I1): the documented embeddings escape hatch must be
// writable via admin PUT /api/settings. Value shape mirrors kb.splitSpec in
// internal/kb/embed.go: "providerID:modelID" (first colon, both halves
// non-empty) or empty to clear. Non-admins keep inheriting the 403 bucket.
func TestAdminSettingsKBEmbedModel(t *testing.T) {
	env := newMeSettingsEnv(t)
	ctx := context.Background()

	// Round-trip: accepted, stored raw (no key-ish masking on this key),
	// echoed back in the admin view.
	rec := env.do(t, http.MethodPut, "/api/settings", `{"kb_embed_model":"openai:text-embedding-3-small"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT kb_embed_model = %d, want 200 (body %q)", rec.Code, rec.Body)
	}
	if got := decodeMap(t, rec)["kb_embed_model"]; got != "openai:text-embedding-3-small" {
		t.Fatalf("PUT response view = %q, want the value echoed", got)
	}
	if v, err := env.D.Store.Settings.Get(ctx, "kb_embed_model"); err != nil || v == nil || *v != "openai:text-embedding-3-small" {
		t.Fatalf("stored row = %v (err %v), want openai:text-embedding-3-small", v, err)
	}

	// Rejections: no colon, and the degenerate halves kb.splitSpec drops.
	for _, bad := range []string{"garbage no colon", ":only-model", "provider-only:"} {
		rec = env.do(t, http.MethodPut, "/api/settings", `{"kb_embed_model":"`+bad+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("PUT %q = %d %q, want 400", bad, rec.Code, rec.Body)
		}
	}
	// The rejected writes must not have clobbered the good value.
	if v, err := env.D.Store.Settings.Get(ctx, "kb_embed_model"); err != nil || v == nil || *v != "openai:text-embedding-3-small" {
		t.Fatalf("rejected PUT mutated the row: %v (err %v)", v, err)
	}

	// Empty clears (200) — kb falls back to name-heuristic auto-detection.
	rec = env.do(t, http.MethodPut, "/api/settings", `{"kb_embed_model":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT empty kb_embed_model = %d, want 200 (body %q)", rec.Code, rec.Body)
	}
	if v, err := env.D.Store.Settings.Get(ctx, "kb_embed_model"); err != nil || v == nil || *v != "" {
		t.Fatalf("after clear stored = %v (err %v), want empty string", v, err)
	}

	// Non-admin → 403 (same requireAdmin bucket as every other setting).
	u := &store.User{Email: "plain@example.com", Name: "P", Role: store.RoleUser}
	if err := env.D.Store.Users.Insert(ctx, u); err != nil {
		t.Fatal(err)
	}
	sess, err := env.D.Auth.CreateSession(ctx, u.ID, "test", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/settings",
		strings.NewReader(`{"kb_embed_model":"evil:model"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sess.Token})
	req.RemoteAddr = "192.0.2.10:4321"
	rec = httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin PUT = %d %q, want 403", rec.Code, rec.Body)
	}
	if v, err := env.D.Store.Settings.Get(ctx, "kb_embed_model"); err != nil || v == nil || *v != "" {
		t.Fatalf("non-admin PUT wrote despite 403: %v (err %v)", v, err)
	}
}

// P0-2: profile Save (SettingsMe → api-shell setMeName) PUTs the "name"
// me-setting, which used to miss the allowlist entirely and 400 every time.
// Round-trip: 200 + echoed bare, stored namespaced, GET strips the prefix,
// and empty clears (the FE never sends blank, but the key behaves like the
// others).
func TestMeSettingsNameRoundTrip(t *testing.T) {
	env := newMeSettingsEnv(t)
	ctx := context.Background()

	rec := env.do(t, http.MethodPut, "/api/me/settings", `{"name":"Ada Lovelace"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT name = %d, want 200 (body %q)", rec.Code, rec.Body)
	}
	if got := decodeMap(t, rec)["name"]; got != "Ada Lovelace" {
		t.Fatalf("PUT response = %q, want the name echoed", got)
	}
	if v, err := env.D.Store.Settings.Get(ctx, "me."+env.userID+".name"); err != nil || v == nil || *v != "Ada Lovelace" {
		t.Fatalf("stored row = %v (err %v), want Ada Lovelace", v, err)
	}

	rec = env.do(t, http.MethodGet, "/api/me/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d (body %q)", rec.Code, rec.Body)
	}
	if got := decodeAnyMap(t, rec)["name"]; got != "Ada Lovelace" {
		t.Fatalf("GET round-trip = %q, want Ada Lovelace", got)
	}

	// Empty clears (200), like default_model/kb_embed_model do.
	rec = env.do(t, http.MethodPut, "/api/me/settings", `{"name":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT empty name = %d, want 200 (body %q)", rec.Code, rec.Body)
	}
	if v, err := env.D.Store.Settings.Get(ctx, "me."+env.userID+".name"); err != nil || v == nil || *v != "" {
		t.Fatalf("after clear stored = %v (err %v), want empty string", v, err)
	}
}

// CONFIRMED-BROKEN #1 (docs/PUB-CONTRACT-SWEEP.md): GET /api/me/settings used
// to emit search_enabled as the STRING "true"/"false" inside the bare-string
// me-settings map, so the composer's boolean probe (chatview-api.ts
// probeSearchEnabled) never matched a boolean and the web-search toggle never
// appeared. It must be a real top-level JSON boolean for admins AND regular
// users — derived from the global search_url setting, or the SEARXNG_URL
// config as its other source — while stored pref values stay JSON strings.
func TestMeSettingsSearchEnabledBoolean(t *testing.T) {
	env := newMeSettingsEnv(t)
	ctx := context.Background()

	// Regular user + session — the role for which the deleted admin-settings
	// fallback (GET /api/settings is requireAdmin) could never see a key.
	plain := &store.User{Email: "plain-search@example.com", Name: "P", Role: store.RoleUser}
	if err := env.D.Store.Users.Insert(ctx, plain); err != nil {
		t.Fatal(err)
	}
	sess, err := env.D.Auth.CreateSession(ctx, plain.ID, "test", "192.0.2.11")
	if err != nil {
		t.Fatal(err)
	}

	// A stored string pref must stay a JSON string beside the bool sibling.
	if rec := env.do(t, http.MethodPut, "/api/me/settings", `{"accent":"#0a84ff"}`); rec.Code != http.StatusOK {
		t.Fatalf("seed PUT accent = %d (body %q)", rec.Code, rec.Body)
	}

	// search_url unset + SEARXNG_URL unset → boolean false for BOTH roles.
	rec := env.do(t, http.MethodGet, "/api/me/settings", "")
	if got := searchFlag(t, rec); got {
		t.Fatalf("admin search_enabled = %v, want false (search_url unset)", got)
	}
	if s, ok := decodeAnyMap(t, rec)["accent"].(string); !ok || s != "#0a84ff" {
		t.Fatalf("accent lost its string type beside search_enabled: %s", rec.Body)
	}
	rec = env.doAs(t, sess.Token, http.MethodGet, "/api/me/settings", "")
	if got := searchFlag(t, rec); got {
		t.Fatalf("user search_enabled = %v, want false (search_url unset)", got)
	}

	// Admin configures search_url → boolean true for BOTH roles.
	if rec = env.do(t, http.MethodPut, "/api/settings", `{"search_url":"https://searx.example"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT search_url = %d (body %q)", rec.Code, rec.Body)
	}
	if got := searchFlag(t, env.do(t, http.MethodGet, "/api/me/settings", "")); !got {
		t.Fatal("admin search_enabled = false after search_url was set")
	}
	if got := searchFlag(t, env.doAs(t, sess.Token, http.MethodGet, "/api/me/settings", "")); !got {
		t.Fatal("user search_enabled = false after search_url was set")
	}

	// Clearing flips it back; the SEARXNG_URL config is the other boolean source.
	if rec = env.do(t, http.MethodPut, "/api/settings", `{"search_url":""}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT empty search_url = %d (body %q)", rec.Code, rec.Body)
	}
	if got := searchFlag(t, env.doAs(t, sess.Token, http.MethodGet, "/api/me/settings", "")); got {
		t.Fatal("user search_enabled = true after search_url was cleared")
	}
	env.D.Cfg.SearXNGURL = "https://searx.env"
	t.Cleanup(func() { env.D.Cfg.SearXNGURL = "" })
	if got := searchFlag(t, env.doAs(t, sess.Token, http.MethodGet, "/api/me/settings", "")); !got {
		t.Fatal("user search_enabled = false with SEARXNG_URL config set")
	}

	// Boundary: the admin global view stays the pure map[string]string it
	// always was — search_enabled is only ever a /api/me/settings sibling,
	// never a row of GET /api/settings (decodeMap rejects bools).
	if got := decodeMap(t, env.do(t, http.MethodGet, "/api/settings", "")); got["search_url"] != "" {
		t.Fatalf("admin settings search_url = %q, want empty", got["search_url"])
	}
}

// P0-2 validator: ≤100 RUNES, not bytes — 100 two-byte chars pass while 101
// fail with the house 400 envelope, and the rejection must not clobber the
// previously good value.
func TestMeSettingsNameLengthCap(t *testing.T) {
	env := newMeSettingsEnv(t)
	ctx := context.Background()

	rec := env.do(t, http.MethodPut, "/api/me/settings", `{"name":"`+strings.Repeat("é", 100)+`"}`) // 200 bytes, 100 runes
	if rec.Code != http.StatusOK {
		t.Fatalf("100-rune (200-byte) name = %d %q, want 200", rec.Code, rec.Body)
	}

	rec = env.do(t, http.MethodPut, "/api/me/settings", `{"name":"`+strings.Repeat("é", 101)+`"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "name must be at most 100 characters") {
		t.Fatalf("101-rune name = %d %q, want 400 'name must be at most 100 characters'", rec.Code, rec.Body)
	}
	if v, err := env.D.Store.Settings.Get(ctx, "me."+env.userID+".name"); err != nil || v == nil || *v != strings.Repeat("é", 100) {
		t.Fatalf("rejected PUT mutated the row: %v (err %v), want the 100-rune value intact", v, err)
	}
}
