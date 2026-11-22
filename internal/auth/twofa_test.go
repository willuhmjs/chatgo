package auth_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"chatgo/internal/audit"
	"chatgo/internal/totp"
)

const totpPW = "correct-horse-battery-9"

// enableTOTP runs the full setup→enable ritual for a logged-in seeded user
// and returns the secret in play.
func enableTOTP(t *testing.T, e *testEnv, email string) string {
	t.Helper()
	if r := e.login(email, totpPW); r.Status != 200 {
		t.Fatalf("login for setup: %d %q", r.Status, r.Body)
	}
	r := e.do("POST", "/api/me/2fa/setup", map[string]string{"password": totpPW})
	if r.Status != 200 {
		t.Fatalf("setup: %d %q", r.Status, r.Body)
	}
	var out struct {
		Secret     string `json:"secret"`
		OTPAuthURI string `json:"otpauth_uri"`
	}
	r.json(t, &out)
	if out.Secret == "" || !strings.HasPrefix(out.OTPAuthURI, "otpauth://totp/ChatGo:") {
		t.Fatalf("setup payload: %+v", out)
	}
	code, err := totp.Code(out.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r := e.do("POST", "/api/me/2fa/enable", map[string]string{"code": code}); r.Status != 200 {
		t.Fatalf("enable: %d %q", r.Status, r.Body)
	}
	return out.Secret
}

func auditActions(t *testing.T, e *testEnv) []string {
	t.Helper()
	rows, err := e.Store.DB.Query(`SELECT action FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

// Test2FARoundTrip: setup → enable → TOTP-gated login (no cookie on the
// password step) → /api/auth/totp session → replay rejected → disable.
func Test2FARoundTrip(t *testing.T) {
	e := newTestEnv(t, nil)
	const email = "alice@example.com"
	e.seedUser(t, email, "user", totpPW)

	// Wrong password → 401 (and a login.fail audit row checked below).
	if r := e.login(email, "totally-wrong-pw"); r.Status != 401 {
		t.Fatalf("wrong pw login = %d, want 401", r.Status)
	}

	secret := enableTOTP(t, e, email)

	// Secret is NOT stored in plaintext (at-rest sealing works).
	var stored sql.NullString
	if err := e.Store.DB.QueryRow(`SELECT totp_secret FROM users WHERE email = ?1`, email).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !stored.Valid || stored.String == "" {
		t.Fatal("totp_secret column empty after enable")
	}
	if strings.Contains(stored.String, secret) {
		t.Fatal("totp_secret stored in plaintext!")
	}

	// Re-setup is rejected once enabled.
	if r := e.do("POST", "/api/me/2fa/setup", map[string]string{"password": totpPW}); r.Status != 409 {
		t.Fatalf("re-setup = %d, want 409", r.Status)
	}

	// Logout, then password login must NOT produce a session cookie. (The
	// logout response already expires the jar's copy of the session cookie.)
	if r := e.do("POST", "/api/auth/logout", nil); r.Status != 200 {
		t.Fatalf("logout = %d", r.Status)
	}
	lr := e.login(email, totpPW)
	if lr.Status != 200 {
		t.Fatalf("2fa login = %d %q", lr.Status, lr.Body)
	}
	if ck := lr.Header.Get("Set-Cookie"); ck != "" {
		t.Fatalf("requires_totp login leaked a cookie: %q", ck)
	}
	var challenge struct {
		RequiresTOTP bool   `json:"requires_totp"`
		TOTPToken    string `json:"totp_token"`
	}
	lr.json(t, &challenge)
	if !challenge.RequiresTOTP || challenge.TOTPToken == "" {
		t.Fatalf("challenge payload = %+v", challenge)
	}

	// Wrong code → 401; correct code → session via the SAME cookie path.
	bad := e.do("POST", "/api/auth/totp", map[string]string{"totp_token": challenge.TOTPToken, "code": "123456"})
	if bad.Status != 401 {
		t.Fatalf("bad totp code = %d, want 401", bad.Status)
	}
	code, _ := totp.Code(secret, time.Now())
	good := e.do("POST", "/api/auth/totp", map[string]string{"totp_token": challenge.TOTPToken, "code": code})
	if good.Status != 200 {
		t.Fatalf("totp verify = %d %q", good.Status, good.Body)
	}
	if ck := good.Header.Get("Set-Cookie"); !strings.Contains(ck, "chatgo_session=") {
		t.Fatalf("no session cookie after verify: %q", ck)
	}
	if r := e.get("/api/thing"); r.Status != 200 || string(r.Body) != "thing:"+email {
		t.Fatalf("session not usable: %d %q", r.Status, r.Body)
	}

	// Replay: the consumed token cannot buy a second session (next-step code,
	// so only the used-nonce guard can catch it — proves the nonce path).
	code2, _ := totp.Code(secret, time.Now().Add(35*time.Second)) // next-step code
	if r := e.do("POST", "/api/auth/totp", map[string]string{"totp_token": challenge.TOTPToken, "code": code2}); r.Status != 401 {
		t.Fatalf("replay of consumed token = %d, want 401", r.Status)
	}

	// Forged token (signature over a foreign payload) → 401.
	forged := "v1.some-user." + fmt.Sprint(time.Now().Add(time.Hour).Unix()) + ".deadbeef.deadbeefdeadbeef"
	if r := e.do("POST", "/api/auth/totp", map[string]string{"totp_token": forged, "code": code}); r.Status != 401 {
		t.Fatalf("forged token = %d, want 401", r.Status)
	}

	// Disable: wrong password 401, right password clears, login is plain again.
	if r := e.do("POST", "/api/me/2fa/disable", map[string]string{"password": "nope"}); r.Status != 401 {
		t.Fatalf("disable wrong pw = %d, want 401", r.Status)
	}
	if r := e.do("POST", "/api/me/2fa/disable", map[string]string{"password": totpPW}); r.Status != 200 {
		t.Fatalf("disable = %d %q", r.Status, r.Body)
	}
	plain := e.login(email, totpPW)
	if plain.Status != 200 || plain.Header.Get("Set-Cookie") == "" {
		t.Fatalf("post-disable login = %d (cookie %q), want 200 + cookie", plain.Status, plain.Header.Get("Set-Cookie"))
	}
	var late struct {
		RequiresTOTP bool `json:"requires_totp"`
	}
	plain.json(t, &late)
	if late.RequiresTOTP {
		t.Fatal("requires_totp after disable")
	}

	// Audit trail exists for the whole arc, in causal order.
	want := []string{
		audit.ActionLoginFail,    // wrong password probe
		audit.ActionLoginSuccess, // session used for setup (method=password)
		audit.Action2FASetup,
		audit.Action2FAEnable,
		audit.ActionLogout,
		audit.ActionTOTPChallenge, // password OK → no cookie
		audit.ActionTOTPVerifyFail,
		audit.ActionTOTPVerifyOK,
		audit.ActionLoginSuccess, // method=totp via finishLogin
		audit.ActionTOTPVerifyFail,
		audit.ActionTOTPVerifyFail, // forged token
		audit.Action2FADisable,     // wrong password attempt
		audit.Action2FADisable,     // committed
		audit.ActionLoginSuccess,   // 2FA gone → plain login
	}
	got := auditActions(t, e)
	if len(got) != len(want) {
		t.Fatalf("audit actions =\n%v\nwant\n%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("audit[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// Test2FASetupGuards: OIDC-only rejected, setup-again replaces the pending
// slot, enable-without-setup 400, disable-when-off 409.
func Test2FASetupGuards(t *testing.T) {
	e := newTestEnv(t, nil)
	const email = "bob@example.com"
	e.seedUser(t, email, "user", totpPW)
	if r := e.login(email, totpPW); r.Status != 200 {
		t.Fatalf("login: %d", r.Status)
	}

	if r := e.do("POST", "/api/me/2fa/enable", map[string]string{"code": "123456"}); r.Status != 400 {
		t.Fatalf("enable without setup = %d, want 400", r.Status)
	}
	if r := e.do("POST", "/api/me/2fa/disable", map[string]string{"password": totpPW}); r.Status != 409 {
		t.Fatalf("disable when off = %d, want 409", r.Status)
	}

	// Two setups: only the LAST pending secret can be enabled (single slot).
	r1 := e.do("POST", "/api/me/2fa/setup", map[string]string{"password": totpPW})
	var s1 struct{ Secret string }
	r1.json(t, &s1)
	r2 := e.do("POST", "/api/me/2fa/setup", map[string]string{"password": totpPW})
	var s2 struct{ Secret string }
	r2.json(t, &s2)
	if s1.Secret == "" || s2.Secret == "" || s1.Secret == s2.Secret {
		t.Fatalf("pending slot reuse produced no fresh secret: %q %q", s1.Secret, s2.Secret)
	}
	old, _ := totp.Code(s1.Secret, time.Now())
	if r := e.do("POST", "/api/me/2fa/enable", map[string]string{"code": old}); r.Status != 401 {
		t.Fatalf("enabling superseded secret = %d, want 401", r.Status)
	}
	newCode, _ := totp.Code(s2.Secret, time.Now())
	if r := e.do("POST", "/api/me/2fa/enable", map[string]string{"code": newCode}); r.Status != 200 {
		t.Fatalf("enabling current secret = %d, want 200", r.Status)
	}
}

// Test2FAAnonymousBlocked: Require guards /api/me/2fa/*; /api/auth/totp is
// public but rejects junk tokens.
func Test2FAAnonymousBlocked(t *testing.T) {
	e := newTestEnv(t, nil)
	assertJSONErr(t, e.do("POST", "/api/me/2fa/setup", map[string]string{"password": "x"}), 401)
	assertJSONErr(t, e.do("POST", "/api/auth/totp", map[string]string{"totp_token": "junk", "code": "123456"}), 401)
	assertJSONErr(t, e.do("POST", "/api/auth/totp", map[string]string{}), 400)
}

// TestTOTPVerifyRateLimited: the 11th fast verify on one token answers 429
// while the first ten (bad codes) were 401s.
func TestTOTPVerifyRateLimited(t *testing.T) {
	e := newTestEnv(t, nil)
	const email = "rate@example.com"
	e.seedUser(t, email, "user", totpPW)
	enableTOTP(t, e, email)

	e.do("POST", "/api/auth/logout", nil)
	lr := e.login(email, totpPW)
	var challenge struct {
		TOTPToken string `json:"totp_token"`
	}
	lr.json(t, &challenge)

	for i := range 10 {
		r := e.do("POST", "/api/auth/totp", map[string]string{"totp_token": challenge.TOTPToken, "code": "000001"})
		if r.Status != 401 {
			t.Fatalf("attempt %d: got %d, want 401", i+1, r.Status)
		}
	}
	if r := e.do("POST", "/api/auth/totp", map[string]string{"totp_token": challenge.TOTPToken, "code": "000002"}); r.Status != 429 {
		t.Fatalf("11th attempt: got %d, want 429", r.Status)
	}
}

// TestAuditLoginFailureLands proves Record fires from the failed-login path
// (fire-and-safe) with the submitted email but no fabricated user id.
func TestAuditLoginFailureLands(t *testing.T) {
	e := newTestEnv(t, nil)
	e.seedUser(t, "carol@example.com", "user", totpPW)
	if r := e.login("carol@example.com", "wrong-password-1"); r.Status != 401 {
		t.Fatalf("login = %d", r.Status)
	}
	e.login("ghost@example.com", "whatever-12") // unknown user

	var uid, email, detail string
	err := e.Store.DB.QueryRow(
		`SELECT user_id, email, detail FROM audit_log WHERE action = 'login.fail' ORDER BY id LIMIT 1`).
		Scan(&uid, &email, &detail)
	if err != nil {
		t.Fatalf("no login.fail row: %v", err)
	}
	if uid != "" || email != "carol@example.com" || strings.Contains(strings.ToLower(detail), "password") {
		t.Errorf("login.fail row = (%q,%q,%q), want empty uid + email + structural detail", uid, email, detail)
	}
	var n int
	if err := e.Store.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE email = 'ghost@example.com'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("unknown-user attempt audited %d rows (err %v), want 1", n, err)
	}
}
