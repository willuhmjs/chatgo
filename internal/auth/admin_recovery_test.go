package auth_test

// Tests for admin-driven 2FA lockout recovery (internal/auth/admin_recovery.go
// + POST /api/users/{id}/reset-2fa wiring in internal/apimgr/users.go). Lives
// in the external test package so the whole flow runs over the real HTTP
// stack: testPolicy's RequireAdmin gate, the users handler, session middleware
// and login — exactly what an operator exercises.

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"chatgo/internal/auth"
	"chatgo/internal/totp"
)

const (
	recAdminPW = "root-recovery-pw-1"
	recUserPW  = "lost-recovery-pw-1"
)

// recEnableTOTP runs setup→enable for a seeded local user (its own copy of
// the ritual so this file stays self-contained). Leaves the target's session
// in the env's cookie jar.
func recEnableTOTP(t *testing.T, e *testEnv, email, pw string) {
	t.Helper()
	if r := e.login(email, pw); r.Status != 200 {
		t.Fatalf("login for 2fa setup: %d %q", r.Status, r.Body)
	}
	rs := e.do("POST", "/api/me/2fa/setup", map[string]string{"password": pw})
	if rs.Status != 200 {
		t.Fatalf("2fa setup: %d %q", rs.Status, rs.Body)
	}
	var out struct {
		Secret string `json:"secret"`
	}
	rs.json(t, &out)
	code, err := totp.Code(out.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r := e.do("POST", "/api/me/2fa/enable", map[string]string{"code": code}); r.Status != 200 {
		t.Fatalf("2fa enable: %d %q", r.Status, rs.Body)
	}
}

// reset2FA calls POST /api/users/{id}/reset-2fa and decodes the JSON body.
func reset2FA(t *testing.T, e *testEnv, userID string) (*response, map[string]any) {
	t.Helper()
	r := e.do("POST", "/api/users/"+userID+"/reset-2fa", nil)
	var out map[string]any
	if r.Status == 200 {
		r.json(t, &out)
	}
	return r, out
}

func sessionCount(t *testing.T, e *testEnv, userID string) int {
	t.Helper()
	var n int
	if err := e.Store.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?1`, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func totpSecret(t *testing.T, e *testEnv, userID string) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := e.Store.DB.QueryRow(`SELECT totp_secret FROM users WHERE id = ?1`, userID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func cookieValue(t *testing.T, setCookie string) string {
	t.Helper()
	for _, part := range strings.Split(setCookie, ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok && k == "chatgo_session" {
			return v
		}
	}
	t.Fatalf("no chatgo_session cookie in %q", setCookie)
	return ""
}

// TestAdminReset2FALocalUser: the headline recovery. 2FA cleared, every
// session revoked (other users unaffected), a one-time password unlocks the
// account — old password dies, login needs NO TOTP step, and the audit row
// never contains the password.
func TestAdminReset2FALocalUser(t *testing.T) {
	e := newTestEnv(t, nil)
	e.seedUser(t, "root@example.com", "admin", recAdminPW)
	target := e.seedUser(t, "lost@example.com", "user", recUserPW)
	ctx := context.Background()

	recEnableTOTP(t, e, "lost@example.com", recUserPW)
	// Two more live sessions on top of the setup one (3 total).
	s1, err := e.Auth.CreateSession(ctx, target.ID, "test", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Auth.CreateSession(ctx, target.ID, "test", "192.0.2.2"); err != nil {
		t.Fatal(err)
	}
	e.setCookie("chatgo_session", s1.Token)
	if r := e.get("/api/auth/me"); r.Status != 200 {
		t.Fatalf("target session should work pre-reset: %d %q", r.Status, r.Body)
	}
	before := sessionCount(t, e, target.ID)
	if before != 3 {
		t.Fatalf("target sessions = %d, want 3", before)
	}

	lr := e.login("root@example.com", recAdminPW)
	if lr.Status != 200 {
		t.Fatalf("admin login: %d %q", lr.Status, lr.Body)
	}
	adminTok := cookieValue(t, lr.Header.Get("Set-Cookie"))

	r, out := reset2FA(t, e, target.ID)
	if r.Status != 200 {
		t.Fatalf("reset = %d %q", r.Status, r.Body)
	}
	if out["ok"] != true || out["was_enabled"] != true {
		t.Errorf("payload = %v, want ok + was_enabled", out)
	}
	if got := int64(out["sessions_revoked"].(float64)); got != int64(before) {
		t.Errorf("sessions_revoked = %d, want %d", got, before)
	}
	pw, _ := out["new_password"].(string)
	if len(pw) != 22 {
		t.Fatalf("new_password = %q, want 22 url-safe chars", pw)
	}

	// DB truth: secret gone, password hash replaced with the returned one.
	if s := totpSecret(t, e, target.ID); s.Valid {
		t.Errorf("totp_secret still set after reset: %q", s.String)
	}
	u, err := e.Store.Users.GetByID(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !u.HasPassword() || !auth.VerifyPassword(*u.PasswordHash, pw) {
		t.Error("returned password was not installed as the bcrypt hash")
	}

	// All target sessions revoked; the admin's own session untouched.
	if n := sessionCount(t, e, target.ID); n != 0 {
		t.Errorf("target sessions after reset = %d, want 0", n)
	}
	e.setCookie("chatgo_session", s1.Token)
	if r := e.get("/api/auth/me"); r.Status != 401 {
		t.Errorf("revoked session still authed: %d %q", r.Status, r.Body)
	}
	e.setCookie("chatgo_session", adminTok)
	if r := e.get("/api/auth/me"); r.Status != 200 {
		t.Errorf("admin session must survive target reset: %d %q", r.Status, r.Body)
	}

	// Old password is dead, the one-time password logs in WITHOUT a TOTP
	// challenge, and the fresh session works.
	if r := e.login("lost@example.com", recUserPW); r.Status != 401 {
		t.Errorf("old password = %d, want 401", r.Status)
	}
	lr2 := e.login("lost@example.com", pw)
	if lr2.Status != 200 {
		t.Fatalf("one-time password login = %d %q", lr2.Status, lr2.Body)
	}
	if lr2.Header.Get("Set-Cookie") == "" {
		t.Fatalf("recovery login issued no session: %q", lr2.Body)
	}
	if strings.Contains(string(lr2.Body), "requires_totp") {
		t.Errorf("recovered login still TOTP-gated: %q", lr2.Body)
	}
	if r := e.get("/api/thing"); r.Status != 200 {
		t.Errorf("post-recovery session call = %d %q", r.Status, r.Body)
	}

	// Audit row exists, names the target + method, and leaks NO password.
	var detail string
	if err := e.Store.DB.QueryRow(
		`SELECT detail FROM audit_log WHERE action = ?1 ORDER BY id DESC LIMIT 1`,
		auth.ActionAdminReset2FA).Scan(&detail); err != nil {
		t.Fatalf("audit row missing: %v", err)
	}
	if !strings.Contains(detail, "user_id="+target.ID) || !strings.Contains(detail, "method=password") {
		t.Errorf("audit detail = %q, want user_id + method=password", detail)
	}
	if strings.Contains(detail, pw) {
		t.Error("audit detail leaked the one-time password!")
	}
}

// TestAdminReset2FAOIDCOnlyUser: an SSO-only account must NOT gain a local
// password (hash stays NULL) — 2FA is cleared and sessions revoked only.
func TestAdminReset2FAOIDCOnlyUser(t *testing.T) {
	e := newTestEnv(t, nil)
	e.seedUser(t, "root@example.com", "admin", recAdminPW)
	target := e.seedUser(t, "sso@example.com", "user", "") // OIDC-only: password_hash NULL
	ctx := context.Background()

	// A committed secret can pre-exist on an SSO account (2FA enabled while
	// the account still had a password, IdP switched later). Simulate with a
	// non-empty sealed column; reset only needs it cleared, never unsealed.
	if _, err := e.Store.DB.Exec(`UPDATE users SET totp_secret = ?1 WHERE id = ?2`, "c2ltYmxlLW5vdC1yZWFs", target.ID); err != nil {
		t.Fatal(err)
	}
	s, err := e.Auth.CreateSession(ctx, target.ID, "test", "192.0.2.9")
	if err != nil {
		t.Fatal(err)
	}

	if r := e.login("root@example.com", recAdminPW); r.Status != 200 {
		t.Fatalf("admin login: %d %q", r.Status, r.Body)
	}
	r, out := reset2FA(t, e, target.ID)
	if r.Status != 200 {
		t.Fatalf("reset = %d %q", r.Status, r.Body)
	}
	if out["was_enabled"] != true {
		t.Errorf("was_enabled = %v, want true", out["was_enabled"])
	}
	if _, has := out["new_password"]; has {
		t.Errorf("SSO account must not receive a password: %v", out)
	}
	if got := int64(out["sessions_revoked"].(float64)); got != 1 {
		t.Errorf("sessions_revoked = %d, want 1", got)
	}

	if s := totpSecret(t, e, target.ID); s.Valid {
		t.Errorf("totp_secret not cleared: %q", s.String)
	}
	var pwHash sql.NullString
	if err := e.Store.DB.QueryRow(`SELECT password_hash FROM users WHERE id = ?1`, target.ID).Scan(&pwHash); err != nil {
		t.Fatal(err)
	}
	if pwHash.Valid {
		t.Error("recovery fabricated a local password for an OIDC-only account")
	}
	if n := sessionCount(t, e, target.ID); n != 0 {
		t.Errorf("sessions after reset = %d, want 0", n)
	}
	e.setCookie("chatgo_session", s.Token)
	if r := e.get("/api/auth/me"); r.Status != 401 {
		t.Errorf("revoked SSO session still authed: %d", r.Status)
	}
}

// TestAdminReset2FAWithout2FAStillRecovers: was_enabled=false is not an
// error — password rotation + session revoke still land (stolen-session
// escape hatch, and admins use this to break any lockout).
func TestAdminReset2FAWithout2FAStillRecovers(t *testing.T) {
	e := newTestEnv(t, nil)
	e.seedUser(t, "root@example.com", "admin", recAdminPW)
	target := e.seedUser(t, "flat@example.com", "user", recUserPW)
	if _, err := e.Auth.CreateSession(context.Background(), target.ID, "test", "192.0.2.3"); err != nil {
		t.Fatal(err)
	}

	if r := e.login("root@example.com", recAdminPW); r.Status != 200 {
		t.Fatalf("admin login: %d %q", r.Status, r.Body)
	}
	r, out := reset2FA(t, e, target.ID)
	if r.Status != 200 {
		t.Fatalf("reset = %d %q", r.Status, r.Body)
	}
	if out["was_enabled"] != false {
		t.Errorf("was_enabled = %v, want false", out["was_enabled"])
	}
	pw, _ := out["new_password"].(string)
	if pw == "" {
		t.Fatal("local account should still get a one-time password")
	}
	if got := int64(out["sessions_revoked"].(float64)); got != 1 {
		t.Errorf("sessions_revoked = %d, want 1", got)
	}
	if r := e.login("flat@example.com", pw); r.Status != 200 {
		t.Errorf("one-time password login = %d %q", r.Status, r.Body)
	}
}

// TestAdminReset2FAAuthzAndUnknown: non-admin callers are rejected (403 with
// a session, 401 anonymously — by the shared RequireAdmin policy gate and
// again by the handler), and unknown ids answer the standard 404.
func TestAdminReset2FAAuthzAndUnknown(t *testing.T) {
	e := newTestEnv(t, nil)
	e.seedUser(t, "root@example.com", "admin", recAdminPW)
	victim := e.seedUser(t, "victim@example.com", "user", recUserPW)
	e.seedUser(t, "plain@example.com", "user", recUserPW)

	if r := e.login("plain@example.com", recUserPW); r.Status != 200 {
		t.Fatalf("login: %d %q", r.Status, r.Body)
	}
	if r := e.do("POST", "/api/users/"+victim.ID+"/reset-2fa", nil); r.Status != 403 {
		t.Fatalf("non-admin reset = %d, want 403 (body %q)", r.Status, r.Body)
	}
	// Victim untouched.
	if r := e.login("victim@example.com", recUserPW); r.Status != 200 {
		t.Errorf("victim login after refused reset = %d, want 200", r.Status)
	}
	// Anonymous (logged out) → 401 from RequireAdmin, not even 403.
	if r := e.do("POST", "/api/auth/logout", nil); r.Status != 200 {
		t.Fatalf("logout = %d", r.Status)
	}
	if r := e.do("POST", "/api/users/"+victim.ID+"/reset-2fa", nil); r.Status != 401 {
		t.Fatalf("anonymous reset = %d, want 401", r.Status)
	}
	// Admin on a bogus id → 404 like approve/role.
	if r := e.login("root@example.com", recAdminPW); r.Status != 200 {
		t.Fatalf("admin login: %d %q", r.Status, r.Body)
	}
	if r := e.do("POST", "/api/users/does-not-exist/reset-2fa", nil); r.Status != 404 {
		t.Fatalf("unknown id = %d, want 404", r.Status)
	}
}
