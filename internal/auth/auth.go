// Package auth provides session auth middleware, OIDC and local login.
// BE-auth owns all logic in this package. The exported surface is frozen by
// docs/CONTRACTS.md:
//   - Attach: optional auth — chatgo_session cookie → user in context
//   - Require: mandatory auth, 401 anonymous, 403 pending (except /api/auth/me)
//   - RequireAdmin: 401 anonymous, 403 non-admin
package auth

import (
	"errors"
	"fmt"
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

	secret []byte // raw SESSION_SECRET (state-cookie HMAC)

	oidc *oidcFlow // nil when cfg.OIDC is disabled

	// Per-IP login throttle, 5/min burst 5 (contract §auth). Idle buckets are
	// evicted after limiterIdleFor so the map cannot grow without bound.
	mu        sync.Mutex
	limiters  map[string]*loginBucket
	lastSweep time.Time

	// lastTouch throttles last_active_at / last_seen_at writes to ≥60s per
	// user/session (contract §auth). key → last write unix seconds.
	lastTouch sync.Map
}

type loginBucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// New constructs Auth. oidc.NewProvider is intentionally NOT called here
// (boot must not require network); discovery happens lazily in oidc.go.
func New(cfg *config.Config, st *store.Store) (*Auth, error) {
	if cfg == nil || st == nil {
		return nil, errors.New("auth: New requires a config and a store")
	}
	secret, err := cfg.SecretBytes()
	if err != nil {
		return nil, fmt.Errorf("auth: session secret: %w", err)
	}
	a := &Auth{Cfg: cfg, Store: st, secret: secret, limiters: map[string]*loginBucket{}}
	if cfg.OIDC.Enabled() {
		a.oidc = newOIDCFlow(cfg, secret)
	}
	return a, nil
}

// AllowLocal reports whether local (password) login is in play. Local login
// always stays available (existing users must reach their account even when
// Registration is "closed"); register policies are enforced in Register.
func (a *Auth) AllowLocal() bool { return true }

// OIDCEnabled reports whether SSO login is offered (drives /api/auth/config).
func (a *Auth) OIDCEnabled() bool { return a.oidc != nil }

// Login throttling: refill of 5 attempts/60s with a burst of 5.
const (
	loginBurst     = 5
	limiterIdleFor = 10 * time.Minute
)

var loginRefill = rate.Every(12 * time.Second)

// loginAllowed reports whether ip may attempt a local login now (5/min/IP,
// contract §auth), creating the bucket when unseen and sweeping idle entries.
func (a *Auth) loginAllowed(ip string) bool {
	now := time.Now()

	a.mu.Lock()
	defer a.mu.Unlock()

	if now.Sub(a.lastSweep) > time.Minute {
		for k, b := range a.limiters {
			if now.Sub(b.lastSeen) > limiterIdleFor {
				delete(a.limiters, k)
			}
		}
		a.lastSweep = now
	}

	b, ok := a.limiters[ip]
	if !ok {
		b = &loginBucket{lim: rate.NewLimiter(loginRefill, loginBurst)}
		a.limiters[ip] = b
	}
	b.lastSeen = now
	return b.lim.Allow()
}

// ErrUnauthenticated surfaces to callers that need a sentinel for "no user".
var ErrUnauthenticated = errors.New("auth: not authenticated")
