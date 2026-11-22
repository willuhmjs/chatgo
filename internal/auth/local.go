package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"net/mail"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"

	"chatgo/internal/store"
)

// Local (password) auth helpers. bcrypt cost 12 per contract §auth.
// Login is rate-limited 5/min/IP (Auth.loginAllowed) and timing-equalized:
// bcrypt runs even for unknown users / OIDC-only accounts via a dummy hash.

// Sentinel errors; the HTTP layer maps these to status codes, never leaking
// which part failed to unauthenticated callers.
var (
	ErrInvalidCredentials = errors.New("auth: invalid email or password")
	ErrRateLimited        = errors.New("auth: too many login attempts")
	ErrEmailExists        = errors.New("auth: an account with that email already exists")
	ErrRegistrationClosed = errors.New("auth: registration is closed")
	ErrInvalidEmail       = errors.New("auth: invalid email address")
	ErrWeakPassword       = errors.New("auth: password too weak")
	ErrInternal           = errors.New("auth: internal error")
)

// Password policy. bcrypt silently truncates at 72 bytes, so reject longer
// inputs instead of hashing a surprising prefix.
const (
	MinPasswordLen = 8
	MaxPasswordLen = 72
)

// HashPassword returns a bcrypt cost-12 hash.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyPassword reports whether password matches the stored bcrypt hash.
func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// dummyHash keeps failed lookups as slow as failed comparisons (constant-ish
// time on the login path). Generated lazily once, never reused as a real hash.
var (
	dummyOnce sync.Once
	dummyHash []byte
)

func burnBcrypt(password string) {
	dummyOnce.Do(func() {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return
		}
		if h, err := bcrypt.GenerateFromPassword(b, 12); err == nil {
			dummyHash = h
		}
	})
	if len(dummyHash) > 0 {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
	}
}

// Login verifies local credentials. The per-IP 5/min bucket is consumed first;
// unknown email and wrong password are indistinguishable (ErrInvalidCredentials).
func (a *Auth) Login(ctx context.Context, ip, email, password string) (*store.User, error) {
	if !a.loginAllowed(ip) {
		return nil, ErrRateLimited
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		burnBcrypt(password)
		return nil, ErrInvalidCredentials
	}
	u, err := a.Store.Users.GetByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		burnBcrypt(password) // same cost as a real comparison
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if !u.HasPassword() {
		burnBcrypt(password) // OIDC-only account: don't leak via timing
		return nil, ErrInvalidCredentials
	}
	if !VerifyPassword(*u.PasswordHash, password) {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}

// Register creates a local account per the configured Registration policy:
// open → role user, approve → role pending, closed → ErrRegistrationClosed.
func (a *Auth) Register(ctx context.Context, email, password, name string) (*store.User, error) {
	var role string
	switch a.Cfg.Registration {
	case "open":
		role = store.RoleUser
	case "approve":
		role = store.RolePending
	default: // "closed" (config.Load validates the set)
		return nil, ErrRegistrationClosed
	}

	email = strings.ToLower(strings.TrimSpace(email))
	if !validEmail(email) {
		return nil, ErrInvalidEmail
	}
	if len(password) < MinPasswordLen || len(password) > MaxPasswordLen {
		return nil, ErrWeakPassword
	}

	switch _, err := a.Store.Users.GetByEmail(ctx, email); {
	case err == nil:
		return nil, ErrEmailExists
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}

	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}

	name = strings.TrimSpace(name)
	if name == "" {
		name = email[:strings.IndexByte(email, '@')]
	}
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}

	// Bootstrap: the very first account becomes admin (instance can never be
	// ownerless under approve/closed policies).
	if n, err := a.Store.Users.Count(ctx); err == nil && n == 0 {
		role = store.RoleAdmin
	}

	u := &store.User{Email: email, Name: name, Role: role, PasswordHash: &hash}
	if err := a.Store.Users.Insert(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

func validEmail(email string) bool {
	if email == "" || len(email) > 254 || strings.ContainsAny(email, " \t") {
		return false
	}
	_, err := mail.ParseAddress(email)
	return err == nil && strings.Contains(email, "@")
}
