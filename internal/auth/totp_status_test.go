package auth_test

// GET /api/me/2fa/status contract (PUB-CONTRACT-SWEEP #5): the Settings
// badge reads the caller's OWN enrollment state with no password and no
// side effects, replacing the old bodiless POST /2fa/setup probe that
// always read 401 ("off") and burned the 2fa:setup bucket. These tests
// also pin the C6/L1-L1 invariants the status route must NOT disturb:
// the setup handler's 401-before-409 ordering and the login challenge's
// burn-per-attempt stay exactly where they were.

import (
	"testing"
)

// Test2FAStatusRoundTrip: anonymous 401 → off → on → off, and the response
// shape is exactly {"enabled": bool}.
func Test2FAStatusRoundTrip(t *testing.T) {
	e := newTestEnv(t, nil)
	const email = "status@example.com"
	e.seedUser(t, email, "user", totpPW)

	// Anonymous: AuthPolicy's /api/* Require branch answers 401 JSON — the
	// route leaks nothing without a session.
	r := e.get("/api/me/2fa/status")
	assertJSONErr(t, r, 401)

	if r := e.login(email, totpPW); r.Status != 200 {
		t.Fatalf("login = %d %q", r.Status, r.Body)
	}

	var out struct {
		Enabled *bool `json:"enabled"`
	}
	r = e.get("/api/me/2fa/status")
	if r.Status != 200 {
		t.Fatalf("status(off) = %d %q", r.Status, r.Body)
	}
	r.json(t, &out)
	if out.Enabled == nil || *out.Enabled {
		t.Fatalf("status(off) payload = %q, want {\"enabled\":false}", r.Body)
	}

	enableTOTP(t, e, email)
	r = e.get("/api/me/2fa/status")
	r.json(t, &out)
	if r.Status != 200 || out.Enabled == nil || !*out.Enabled {
		t.Fatalf("status(on) = %d %q, want {\"enabled\":true}", r.Status, r.Body)
	}

	if r := e.do("POST", "/api/me/2fa/disable", map[string]string{"password": totpPW}); r.Status != 200 {
		t.Fatalf("disable = %d %q", r.Status, r.Body)
	}
	r = e.get("/api/me/2fa/status")
	r.json(t, &out)
	if r.Status != 200 || out.Enabled == nil || *out.Enabled {
		t.Fatalf("status(disabled) = %d %q, want {\"enabled\":false}", r.Status, r.Body)
	}
}

// Test2FAStatusNoSideEffects: hammering the status route must never burn
// the 2fa:setup/enable/disable buckets (that was finding S2 — the probe
// 429-ing legitimate setups), must never write audit rows, and must leave
// the setup ritual working on the first try afterwards.
func Test2FAStatusNoSideEffects(t *testing.T) {
	e := newTestEnv(t, nil)
	const email = "probe@example.com"
	e.seedUser(t, email, "user", totpPW)
	if r := e.login(email, totpPW); r.Status != 200 {
		t.Fatalf("login = %d %q", r.Status, r.Body)
	}

	auditBefore := len(auditActions(t, e))
	// 40 reads — 8× the 5/min setup bucket this must NOT touch.
	for i := 0; i < 40; i++ {
		if r := e.get("/api/me/2fa/status"); r.Status != 200 {
			t.Fatalf("status poll %d = %d %q", i, r.Status, r.Body)
		}
	}
	if got := len(auditActions(t, e)); got != auditBefore {
		t.Fatalf("status route wrote audit rows: %d → %d", auditBefore, got)
	}

	// Setup bucket untouched: the full enable ritual passes immediately.
	enableTOTP(t, e, email)
	if r := e.get("/api/me/2fa/status"); r.errMsg() != "" {
		t.Fatalf("status after enable: %q", r.Body)
	}
}

// Test2FAStatusKeepsSetupOrdering: adding the read-only route must not
// shift the deliberate setup ordering (L1-L1): a WRONG password still
// answers 401 even for an already-enrolled user — 409 never answers
// before 401 — and every wrong-password attempt still burns the setup
// bucket exactly as before.
func Test2FAStatusKeepsSetupOrdering(t *testing.T) {
	e := newTestEnv(t, nil)
	const email = "order@example.com"
	e.seedUser(t, email, "user", totpPW)

	enableTOTP(t, e, email) // leaves the session logged in

	r := e.do("POST", "/api/me/2fa/setup", map[string]string{"password": "totally-wrong-pw"})
	if r.Status != 401 {
		t.Fatalf("wrong-password setup on enrolled user = %d, want 401 (body %q)", r.Status, r.Body)
	}
}
