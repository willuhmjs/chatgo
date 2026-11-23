package auth_test

import (
	"context"
	"net/http"
	"testing"

	"chatgo/internal/auth"
	"chatgo/internal/config"
	"chatgo/internal/store"
)

// Tests for docs/AUTHGATE-CONTRACT.md §2: GET /api/auth/status and the
// first-run admin bootstrap (closed policy must not brick a fresh instance).

type statusView struct {
	Authenticated bool `json:"authenticated"`
	FirstRun      bool `json:"first_run"`
}

func getStatus(t *testing.T, env *testEnv) statusView {
	t.Helper()
	r := env.get("/api/auth/status")
	if r.Status != http.StatusOK {
		t.Fatalf("GET /api/auth/status = %d, body %q", r.Status, r.Body)
	}
	var v statusView
	r.json(t, &v)
	return v
}

func TestAuthStatusAnonymousFirstRun(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })

	v := getStatus(t, env)
	if v.Authenticated {
		t.Error("anonymous status: authenticated = true, want false")
	}
	if !v.FirstRun {
		t.Error("fresh store status: first_run = false, want true")
	}
}

func TestAuthStatusAuthenticatedAfterBootstrap(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })

	// First open registration is promoted to admin inside the bootstrap tx
	// and logs straight in (session cookie lands in the test jar).
	reg := env.register("root@example.com", "secretpass-1", "Root")
	if reg.Status != http.StatusOK {
		t.Fatalf("first register = %d, body %q", reg.Status, reg.Body)
	}

	v := getStatus(t, env)
	if !v.Authenticated {
		t.Error("status after login: authenticated = false, want true")
	}
	if v.FirstRun {
		t.Error("status after first user: first_run = true, want false")
	}
}

func TestAuthConfigIncludesFirstRun(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })

	var before struct {
		OIDCEnabled  bool   `json:"oidc_enabled"`
		OIDCName     string `json:"oidc_name"`
		Registration string `json:"registration"`
		FirstRun     *bool  `json:"first_run"` // pointer: must be PRESENT
	}
	r := env.get("/api/auth/config")
	if r.Status != http.StatusOK {
		t.Fatalf("GET /api/auth/config = %d, body %q", r.Status, r.Body)
	}
	r.json(t, &before)
	if before.FirstRun == nil || !*before.FirstRun {
		t.Errorf("fresh config first_run = %v, want true", before.FirstRun)
	}
	if before.Registration != "open" {
		t.Errorf("config lost pre-existing field: registration = %q", before.Registration)
	}

	env.register("root@example.com", "secretpass-1", "Root")

	var after struct {
		FirstRun *bool `json:"first_run"`
	}
	env.get("/api/auth/config").json(t, &after)
	if after.FirstRun == nil || *after.FirstRun {
		t.Errorf("config first_run after bootstrap = %v, want false", after.FirstRun)
	}
}

func TestFirstRunBootstrapUnderClosedPolicy(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "closed" })

	// Closed policy on an EMPTY instance: exactly one admin may register —
	// the point of AUTHGATE §2 (a fresh deploy must not be bricked).
	reg := env.register("root@example.com", "secretpass-1", "Root")
	if reg.Status != http.StatusOK {
		t.Fatalf("first-run register under closed = %d, body %q; want 200 (admin, logged in)", reg.Status, reg.Body)
	}
	var u struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	reg.json(t, &u)
	if u.Role != store.RoleAdmin {
		t.Errorf("first-run role = %q, want admin", u.Role)
	}

	// first_run_done must be set by the bootstrap transaction itself.
	v, err := env.Store.Settings.Get(context.Background(), auth.SettingFirstRunDone)
	if err != nil {
		t.Fatalf("settings get: %v", err)
	}
	if v == nil || *v != "true" {
		t.Errorf("first_run_done = %v, want \"true\"", v)
	}

	// The bootstrap session is live…
	if s := getStatus(t, env); !s.Authenticated || s.FirstRun {
		t.Errorf("status after bootstrap = %+v, want {true,false}", s)
	}

	// …but closed is closed for everyone after the first user.
	second := env.register("late@example.com", "secretpass-1", "Late")
	assertJSONErr(t, second, http.StatusForbidden)
	if _, err := env.Store.Users.GetByEmail(context.Background(), "late@example.com"); err != store.ErrNotFound {
		t.Errorf("post-bootstrap closed registration created a user (err = %v)", err)
	}
}

func TestFirstRunFlagNotSetForLaterUsers(t *testing.T) {
	// A user seeded straight into the table (upgrade scenario) must not make
	// a LATER register call stamp first_run_done as if it were the founder.
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })
	env.seedFirstUser(t)

	if r := env.register("two@example.com", "secretpass-1", "Two"); r.Status != http.StatusOK {
		t.Fatalf("second-user register = %d, body %q", r.Status, r.Body)
	}
	v, err := env.Store.Settings.Get(context.Background(), auth.SettingFirstRunDone)
	if err != nil {
		t.Fatalf("settings get: %v", err)
	}
	if v != nil {
		t.Errorf("first_run_done stamped by non-first user: %q", *v)
	}
}

func TestFirstRunFlagSetForFirstUserUnderApprove(t *testing.T) {
	// The first account also bootstraps under approve (existing promotion) —
	// first_run_done must flip atomically with it, not only for closed.
	env := newTestEnv(t, nil) // default: approve

	// First user is promoted to admin, so register answers 200 + session.
	if r := env.register("root@example.com", "secretpass-1", "Root"); r.Status != http.StatusOK {
		t.Fatalf("first register (approve) = %d, body %q", r.Status, r.Body)
	}
	v, err := env.Store.Settings.Get(context.Background(), auth.SettingFirstRunDone)
	if err != nil {
		t.Fatalf("settings get: %v", err)
	}
	if v == nil || *v != "true" {
		t.Errorf("first_run_done = %v, want \"true\" after first user", v)
	}
}

// Upgrade trap: instances predating first_run_done have users but no setting.
// Reporting first_run=true there bounces a signed-in admin to /welcome, so
// the COUNT of users is the authority when the setting is absent.
func TestFirstRunFalseForUpgradedInstanceWithoutSetting(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })
	env.seedFirstUser(t) // straight insert: no settings stamp

	v, err := env.Store.Settings.Get(context.Background(), auth.SettingFirstRunDone)
	if err != nil {
		t.Fatalf("settings get: %v", err)
	}
	if v != nil {
		t.Fatalf("fixture: first_run_done unexpectedly set: %q", *v)
	}
	if s := getStatus(t, env); s.FirstRun {
		t.Error("upgraded instance (users exist, setting absent): first_run = true, want false")
	}
	if c := getStatusViaConfig(t, env); c {
		t.Error("upgraded instance via /config: first_run = true, want false")
	}
}

// With the setting stamped (regular post-bootstrap instance) first_run is
// false regardless — the cheap fast-path still holds.
func TestFirstRunFalseWithSettingStamped(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })
	env.seedFirstUser(t)
	if err := env.Store.Settings.Set(context.Background(), auth.SettingFirstRunDone, "true"); err != nil {
		t.Fatalf("settings set: %v", err)
	}

	if s := getStatus(t, env); s.FirstRun {
		t.Error("stamped instance: first_run = true, want false")
	}
	if c := getStatusViaConfig(t, env); c {
		t.Error("stamped instance via /config: first_run = true, want false")
	}
}

// getStatusViaConfig reads the first_run flag off the public /config payload
// (isFirstRun drives both surfaces; keep the two in lockstep).
func getStatusViaConfig(t *testing.T, env *testEnv) bool {
	t.Helper()
	var v struct {
		FirstRun *bool `json:"first_run"`
	}
	env.get("/api/auth/config").json(t, &v)
	if v.FirstRun == nil {
		t.Fatal("config first_run missing")
	}
	return *v.FirstRun
}
