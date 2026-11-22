package auth

// 2FA plumbing (BE-SEC, docs/FEATURES-V2.md §BE-SEC):
//
//   - users.totp_secret TEXT holds the TOTP secret SEALED with AES-256-GCM
//     under HKDF-SHA256(SESSION_SECRET, "chatgo:totp-atrest/v1") — the same
//     derivation pattern provider api_key_enc uses (store/crypto.go), with a
//     domain-separated info string. The column is added by OUR OWN DDL
//     (PRAGMA table_info probe + ALTER), never a store-owned migration.
//   - Login for a TOTP-enabled user hands back a stateless totp_token —
//     HMAC-SHA256 over "v1.<userID>.<expiry>.<nonce>" keyed by
//     HKDF-SHA256(SESSION_SECRET, "chatgo:totp-token/v1") — instead of a
//     session cookie. POST /api/auth/totp validates it (signature, expiry,
//     single-use nonce, code window, monotonic step) and only then issues
//     the session through the exact finishLogin path of a password login.
//
// RAM discipline: every map here is TTL-swept and/or bounded by user count.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
	"io"

	"chatgo/internal/audit"
	"chatgo/internal/totp"
)

const (
	// Domain separators inside HKDF so these keys can never collide with the
	// provider-key or OIDC-state keys derived from the same secret.
	totpAtRestKeyInfo = "chatgo:totp-atrest/v1"
	totpTokenKeyInfo  = "chatgo:totp-token/v1"

	totpPendingTTL = 10 * time.Minute // setup → enable grace
	totpTokenTTL   = 10 * time.Minute // login → verify grace
	totpIssuer     = "ChatGo"

	// totpPendingCap bounds simultaneously pending setups (single slot per
	// user; hitting the cap means someone is scripting setup — refuse).
	totpPendingCap = 1024

	// Throttle rates (per minute) via the shared Auth.Allow buckets.
	totpConfigPerMin = 5  // /2fa/setup + /2fa/disable per user
	totpEnablePerMin = 10 // /2fa/enable per user
	totpVerifyPerMin = 10 // /api/auth/totp per IP+token (task: 10/min)
)

// pendingTOTP is a generated secret awaiting confirmation via /2fa/enable.
// It lives in memory only (plaintext, TTL-bounded) — never written to the DB.
type pendingTOTP struct {
	secret    string
	expiresAt time.Time
}

var (
	errBadTOTPToken = errors.New("auth: invalid or expired 2FA token")
	errPendingFull  = errors.New("auth: too many pending 2FA setups")
)

// initTOTP derives the 2FA keys and applies the feature-owned users DDL.
// Called from New: a missing/unaddable totp_secret column must fail boot
// (every login reads that column), not rot silently.
func (a *Auth) initTOTP() error {
	a.totpSecretKey = deriveTOTPKey(a.secret, totpAtRestKeyInfo)
	a.totpTokenKey = deriveTOTPKey(a.secret, totpTokenKeyInfo)
	a.pendingTOTP = make(map[string]pendingTOTP)
	a.usedNonces = make(map[string]int64)
	a.totpLastStep = make(map[string]int64)

	if err := ensureTOTPColumn(a.Store.DB); err != nil {
		return fmt.Errorf("auth: users.totp_secret column: %w", err)
	}
	// The audit table is owned by internal/audit; boot it here too so auth's
	// Record calls work even if the integrator wires audit.Register later
	// (CREATE IF NOT EXISTS — idempotent either way).
	if err := audit.Init(a.Store.DB); err != nil {
		return fmt.Errorf("auth: audit init: %w", err)
	}
	return nil
}

func deriveTOTPKey(secret []byte, info string) []byte {
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, secret, nil, []byte(info)), key); err != nil {
		// sha256 HKDF cannot realistically fail on 32 bytes.
		panic("auth: derive " + info)
	}
	return key
}

// ensureTOTPColumn adds users.totp_secret TEXT when absent. SQLite has no
// "ADD COLUMN IF NOT EXISTS", so: PRAGMA table_info probe first, ALTER only
// when missing, and a late duplicate-column error (concurrent boot) is a
// success, not a failure.
func ensureTOTPColumn(db *sql.DB) error {
	has, err := usersHasTOTPColumn(db)
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE users ADD COLUMN totp_secret TEXT`); err != nil {
		if strings.Contains(err.Error(), "duplicate column name") {
			return nil // another process finished the ALTER first
		}
		return err
	}
	return nil
}

func usersHasTOTPColumn(db *sql.DB) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(users)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, typ        string
			dflt             any
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == "totp_secret" {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ---------- sealed secret storage ----------

// sealTOTP encrypts with AES-256-GCM (nonce||ct||tag), base64 for the TEXT
// column — layout mirrors store/crypto.go's provider-key sealing.
func (a *Auth) sealTOTP(plain string) (string, error) {
	block, err := aes.NewCipher(a.totpSecretKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (a *Auth) unsealTOTP(sealed string) (string, error) {
	blob, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("auth: totp blob corrupt: %w", err)
	}
	block, err := aes.NewCipher(a.totpSecretKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(blob) < gcm.NonceSize() {
		return "", errors.New("auth: totp blob too short")
	}
	plain, err := gcm.Open(nil, blob[:gcm.NonceSize()], blob[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("auth: totp decrypt failed (SESSION_SECRET changed?): %w", err)
	}
	return string(plain), nil
}

func (a *Auth) totpCiphertext(ctx context.Context, userID string) (string, error) {
	var v sql.NullString
	err := a.Store.DB.QueryRowContext(ctx, `SELECT totp_secret FROM users WHERE id = ?1`, userID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v.String, nil // "" when NULL = 2FA not configured
}

// totpEnabled reports whether the user has committed a TOTP secret.
func (a *Auth) totpEnabled(ctx context.Context, userID string) (bool, error) {
	ct, err := a.totpCiphertext(ctx, userID)
	return ct != "", err
}

func (a *Auth) setTOTPSecret(ctx context.Context, userID, plain string) error {
	ct, err := a.sealTOTP(plain)
	if err != nil {
		return err
	}
	_, err = a.Store.DB.ExecContext(ctx, `UPDATE users SET totp_secret = ?2 WHERE id = ?1`, userID, ct)
	return err
}

func (a *Auth) clearTOTPSecret(ctx context.Context, userID string) error {
	_, err := a.Store.DB.ExecContext(ctx, `UPDATE users SET totp_secret = NULL WHERE id = ?1`, userID)
	return err
}

// ---------- pending setup (single slot per user) ----------

func (a *Auth) setPendingTOTP(userID, secret string) error {
	now := time.Now()
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	for id, p := range a.pendingTOTP {
		if now.After(p.expiresAt) {
			delete(a.pendingTOTP, id)
		}
	}
	if len(a.pendingTOTP) >= totpPendingCap {
		return errPendingFull
	}
	a.pendingTOTP[userID] = pendingTOTP{secret: secret, expiresAt: now.Add(totpPendingTTL)}
	return nil
}

func (a *Auth) pendingTOTPSecret(userID string) (string, bool) {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	p, ok := a.pendingTOTP[userID]
	if !ok || time.Now().After(p.expiresAt) {
		delete(a.pendingTOTP, userID)
		return "", false
	}
	return p.secret, true
}

func (a *Auth) clearPendingTOTP(userID string) {
	a.pendingMu.Lock()
	delete(a.pendingTOTP, userID)
	a.pendingMu.Unlock()
}

// ---------- totp_token: stateless login→verify handoff ----------

// issueTOTPToken returns "v1.<userID>.<expiryUnix>.<nonce>.<hmacHex>". The
// HMAC covers the whole payload, so a leaked-but-unsigned token is inert.
func (a *Auth) issueTOTPToken(userID string) (string, error) {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload := "v1." + userID + "." + strconv.FormatInt(time.Now().Add(totpTokenTTL).Unix(), 10) +
		"." + hex.EncodeToString(nonce)
	return payload + "." + a.totpTokenMAC(payload), nil
}

func (a *Auth) totpTokenMAC(payload string) string {
	mac := hmac.New(sha256.New, a.totpTokenKey)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// parseTOTPToken verifies signature + expiry and returns (userID, nonce).
func (a *Auth) parseTOTPToken(token string) (userID, nonce string, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 5 || parts[0] != "v1" {
		return "", "", errBadTOTPToken
	}
	want := a.totpTokenMAC(strings.Join(parts[:4], "."))
	if !hmac.Equal([]byte(want), []byte(parts[4])) {
		return "", "", errBadTOTPToken
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", "", errBadTOTPToken
	}
	if parts[1] == "" || parts[3] == "" {
		return "", "", errBadTOTPToken
	}
	return parts[1], parts[3], nil
}

// ---------- nonce replay guard (swept on use, TTL = token TTL) ----------

func (a *Auth) totpMarkNonceUsed(nonce string) bool {
	now := time.Now()
	a.nonceMu.Lock()
	defer a.nonceMu.Unlock()
	for n, exp := range a.usedNonces { // sweep on use: no goroutine, bounded
		if now.Unix() >= exp {
			delete(a.usedNonces, n)
		}
	}
	if _, dup := a.usedNonces[nonce]; dup {
		return false
	}
	a.usedNonces[nonce] = now.Add(totpTokenTTL).Unix()
	return true
}

// ---------- code validation with monotonic step memory ----------

// consumeTOTPCode validates code against the user's committed secret
// (±Window) and enforces the last-used-step memory from docs/FEATURES-V2.md:
// a code whose step is ≤ the last consumed one is a replay. The map holds
// one entry per user (cleared on disable) — capped by user count.
func (a *Auth) consumeTOTPCode(ctx context.Context, userID, code string) bool {
	ct, err := a.totpCiphertext(ctx, userID)
	if err != nil || ct == "" {
		return false
	}
	secret, err := a.unsealTOTP(ct)
	if err != nil {
		return false
	}
	step, ok := totp.ValidateStep(secret, code, time.Now())
	if !ok {
		return false
	}
	a.lastStepMu.Lock()
	defer a.lastStepMu.Unlock()
	if last, seen := a.totpLastStep[userID]; seen && step <= last {
		return false
	}
	a.totpLastStep[userID] = step
	return true
}

func (a *Auth) forgetTOTPState(userID string) {
	a.lastStepMu.Lock()
	delete(a.totpLastStep, userID)
	a.lastStepMu.Unlock()
	a.clearPendingTOTP(userID)
}
