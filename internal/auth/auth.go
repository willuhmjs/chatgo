// Package auth provides session auth middleware, OIDC and local login.
// BE-auth owns all logic in this package. The exported surface below is
// frozen by docs/CONTRACTS.md; scaffold middleware is intentionally inert:
//   - Attach: no-op pass-through (later: reads chatgo_session cookie → user ctx)
//   - Require: always 401 (later: real session check)
//   - RequireAdmin: always 401 (later: 403 for non-admin once attached)
package auth

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"chatgo/internal/config"
	"chatgo/internal/store"
)

// Auth wires config + store into HTTP middleware for every route group.
type Auth struct {
	Cfg   *config.Config
	Store *store.Store

	oidc *oidcFlow // nil when cfg.OIDC is disabled

	// localLoginLimiters: 5/min/IP bucket map (contract §auth). The map is
	// built by scaffold so BE-auth only owns the check itself.
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

// New constructs Auth. oidc.NewProvider is intentionally NOT called here
// (boot must not require network); it happens lazily in oidc.go.
func New(cfg *config.Config, st *store.Store) (*Auth, error) {
	a := &Auth{Cfg: cfg, Store: st, limiters: map[string]*rate.Limiter{}}
	if cfg.OIDC.Enabled() {
		a.oidc = newOIDCFlow(cfg)
	}
	return a, nil
}

// AllowLocal reports whether local (password) login/register is in play.
func (a *Auth) AllowLocal() bool { return true } // BE-auth: policy per Registration

// localLimiter returns the per-IP 5/min login bucket (contract §auth).
func (a *Auth) localLimiter(ip string) *rate.Limiter {
	a.mu.Lock()
	defer a.mu.Unlock()
	l, ok := a.limiters[ip]
	if !ok {
		l = rate.NewLimiter(rate.Every(12*time.Second), 5)
		a.limiters[ip] = l
	}
	return l
}

// ErrUnauthenticated is what stubs surface until BE-auth implements checks.
var ErrUnauthenticated = errors.New("auth: not authenticated")
