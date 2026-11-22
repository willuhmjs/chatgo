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
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"chatgo/internal/config"
	"chatgo/internal/store"
)

// oidcFlow holds code-flow material for the single configured SSO provider.
// BE-auth owns the flow (state+nonce cookie, exchange, claims mapping).
type oidcFlow struct {
	cfg    *config.Config
	secret []byte
	oauth2 *oauth2.Config

	mu    sync.Mutex
	prov  *oidc.Provider
	verif *oidc.IDTokenVerifier
}

func newOIDCFlow(cfg *config.Config, secret []byte) *oidcFlow {
	return &oidcFlow{
		cfg:    cfg,
		secret: secret,
		oauth2: &oauth2.Config{
			ClientID:     cfg.OIDC.ClientID,
			ClientSecret: cfg.OIDC.ClientSecret,
			Endpoint:     oauth2.Endpoint{}, // filled lazily by providerFor
			// RedirectURL is computed per request (config may omit PUBLIC_URL).
			Scopes: []string{"openid", "email", "profile"},
		},
	}
}

// stateCookie carries state+nonce between login and callback, keyed with an
// HMAC of SESSION_SECRET so callbacks cannot be forged or replayed cross-flow.
const (
	stateCookieName    = "chatgo_oidc_state"
	stateCookiePath    = "/api/auth/oidc"
	stateCookieMaxAge  = 600 * time.Second
	oidcStateCookieMax = 3 // parts: state.nonce.hmac
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
// Failures redirect to /login?error=… — never a token in the URL.
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
	f.setStateCookie(w, state, nonce)
	url := f.configFor(r).AuthCodeURL(state,
		oauth2.SetAuthURLParam("nonce", nonce),
	)
	http.Redirect(w, r, url, http.StatusFound)
}

// OIDCCallback completes the flow: verify state+nonce, exchange the code,
// verify id_token (signature via oidc verifier; nonce by hand), provision or
// link the user per Registration, then issue the session cookie and 302 to /.
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
	wantState, nonce, ok := f.parseStateCookie(c.Value)
	f.clearStateCookie(w)
	if !ok || !hmac.Equal([]byte(wantState), []byte(state)) {
		redirectLoginError(w, r, "oidc_state_mismatch")
		return
	}

	_, verifier, err := f.providerFor(ctx)
	if err != nil {
		slog.Error("auth: oidc discovery failed at callback", "err", err)
		redirectLoginError(w, r, "oidc_unavailable")
		return
	}
	tok, err := f.configFor(r).Exchange(ctx, code)
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
	idTok, err := verifier.Verify(ctx, rawID) // signature, iss, aud, expiry
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
	if email == "" || !claims.EmailVerified {
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

	sess, err := a.CreateSession(ctx, u.ID, r.UserAgent(), RequestIP(r))
	if err != nil {
		slog.Error("auth: oidc session create failed", "err", err)
		redirectLoginError(w, r, "oidc_user_failed")
		return
	}
	a.SetSessionCookie(w, sess.Token, time.Unix(sess.ExpiresAt, 0))
	http.Redirect(w, r, "/", http.StatusFound)
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

	name := claims.Name
	if name == "" {
		name = claims.PreferredUser
	}
	if name == "" {
		name = email[:strings.IndexByte(email, '@')]
	}
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}

	// Bootstrap: first-ever OIDC login becomes admin (mirrors local.go).
	if n, err := a.Store.Users.Count(ctx); err == nil && n == 0 {
		role = store.RoleAdmin
	}

	u = &store.User{Email: email, Name: name, Role: role, AvatarURL: claims.Picture}
	if err := a.Store.Users.Insert(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

// --- state cookie -----------------------------------------------------------

func (f *oidcFlow) setStateCookie(w http.ResponseWriter, state, nonce string) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    f.stateCookieValue(state, nonce),
		Path:     stateCookiePath,
		MaxAge:   int(stateCookieMaxAge / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   f.cfg.SessionCookieSecure(),
	})
}

func (f *oidcFlow) clearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     stateCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   f.cfg.SessionCookieSecure(),
	})
}

// stateCookieValue: tokens are base64url (never contain "."), so the plain
// 3-part value state.nonce.hmacHex is unambiguous to split.
func (f *oidcFlow) stateCookieValue(state, nonce string) string {
	return state + "." + nonce + "." + f.stateHMAC(state, nonce)
}

func (f *oidcFlow) parseStateCookie(v string) (state, nonce string, ok bool) {
	parts := strings.Split(v, ".")
	if len(parts) != oidcStateCookieMax {
		return "", "", false
	}
	if !hmac.Equal([]byte(parts[2]), []byte(f.stateHMAC(parts[0], parts[1]))) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (f *oidcFlow) stateHMAC(state, nonce string) string {
	mac := hmac.New(sha256.New, f.secret)
	mac.Write([]byte(state))
	mac.Write([]byte("."))
	mac.Write([]byte(nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

// --- browser-facing error surfaces ------------------------------------------

// redirectLoginError sends the SPA login route a fixed error code; raw
// provider errors and all token material stay server-side.
func redirectLoginError(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/login?error="+code, http.StatusFound)
}

// oidcDeniedPage answers 403 with a static page (email missing/unverified).
func oidcDeniedPage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, `<!doctype html><html><head><title>Sign-in blocked</title></head>`+
		`<body style="font-family:sans-serif;max-width:32rem;margin:4rem auto">`+
		`<h1>Sign-in blocked</h1><p>%s</p><p><a href="/login">Back to login</a></p>`+
		`</body></html>`, html.EscapeString(msg))
}
