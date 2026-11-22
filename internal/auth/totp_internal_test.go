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
	uid, nonce, err := a.parseTOTPToken(tok)
	if err != nil {
		t.Fatalf("parse own token: %v", err)
	}
	if uid != "user-42" || nonce == "" {
		t.Fatalf("claims = %q/%q", uid, nonce)
	}

	parts := strings.Split(tok, ".")

	// Tampered payload (different user) → reject.
	bad := "v1.attacker." + parts[2] + "." + parts[3] + "." + parts[4]
	if _, _, err := a.parseTOTPToken(bad); err == nil {
		t.Error("tampered token accepted")
	}
	// Expired but correctly signed → reject.
	expiredPayload := "v1.user-42." + strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10) + "." + parts[3]
	if _, _, err := a.parseTOTPToken(expiredPayload + "." + a.totpTokenMAC(expiredPayload)); err == nil {
		t.Error("expired token accepted")
	}
	for _, junk := range []string{"", "v1", "nope.nope.nope.nope.nope", parts[0] + "." + parts[1] + "." + parts[2] + "." + parts[3]} {
		if _, _, err := a.parseTOTPToken(junk); err == nil {
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
