package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"time"

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

// SetSessionCookie writes the chatgo_session cookie (HttpOnly, SameSite=Lax,
// Secure per PUBLIC_URL — contract §Stack).
func (a *Auth) SetSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.Cfg.SessionCookieSecure(),
	})
}

// ClearSessionCookie expires the cookie (logout).
func (a *Auth) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.Cfg.SessionCookieSecure(),
	})
}
