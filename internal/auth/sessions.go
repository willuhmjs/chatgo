package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"chatgo/internal/config"
	"chatgo/internal/store"
)

// Session cookie + token primitives (shared by OIDC and local login).

// CookieName is the frozen session cookie name (contract §Stack).
const CookieName = "chatgo_session"

// SessionTTL is the default session lifetime.
const SessionTTL = 30 * 24 * time.Hour

// NewToken generates a 32-byte URL-safe opaque token (contract §Stack).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateSession issues a DB session row for a user with UA/IP stamped.
// Scaffold implements row creation; BE-auth calls this from login/callback.
func (a *Auth) CreateSession(ctx context.Context, userID, userAgent, ip string) (*store.Session, error) {
	token, err := NewToken()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s := &store.Session{
		Token:      token,
		UserID:     userID,
		CreatedAt:  now.Unix(),
		ExpiresAt:  now.Add(SessionTTL).Unix(),
		LastSeenAt: now.Unix(),
		UserAgent:  userAgent,
		IP:         ip,
	}
	if err := a.Store.Sessions.Create(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

// cookieSecure decides the Secure flag for auth cookies (session + OIDC
// state): the SECURE_COOKIE override wins, else PUBLIC_URL being https, else
// this very request arriving over https per its first X-Forwarded-Proto hop
// (works without PUBLIC_URL behind a TLS-terminating proxy).
func cookieSecure(cfg *config.Config, r *http.Request) bool {
	switch cfg.SecureCookie {
	case "true":
		return true
	case "false":
		return false
	}
	if cfg.SessionCookieSecure() {
		return true
	}
	if r != nil {
		if proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ","); strings.EqualFold(strings.TrimSpace(proto), "https") {
			return true
		}
	}
	return false
}

// SetSessionCookie writes the chatgo_session cookie (HttpOnly, SameSite=Lax,
// Secure per SECURE_COOKIE / PUBLIC_URL / X-Forwarded-Proto — contract §Stack).
func (a *Auth) SetSessionCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   cookieSecure(a.Cfg, r),
	})
}

// ClearSessionCookie expires the cookie (logout).
func (a *Auth) ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   cookieSecure(a.Cfg, r),
	})
}
