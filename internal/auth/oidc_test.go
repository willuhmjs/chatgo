package auth_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgo/internal/config"
	"chatgo/internal/store"
)

// fakeOIDC is a minimal RS256 OIDC provider: discovery + JWKS + token
// endpoints, JWTs signed with an in-test RSA key (stdlib only).
type fakeOIDC struct {
	t   *testing.T
	srv *httptest.Server
	key *rsa.PrivateKey
	kid string

	mu            sync.Mutex
	nonce         string
	email         string
	emailVerified bool
	code          string
	codeVerifier  string // captured from the token exchange (PKCE)
}

const (
	oidcClientID     = "test-client"
	oidcClientSecret = "test-client-secret"
	oidcUserEmail    = "sso-user@example.com"
)

func newFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOIDC{t: t, key: key, kid: "test-key", email: oidcUserEmail,
		emailVerified: true, code: "good-code"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("GET /keys", f.keys)
	mux.HandleFunc("POST /token", f.token)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOIDC) issuer() string { return f.srv.URL }

func (f *fakeOIDC) setNonce(n string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nonce = n
}

func (f *fakeOIDC) setEmailVerified(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emailVerified = v
}

func (f *fakeOIDC) setEmail(e string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.email = e
}

func (f *fakeOIDC) capturedVerifier() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.codeVerifier
}

func (f *fakeOIDC) snapshot() (string, string, bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nonce, f.email, f.emailVerified, f.code
}

func (f *fakeOIDC) discovery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                f.issuer(),
		"authorization_endpoint":                f.issuer() + "/authorize",
		"token_endpoint":                        f.issuer() + "/token",
		"jwks_uri":                              f.issuer() + "/keys",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "email", "profile"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"grant_types_supported":                 []string{"authorization_code"},
	})
}

func (f *fakeOIDC) keys(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": f.kid,
			"n":   base64.RawURLEncoding.EncodeToString(f.key.PublicKey.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.PublicKey.E)).Bytes()),
		}},
	})
}

func (f *fakeOIDC) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	nonce, email, verified, wantCode := f.snapshot()
	secret := r.FormValue("client_secret")
	if _, s, ok := r.BasicAuth(); ok {
		secret = s
	}
	// PKCE is mandatory against this fake: an exchange without a verifier is
	// rejected exactly like a real RFC 7636 AS configured for S256.
	verifier := r.FormValue("code_verifier")
	if r.FormValue("grant_type") != "authorization_code" || r.FormValue("code") != wantCode ||
		r.FormValue("client_id") != oidcClientID || secret != oidcClientSecret || verifier == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "invalid_grant"})
		return
	}
	f.mu.Lock()
	f.codeVerifier = verifier
	f.mu.Unlock()
	idTok, err := f.idToken(map[string]any{
		"iss":            f.issuer(),
		"aud":            []string{oidcClientID},
		"sub":            "oidc-user-1",
		"iat":            time.Now().Unix(),
		"exp":            time.Now().Add(10 * time.Minute).Unix(),
		"nonce":          nonce,
		"email":          email,
		"email_verified": verified,
		"name":           "SSO User",
	})
	if err != nil {
		f.t.Errorf("sign id_token: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"access_token": "fake-access-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     idTok,
	})
}

func (f *fakeOIDC) idToken(claims map[string]any) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": f.kid, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	b64 := base64.RawURLEncoding.EncodeToString
	unsigned := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + b64(sig), nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// --- env + flow helpers ------------------------------------------------------

func newOIDCEnv(t *testing.T, fake *fakeOIDC, registration string) *testEnv {
	t.Helper()
	return newTestEnv(t, func(cfg *config.Config) {
		cfg.Registration = registration
		cfg.OIDC = config.OIDCConfig{
			Issuer:       fake.issuer(),
			ClientID:     oidcClientID,
			ClientSecret: oidcClientSecret,
			DisplayName:  "SSO",
		}
	})
}

// oidcStart hits GET /api/auth/oidc/login and returns (state, nonce,
// codeChallenge) from the 302 Location.
func oidcStart(t *testing.T, env *testEnv, fake *fakeOIDC) (state, nonce, challenge string) {
	t.Helper()
	r := env.get("/api/auth/oidc/login")
	if r.Status != http.StatusFound {
		t.Fatalf("oidc login = %d %q", r.Status, r.Body)
	}
	loc, err := url.Parse(r.Location)
	if err != nil || !strings.HasPrefix(r.Location, fake.issuer()+"/authorize") {
		t.Fatalf("bad oidc redirect: %q", r.Location)
	}
	if got := loc.Query().Get("redirect_uri"); got != env.Srv.URL+"/api/auth/oidc/callback" {
		t.Errorf("redirect_uri = %q", got)
	}
	if rt := loc.Query().Get("response_type"); rt != "code" {
		t.Errorf("response_type = %q", rt)
	}
	if sc := loc.Query().Get("scope"); !strings.Contains(sc, "openid") {
		t.Errorf("scope = %q", sc)
	}
	state, nonce, challenge = loc.Query().Get("state"), loc.Query().Get("nonce"), loc.Query().Get("code_challenge")
	if state == "" || nonce == "" {
		t.Fatalf("missing state/nonce in %q", r.Location)
	}
	// PKCE: S256 challenge must ride on the authorize request.
	if challenge == "" || loc.Query().Get("code_challenge_method") != "S256" {
		t.Errorf("missing PKCE challenge in %q (method %q)", r.Location, loc.Query().Get("code_challenge_method"))
	}
	if !strings.Contains(strings.Join(r.Header.Values("Set-Cookie"), "\n"), "chatgo_oidc_state") {
		t.Errorf("state cookie not set: %v", r.Header.Values("Set-Cookie"))
	}
	return state, nonce, challenge
}

func oidcCallback(env *testEnv, code, state string) *response {
	return env.get("/api/auth/oidc/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state))
}

// --- tests --------------------------------------------------------------------

func TestOIDCHappyPath(t *testing.T) {
	fake := newFakeOIDC(t)
	env := newOIDCEnv(t, fake, "open")
	env.seedFirstUser(t) // keep the provisioned user out of the first-user-admin bootstrap

	state, nonce, _ := oidcStart(t, env, fake)
	fake.setNonce(nonce)

	cb := oidcCallback(env, "good-code", state)
	if cb.Status != http.StatusFound || cb.Location != "/" {
		t.Fatalf("callback = %d %q", cb.Status, cb.Location)
	}
	if !strings.Contains(strings.Join(cb.Header.Values("Set-Cookie"), "\n"), "chatgo_session=") {
		t.Errorf("no session cookie on callback: %v", cb.Header.Values("Set-Cookie"))
	}
	if strings.Contains(cb.Location, "token") {
		t.Error("token leaked into redirect URL")
	}

	var u userViewJSON
	if me := env.get("/api/auth/me"); me.Status != http.StatusOK {
		t.Fatalf("/api/auth/me = %d %q", me.Status, me.Body)
	} else {
		me.json(t, &u)
	}
	if u.Email != oidcUserEmail || u.Role != store.RoleUser {
		t.Errorf("provisioned user = %+v", u)
	}
	stored, err := env.Store.Users.GetByEmail(context.Background(), oidcUserEmail)
	if err != nil {
		t.Fatalf("user not in store: %v", err)
	}
	if stored.HasPassword() {
		t.Error("OIDC user must not have a password hash")
	}
}

func TestOIDCAutoLinkExistingEmail(t *testing.T) {
	fake := newFakeOIDC(t)
	env := newOIDCEnv(t, fake, "open")
	env.seedUser(t, oidcUserEmail, store.RoleAdmin, "") // OIDC-only admin

	state, nonce, _ := oidcStart(t, env, fake)
	fake.setNonce(nonce)
	cb := oidcCallback(env, "good-code", state)
	if cb.Status != http.StatusFound || cb.Location != "/" {
		t.Fatalf("callback = %d %q", cb.Status, cb.Location)
	}

	var u userViewJSON
	env.get("/api/auth/me").json(t, &u)
	if u.Role != store.RoleAdmin {
		t.Errorf("existing user not linked, role = %q", u.Role)
	}
	all, err := env.Store.Users.List(context.Background())
	if err != nil || len(all) != 1 {
		t.Errorf("expected 1 user, got %d (err %v)", len(all), err)
	}
}

func TestOIDCPendingPerApprovePolicy(t *testing.T) {
	fake := newFakeOIDC(t)
	env := newOIDCEnv(t, fake, "approve")
	env.seedFirstUser(t)

	state, nonce, _ := oidcStart(t, env, fake)
	fake.setNonce(nonce)
	cb := oidcCallback(env, "good-code", state)
	if cb.Status != http.StatusFound || cb.Location != "/" {
		t.Fatalf("callback = %d %q", cb.Status, cb.Location)
	}

	var u userViewJSON
	env.get("/api/auth/me").json(t, &u)
	if u.Role != store.RolePending {
		t.Errorf("role = %q, want pending", u.Role)
	}
	if got := env.get("/api/thing"); got.Status != http.StatusForbidden {
		t.Errorf("pending /api/thing = %d, want 403", got.Status)
	}
}

func TestOIDCRegistrationClosedRejectsUnknownEmail(t *testing.T) {
	fake := newFakeOIDC(t)
	env := newOIDCEnv(t, fake, "closed")

	state, nonce, _ := oidcStart(t, env, fake)
	fake.setNonce(nonce)
	cb := oidcCallback(env, "good-code", state)
	if cb.Status != http.StatusFound || !strings.HasPrefix(cb.Location, "/login?error=") {
		t.Fatalf("callback = %d %q, want /login?error=…", cb.Status, cb.Location)
	}
	if _, err := env.Store.Users.GetByEmail(context.Background(), oidcUserEmail); err != store.ErrNotFound {
		t.Errorf("closed policy created a user (err %v)", err)
	}
}

func TestOIDCStateMismatch(t *testing.T) {
	fake := newFakeOIDC(t)
	env := newOIDCEnv(t, fake, "open")

	state, nonce, _ := oidcStart(t, env, fake)
	fake.setNonce(nonce)

	cb := oidcCallback(env, "good-code", "x"+state) // tampered state
	if cb.Status != http.StatusFound || !strings.HasPrefix(cb.Location, "/login?error=") {
		t.Fatalf("callback = %d %q, want /login?error=…", cb.Status, cb.Location)
	}
	if me := env.get("/api/auth/me"); me.Status != http.StatusUnauthorized {
		t.Errorf("session issued despite state mismatch (me = %d)", me.Status)
	}
}

func TestOIDCNonceMismatch(t *testing.T) {
	fake := newFakeOIDC(t)
	env := newOIDCEnv(t, fake, "open")

	state, nonce, _ := oidcStart(t, env, fake)
	_ = nonce
	fake.setNonce("attacker-nonce") // id_token carries a nonce the flow never asked for

	cb := oidcCallback(env, "good-code", state)
	if cb.Status != http.StatusFound || !strings.HasPrefix(cb.Location, "/login?error=oidc_nonce") {
		t.Fatalf("callback = %d %q, want /login?error=oidc_nonce_mismatch", cb.Status, cb.Location)
	}
	if me := env.get("/api/auth/me"); me.Status != http.StatusUnauthorized {
		t.Errorf("session issued despite nonce mismatch (me = %d)", me.Status)
	}
}

func TestOIDCUnverifiedEmailDenied403(t *testing.T) {
	fake := newFakeOIDC(t)
	env := newOIDCEnv(t, fake, "open")

	state, nonce, _ := oidcStart(t, env, fake)
	fake.setNonce(nonce)
	fake.setEmailVerified(false)

	cb := oidcCallback(env, "good-code", state)
	if cb.Status != http.StatusForbidden {
		t.Fatalf("callback = %d, want 403 (body %q)", cb.Status, cb.Body)
	}
	if !strings.Contains(string(cb.Body), "verified") {
		t.Errorf("deny page should explain: %q", cb.Body)
	}
	if _, err := env.Store.Users.GetByEmail(context.Background(), oidcUserEmail); err != store.ErrNotFound {
		t.Errorf("unverified email created a user (err %v)", err)
	}
}

// TestOIDCPKCE pins the full code-challenge/verifier round-trip: the S256
// challenge on /authorize must match the verifier carried through the state
// cookie into the token exchange (the fake AS rejects exchanges without one).
func TestOIDCPKCE(t *testing.T) {
	fake := newFakeOIDC(t)
	env := newOIDCEnv(t, fake, "open")
	env.seedFirstUser(t)

	state, nonce, challenge := oidcStart(t, env, fake)
	fake.setNonce(nonce)

	cb := oidcCallback(env, "good-code", state)
	if cb.Status != http.StatusFound || cb.Location != "/" {
		t.Fatalf("callback = %d %q (fake rejects exchanges without code_verifier)", cb.Status, cb.Location)
	}

	verifier := fake.capturedVerifier()
	if verifier == "" {
		t.Fatal("token exchange carried no code_verifier")
	}
	sum := sha256.Sum256([]byte(verifier))
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != challenge {
		t.Errorf("code_challenge = %q, want S256(verifier) = %q", challenge, got)
	}
}

// A provider asserting a garbage "email" (no '@') must not panic the name
// fallback nor create a user — 403, same as unverified.
func TestOIDCInvalidEmailDenied403(t *testing.T) {
	fake := newFakeOIDC(t)
	env := newOIDCEnv(t, fake, "open")

	state, nonce, _ := oidcStart(t, env, fake)
	fake.setNonce(nonce)
	fake.setEmail("not-an-email") // verified, but no '@' → used to panic

	cb := oidcCallback(env, "good-code", state)
	if cb.Status != http.StatusForbidden {
		t.Fatalf("callback = %d, want 403 (body %q)", cb.Status, cb.Body)
	}
	if _, err := env.Store.Users.GetByEmail(context.Background(), "not-an-email"); err != store.ErrNotFound {
		t.Errorf("invalid email created a user (err %v)", err)
	}
}

// ADMIN_EMAIL grants NOTHING on the OIDC path either (SECREV4 F-4): before
// this it was provisioned as admin deterministically, so anyone able to finish
// an SSO round trip with the owner's address got admin on a running instance.
// The OIDC-provisioned account now follows the plain policy (pending here);
// admin is only ever the first account ever created (empty DB), promoted
// inside the provisioning transaction.
func TestOIDCAdminEmailNeverPromotes(t *testing.T) {
	fake := newFakeOIDC(t)
	env := newOIDCEnv(t, fake, "approve")
	env.seedFirstUser(t) // existing non-admin: first-user bootstrap is NOT why this passes
	env.Cfg.AdminEmail = oidcUserEmail

	state, nonce, _ := oidcStart(t, env, fake)
	fake.setNonce(nonce)
	cb := oidcCallback(env, "good-code", state)
	if cb.Status != http.StatusFound || cb.Location != "/" {
		t.Fatalf("callback = %d %q", cb.Status, cb.Location)
	}

	u, err := env.Store.Users.GetByEmail(context.Background(), oidcUserEmail)
	if err != nil {
		t.Fatalf("admin email user missing: %v", err)
	}
	if u.Role != store.RolePending {
		t.Errorf("ADMIN_EMAIL user role = %q, want pending (no promotion)", u.Role)
	}
}

// The surviving grant on this path: provisioning the FIRST account (empty DB)
// yields admin, policy notwithstanding.
func TestOIDCFirstAccountIsAdminBootstrap(t *testing.T) {
	fake := newFakeOIDC(t)
	env := newOIDCEnv(t, fake, "approve") // no seedFirstUser → this is the first account

	state, nonce, _ := oidcStart(t, env, fake)
	fake.setNonce(nonce)
	cb := oidcCallback(env, "good-code", state)
	if cb.Status != http.StatusFound || cb.Location != "/" {
		t.Fatalf("callback = %d %q", cb.Status, cb.Location)
	}

	u, err := env.Store.Users.GetByEmail(context.Background(), oidcUserEmail)
	if err != nil {
		t.Fatalf("first account missing: %v", err)
	}
	if u.Role != store.RoleAdmin {
		t.Errorf("first account role = %q, want admin", u.Role)
	}
}
