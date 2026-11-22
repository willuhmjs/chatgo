package auth

// Internal unit tests for the 2FA primitives: at-rest sealing, totp_token
// issue/verify, nonce replay set, monotonic-step memory, and the OIDC-only
// setup rejection (which cannot be reached over HTTP — an SSO-only account
// has no password login to bootstrap the session from).

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"chatgo/internal/config"
	"chatgo/internal/store"
	"chatgo/internal/totp"
)

func newInternalAuth(t *testing.T) (*Auth, *store.Store) {
	t.Helper()
	st := newInternalStore(t)
	cfg := &config.Config{
		SessionSecret: hex.EncodeToString(internalSecret),
		Registration:  "approve",
		Version:       "test",
	}
	a, err := New(cfg, st)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a, st
}

func TestTOTPCColumnAddedIdempotent(t *testing.T) {
	a, st := newInternalAuth(t)
	if err := ensureTOTPColumn(st.DB); err != nil { // second ALTER pass
		t.Fatalf("re-run ensureTOTPColumn: %v", err)
	}
	if enabled, err := a.totpEnabled(context.Background(), "nobody"); err != nil || enabled {
		t.Fatalf("totpEnabled(unknown) = %v, %v", enabled, err)
	}
}

func TestSealUnsealRoundTrip(t *testing.T) {
	a, _ := newInternalAuth(t)
	const secret = "JBSWY3DPEHPK3PXP"
	ct, err := a.sealTOTP(secret)
	if err != nil {
		t.Fatal(err)
	}
	if ct == secret || strings.Contains(ct, secret) {
		t.Fatal("sealed value leaks the plaintext")
	}
	plain, err := a.unsealTOTP(ct)
	if err != nil || plain != secret {
		t.Fatalf("unseal = %q, %v", plain, err)
	}
	// Tamper detection: flip one payload byte.
	blob, _ := base64.StdEncoding.DecodeString(ct)
	blob[len(blob)-1] ^= 0xff
	if _, err := a.unsealTOTP(base64.StdEncoding.EncodeToString(blob)); err == nil {
		t.Fatal("tampered blob unsealed cleanly")
	}
	if _, err := a.unsealTOTP("!!!not base64!!!"); err == nil {
		t.Fatal("garbage unsealed cleanly")
	}
}

func TestTOTPTokenIssueParse(t *testing.T) {
	a, _ := newInternalAuth(t)
	tok, err := a.issueTOTPToken("user-42")
	if err != nil {
		t.Fatal(err)
	}
	uid, nonce, _, err := a.parseTOTPToken(tok)
	if err != nil {
		t.Fatalf("parse own token: %v", err)
	}
	if uid != "user-42" || nonce == "" {
		t.Fatalf("claims = %q/%q", uid, nonce)
	}

	parts := strings.Split(tok, ".")

	// Tampered payload (different user) → reject.
	bad := "v1.attacker." + parts[2] + "." + parts[3] + "." + parts[4]
	if _, _, _, err := a.parseTOTPToken(bad); err == nil {
		t.Error("tampered token accepted")
	}
	// Expired but correctly signed → reject.
	expiredPayload := "v1.user-42." + strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10) + "." + parts[3]
	if _, _, _, err := a.parseTOTPToken(expiredPayload + "." + a.totpTokenMAC(expiredPayload)); err == nil {
		t.Error("expired token accepted")
	}
	for _, junk := range []string{"", "v1", "nope.nope.nope.nope.nope", parts[0] + "." + parts[1] + "." + parts[2] + "." + parts[3]} {
		if _, _, _, err := a.parseTOTPToken(junk); err == nil {
			t.Errorf("junk %q accepted", junk)
		}
	}
}

func TestNonceReplaySet(t *testing.T) {
	a, _ := newInternalAuth(t)
	if !a.totpMarkNonceUsed("abc") {
		t.Fatal("first use rejected")
	}
	if a.totpMarkNonceUsed("abc") {
		t.Fatal("replay accepted")
	}
	if !a.totpMarkNonceUsed("def") {
		t.Fatal("different nonce rejected")
	}
	// Expired entries are swept on use (bounded map).
	a.usedNonces["zzz"] = time.Now().Add(-time.Hour).Unix()
	a.totpMarkNonceUsed("yyy")
	if _, stale := a.usedNonces["zzz"]; stale {
		t.Fatal("expired nonce not swept")
	}
}

func TestConsumeTOTPCodeMonotonic(t *testing.T) {
	a, st := newInternalAuth(t)
	ctx := context.Background()
	u := &store.User{Email: "step@example.com", Role: store.RoleUser}
	if err := st.Users.Insert(ctx, u); err != nil {
		t.Fatal(err)
	}
	secret, _ := totp.GenerateSecret()
	if err := a.setTOTPSecret(ctx, u.ID, secret); err != nil {
		t.Fatal(err)
	}
	if enabled, err := a.totpEnabled(ctx, u.ID); err != nil || !enabled {
		t.Fatalf("enabled = %v, %v", enabled, err)
	}

	now := time.Now()
	cur, _ := totp.Code(secret, now)
	if !a.consumeTOTPCode(ctx, u.ID, cur) {
		t.Fatal("current code rejected")
	}
	if a.consumeTOTPCode(ctx, u.ID, cur) {
		t.Fatal("same code accepted twice")
	}
	prev, _ := totp.Code(secret, now.Add(-30*time.Second))
	if a.consumeTOTPCode(ctx, u.ID, prev) {
		t.Fatal("backwards step accepted (replay)")
	}
	next, _ := totp.Code(secret, now.Add(30*time.Second))
	if !a.consumeTOTPCode(ctx, u.ID, next) {
		t.Fatal("next-window code rejected after higher step was consumed")
	}

	a.forgetTOTPState(u.ID)
	if err := a.clearTOTPSecret(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if a.consumeTOTPCode(ctx, u.ID, next) {
		t.Fatal("code accepted after secret cleared")
	}
}

func Test2FASetupRejectsOIDCOnly(t *testing.T) {
	a, st := newInternalAuth(t)
	ctx := context.Background()
	oidcOnly := &store.User{Email: "sso@example.com", Role: store.RoleUser} // no password
	if err := st.Users.Insert(ctx, oidcOnly); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/me/2fa/setup",
		strings.NewReader(`{"password":"whatever-1"}`)).WithContext(withIdentity(ctx, oidcOnly, nil))
	rec := httptest.NewRecorder()
	a.Handle2FASetup(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("OIDC-only setup = %d, want 400 (body %s)", rec.Code, rec.Body)
	}

	local := &store.User{Email: "local@example.com", Role: store.RoleUser}
	h, err := HashPassword("local-password-1")
	if err != nil {
		t.Fatal(err)
	}
	local.PasswordHash = &h
	if err := st.Users.Insert(ctx, local); err != nil {
		t.Fatal(err)
	}

	// Wrong password → 401 before any secret is generated/persisted.
	req = httptest.NewRequest(http.MethodPost, "/api/me/2fa/setup",
		strings.NewReader(`{"password":"nope-nope-nope"}`)).WithContext(withIdentity(ctx, local, nil))
	rec = httptest.NewRecorder()
	a.Handle2FASetup(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password setup = %d, want 401", rec.Code)
	}
	if _, ok := a.pendingTOTPSecret(local.ID); ok {
		t.Fatal("pending secret stored despite wrong password")
	}
	// Anonymous (Require bypassed) → 401.
	req = httptest.NewRequest(http.MethodPost, "/api/me/2fa/setup", strings.NewReader(`{"password":"x"}`))
	rec = httptest.NewRecorder()
	a.Handle2FASetup(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous setup = %d, want 401", rec.Code)
	}
}

// ---------- C6: burn-on-every-attempt + per-user lockout wall ----------

// TestTOTPTokenIssuedAtRoundTrip pins the expiry→mint mapping the kill-stamp
// check relies on (issueTOTPToken sets exp = mint + totpTokenTTL).
func TestTOTPTokenIssuedAtRoundTrip(t *testing.T) {
	a, _ := newInternalAuth(t)
	before := time.Now().Unix()
	tok, err := a.issueTOTPToken("u42")
	if err != nil {
		t.Fatal(err)
	}
	_, _, exp, err := a.parseTOTPToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	issued := totpTokenIssuedAt(exp)
	if issued < before-1 || issued > time.Now().Unix()+1 {
		t.Fatalf("implied mint = %d, want ≈now (%d)", issued, before)
	}
}

// TestTOTPChallengeKillStamp covers the deny-list epoch primitive: a trip
// at the threshold kills everything minted at/before the stamp, later
// mints survive, stale stamps self-sweep, and forgetTOTPState scrubs all.
func TestTOTPChallengeKillStamp(t *testing.T) {
	a, _ := newInternalAuth(t)
	const uid = "killme"

	// Sub-threshold failures do not arm the stamp.
	for i := range totpFailLockout - 1 {
		if a.noteTOTPFailure(uid) {
			t.Fatalf("lockout tripped early at %d failures", i+1)
		}
	}
	if _, armed := a.totpKilledAt[uid]; armed {
		t.Fatal("kill stamp armed below threshold")
	}
	if !a.noteTOTPFailure(uid) {
		t.Fatal("threshold trip not reported")
	}
	killed, armed := a.totpKilledAt[uid]
	if !armed {
		t.Fatal("kill stamp not armed at threshold")
	}
	if a.totpVerifyFails[uid] != 0 {
		t.Fatalf("counter not restarted after trip: %d", a.totpVerifyFails[uid])
	}
	if !a.totpChallengeKilled(uid, killed) {
		t.Error("token minted at the stamp not killed")
	}
	if !a.totpChallengeKilled(uid, killed-60) {
		t.Error("older token not killed")
	}
	if a.totpChallengeKilled(uid, killed+1) {
		t.Error("token minted AFTER the stamp was killed")
	}
	// Stale stamp (> token TTL past) self-sweeps: nothing left to invalidate.
	a.totpKilledAt[uid] = time.Now().Add(-2 * totpTokenTTL).Unix()
	if a.totpChallengeKilled(uid, 0) {
		t.Error("stale stamp still killing")
	}
	if _, ok := a.totpKilledAt[uid]; ok {
		t.Error("stale stamp not swept")
	}
	// forgetTOTPState is the single scrub point.
	a.totpVerifyFails[uid] = 3
	a.totpKilledAt[uid] = time.Now().Unix()
	a.forgetTOTPState(uid)
	if _, ok := a.totpKilledAt[uid]; ok {
		t.Error("forgetTOTPState left kill stamp")
	}
	if _, ok := a.totpVerifyFails[uid]; ok {
		t.Error("forgetTOTPState left fail counter")
	}
}

// TestTOTPVerifyLockoutHandler drives the REAL handler through
// totpFailLockout wrong-code attempts (fresh token per attempt, mimicking
// the burn rule's "one login per guess" cost) and proves the trip: the
// lockout audit alert lands, and a token minted BEFORE the trip is dead
// even with a CORRECT code, while a post-trip mint still works.
func TestTOTPVerifyLockoutHandler(t *testing.T) {
	a, st := newInternalAuth(t)
	ctx := context.Background()
	u := &store.User{Email: "lock@example.com", Role: store.RoleUser}
	if err := st.Users.Insert(ctx, u); err != nil {
		t.Fatal(err)
	}
	secret, _ := totp.GenerateSecret()
	if err := a.setTOTPSecret(ctx, u.ID, secret); err != nil {
		t.Fatal(err)
	}
	// Mint BEFORE the streak starts: must be dead once the stamp lands.
	staleTok, err := a.issueTOTPToken(u.ID)
	if err != nil {
		t.Fatal(err)
	}

	post := func(token, code string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/totp",
			strings.NewReader(`{"totp_token":"`+token+`","code":"`+code+`"}`))
		req.RemoteAddr = "203.0.113.9:4000" // one IP; per-IP cap is 30/min
		rec := httptest.NewRecorder()
		a.HandleTOTPVerify(rec, req)
		return rec
	}

	// The per-user 10/min bucket must not mask the 15-attempt trip: drop it
	// between attempts so each behaves like a freshly-refilled minute
	// (white-box limiter reset; real deployments would spend wall clock).
	for i := range totpFailLockout {
		a.mu.Lock()
		delete(a.limiters, "totp:uid:"+u.ID)
		a.mu.Unlock()
		// Each attempt needs its own unburned token (burn-on-attempt).
		tok, err := a.issueTOTPToken(u.ID)
		if err != nil {
			t.Fatal(err)
		}
		rec := post(tok, "000000")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d, want 401", i+1, rec.Code)
		}
	}
	if _, armed := a.totpKilledAt[u.ID]; !armed {
		t.Fatal("handler streak did not arm the kill stamp")
	}
	var alerts int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = ?1 AND detail = 'lockout'`,
		"totp.verify.fail").Scan(&alerts); err != nil || alerts != 1 {
		t.Fatalf("lockout audit alerts = %d (err %v), want 1", alerts, err)
	}

	// Pre-trip token + CORRECT code → 401 superseded (all outstanding
	// challenges invalidated, even a currently-valid code cannot save it).
	code, _ := totp.Code(secret, time.Now())
	if rec := post(staleTok, code); rec.Code != http.StatusUnauthorized {
		t.Fatalf("pre-trip token after lockout: %d, want 401", rec.Code)
	}
	var superseded int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = ?1 AND detail = 'superseded'`,
		"totp.verify.fail").Scan(&superseded); err != nil || superseded != 1 {
		t.Fatalf("superseded audit rows = %d (err %v), want 1", superseded, err)
	}

	// Post-trip mint (issuedAt AFTER the stamp) with an in-window NEXT-step
	// code works: lockout invalidates outstanding challenges, it does not
	// brick 2FA. (Deterministic +1-step pick — a fixed +35s can silently
	// land two steps ahead of the clock and confuse the ±1 window.)
	time.Sleep(time.Second) // mint-second strictly after the stamp
	freshTok, err := a.issueTOTPToken(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	nextStep := time.Now().Add(time.Duration(31-time.Now().Unix()%30) * time.Second)
	freshCode, _ := totp.Code(secret, nextStep)
	if rec := post(freshTok, freshCode); rec.Code != http.StatusOK {
		t.Fatalf("post-lockout verify: %d (want 200)", rec.Code)
	}
	if a.totpVerifyFails[u.ID] != 0 {
		t.Error("success left the fail counter")
	}
}
