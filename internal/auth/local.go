package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// Local (password) auth helpers. bcrypt cost 12 per contract §auth.
// BE-auth wires these into POST /api/auth/login (rate-limited 5/min/IP via
// Auth.localLimiter) and register.

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

// ErrWeakPassword is returned for policy violations (BE-auth defines policy).
var ErrWeakPassword = errors.New("auth: password too weak")
