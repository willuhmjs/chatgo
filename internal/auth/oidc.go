package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/oauth2"

	"chatgo/internal/audit"
	"chatgo/internal/config"
	"chatgo/internal/store"
)

// oidcStateKeyInfo domain-separates the state-cookie HMAC key from every
// other use of SESSION_SECRET (HKDF-SHA256, empty salt).
const oidcStateKeyInfo = "chatgo:oidc-state/v1"

// oidcFlow holds code-flow material for the single configured SSO provider.
// BE-auth owns the flow (state+nonce cookie, exchange, claims mapping).
type oidcFlow struct {
	cfg    *config.Config
	secret []byte
	oauth2 *oauth2.Config

	// stateKey = HKDF-SHA256(secret, nil, oidcStateKeyInfo); never the raw
	// secret, so this HMAC cannot be replayed against other secret users.
	stateKey []byte

	mu    sync.Mutex
	prov  *oidc.Provider
	verif *oidc.IDTokenVerifier
}

func newOIDCFlow(cfg *config.Config, secret []byte) (*oidcFlow, error) {
	stateKey := make([]byte, 32)
	if _, err := hkdf.New(sha256.New, secret, nil, []byte(oidcStateKeyInfo)).Read(stateKey); err != nil {
		return nil, fmt.Errorf("auth: derive oidc state key: %w", err)
	}
	return &oidcFlow{
		cfg:      cfg,
		secret:   secret,
		stateKey: stateKey,
		oauth2: &oauth2.Config{
			ClientID:     cfg.OIDC.ClientID,
			ClientSecret: cfg.OIDC.ClientSecret,
			Endpoint:     oauth2.Endpoint{}, // filled lazily by providerFor
			// RedirectURL is computed per request (config may omit PUBLIC_URL).
			Scopes: []string{"openid", "email", "profile"},
		},
	}, nil
}

// stateCookie carries state+nonce+PKCE verifier between login and callback,
// keyed with a derived HMAC of SESSION_SECRET so callbacks cannot be forged
// or replayed cross-flow. The embedded issue timestamp is enforced
// server-side (a browser could keep the cookie past its Max-Age).
const (
	stateCookieName   = "chatgo_oidc_state"
	stateCookiePath   = "/api/auth/oidc"
	stateCookieMaxAge = 600 * time.Second
	// parts: state.nonce.pkce.iat.hmacHex
	oidcStateCookieMax = 5
)

// oidcClaims is the id_token claim subset we consume. email_verified uses the
// zero value (absent → false → denied), per task brief: email must be present
// AND verified.
type oidcClaims struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	PreferredUser string `json:"preferred_username"`
	Picture       string `json:"picture"`
	Nonce         string `json:"nonce"`
}

// providerFor resolves the discovery document lazily and caches provider +
// verifier (boot must stay offline-safe; first login pays the fetch).
func (f *oidcFlow) providerFor(ctx context.Context) (*oidc.Provider, *oidc.IDTokenVerifier, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prov == nil {
		p, err := oidc.NewProvider(ctx, f.cfg.OIDC.Issuer)
		if err != nil {
			return nil, nil, fmt.Errorf("auth: oidc discovery: %w", err)
		}
		f.prov = p
		f.verif = p.Verifier(&oidc.Config{ClientID: f.cfg.OIDC.ClientID})
		f.oauth2.Endpoint = p.Endpoint()
	}
	return f.prov, f.verif, nil
}

// configFor copies the base oauth2 config with the per-request redirect URL.
func (f *oidcFlow) configFor(r *http.Request) *oauth2.Config {
	c := *f.oauth2
	c.RedirectURL = f.redirectURL(r)
	return &c
}

func (f *oidcFlow) redirectURL(r *http.Request) string {
	base := f.cfg.ExternalURL()
	if base == "" {
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return base + "/api/auth/oidc/callback"
}

// OIDCLogin starts the authorization-code flow: GET /api/auth/oidc/login →302.
// PKCE (S256) protects the code even if the browser leaks it; the verifier
// rides along in the HMAC'd state cookie. Failures redirect to /login?error=…
// — never a token in the URL.
func (a *Auth) OIDCLogin(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		redirectLoginError(w, r, "oidc_disabled")
		return
	}
	f := a.oidc
	if _, _, err := f.providerFor(r.Context()); err != nil {
		slog.Error("auth: oidc discovery failed", "err", err)
		redirectLoginError(w, r, "oidc_unavailable")
		return
	}
	state, err := NewToken()
	if err != nil {
		slog.Error("auth: oidc state gen failed", "err", err)
		redirectLoginError(w, r, "oidc_unavailable")
		return
	}
	nonce, err := NewToken()
	if err != nil {
		slog.Error("auth: oidc nonce gen failed", "err", err)
		redirectLoginError(w, r, "oidc_unavailable")
		return
	}
	// base64url(32B): 43 chars, inside PKCE's 43-128 char unreserved set.
	verifier, err := NewToken()
	if err != nil {
		slog.Error("auth: oidc pkce gen failed", "err", err)
		redirectLoginError(w, r, "oidc_unavailable")
		return
	}
	f.setStateCookie(w, r, state, nonce, verifier)
	url := f.configFor(r).AuthCodeURL(state,
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.S256ChallengeOption(verifier),
	)
	http.Redirect(w, r, url, http.StatusFound)
}

// OIDCCallback completes the flow: verify state+nonce, exchange the code
// (with the stored PKCE verifier), verify id_token (signature via oidc
// verifier; nonce by hand), provision or link the user per Registration,
// then issue the session cookie and 302 to /. Users with 2FA enabled get no
// cookie: they are 302'd to /login?totp=1&t=… and must pass
// POST /api/auth/totp first (see oidcTOTPChallenge).
func (a *Auth) OIDCCallback(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		redirectLoginError(w, r, "oidc_disabled")
		return
	}
	f := a.oidc
	ctx := r.Context()

	if r.URL.Query().Get("error") != "" {
		redirectLoginError(w, r, "provider_denied") // provider's own message stays out of our URLs
		return
	}
	code, state := r.URL.Query().Get("code"), r.URL.Query().Get("state")
	c, err := r.Cookie(stateCookieName)
	if code == "" || state == "" || err != nil {
		redirectLoginError(w, r, "oidc_state_expired")
		return
	}
	wantState, nonce, verifier, ok := f.parseStateCookie(c.Value)
	f.clearStateCookie(w, r)
	if !ok {
		redirectLoginError(w, r, "oidc_state_expired")
		return
	}
	if !hmac.Equal([]byte(wantState), []byte(state)) {
		redirectLoginError(w, r, "oidc_state_mismatch")
		return
	}

	_, verifierSet, err := f.providerFor(ctx)
	if err != nil {
		slog.Error("auth: oidc discovery failed at callback", "err", err)
		redirectLoginError(w, r, "oidc_unavailable")
		return
	}
	tok, err := f.configFor(r).Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		slog.Warn("auth: oidc code exchange failed", "err", err)
		redirectLoginError(w, r, "oidc_exchange_failed")
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		redirectLoginError(w, r, "oidc_no_id_token")
		return
	}
	idTok, err := verifierSet.Verify(ctx, rawID) // signature, iss, aud, expiry
	if err != nil {
		slog.Warn("auth: oidc id_token verification failed", "err", err)
		redirectLoginError(w, r, "oidc_id_token_invalid")
		return
	}
	var claims oidcClaims
	if err := idTok.Claims(&claims); err != nil {
		redirectLoginError(w, r, "oidc_id_token_invalid")
		return
	}
	if claims.Nonce != nonce || nonce == "" {
		redirectLoginError(w, r, "oidc_nonce_mismatch")
		return
	}

	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" || !validEmail(email) || !claims.EmailVerified {
		// validEmail also guards the name fallback below (IndexByte must
		// find '@') and keeps junk subjects out of the users table.
		oidcDeniedPage(w, "Your identity provider did not assert a verified email address, which this instance requires.")
		return
	}

	u, err := a.oidcUser(ctx, email, claims)
	if err != nil {
		if errors.Is(err, ErrRegistrationClosed) {
			redirectLoginError(w, r, "registration_closed")
			return
		}
		slog.Error("auth: oidc user provisioning failed", "err", err)
		redirectLoginError(w, r, "oidc_user_failed")
		return
	}

	// Lens-8 M2: an IdP assertion must not bypass 2FA. Same contract as the
	// password path (HandleLogin → totpChallenge): a user with a committed
	// TOTP secret gets NO session cookie here — instead the same stateless
	// totp_token is minted and handed to the SPA, which finishes the login
	// through POST /api/auth/totp (identical finishLogin path afterwards).
	enabled, err := a.totpEnabled(ctx, u.ID)
	if err != nil {
		slog.Error("auth: oidc totp state check failed", "err", err)
		redirectLoginError(w, r, "oidc_user_failed")
		return
	}
	if enabled {
		a.oidcTOTPChallenge(w, r, u)
		return
	}

	sess, err := a.CreateSession(ctx, u.ID, r.UserAgent(), a.RequestIP(r))
	if err != nil {
		slog.Error("auth: oidc session create failed", "err", err)
		redirectLoginError(w, r, "oidc_user_failed")
		return
	}
	a.SetSessionCookie(w, r, sess.Token, time.Unix(sess.ExpiresAt, 0))
	audit.Record(ctx, a.Store.DB, u, audit.ActionLoginSuccess, "method=oidc", a.RequestIP(r))
	http.Redirect(w, r, "/", http.StatusFound)
}

// oidcTOTPChallenge mirrors the password path's totpChallenge for the OIDC
// callback: mint the same stateless v1 totp_token and 302 the SPA login route
// with the challenge in the query (/login?totp=1&t=…).
//
// Token-in-URL risk decision (deliberate, reviewed): the 302 Location is the
// only channel from this server-side redirect to the SPA — no session exists
// yet, so there is nothing to hang server-side challenge state on, and a URL
// fragment would never reach the server-observable code path at all (and the
// SPA still parses via query elsewhere). Exposure is bounded: the token is
// HMAC-signed over "v1.<uid>.<exp>.<nonce>", single-use (nonce is burned when
// POST /api/auth/totp consumes it), TTL-bounded to totpTokenTTL (10 min), and
// useless without a current authenticator code. /login is same-origin, so the
// token never rides a cross-origin Referer, and the SPA scrub-strips the query
// on mount (history.replaceState), so it does not persist in history.
func (a *Auth) oidcTOTPChallenge(w http.ResponseWriter, r *http.Request, u *store.User) {
	token, err := a.issueTOTPToken(u.ID)
	if err != nil {
		slog.Error("auth: oidc totp token mint failed", "err", err)
		redirectLoginError(w, r, "oidc_user_failed")
		return
	}
	audit.Record(r.Context(), a.Store.DB, u, audit.ActionTOTPChallenge, "method=oidc", a.RequestIP(r))
	http.Redirect(w, r, "/login?totp=1&t="+url.QueryEscape(token), http.StatusFound)
}

// oidcUser links an existing account by email, else auto-creates per the
// Registration policy (open → user, approve → pending, closed → refused).
func (a *Auth) oidcUser(ctx context.Context, email string, claims oidcClaims) (*store.User, error) {
	u, err := a.Store.Users.GetByEmail(ctx, email)
	if err == nil {
		return u, nil // auto-link: same email, keep existing role/password
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	var role string
	switch a.Cfg.Registration {
	case "open":
		role = store.RoleUser
	case "approve":
		role = store.RolePending
	default:
		return nil, ErrRegistrationClosed
	}
	// Promotion is bootstrap-only (insertUserBootstrap grants admin iff this is
	// the first account ever). ADMIN_EMAIL deliberately does NOT grant admin
	// here: an attacker who can complete an SSO round trip with the owner's
	// address would otherwise get admin on a running instance.

	name := claims.Name
	if name == "" {
		name = claims.PreferredUser
	}
	if name == "" {
		if at := strings.IndexByte(email, '@'); at > 0 {
			name = email[:at]
		} else {
			name = email
		}
	}
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}

	u = &store.User{Email: email, Name: name, Role: role, AvatarURL: claims.Picture}
	// requireFirst=false: the AUTHGATE §2 first-run bootstrap is register-
	// only; closed OIDC sign-ins stay rejected upstream (see registerUser).
	if err := a.insertUserBootstrap(ctx, u, false); err != nil {
		if errors.Is(err, errEmailTaken) {
			// Lost a registration race for the same address: link the winner.
			return a.Store.Users.GetByEmail(ctx, email)
		}
		return nil, err
	}
	return u, nil
}

// --- state cookie -----------------------------------------------------------

func (f *oidcFlow) setStateCookie(w http.ResponseWriter, r *http.Request, state, nonce, pkce string) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    f.stateCookieValue(state, nonce, pkce, time.Now().Unix()),
		Path:     stateCookiePath,
		MaxAge:   int(stateCookieMaxAge / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   cookieSecure(f.cfg, r),
	})
}

func (f *oidcFlow) clearStateCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     stateCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   cookieSecure(f.cfg, r),
	})
}

// stateCookieValue: tokens are base64url / decimal (never contain "."), so
// the plain 5-part value state.nonce.pkce.iat.hmacHex is unambiguous to split.
func (f *oidcFlow) stateCookieValue(state, nonce, pkce string, iat int64) string {
	payload := state + "." + nonce + "." + pkce + "." + strconv.FormatInt(iat, 10)
	return payload + "." + f.stateHMAC(payload)
}

func (f *oidcFlow) parseStateCookie(v string) (state, nonce, pkce string, ok bool) {
	parts := strings.Split(v, ".")
	if len(parts) != oidcStateCookieMax {
		return "", "", "", false
	}
	payload := strings.Join(parts[:len(parts)-1], ".")
	if !hmac.Equal([]byte(parts[len(parts)-1]), []byte(f.stateHMAC(payload))) {
		return "", "", "", false
	}
	iat, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || iat <= 0 {
		return "", "", "", false
	}
	if age := time.Since(time.Unix(iat, 0)); age > stateCookieMaxAge || age < -stateCookieMaxAge {
		return "", "", "", false // browser Max-Age is advisory; enforce server-side
	}
	return parts[0], parts[1], parts[2], true
}

func (f *oidcFlow) stateHMAC(payload string) string {
	mac := hmac.New(sha256.New, f.stateKey)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// --- browser-facing error surfaces ------------------------------------------

// redirectLoginError sends the SPA login route a fixed error code; raw
// provider errors and all token material stay server-side.
func redirectLoginError(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/login?error="+code, http.StatusFound)
}

// oidcDeniedPage answers 403 with a static page (email missing/invalid/unverified).
func oidcDeniedPage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, `<!doctype html><html><head><title>Sign-in blocked</title></head>`+
		`<body style="font-family:sans-serif;max-width:32rem;margin:4rem auto">`+
		`<h1>Sign-in blocked</h1><p>%s</p><p><a href="/login">Back to login</a></p>`+
		`</body></html>`, html.EscapeString(msg))
}
