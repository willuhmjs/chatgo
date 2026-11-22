// SECREV-3 S3-4 side observation: PUT /api/me/settings {"name":…} stored the
// me-setting but GET /api/auth/me kept serving the registration name, so the
// display name reverted on every reload. handleMePut now mirrors a validated
// "name" onto the users row. Semantics chosen by reading the FE (SettingsMe
// + Sidebar displayName chain name→email→"Signed out"): the mirror is exact,
// including "" = clear — users.name is NOT NULL with an empty-string default
// so "" is the
// schema's own "no name" (SettingsMe never sends blank, but a direct caller
// clearing the setting must not leave the identity probe showing a stale
// name). A failed user-row update answers 500 (all-or-nothing), never 200.
//
// Lives in its own file (ownership boundary, like admin_mesettings_test.go)
// and builds its own env — including the GET /api/auth/me leg, which is the
// reload path the finding says was blind.
package apimgr

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

type meNameEnvT struct {
	D       *Deps
	handler http.Handler
	cookie  string
	userID  string
}

// newMeNameEnv wires PUT/GET /api/me/settings + GET /api/auth/me behind the
// real Require middleware, with a live session for a plain (non-admin) user
// whose registration name differs from anything the test saves.
func newMeNameEnv(t *testing.T) *meNameEnvT {
	t.Helper()
	name := "file:mem_mename_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
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

	u := &store.User{Email: "me-name@example.com", Name: "Reg Name", Role: store.RoleUser}
	if err := s.Users.Insert(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	sess, err := a.CreateSession(context.Background(), u.ID, "test", "192.0.2.31")
	if err != nil {
		t.Fatal(err)
	}

	d := &Deps{Cfg: cfg, Store: s, Auth: a}
	hnd := &adminH{d: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/me/settings", hnd.handleMeGet)
	mux.HandleFunc("PUT /api/me/settings", hnd.handleMePut)
	mux.HandleFunc("GET /api/auth/me", a.HandleMe)
	return &meNameEnvT{D: d, handler: a.Require(mux), cookie: sess.Token, userID: u.ID}
}

func (e *meNameEnvT) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: e.cookie})
	req.RemoteAddr = "192.0.2.31:4321"
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// authMeName runs the reload path (GET /api/auth/me through the middleware,
// which re-reads the users row per request) and returns the user's name,
// asserting the frozen /api/auth/me shape stayed intact.
func (e *meNameEnvT) authMeName(t *testing.T) string {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/api/auth/me", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/me = %d (body %q)", rec.Code, rec.Body)
	}
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	for _, k := range []string{"id", "email", "name", "role"} {
		if _, ok := view[k].(string); !ok {
			t.Fatalf("/api/auth/me lost string field %q: %s", k, rec.Body)
		}
	}
	name, _ := view["name"].(string)
	return name
}

// Core regression: PUT name → GET /api/auth/me (the reload path) shows the
// saved name, the users row carries it, and the me-setting row still round-
// trips unchanged.
func TestMePutNameOverlaysUsersRowForAuthMeReload(t *testing.T) {
	env := newMeNameEnv(t)
	ctx := context.Background()

	if got := env.authMeName(t); got != "Reg Name" {
		t.Fatalf("initial /api/auth/me name = %q, want the registration name", got)
	}
	if rec := env.do(t, http.MethodPut, "/api/me/settings", `{"name":"Ada Lovelace"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT name = %d, want 200 (body %q)", rec.Code, rec.Body)
	}
	if got := env.authMeName(t); got != "Ada Lovelace" {
		t.Fatalf("after PUT, /api/auth/me name = %q, want Ada Lovelace", got)
	}
	u, err := env.D.Store.Users.GetByID(ctx, env.userID)
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "Ada Lovelace" {
		t.Fatalf("users row name = %q, want Ada Lovelace", u.Name)
	}
	// The setting itself still round-trips (me-settings shape untouched).
	if got := decodeAnyMap(t, env.do(t, http.MethodGet, "/api/me/settings", ""))["name"]; got != "Ada Lovelace" {
		t.Fatalf("me-settings name = %v, want Ada Lovelace", got)
	}
}

// Chosen empty-string semantic (documented per the finding): an empty "name"
// setting CLEARS users.Name to "" — the schema's own NOT NULL empty-string
// default value,
// which the Sidebar displayName chain renders as the email fallback. The
// setting and the identity probe can never diverge: clearing one clears the
// other. (SettingsMe's Save trims and bails on blank, so the UI never
// triggers this leg; direct API callers get honest all-or-nothing behavior.)
func TestMePutNameEmptyClearsUsersRowToEmailFallback(t *testing.T) {
	env := newMeNameEnv(t)
	ctx := context.Background()

	if rec := env.do(t, http.MethodPut, "/api/me/settings", `{"name":"Ada Lovelace"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT name = %d (body %q)", rec.Code, rec.Body)
	}
	if rec := env.do(t, http.MethodPut, "/api/me/settings", `{"name":""}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT empty name = %d, want 200 (body %q)", rec.Code, rec.Body)
	}
	u, err := env.D.Store.Users.GetByID(ctx, env.userID)
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "" {
		t.Fatalf("users row name = %q, want cleared (schema default \"\")", u.Name)
	}
	if got := env.authMeName(t); got != "" {
		t.Fatalf("/api/auth/me name = %q, want \"\" (FE chain then shows the email)", got)
	}
}

// A rejected name write (validator 400) must not touch the users row, and
// name-less me-PUTs must leave it alone too — the overlay only rides the
// "name" key's successful write path.
func TestMePutNameRejectionAndOtherKeysLeaveUsersRowAlone(t *testing.T) {
	env := newMeNameEnv(t)
	ctx := context.Background()

	// 101 runes → 400 (validateMeName), row untouched.
	if rec := env.do(t, http.MethodPut, "/api/me/settings", `{"name":"`+strings.Repeat("é", 101)+`"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("101-rune name = %d, want 400 (body %q)", rec.Code, rec.Body)
	}
	// Wrong JSON type → 400, row untouched.
	if rec := env.do(t, http.MethodPut, "/api/me/settings", `{"name":42}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-string name = %d, want 400 (body %q)", rec.Code, rec.Body)
	}
	// Other keys never overlay: a successful theme/accent PUT leaves the
	// registration name intact.
	if rec := env.do(t, http.MethodPut, "/api/me/settings", `{"theme":"dark","accent":"#0a84ff"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT theme/accent = %d (body %q)", rec.Code, rec.Body)
	}
	u, err := env.D.Store.Users.GetByID(ctx, env.userID)
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "Reg Name" {
		t.Fatalf("users row name = %q, want the untouched registration name", u.Name)
	}

	// Multi-key batch carrying a valid name overlays like a solo PUT.
	if rec := env.do(t, http.MethodPut, "/api/me/settings", `{"theme":"light","name":"Grace H."}`); rec.Code != http.StatusOK {
		t.Fatalf("batch PUT = %d (body %q)", rec.Code, rec.Body)
	}
	if got := env.authMeName(t); got != "Grace H." {
		t.Fatalf("/api/auth/me name = %q, want Grace H.", got)
	}
}
