package auth

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"chatgo/internal/store"
)

// Context keys for the attached user. Exported accessors only.
type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeySession
)

// touchInterval is the minimum spacing between last_active_at / last_seen_at
// writes per user/session (contract §auth: throttled ≥60s).
const touchInterval = 60

// resolve maps the request's session cookie to (user, session). Any invalid,
// expired or unknown token yields (nil, nil) — anonymous, not an error.
func (a *Auth) resolve(r *http.Request) (*store.User, *store.Session) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	ctx := r.Context()
	s, err := a.Store.Sessions.Get(ctx, c.Value, time.Now().Unix())
	if err != nil {
		return nil, nil // unknown or expired session → anonymous
	}
	u, err := a.Store.Users.GetByID(ctx, s.UserID)
	if err != nil {
		return nil, nil // orphaned session → anonymous
	}
	return u, s
}

func withIdentity(ctx context.Context, u *store.User, s *store.Session) context.Context {
	ctx = context.WithValue(ctx, ctxKeyUser, u)
	return context.WithValue(ctx, ctxKeySession, s)
}

// Attach is OPTIONAL-auth middleware: when a valid session cookie is present
// the user lands in context; anonymous (or invalid/expired cookie) requests
// pass through untouched.
func (a *Auth) Attach(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, s := a.resolve(r); u != nil {
			a.touch(u.ID, s.Token)
			next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), u, s)))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Require is MANDATORY-auth middleware: sets user+session in context or
// answers 401 {error}. Pending users may only reach /api/auth/me (so the SPA
// can render "awaiting approval"); everything else is 403.
//
// CONTRACT-DEVIATION: the scaffold comment expected the pending check at the
// route-group layer, but api.AuthPolicy (frozen) routes /api/auth/me through
// Require with no pending branch, so the exception lives here.
func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, s := a.resolve(r)
		if u == nil {
			writeAuthError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if u.Role == store.RolePending && r.URL.Path != "/api/auth/me" {
			writeAuthError(w, http.StatusForbidden, "account pending approval")
			return
		}
		a.touch(u.ID, s.Token)
		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), u, s)))
	})
}

// RequireAdmin wraps the Require semantics for admin groups: 401 anonymous,
// 403 for any non-admin role incl. pending (contract §Non-negotiables).
func (a *Auth) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, s := a.resolve(r)
		if u == nil {
			writeAuthError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if !u.IsAdmin() {
			writeAuthError(w, http.StatusForbidden, "admin access required")
			return
		}
		a.touch(u.ID, s.Token)
		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), u, s)))
	})
}

// UserFrom returns the attached user (nil,false when anonymous).
func (a *Auth) UserFrom(ctx context.Context) (*store.User, bool) {
	u, ok := ctx.Value(ctxKeyUser).(*store.User)
	return u, ok
}

// SessionFrom returns the attached session (nil,false when anonymous).
func (a *Auth) SessionFrom(ctx context.Context) (*store.Session, bool) {
	s, ok := ctx.Value(ctxKeySession).(*store.Session)
	return s, ok
}

// touch records activity for the user and session, throttled to one write per
// touchInterval per key. Failures are ignored: an activity stamp must never
// fail a request.
func (a *Auth) touch(userID, sessionToken string) {
	a.throttle("user:"+userID, func(now int64) error {
		return a.Store.Users.Touch(context.Background(), userID, now)
	})
	a.throttle("sess:"+sessionToken, func(now int64) error {
		return a.Store.Sessions.Touch(context.Background(), sessionToken, now)
	})
}

func (a *Auth) throttle(key string, fn func(now int64) error) {
	now := time.Now().Unix()
	if v, ok := a.lastTouch.Load(key); ok {
		if last, isInt := v.(int64); isInt && now-last < touchInterval {
			return
		}
	}
	a.lastTouch.Store(key, now)
	_ = fn(now)
}

// RequestIP extracts the caller IP for rate limiting and session records. It
// prefers the first X-Forwarded-For hop (the app is expected behind a proxy;
// see SECURITY.md), falling back to the transport peer address.
func RequestIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, _ := strings.Cut(xff, ","); strings.TrimSpace(first) != "" {
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// writeAuthError mirrors internal/api's error envelope; kept local because
// internal/api imports internal/auth (import cycle otherwise).
func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
