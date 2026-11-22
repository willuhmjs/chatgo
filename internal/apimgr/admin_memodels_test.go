package apimgr

// Tests for the per-user model-preference me-settings ("default_model" and
// "hidden_models"). Reuses newMeSettingsEnv/do/decodeMap/decodeAnyMap from
// admin_mesettings_test.go (same package) so the me-settings bucket stays one
// coherent suite; lives in its own file for ownership boundaries.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgo/internal/auth"
	"chatgo/internal/store"
)

// meModelsBody JSON-marshals the PUT body so each value lands on the wire as
// exactly one JSON string (the FE contract: hidden_models is a JSON-string
// holding a JSON array).
func meModelsBody(t *testing.T, kv map[string]string) string {
	t.Helper()
	b, err := json.Marshal(kv)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return string(b)
}

// doAs is meSettingsEnvT.do with a different session cookie.
func (e *meSettingsEnvT) doAs(t *testing.T, cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
	req.RemoteAddr = "192.0.2.9:4321"
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *meSettingsEnvT) rawRow(t *testing.T, key string) (string, bool) {
	t.Helper()
	v, err := e.D.Store.Settings.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	if v == nil {
		return "", false
	}
	return *v, true
}

const (
	meGoodDefault = "ollama:llama3"
	meGoodHidden  = `["ollama:llama3","openai:gpt-4"]`
)

// Contract round-trip: both keys PUT→GET byte-exact (PUT response echoes the
// same map GET would return), stored raw under me.<uid>.<key>, no escaping or
// reshaping — the FE JSON.parses hidden_models straight off the GET value.
// Empty string clears default_model; "[]" clears hidden_models.
func TestMeModelsDefaultAndHiddenRoundTrip(t *testing.T) {
	env := newMeSettingsEnv(t)

	body := meModelsBody(t, map[string]string{"default_model": meGoodDefault, "hidden_models": meGoodHidden})
	rec := env.do(t, http.MethodPut, "/api/me/settings", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200 (body %q)", rec.Code, rec.Body)
	}
	saved := decodeMap(t, rec)
	if saved["default_model"] != meGoodDefault || saved["hidden_models"] != meGoodHidden {
		t.Fatalf("PUT response = %v, want both values echoed exactly", saved)
	}

	if v, ok := env.rawRow(t, "me."+env.userID+".default_model"); !ok || v != meGoodDefault {
		t.Fatalf("stored default_model = %q (ok %v), want %q", v, ok, meGoodDefault)
	}
	if v, ok := env.rawRow(t, "me."+env.userID+".hidden_models"); !ok || v != meGoodHidden {
		t.Fatalf("stored hidden_models = %q (ok %v), want %q", v, ok, meGoodHidden)
	}

	rec = env.do(t, http.MethodGet, "/api/me/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d (body %q)", rec.Code, rec.Body)
	}
	got := decodeAnyMap(t, rec)
	if got["default_model"] != meGoodDefault || got["hidden_models"] != meGoodHidden {
		t.Fatalf("GET round-trip = %v, want both values byte-exact", got)
	}

	// Clearing paths the FE relies on: "" for default_model, "[]" for
	// hidden_models.
	rec = env.do(t, http.MethodPut, "/api/me/settings",
		meModelsBody(t, map[string]string{"default_model": "", "hidden_models": "[]"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT clear = %d, want 200 (body %q)", rec.Code, rec.Body)
	}
	if v, ok := env.rawRow(t, "me."+env.userID+".default_model"); !ok || v != "" {
		t.Fatalf("after clear default_model = %q (ok %v), want empty string", v, ok)
	}
	if v, ok := env.rawRow(t, "me."+env.userID+".hidden_models"); !ok || v != "[]" {
		t.Fatalf("after clear hidden_models = %q (ok %v), want []", v, ok)
	}
}

// default_model shape = validateKBEmbedModel's first-colon rule: no colon or
// an empty half is 400 (house envelope), and a rejected PUT must not clobber
// the stored row (handleMePut validates each key before its write).
func TestMeModelsDefaultModelRejects(t *testing.T) {
	env := newMeSettingsEnv(t)

	rec := env.do(t, http.MethodPut, "/api/me/settings",
		meModelsBody(t, map[string]string{"default_model": meGoodDefault}))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed PUT = %d (body %q)", rec.Code, rec.Body)
	}

	for _, bad := range []string{"no-colon", ":x", "x:"} {
		rec = env.do(t, http.MethodPut, "/api/me/settings",
			meModelsBody(t, map[string]string{"default_model": bad}))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("PUT default_model %q = %d %q, want 400", bad, rec.Code, rec.Body)
		}
	}
	if v, _ := env.rawRow(t, "me."+env.userID+".default_model"); v != meGoodDefault {
		t.Fatalf("rejected PUTs mutated the row: %q, want %q", v, meGoodDefault)
	}
}

// hidden_models must be a JSON array of "pid:mid" strings: garbage, non-string
// elements, colon-less or degenerate-half entries, JSON null, and >512 entries
// are all 400 with the row untouched. Exactly 512 entries pass (the 8KB size
// check alone would let 513 through, so this pins the entry cap as the reason).
func TestMeModelsHiddenModelsRejects(t *testing.T) {
	env := newMeSettingsEnv(t)

	seed := `["ollama:llama3"]`
	rec := env.do(t, http.MethodPut, "/api/me/settings",
		meModelsBody(t, map[string]string{"hidden_models": seed}))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed PUT = %d (body %q)", rec.Code, rec.Body)
	}

	entries := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf(`"p%d:m"`, i)
		}
		return "[" + strings.Join(parts, ",") + "]"
	}

	oversize := entries(513)
	if len(oversize) > maxSettingValue {
		t.Fatalf("test bug: 513-entry value is %d bytes, want under 8KB so the entry cap is what rejects", len(oversize))
	}

	bad := []string{"notjson", "[123]", `["nocolon"]`, `[":x"]`, "null", oversize}
	for _, v := range bad {
		rec = env.do(t, http.MethodPut, "/api/me/settings",
			meModelsBody(t, map[string]string{"hidden_models": v}))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("PUT hidden_models %.40q = %d %q, want 400", v, rec.Code, rec.Body)
		}
		if v == oversize && !strings.Contains(rec.Body.String(), "512") {
			t.Fatalf("513-entry rejection = %q, want the 512-entry cap as the reason", rec.Body)
		}
	}
	if v, _ := env.rawRow(t, "me."+env.userID+".hidden_models"); v != seed {
		t.Fatalf("rejected PUTs mutated the row: %q, want %q", v, seed)
	}

	// Boundary: exactly 512 entries is accepted.
	rec = env.do(t, http.MethodPut, "/api/me/settings",
		meModelsBody(t, map[string]string{"hidden_models": entries(512)}))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT 512 entries = %d %q, want 200", rec.Code, rec.Body)
	}
}

// The me.<uid>. namespace is derived from the session, never the request: a
// non-admin's PUT can only move its own rows — the admin's default_model (and
// every other admin key) stays untouched.
func TestMeModelsNonAdminTouchesOnlyOwnRows(t *testing.T) {
	env := newMeSettingsEnv(t)
	ctx := context.Background()

	rec := env.do(t, http.MethodPut, "/api/me/settings",
		meModelsBody(t, map[string]string{"default_model": "admin:pick"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin seed PUT = %d (body %q)", rec.Code, rec.Body)
	}

	u := &store.User{Email: "plain-models@example.com", Name: "P", Role: store.RoleUser}
	if err := env.D.Store.Users.Insert(ctx, u); err != nil {
		t.Fatal(err)
	}
	sess, err := env.D.Auth.CreateSession(ctx, u.ID, "test", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}

	rec = env.doAs(t, sess.Token, http.MethodPut, "/api/me/settings",
		meModelsBody(t, map[string]string{"default_model": "plain:pick", "hidden_models": meGoodHidden}))
	if rec.Code != http.StatusOK {
		t.Fatalf("non-admin PUT = %d, want 200 (body %q)", rec.Code, rec.Body)
	}

	if v, ok := env.rawRow(t, "me."+u.ID+".default_model"); !ok || v != "plain:pick" {
		t.Fatalf("non-admin own row = %q (ok %v), want plain:pick", v, ok)
	}
	if v, _ := env.rawRow(t, "me."+env.userID+".default_model"); v != "admin:pick" {
		t.Fatalf("non-admin PUT crossed namespaces: admin row = %q, want admin:pick", v)
	}
	if _, ok := env.rawRow(t, "me."+env.userID+".hidden_models"); ok {
		t.Fatal("non-admin PUT created a hidden_models row in the admin's namespace")
	}

	// And its GET only ever shows its own values (decodeAnyMap: GET also
	// carries the computed boolean search_enabled sibling).
	rec = env.doAs(t, sess.Token, http.MethodGet, "/api/me/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("non-admin GET = %d (body %q)", rec.Code, rec.Body)
	}
	got := decodeAnyMap(t, rec)
	if got["default_model"] != "plain:pick" || got["hidden_models"] != meGoodHidden {
		t.Fatalf("non-admin GET = %v, want only its own values", got)
	}
}
