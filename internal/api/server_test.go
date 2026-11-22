package api

import (
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgo/internal/auth"
	"chatgo/internal/config"
	"chatgo/internal/store"
	"chatgo/internal/web"
)

// SPA shell guard (docs/AUTHGATE-CONTRACT.md §1): anonymous requests for app
// shell routes 302 to /login; assets, the auth pages and /s/{token} stay
// public; a valid session gets the shell back. The mux below replicates
// api.New's fallback wiring: AuthPolicy → "GET /" → spaHandler(auth).

type spaTestEnv struct {
	Srv   *httptest.Server
	Auth  *auth.Auth
	Store *store.Store
}

func newSPATestEnv(t *testing.T) *spaTestEnv {
	t.Helper()
	secret := []byte("0123456789abcdef0123456789abcdef")
	name := "file:mem_api_spa_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared"
	st, err := store.Open(name, secret)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		Port:          "0",
		Host:          "127.0.0.1",
		SessionSecret: hex.EncodeToString(secret),
		Registration:  "open",
		Version:       "test",
	}
	a, err := auth.New(cfg, st)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", spaHandler(a))
	srv := httptest.NewServer(AuthPolicy(a, mux)) // "/" → optional(Attach) → spaHandler
	t.Cleanup(srv.Close)
	return &spaTestEnv{Srv: srv, Auth: a, Store: st}
}

// login seeds an admin and returns its session cookie header value.
func (e *spaTestEnv) login(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	u := &store.User{Email: "owner@example.com", Name: "Owner", Role: store.RoleAdmin}
	if err := e.Store.Users.Insert(ctx, u); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	s, err := e.Auth.CreateSession(ctx, u.ID, "spa-test", "127.0.0.1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return auth.CookieName + "=" + s.Token
}

// req issues a request (redirects are never followed, mirroring the intent
// of every caller that cares about the 302).
func (e *spaTestEnv) req(t *testing.T, method, path, cookie string) *http.Response {
	t.Helper()
	r, err := http.NewRequest(method, e.Srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	resp, err := (&http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}).Do(r)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	})
	return resp
}

func (e *spaTestEnv) wantRedirect(t *testing.T, method, path, cookie string) {
	t.Helper()
	resp := e.req(t, method, path, cookie)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/login" {
		t.Errorf("%s %s = %d Location=%q, want 302 → /login", method, path, resp.StatusCode, resp.Header.Get("Location"))
	}
}

func (e *spaTestEnv) wantShell(t *testing.T, method, path, cookie string) {
	t.Helper()
	resp := e.req(t, method, path, cookie)
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != http.StatusOK || !strings.HasPrefix(ct, "text/html") {
		t.Errorf("%s %s = %d ct=%q, want 200 text/html (SPA shell)", method, path, resp.StatusCode, ct)
	}
}

func TestSPAGuardRedirectsAnonymous(t *testing.T) {
	e := newSPATestEnv(t)
	for _, p := range []string{"/", "/c/42", "/admin", "/settings", "/onboarding"} {
		e.wantRedirect(t, http.MethodGet, p, "")
	}
	// HEAD is served by the same "GET /" mux pattern — guarded too.
	e.wantRedirect(t, http.MethodHead, "/", "")
	// A stale/garbage cookie is anonymous.
	e.wantRedirect(t, http.MethodGet, "/", auth.CookieName+"=not-a-real-token")
}

func TestSPAGuardServesShellToSession(t *testing.T) {
	e := newSPATestEnv(t)
	cookie := e.login(t)
	e.wantShell(t, http.MethodGet, "/", cookie)
	e.wantShell(t, http.MethodGet, "/c/42", cookie)
	e.wantShell(t, http.MethodHead, "/", cookie)
}

func TestSPAGuardPublicPathsStayOpen(t *testing.T) {
	e := newSPATestEnv(t)
	// Login page itself (guard target — gating it would loop), register,
	// AG-2's first-run wizard, and the public shared-chat route.
	for _, p := range []string{"/login", "/register", "/welcome", "/s/3nJ0C3tToken"} {
		e.wantShell(t, http.MethodGet, p, "")
	}
}

func TestSPAGuardAssetsPublic(t *testing.T) {
	e := newSPATestEnv(t)
	// Pick a real embedded asset (hashed names change with web builds, so
	// discover instead of hardcoding).
	var asset string
	err := fs.WalkDir(web.Dist(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && p != "index.html" && asset == "" {
			asset = p
		}
		return nil
	})
	if err != nil || asset == "" {
		t.Fatalf("no embedded asset found in web.Dist (err %v)", err)
	}
	resp := e.req(t, http.MethodGet, "/"+asset, "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("anonymous GET /%s = %d, want 200 (login page needs assets)", asset, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET /%s served the SPA shell (%q) instead of the asset", asset, ct)
	}
}

func TestSPAGuardKeepsAPI404sAsJSON(t *testing.T) {
	e := newSPATestEnv(t)
	// Unknown namespaces must never 302 to /login — AuthPolicy's authed
	// branch answers anonymous first (401), spaHandler's JSON-404 stands.
	resp := e.req(t, http.MethodGet, "/api/definitely-unregistered", "")
	if resp.StatusCode == http.StatusFound {
		t.Error("anonymous /api/* got 302 to /login; want the authed 401")
	}
	// Authenticated → reaches spaHandler, which must still JSON-404, not
	// serve the shell and certainly not redirect.
	resp = e.req(t, http.MethodGet, "/api/definitely-unregistered", e.login(t))
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != http.StatusNotFound || strings.HasPrefix(ct, "text/html") {
		t.Errorf("authed GET /api/… = %d ct=%q, want 404 JSON", resp.StatusCode, ct)
	}
}
