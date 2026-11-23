package auth

// Admin-driven 2FA lockout recovery (docs/SECURITY.md's "reset 2FA" path).
// Self-service disable is deliberately gated on a live session AND the
// password, so losing the authenticator device needs an admin escape hatch.
// This is the minimal honest version:
//
//   - clear the sealed TOTP secret (users.totp_secret → NULL) and drop the
//     user's in-memory 2FA state (pending-setup slot + last-step memory);
//   - revoke EVERY session of the target — recovery always forces re-login,
//     so a stolen cookie cannot outlive the reset;
//   - LOCAL (password) accounts additionally get a fresh crypto/rand
//     one-time password: it exists only in the DB hash and this result
//     struct — never in an audit row, log line, or response body cache.
//     OIDC-only accounts keep password_hash NULL (recovery never fabricates
//     a local credential for a SSO identity) and sign in via SSO instead.
//
// Failure ordering: password rotation runs BEFORE the secret clear, so a
// bcrypt/DB failure mid-flight leaves the old 2FA intact (an honest 500 and
// a retry) instead of half-recovering. Audit is fire-and-safe (internal/
// audit never fails the request).

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"chatgo/internal/audit"
	"chatgo/internal/store"
)

// ActionAdminReset2FA is the audit_log action for this flow. (Kept local:
// audit's canonical const block is owned by internal/audit; apimgr call
// sites already use inline action strings for their own events.)
const ActionAdminReset2FA = "admin.2fa_reset"

// RecoveryResult reports what AdminReset2FA actually did.
type RecoveryResult struct {
	// WasEnabled is true when the target had a committed TOTP secret.
	// A reset of a 2FA-less user is still legal (it rotates the password
	// and revokes sessions), which is why the flag is returned, not enforced.
	WasEnabled bool
	// PasswordSet is true only when the target was a local (password)
	// account and NewPassword was installed as its password_hash.
	PasswordSet bool
	// NewPassword is the one-time plaintext password, shown exactly once to
	// the calling admin. Empty unless PasswordSet. Never logged.
	NewPassword string
	// SessionsRevoked counts the target's session rows deleted (pre-counted
	// just before deletion; a concurrent login could add one that also dies
	// or escapes — reporting the pre-count is fine at homelab scale).
	SessionsRevoked int64
}

// AdminReset2FA performs an admin-driven 2FA recovery for targetUserID.
// Authorization (admin role) is the caller's job — the apimgr handler gates
// with requireAdmin and passes itself in as `caller` for the audit row,
// with ip from Auth.RequestIP. Unknown ids return an error wrapping
// store.ErrNotFound so the HTTP layer maps the standard 404.
func (a *Auth) AdminReset2FA(ctx context.Context, targetUserID string, caller *store.User, ip string) (*RecoveryResult, error) {
	u, err := a.Store.Users.GetByID(ctx, targetUserID)
	if err != nil {
		return nil, fmt.Errorf("auth: reset-2fa target: %w", err)
	}
	res := &RecoveryResult{}
	if res.WasEnabled, err = a.totpEnabled(ctx, u.ID); err != nil {
		return nil, fmt.Errorf("auth: reset-2fa probe: %w", err)
	}
	if err := a.Store.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE user_id = ?1`, u.ID).Scan(&res.SessionsRevoked); err != nil {
		return nil, fmt.Errorf("auth: reset-2fa session count: %w", err)
	}

	// Password first (see file header): only accounts that already can sign
	// in locally get a replacement credential.
	if u.HasPassword() {
		pw, err := generateRecoveryPassword()
		if err != nil {
			return nil, err
		}
		release, err := a.AcquireBcrypt(ctx) // shared cost-12 semaphore
		if err != nil {
			return nil, err
		}
		hash, herr := HashPassword(pw)
		release()
		if herr != nil {
			return nil, fmt.Errorf("auth: reset-2fa hash: %w", herr)
		}
		if err := a.Store.Users.SetPassword(ctx, u.ID, hash); err != nil {
			return nil, fmt.Errorf("auth: reset-2fa password: %w", err)
		}
		res.PasswordSet = true
		res.NewPassword = pw
	}

	if err := a.clearTOTPSecret(ctx, u.ID); err != nil {
		return nil, fmt.Errorf("auth: reset-2fa clear: %w", err)
	}
	a.forgetTOTPState(u.ID) // pending-setup slot + last-step memory

	if err := a.Store.Sessions.DeleteAllForUser(ctx, u.ID); err != nil {
		return nil, fmt.Errorf("auth: reset-2fa revoke: %w", err)
	}

	// Detail is deliberately password-free: audit rows are readable through
	// GET /api/audit, so the one-time credential must never reach them.
	method := "sso"
	if res.PasswordSet {
		method = "password"
	}
	audit.Record(ctx, a.Store.DB, caller, ActionAdminReset2FA, "user_id="+u.ID+" method="+method, ip)
	return res, nil
}

// generateRecoveryPassword returns 16 crypto/rand bytes as 22 URL-safe chars
// — comfortably inside [MinPasswordLen, MaxPasswordLen] with no character-
// set surprises for whatever the admin pastes it into.
func generateRecoveryPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: recovery password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
