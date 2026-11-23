package apimgr

// Tests for the admin-side per-user model-prefs surface
// (GET/PUT /api/users/{id}/model-prefs). The contract it exists to prove is
// SAME-STORAGE: an admin write lands on the very me.<uid>.{default_model,
// hidden_models} rows handleMePut/handleMeGet use, so the target user's own
// GET /api/me/settings reflects it with no extra plumbing (and the user's own
// writes overwrite the admin's just as plainly — there is no lock).
//
// Reuses meSettingsEnvT + do/doAs/rawRow/decodeMap/decodeAnyMap from
// admin_mesettings_test.go and meModelsBody from admin_memodels_test.go
// (same package); lives in its own file for ownership boundaries.

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"chatgo/internal/auth"
	"chatgo/internal/config"
	"chatgo/internal/store"
)

// newUserModelsEnv mirrors newMeSettingsEnv but mounts the model-prefs admin
// routes beside the me-settings routes — both surfaces on one mux is exactly
// what the same-storage property test needs.
func newUserModelsEnv(t *testing.T) *meSettingsEnvT {
	t.Helper()
	name := "file:mem_usermodels_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
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

	admin := &store.User{Email: "prefs-admin@example.com", Name: "A", Role: store.RoleAdmin}
	if err := s.Users.Insert(context.Background(), admin); err != nil {
		t.Fatal(err)
	}
	sess, err := a.CreateSession(context.Background(), admin.ID, "test", "192.0.2.9")
	if err != nil {
		t.Fatal(err)
	}

	d := &Deps{Cfg: cfg, Store: s, Auth: a}
	hnd := &adminH{d: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/me/settings", hnd.handleMeGet)
	mux.HandleFunc("PUT /api/me/settings", hnd.handleMePut)
	mux.HandleFunc("GET /api/users/{id}/model-prefs", hnd.handleUserModelsGet)
	mux.HandleFunc("PUT /api/users/{id}/model-prefs", hnd.handleUserModelsPut)
	return &meSettingsEnvT{D: d, handler: a.Require(mux), cookie: sess.Token, userID: admin.ID}
}

// addPlainUserWithSession inserts a target user and returns it with its
// session token (its cookie for doAs).
func addPlainUserWithSession(t *testing.T, env *meSettingsEnvT, email string) (*store.User, string) {
	t.Helper()
	u := &store.User{Email: email, Name: "T", Role: store.RoleUser}
	if err := env.D.Store.Users.Insert(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	sess, err := env.D.Auth.CreateSession(context.Background(), u.ID, "test", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	return u, sess.Token
}

// KEY PROPERTY — the whole feature stands on this: an admin PUT to
// /api/users/{id}/model-prefs writes the same rows the user's own surface
// reads, so the target's GET /api/me/settings answers the admin's values
// byte-exact (and the user overwriting them via PUT /api/me/settings is
// visible to the admin GET just as plainly — last writer wins, no lock).
func TestAdminUserModelsAdminWriteVisibleToUserMeGet(t *testing.T) {
	env := newUserModelsEnv(t)
	target, targetCookie := addPlainUserWithSession(t, env, "target@example.com")

	body := meModelsBody(t, map[string]string{
		"default_model": "ollama:llama3",
		"hidden_models": `["openai:gpt-4","anthropic:claude"]`,
	})
	rec := env.do(t, http.MethodPut, "/api/users/"+target.ID+"/model-prefs", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin PUT = %d, want 200 (body %q)", rec.Code, rec.Body)
	}
	saved := decodeMap(t, rec)
	if saved["default_model"] != "ollama:llama3" || saved["hidden_models"] != `["openai:gpt-4","anthropic:claude"]` {
		t.Fatalf("PUT response = %v, want both values echoed exactly", saved)
	}

	// Stored under the very me.<uid>.<key> rows the user surface owns.
	if v, ok := env.rawRow(t, "me."+target.ID+".default_model"); !ok || v != "ollama:llama3" {
		t.Fatalf("stored default_model = %q (ok %v)", v, ok)
	}
	if v, ok := env.rawRow(t, "me."+target.ID+".hidden_models"); !ok || v != `["openai:gpt-4","anthropic:claude"]` {
		t.Fatalf("stored hidden_models = %q (ok %v)", v, ok)
	}

	// Admin GET round-trip.
	rec = env.do(t, http.MethodGet, "/api/users/"+target.ID+"/model-prefs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET = %d (body %q)", rec.Code, rec.Body)
	}
	if got := decodeMap(t, rec); got["default_model"] != "ollama:llama3" || got["hidden_models"] != `["openai:gpt-4","anthropic:claude"]` {
		t.Fatalf("admin GET = %v, want both values byte-exact", got)
	}

	// THE property: the user's own GET /api/me/settings shows the admin's
	// write with no other plumbing (decodeAnyMap tolerates the computed
	// search_enabled boolean sibling).
	rec = env.doAs(t, targetCookie, http.MethodGet, "/api/me/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("user GET /api/me/settings = %d (body %q)", rec.Code, rec.Body)
	}
	got := decodeAnyMap(t, rec)
	if got["default_model"] != "ollama:llama3" || got["hidden_models"] != `["openai:gpt-4","anthropic:claude"]` {
		t.Fatalf("user's own me-settings = %v, want the admin's values", got)
	}

	// And back the other way: the user's own write (same validators, same
	// rows) is what the admin now reads — the admin UI must honestly say
	// "this is what they start with", not "this is locked".
	rec = env.doAs(t, targetCookie, http.MethodPut, "/api/me/settings",
		meModelsBody(t, map[string]string{"hidden_models": "[]"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("user own PUT = %d (body %q)", rec.Code, rec.Body)
	}
	rec = env.do(t, http.MethodGet, "/api/users/"+target.ID+"/model-prefs", "")
	if got := decodeMap(t, rec); got["hidden_models"] != "[]" {
		t.Fatalf("admin GET after user overwrite = %v, want hidden_models []", got)
	}

	// Audit: one user.update row, actor identity in email, target in detail.
	var action, detail, email string
	err := env.D.Store.DB.QueryRow(
		`SELECT action, detail, email FROM audit_log ORDER BY id DESC LIMIT 1`,
	).Scan(&action, &detail, &email)
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("query audit_log: %v", err)
	}
	if action != "user.update" {
		t.Fatalf("audit action = %q, want user.update", action)
	}
	if want := "prefs-admin@example.com set model prefs for target@example.com"; detail != want {
		t.Fatalf("audit detail = %q, want %q", detail, want)
	}
	if email != "prefs-admin@example.com" {
		t.Fatalf("audit email = %q, want the actor", email)
	}
}

// Non-admins are 403 on both verbs (requireAdmin), and the rejection creates
// nothing in the target's namespace.
func TestAdminUserModelsNonAdminForbidden(t *testing.T) {
	env := newUserModelsEnv(t)
	target, targetCookie := addPlainUserWithSession(t, env, "other@example.com")
	_, callerCookie := addPlainUserWithSession(t, env, "caller@example.com")

	rec := env.doAs(t, callerCookie, http.MethodGet, "/api/users/"+target.ID+"/model-prefs", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin GET = %d %q, want 403", rec.Code, rec.Body)
	}
	// Not even the target themselves: this is the ADMIN surface; users
	// manage their own prefs through /api/me/settings.
	if rec := env.doAs(t, targetCookie, http.MethodPut, "/api/users/"+target.ID+"/model-prefs",
		meModelsBody(t, map[string]string{"default_model": "self:pick"})); rec.Code != http.StatusForbidden {
		t.Fatalf("target's own admin-route PUT = %d %q, want 403", rec.Code, rec.Body)
	}
	rec = env.doAs(t, callerCookie, http.MethodPut, "/api/users/"+target.ID+"/model-prefs",
		meModelsBody(t, map[string]string{"hidden_models": `["evil:model"]`}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin PUT = %d %q, want 403", rec.Code, rec.Body)
	}
	if _, ok := env.rawRow(t, "me."+target.ID+".hidden_models"); ok {
		t.Fatal("403'd PUT still wrote a row")
	}

	// No/bogus session never reaches the handler at all: the env mounts the
	// real a.Require, so 401 comes from the middleware (the requireUser
	// guard inside stays defense-in-depth).
	if rec := env.doAs(t, "bogus", http.MethodGet, "/api/users/"+target.ID+"/model-prefs", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bogus cookie = %d %q, want 401", rec.Code, rec.Body)
	}
	if _, ok := env.rawRow(t, "me."+target.ID+".default_model"); ok {
		t.Fatal("unexpected default_model row before any admin write")
	}
}

// Unknown user id → 404 on both verbs (userLookupFail, the same bucket as
// approve/role/reset-2fa).
func TestAdminUserModelsUnknownUser(t *testing.T) {
	env := newUserModelsEnv(t)

	rec := env.do(t, http.MethodGet, "/api/users/nosuchuser/model-prefs", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "user not found") {
		t.Fatalf("GET unknown = %d %q, want 404 'user not found'", rec.Code, rec.Body)
	}
	rec = env.do(t, http.MethodPut, "/api/users/nosuchuser/model-prefs",
		meModelsBody(t, map[string]string{"default_model": ""}))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PUT unknown = %d %q, want 404", rec.Code, rec.Body)
	}
	if _, ok := env.rawRow(t, "me.nosuchuser.default_model"); ok {
		t.Fatal("PUT to an unknown user created a row")
	}
}

// Validation reuses the me-setting validators verbatim, and the admin PUT
// validates EVERY key before writing ANY: a mixed good+bad body is 400 with
// BOTH rows untouched (stricter than handleMePut's per-key interleaving, so
// the "row unchanged" guarantee holds for the whole request).
func TestAdminUserModelsInvalidKeepsRowsUnchanged(t *testing.T) {
	env := newUserModelsEnv(t)
	target, _ := addPlainUserWithSession(t, env, "strict@example.com")
	p := "/api/users/" + target.ID + "/model-prefs"

	goodDefault, goodHidden := "ollama:llama3", `["ollama:llama3"]`
	if rec := env.do(t, http.MethodPut, p,
		meModelsBody(t, map[string]string{"default_model": goodDefault, "hidden_models": goodHidden})); rec.Code != http.StatusOK {
		t.Fatalf("seed PUT = %d (body %q)", rec.Code, rec.Body)
	}

	oversize := "[" + strings.TrimSuffix(strings.Repeat(`"p:m",`, 513), ",") + "]"
	cases := []struct{ name, body string }{
		{"unknown key", `{"theme":"dark"}`},
		{"non-string value", `{"default_model":42}`},
		{"hidden not JSON", `{"hidden_models":"notjson"}`},
		{"hidden entry no colon", `{"hidden_models":"[\"nocolon\"]"}`},
		{"hidden over 512", `{"hidden_models":` + jsonQuote(oversize) + `}`},
		{"default no colon", `{"default_model":"garbage"}`},
		// The mixed batch is the interesting one: even the VALID key in a
		// rejected request must not slip through.
		{"mixed good+bad", `{"default_model":"other:model","hidden_models":"[\":x\"]"}`},
	}
	for _, c := range cases {
		rec := env.do(t, http.MethodPut, p, c.body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: PUT %s = %d %q, want 400", c.name, c.body, rec.Code, rec.Body)
		}
		if v, _ := env.rawRow(t, "me."+target.ID+".default_model"); v != goodDefault {
			t.Fatalf("%s: rejection clobbered default_model: %q", c.name, v)
		}
		if v, _ := env.rawRow(t, "me."+target.ID+".hidden_models"); v != goodHidden {
			t.Fatalf("%s: rejection clobbered hidden_models: %q", c.name, v)
		}
	}

	// Clearing paths stay available: "" and "[]" both validate.
	if rec := env.do(t, http.MethodPut, p,
		meModelsBody(t, map[string]string{"default_model": "", "hidden_models": "[]"})); rec.Code != http.StatusOK {
		t.Fatalf("clear PUT = %d (body %q)", rec.Code, rec.Body)
	}
}

// Truth-assertion (no cascade exists): the product has no user-delete
// endpoint at all, the settings table has no FK to users, and the schema only
// cascades sessions/chats/files/tokens. Deleting the user row the hard way
// therefore leaves the me.<uid>.* rows behind — orphaned KV, which this test
// pins so a future cascade (or cleanup) has to change it deliberately.
// With the user gone, the admin surface 404s as it does for any unknown id.
func TestAdminUserModelsUserDeleteLeavesOrphanRows(t *testing.T) {
	env := newUserModelsEnv(t)
	ctx := context.Background()
	target, _ := addPlainUserWithSession(t, env, "ghost@example.com")

	if rec := env.do(t, http.MethodPut, "/api/users/"+target.ID+"/model-prefs",
		meModelsBody(t, map[string]string{"hidden_models": `["ollama:llama3"]`})); rec.Code != http.StatusOK {
		t.Fatalf("seed PUT = %d (body %q)", rec.Code, rec.Body)
	}

	// Direct DELETE — deliberately bypassing the store, because the store
	// has NO delete path (verified: internal/store/users.go offers Insert/
	// GetByID/GetByEmail/UpdateRole/List/SetPassword only).
	if _, err := env.D.Store.DB.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, target.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	// Admin surface now treats them as unknown (404)…
	if rec := env.do(t, http.MethodGet, "/api/users/"+target.ID+"/model-prefs", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET deleted user = %d %q, want 404", rec.Code, rec.Body)
	}
	// …but the rows themselves survive: no cascade on the KV table.
	if v, ok := env.rawRow(t, "me."+target.ID+".hidden_models"); !ok || v != `["ollama:llama3"]` {
		t.Fatalf("hidden_models row = %q (ok %v): the truth today is that it ORPHANS", v, ok)
	}
}

// jsonQuote wraps s in a JSON string literal for embedding in a raw body.
func jsonQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
