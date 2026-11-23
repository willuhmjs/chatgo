package auth_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"chatgo/internal/auth"
	"chatgo/internal/config"
	"chatgo/internal/store"
)

func TestRequireAdminAnonymous401VsNonAdmin403(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })

	// Anonymous → 401 with JSON envelope.
	r := env.get("/api/admin-thing")
	assertJSONErr(t, r, http.StatusUnauthorized)

	// Authenticated non-admin → 403.
	env.seedUser(t, "pleb@example.com", store.RoleUser, "secretpass-1")
	if ok := env.login("pleb@example.com", "secretpass-1"); ok.Status != http.StatusOK {
		t.Fatalf("login = %d %q", ok.Status, ok.Body)
	}
	r = env.get("/api/admin-thing")
	assertJSONErr(t, r, http.StatusForbidden)

	// Admin → 200. (fresh client so the pleb cookie doesn't interfere)
	env2 := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })
	env2.seedUser(t, "root@example.com", store.RoleAdmin, "secretpass-1")
	if ok := env2.login("root@example.com", "secretpass-1"); ok.Status != http.StatusOK {
		t.Fatalf("admin login = %d", ok.Status)
	}
	r = env2.get("/api/admin-thing")
	if r.Status != http.StatusOK || string(r.Body) != "admin" {
		t.Errorf("admin route = %d %q", r.Status, r.Body)
	}
}

func TestRequireAdminPendingIs403(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })
	env.seedUser(t, "waiter@example.com", store.RolePending, "secretpass-1")
	if ok := env.login("waiter@example.com", "secretpass-1"); ok.Status != http.StatusOK {
		t.Fatalf("pending login = %d", ok.Status)
	}
	r := env.get("/api/admin-thing")
	assertJSONErr(t, r, http.StatusForbidden)
}

func TestSessionExpiry(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })
	env.seedUser(t, "exp@example.com", store.RoleUser, "secretpass-1")
	if ok := env.login("exp@example.com", "secretpass-1"); ok.Status != http.StatusOK {
		t.Fatalf("login = %d", ok.Status)
	}
	if got := env.get("/api/thing"); got.Status != http.StatusOK {
		t.Fatalf("pre-expiry /api/thing = %d", got.Status)
	}

	// Force the DB row to be expired; the cookie itself is unchanged.
	if _, err := env.Store.DB.ExecContext(context.Background(),
		`UPDATE sessions SET expires_at = ?1`, time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	r := env.get("/api/thing")
	assertJSONErr(t, r, http.StatusUnauthorized)
	if me := env.get("/api/auth/me"); me.Status != http.StatusUnauthorized {
		t.Errorf("/api/auth/me after expiry = %d, want 401", me.Status)
	}
	// Optional-auth routes still serve, just anonymously.
	if out := env.do("POST", "/api/auth/logout", nil); out.Status != http.StatusOK {
		t.Errorf("logout with expired session = %d, want 200", out.Status)
	}
}

func TestGarbageCookieIsAnonymousNotError(t *testing.T) {
	env := newTestEnv(t, nil)
	env.setCookie(auth.CookieName, "not-a-real-session-token")

	r := env.get("/api/thing")
	assertJSONErr(t, r, http.StatusUnauthorized)
	if out := env.do("POST", "/api/auth/logout", nil); out.Status != http.StatusOK {
		t.Errorf("logout with garbage cookie = %d, want 200", out.Status)
	}
	if c := env.get("/api/auth/config"); c.Status != http.StatusOK {
		t.Errorf("/api/auth/config = %d", c.Status)
	}
}
