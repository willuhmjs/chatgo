package auth

import (
	"context"
	"encoding/json"
	"net/http"

	"chatgo/internal/store"
)

// Context keys for the attached user. Exported accessors only.
type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeySession
)

// Attach is OPTIONAL-auth middleware: when a valid session cookie is present
// the user lands in context; anonymous requests pass through.
//
// Scaffold stub: no-op pass-through. BE-auth implements cookie → session →
// user lookup (+ throttled last_active touch).
func (a *Auth) Attach(next http.Handler) http.Handler {
	return next // CONTRACT-DEVIATION (scaffold): stub per task brief; BE-auth owns this
}

// Require is MANDATORY-auth middleware: sets user+session in context or
// answers 401 {error}. Pending users get 403 at the route-group layer.
//
// Scaffold stub: always 401 (nothing is authenticated yet).
func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAuthError(w, http.StatusUnauthorized, "authentication required")
	})
}

// RequireAdmin wraps Require for admin groups: 401 anonymous, 403 non-admin
// (contract §Non-negotiables).
//
// Scaffold stub: always 401 (no user can attach yet).
func (a *Auth) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAuthError(w, http.StatusUnauthorized, "authentication required")
	})
}

// UserFrom returns the attached user (nil,false when anonymous).
// Scaffold stub: always (nil,false).
func (a *Auth) UserFrom(ctx context.Context) (*store.User, bool) {
	u, ok := ctx.Value(ctxKeyUser).(*store.User)
	return u, ok
}

// SessionFrom returns the attached session (nil,false when anonymous).
func (a *Auth) SessionFrom(ctx context.Context) (*store.Session, bool) {
	s, ok := ctx.Value(ctxKeySession).(*store.Session)
	return s, ok
}

// writeAuthError mirrors internal/api's error envelope; kept local because
// internal/api imports internal/auth (import cycle otherwise).
func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
