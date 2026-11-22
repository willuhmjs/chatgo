// Package auth provides session auth middleware, OIDC and local login.
// BE-auth owns all logic in this package. The exported surface is frozen by
// docs/CONTRACTS.md:
//   - Attach: optional auth — chatgo_session cookie → user in context
//   - Require: mandatory auth, 401 anonymous, 403 pending (except /api/auth/me)
//   - RequireAdmin: 401 anonymous, 403 non-admin
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
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

	// trustedNets are cfg.TrustedProxies parsed once at boot; hops inside
	// them are the only X-Forwarded-For entries RequestIP will believe.
	trustedNets []*net.IPNet

	// Keyed token buckets for the login / register / password-change
	// throttles (see Allow). Idle buckets are evicted after limiterIdleFor
	// so the map cannot grow without bound.
	mu        sync.Mutex
	limiters  map[string]*loginBucket
	lastSweep time.Time

	// bcryptSem caps concurrent expensive bcrypt operations (login verify,
	// registration hashing, password rotation) so credential floods queue
	// here instead of saturating every CPU with cost-12 hashes.
	bcryptSem chan struct{}

	// lastTouch throttles last_active_at / last_seen_at writes to ≥60s per
	// user/session (contract §auth). key → time of last write.
	lastTouch sync.Map

	// --- 2FA state (BE-SEC, internal/auth/totp.go) -------------------------
	// All three maps are capped/TTL-swept per docs/FEATURES-V2.md §RAM:
	// pending ≤ totpPendingCap entries, nonces expire with the 10-min token
	// window (swept on use), last-step ≤ one entry per user.

	// totpSecretKey = HKDF-SHA256(SESSION_SECRET, "chatgo:totp-atrest/v1"):
	// AES-256-GCM key sealing users.totp_secret at rest (same HKDF pattern
	// as provider api_key_enc, domain-separated info so keys never collide).
	totpSecretKey []byte
	// totpTokenKey = HKDF-SHA256(SESSION_SECRET, "chatgo:totp-token/v1"):
	// HMAC key for the stateless login→/api/auth/totp handoff token.
	totpTokenKey []byte

	pendingMu   sync.Mutex
	pendingTOTP map[string]pendingTOTP // userID → not-yet-confirmed secret

	nonceMu    sync.Mutex
	usedNonces map[string]int64 // TOTP-token nonce → expiry unix (replay guard)

	lastStepMu   sync.Mutex
	totpLastStep map[string]int64 // userID → last consumed TOTP step (replay)
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
	a := &Auth{
		Cfg:       cfg,
		Store:     st,
		secret:    secret,
		limiters:  map[string]*loginBucket{},
		bcryptSem: make(chan struct{}, bcryptWorkers),
	}
	for _, c := range cfg.TrustedProxies {
		if _, n, err := net.ParseCIDR(c); err == nil {
			a.trustedNets = append(a.trustedNets, n)
		} // config.Load validates the set; bad entries simply trust nothing.
	}
	if cfg.OIDC.Enabled() {
		if a.oidc, err = newOIDCFlow(cfg, secret); err != nil {
			return nil, err
		}
	}
	if err := a.initTOTP(); err != nil {
		return nil, err
	}
	return a, nil
}

// AllowLocal reports whether local (password) login is in play. Local login
// always stays available (existing users must reach their account even when
// Registration is "closed"); register policies are enforced in Register.
func (a *Auth) AllowLocal() bool { return true }

// OIDCEnabled reports whether SSO login is offered (drives /api/auth/config).
func (a *Auth) OIDCEnabled() bool { return a.oidc != nil }

// Throttle configuration. Burst equals rate: a "5/min" bucket admits 5
// immediate attempts, matching the contract's login wording, and later ones
// refill at 12s spacing.
const (
	loginPerMin          = 5  // contract §auth: 5/min/IP login
	registerPerMin       = 5  // per-IP registration attempts
	registerGlobalPerMin = 10 // registrations instance-wide
	PasswordChangePerMin = 5  // per-session /api/me/password attempts
	limiterIdleFor       = 10 * time.Minute
)

// Allow reports whether key may perform one action now under a shared keyed
// token bucket refilling at r actions/minute (burst = r). It backs the
// login/register throttles and admin's password-change gate; a key must
// always be used with the same r. Idle buckets are swept after limiterIdleFor.
func (a *Auth) Allow(key string, r float64) bool {
	if r <= 0 {
		r = 1
	}
	refill := time.Duration(float64(time.Minute) / r)
	burst := int(r)
	if burst < 1 {
		burst = 1
	}
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

	b, ok := a.limiters[key]
	if !ok {
		b = &loginBucket{lim: rate.NewLimiter(rate.Every(refill), burst)}
		a.limiters[key] = b
	}
	b.lastSeen = now
	return b.lim.Allow()
}

// loginAllowed reports whether ip may attempt a local login now (5/min/IP,
// contract §auth).
func (a *Auth) loginAllowed(ip string) bool {
	return a.Allow("login:"+ip, loginPerMin)
}

// bcryptWorkers bounds concurrent cost-12 hashes/compares process-wide. On
// typical hardware this keeps the auth path well under one core.
const bcryptWorkers = 2

// AcquireBcrypt blocks until one of the shared bcrypt slots is free — login
// verification, registration hashing and password rotation all queue here,
// so floods serialize instead of pinning every CPU — or ctx is done. Call the
// returned release func as soon as bcrypt work finishes.
func (a *Auth) AcquireBcrypt(ctx context.Context) (release func(), err error) {
	select {
	case a.bcryptSem <- struct{}{}:
		return func() { <-a.bcryptSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Janitor cadence (main.go runs RunJanitor as a goroutine).
const (
	janitorInterval      = time.Hour
	sessionCleanupWindow = 5 * time.Minute
)

// RunJanitor prunes expired sessions and stale throttle state once at startup
// and then every hour until ctx is cancelled (graceful stop: it returns).
func (a *Auth) RunJanitor(ctx context.Context) {
	t := time.NewTicker(janitorInterval)
	defer t.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		a.janitorPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *Auth) janitorPass(ctx context.Context) {
	dctx, cancel := context.WithTimeout(ctx, sessionCleanupWindow)
	n, err := a.Store.Sessions.DeleteExpired(dctx, time.Now().Unix())
	cancel()
	if err != nil {
		slog.Warn("auth: expired-session cleanup failed", "err", err)
	} else if n > 0 {
		slog.Info("auth: removed expired sessions", "count", n)
	}
	a.sweepThrottleState(time.Now())
}

// sweepThrottleState evicts lastTouch entries older than limiterIdleFor so
// the sync.Map cannot grow without bound (logout and session expiry leave
// keys behind otherwise). Limiter buckets self-sweep inside Allow.
func (a *Auth) sweepThrottleState(now time.Time) {
	a.lastTouch.Range(func(k, v any) bool {
		if t, ok := v.(time.Time); ok && now.Sub(t) > limiterIdleFor {
			a.lastTouch.Delete(k)
		}
		return true
	})
}

// ErrUnauthenticated surfaces to callers that need a sentinel for "no user".
var ErrUnauthenticated = errors.New("auth: not authenticated")
