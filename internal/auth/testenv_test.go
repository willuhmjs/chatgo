package auth_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"chatgo/internal/apimgr"
	"chatgo/internal/auth"
	"chatgo/internal/config"
	"chatgo/internal/store"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

// openTestStore opens a per-test named in-memory DB (same isolation trick as
// internal/store/store_test.go — plain ":memory:" with cache=shared is
// process-wide and would leak rows between tests).
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	name := "file:mem_auth_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared"
	st, err := store.Open(name, testSecret)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// testEnv is a live app server wired exactly like api.New's auth chain:
// registerAuth routes behind a replica of api.AuthPolicy.
type testEnv struct {
	t     *testing.T
	Cfg   *config.Config
	Store *store.Store
	Auth  *auth.Auth
	Srv   *httptest.Server
	HC    *http.Client // cookie jar, never follows redirects
}

func newTestEnv(t *testing.T, mut func(*config.Config)) *testEnv {
	t.Helper()
	st := openTestStore(t)
	cfg := &config.Config{
		Port:          "0",
		Host:          "127.0.0.1",
		SessionSecret: hex.EncodeToString(testSecret),
		Registration:  "approve",
		Version:       "test",
	}
	if mut != nil {
		mut(cfg)
	}
	a, err := auth.New(cfg, st)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}

	mux := http.NewServeMux()
	apimgr.Register(mux, &apimgr.Deps{Cfg: cfg, Store: st, Auth: a})
	// Fixtures standing in for the other route classes of api.AuthPolicy:
	mux.HandleFunc("GET /api/thing", func(w http.ResponseWriter, r *http.Request) {
		u, ok := a.UserFrom(r.Context())
		if !ok {
			http.Error(w, `{"error":"fixture: no user in ctx"}`, http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "thing:"+u.Email)
	})
	mux.HandleFunc("GET /api/admin-thing", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "admin")
	})

	srv := httptest.NewServer(testPolicy(a, mux))
	t.Cleanup(srv.Close)
	cfg.PublicURL = srv.URL

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	hc := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &testEnv{t: t, Cfg: cfg, Store: st, Auth: a, Srv: srv, HC: hc}
}

// testPolicy replicates internal/api.AuthPolicy (that package cannot be
// imported here without dragging the embedded SPA in; the route-group rules
// are exercised through this frozen copy).
func testPolicy(a *auth.Auth, next http.Handler) http.Handler {
	admin := a.RequireAdmin(next)
	authed := a.Require(next)
	optional := a.Attach(next)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/healthz", strings.HasPrefix(p, "/api/share/"), strings.HasPrefix(p, "/s/"):
			optional.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/api/auth/"):
			switch p {
			case "/api/auth/me":
				authed.ServeHTTP(w, r)
			default: // logout + login/register/config/oidc
				optional.ServeHTTP(w, r)
			}
		case strings.HasPrefix(p, "/api/providers"),
			strings.HasPrefix(p, "/api/users"),
			p == "/api/settings" || strings.HasPrefix(p, "/api/settings/"),
			p == "/api/admin-thing":
			admin.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/api/"), strings.HasPrefix(p, "/files/"), strings.HasPrefix(p, "/v1/"):
			authed.ServeHTTP(w, r)
		default:
			optional.ServeHTTP(w, r)
		}
	})
}

type response struct {
	Status   int
	Header   http.Header
	Body     []byte
	Location string
}

func containsCookie(setCookie string, want string) bool {
	return strings.Contains(setCookie, want)
}

func (r *response) json(t *testing.T, dst any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, dst); err != nil {
		t.Fatalf("decode body %q: %v", r.Body, err)
	}
}

func (r *response) errMsg() string {
	var v struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &v)
	return v.Error
}

func (e *testEnv) do(method, path string, body any) *response {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.Srv.URL+path, rdr)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.HC.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("read body: %v", err)
	}
	return &response{Status: resp.StatusCode, Header: resp.Header, Body: raw, Location: resp.Header.Get("Location")}
}

func (e *testEnv) get(path string) *response { return e.do("GET", path, nil) }

func (e *testEnv) login(email, password string) *response {
	return e.do("POST", "/api/auth/login", map[string]string{"email": email, "password": password})
}

func (e *testEnv) register(email, password, name string) *response {
	return e.do("POST", "/api/auth/register", map[string]string{"email": email, "password": password, "name": name})
}

func (e *testEnv) setCookie(name, value string) {
	e.t.Helper()
	u, err := url.Parse(e.Srv.URL + "/")
	if err != nil {
		e.t.Fatal(err)
	}
	e.HC.Jar.SetCookies(u, []*http.Cookie{{Name: name, Value: value}})
}

func (e *testEnv) seedUser(t *testing.T, email, role, password string) *store.User {
	t.Helper()
	ctx := t.Context()
	u := &store.User{Email: email, Name: "Seed " + email, Role: role}
	if password != "" {
		h, err := auth.HashPassword(password)
		if err != nil {
			t.Fatal(err)
		}
		u.PasswordHash = &h
	}
	if err := e.Store.Users.Insert(ctx, u); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	return u
}

// assertJSONErr checks status + a non-empty {"error":…} envelope.
func assertJSONErr(t *testing.T, r *response, status int) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status = %d, want %d (body %q)", r.Status, status, r.Body)
	}
	if r.errMsg() == "" {
		t.Errorf("expected {\"error\":…} envelope, got %q", r.Body)
	}
}

// seedFirstUser pre-creates an account so the registration exercised by a
// test is NOT the bootstrap first-user (the first account becomes admin —
// see TestFirstUserBecomesAdmin).
func (e *testEnv) seedFirstUser(t *testing.T) {
	t.Helper()
	if err := e.Store.Users.Insert(context.Background(), &store.User{
		Email: "seeded-first@example.com", Name: "Seeded", Role: store.RoleUser,
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}
