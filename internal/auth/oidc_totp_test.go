package auth_test

// C7 regression tests for Lens-8 M2 (OIDC callback must honour 2FA):
// an OIDC sign-in for a user with a committed totp_secret issues NO session
// cookie — instead the callback 302s to /login?totp=1&t=<totp_token> and the
// session is only minted by POST /api/auth/totp, mirroring the password path.
//
// These tests are deliberately tolerant of C6's parallel hardening of the
// totp_token nonce-burn semantics (burn-on-success vs burn-on-every-attempt):
// every positive assertion (a token buys a session) runs on a FRESH challenge
// token, and every negative assertion (401s / replay rejection) holds under
// both semantics.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"chatgo/internal/audit"
	"chatgo/internal/store"
	"chatgo/internal/totp"
)

// c7TOTPPW is this file's own password (distinct from twofa_test.go's totpPW
// so parallel edits to that file cannot couple with these tests).
const c7TOTPPW = "c7-2fa-password-9"

// c7EnableTOTP commits a TOTP secret for a seeded password account through
// the real setup→enable route and logs the session out again. Self-contained
// on purpose (no reliance on twofa_test.go helpers). Returns the secret.
func c7EnableTOTP(t *testing.T, e *testEnv, email string) string {
	t.Helper()
	if r := e.login(email, c7TOTPPW); r.Status != 200 {
		t.Fatalf("login for setup: %d %q", r.Status, r.Body)
	}
	r := e.do("POST", "/api/me/2fa/setup", map[string]string{"password": c7TOTPPW})
	if r.Status != 200 {
		t.Fatalf("setup: %d %q", r.Status, r.Body)
	}
	var out struct {
		Secret string `json:"secret"`
	}
	r.json(t, &out)
	if out.Secret == "" {
		t.Fatalf("setup payload: %+v", out)
	}
	code, err := totp.Code(out.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r := e.do("POST", "/api/me/2fa/enable", map[string]string{"code": code}); r.Status != 200 {
		t.Fatalf("enable: %d %q", r.Status, r.Body)
	}
	if r := e.do("POST", "/api/auth/logout", nil); r.Status != 200 {
		t.Fatalf("logout: %d", r.Status)
	}
	return out.Secret
}

// c7ChallengeToken runs one full OIDC round-trip and asserts the callback
// answered a TOTP challenge (302 /login?totp=1&t=…, no session cookie),
// returning the parsed token.
func c7ChallengeToken(t *testing.T, env *testEnv, fake *fakeOIDC, wantUserID string) string {
	t.Helper()
	state, nonce, _ := oidcStart(t, env, fake)
	fake.setNonce(nonce)
	cb := oidcCallback(env, "good-code", state)

	if cb.Status != http.StatusFound {
		t.Fatalf("callback = %d %q, want 302", cb.Status, cb.Body)
	}
	if !strings.HasPrefix(cb.Location, "/login?totp=1&t=") {
		t.Fatalf("callback Location = %q, want /login?totp=1&t=…", cb.Location)
	}
	for _, ck := range cb.Header.Values("Set-Cookie") {
		if strings.Contains(ck, "chatgo_session=") {
			t.Fatalf("session cookie issued to a TOTP user on the OIDC callback: %q", ck)
		}
	}
	loc, err := url.Parse(cb.Location)
	if err != nil {
		t.Fatalf("parse Location %q: %v", cb.Location, err)
	}
	token := loc.Query().Get("t")
	if token == "" {
		t.Fatal("no totp token in challenge redirect")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 5 || parts[0] != "v1" || parts[1] != wantUserID {
		t.Fatalf("token %q is not a v1 totp_token bound to %s", token, wantUserID)
	}
	return token
}

func TestOIDCCallbackWithTOTPIssuesChallengeNotSession(t *testing.T) {
	fake := newFakeOIDC(t)
	env := newOIDCEnv(t, fake, "open")
	u := env.seedUser(t, oidcUserEmail, store.RoleUser, c7TOTPPW)
	secret := c7EnableTOTP(t, env, oidcUserEmail)

	token := c7ChallengeToken(t, env, fake, u.ID)

	// The challenge did not authenticate anyone.
	if me := env.get("/api/auth/me"); me.Status != http.StatusUnauthorized {
		t.Fatalf("/api/auth/me = %d, want 401 before 2FA completes", me.Status)
	}
	// A login.totp_challenge audit row was recorded for the OIDC leg.
	var challenged int
	if err := env.Store.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = ?`,
		audit.ActionTOTPChallenge).Scan(&challenged); err != nil || challenged < 1 {
		t.Errorf("totp_challenge audit rows = %d (err %v), want >= 1", challenged, err)
	}

	// Wrong code against this token → 401 under either nonce-burn semantics.
	bad := env.do("POST", "/api/auth/totp", map[string]string{"totp_token": token, "code": "123456"})
	if bad.Status != http.StatusUnauthorized {
		t.Fatalf("bad code = %d, want 401", bad.Status)
	}

	// Success path runs on a FRESH challenge token: correct whether C6 burns
	// the nonce on success only or on every attempt (a re-run of the same
	// token would be semantics-dependent). Next-step code so the monotonic
	// step guard accepts it regardless of what the failed attempt recorded.
	token2 := c7ChallengeToken(t, env, fake, u.ID)
	code2, err := totp.Code(secret, time.Now().Add(time.Duration(31-time.Now().Unix()%30)*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	good := env.do("POST", "/api/auth/totp", map[string]string{"totp_token": token2, "code": code2})
	if good.Status != http.StatusOK {
		t.Fatalf("totp verify = %d %q, want 200", good.Status, good.Body)
	}
	if ck := good.Header.Get("Set-Cookie"); !strings.Contains(ck, "chatgo_session=") {
		t.Fatalf("no session cookie after verify: %q", ck)
	}
	if r := env.get("/api/thing"); r.Status != 200 || string(r.Body) != "thing:"+oidcUserEmail {
		t.Fatalf("session not usable: %d %q", r.Status, r.Body)
	}

	// Replay: the consumed token cannot buy a second session (rejected by the
	// used-nonce set, the step memory, or C6's earlier burn — any of these
	// answers 401 and satisfies the security property).
	code3, err := totp.Code(secret, time.Now().Add(time.Duration(31-time.Now().Unix()%30)*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if r := env.do("POST", "/api/auth/totp", map[string]string{"totp_token": token2, "code": code3}); r.Status != http.StatusUnauthorized {
		t.Fatalf("replay of consumed challenge token = %d, want 401", r.Status)
	}

	// Auto-link reused the seeded user; no duplicate was provisioned.
	all, err := env.Store.Users.List(t.Context())
	if err != nil || len(all) != 1 {
		t.Errorf("users = %d (err %v), want 1", len(all), err)
	}
}

// Regression: a linked OIDC user WITHOUT a totp_secret keeps the pre-fix
// behavior exactly — 302 / + session cookie, no totp query in play.
func TestOIDCCallbackWithoutTOTPIssuesSession(t *testing.T) {
	fake := newFakeOIDC(t)
	env := newOIDCEnv(t, fake, "open")
	env.seedUser(t, oidcUserEmail, store.RoleUser, c7TOTPPW) // password, no 2FA

	state, nonce, _ := oidcStart(t, env, fake)
	fake.setNonce(nonce)
	cb := oidcCallback(env, "good-code", state)
	if cb.Status != http.StatusFound || cb.Location != "/" {
		t.Fatalf("callback = %d %q, want 302 /", cb.Status, cb.Location)
	}
	if !strings.Contains(strings.Join(cb.Header.Values("Set-Cookie"), "\n"), "chatgo_session=") {
		t.Errorf("no session cookie on callback: %v", cb.Header.Values("Set-Cookie"))
	}
	if strings.Contains(cb.Location, "totp") || strings.Contains(cb.Location, "t=") {
		t.Errorf("totp challenge leaked to a non-2FA user: %q", cb.Location)
	}
	if r := env.get("/api/thing"); r.Status != 200 || string(r.Body) != "thing:"+oidcUserEmail {
		t.Fatalf("session not usable: %d %q", r.Status, r.Body)
	}
}
