package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"chatgo/internal/store"
)

// Local (password) auth helpers. bcrypt cost 12 per contract §auth.
// Login and Register are rate-limited (Auth.Allow / loginAllowed) and share
// the process-wide bcrypt semaphore (Auth.AcquireBcrypt) so floods queue
// instead of saturating CPU. Timing is equalized: bcrypt runs even for
// unknown users / OIDC-only accounts via a dummy hash.

// Sentinel errors; the HTTP layer maps these to status codes, never leaking
// which part failed to unauthenticated callers.
var (
	ErrInvalidCredentials = errors.New("auth: invalid email or password")
	ErrRateLimited        = errors.New("auth: too many attempts")
	ErrEmailExists        = errors.New("auth: an account with that email already exists")
	ErrRegistrationClosed = errors.New("auth: registration is closed")
	ErrInvalidEmail       = errors.New("auth: invalid email address")
	ErrWeakPassword       = errors.New("auth: password too weak")
	ErrInternal           = errors.New("auth: internal error")
)

// Password policy. bcrypt silently truncates at 72 BYTES (not characters —
// multibyte inputs hit the wall sooner), so reject longer inputs instead of
// hashing a surprising prefix.
const (
	MinPasswordLen = 8
	MaxPasswordLen = 72
)

// HashPassword returns a bcrypt cost-12 hash. Callers on request paths must
// hold an Auth.AcquireBcrypt slot first; this pure helper is also used by
// tests/admin tooling without throttling.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyPassword reports whether password matches the stored bcrypt hash.
// Same semaphore expectation as HashPassword.
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

// rejectBcrypt spends a dummy comparison under the shared bcrypt semaphore —
// so unknown-email floods queue like real ones — then reports the generic
// invalid-credentials error. Returns ctx.Err() when cancelled while queued.
func (a *Auth) rejectBcrypt(ctx context.Context, password string) error {
	release, err := a.AcquireBcrypt(ctx)
	if err != nil {
		return err
	}
	defer release()
	burnBcrypt(password)
	return ErrInvalidCredentials
}

// Login verifies local credentials. The per-IP 5/min bucket is consumed first;
// unknown email and wrong password are indistinguishable (ErrInvalidCredentials).
func (a *Auth) Login(ctx context.Context, ip, email, password string) (*store.User, error) {
	if !a.loginAllowed(ip) {
		return nil, ErrRateLimited
	}
	if len(password) > MaxPasswordLen {
		// bcrypt would silently truncate; reject the whole input instead but
		// spend a compare on its 72-byte prefix to keep timing uniform.
		return nil, a.rejectBcrypt(ctx, password[:MaxPasswordLen])
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		return nil, a.rejectBcrypt(ctx, password)
	}
	u, err := a.Store.Users.GetByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		return nil, a.rejectBcrypt(ctx, password) // same cost as a real comparison
	}
	if err != nil {
		return nil, err
	}
	if !u.HasPassword() {
		return nil, a.rejectBcrypt(ctx, password) // OIDC-only account: don't leak via timing
	}
	release, err := a.AcquireBcrypt(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if !VerifyPassword(*u.PasswordHash, password) {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}

// Register creates a local account per the configured Registration policy:
// open → role user, approve → role pending, closed → ErrRegistrationClosed —
// EXCEPT the first-run bootstrap (docs/AUTHGATE-CONTRACT.md §2): on an
// instance that has never had a user, closed policy still admits exactly one
// admin (the tx below is the authoritative first-ever-user gate), so a fresh
// deployment is not bricked by its default policy.
// The bcrypt cost-12 hash is bounded work: 5/min/IP and 10/min instance-wide
// token buckets gate entry and the shared bcrypt semaphore serializes the
// expensive part, so a registration flood queues instead of pinning CPUs.
func (a *Auth) Register(ctx context.Context, ip, email, password, name string) (*store.User, error) {
	var role string
	requireFirst := false
	switch a.Cfg.Registration {
	case "open":
		role = store.RoleUser
	case "approve":
		role = store.RolePending
	default: // "closed" (config.Load validates the set)
		n, err := a.userCount(ctx)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, ErrRegistrationClosed
		}
		role = store.RoleUser // insertUserBootstrap promotes to admin iff first
		requireFirst = true
	}

	if !a.Allow("register:"+ip, registerPerMin) || !a.Allow("register:global", registerGlobalPerMin) {
		return nil, ErrRateLimited
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

	// Promotion is bootstrap-only: insertUserBootstrap (inside the same insert
	// transaction) grants admin iff this is the first account ever. ADMIN_EMAIL
	// deliberately does NOT grant admin here — otherwise whoever registers that
	// address first (self-registration, or an OIDC provisioning auto-create) owns
	// the instance.
	release, err := a.AcquireBcrypt(ctx)
	if err != nil {
		return nil, err
	}
	hash, err := HashPassword(password)
	release()
	if err != nil {
		return nil, err
	}

	name = strings.TrimSpace(name)
	if name == "" {
		name = email[:strings.IndexByte(email, '@')] // validEmail guarantees '@'
	}
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}

	u := &store.User{Email: email, Name: name, Role: role, PasswordHash: &hash}
	if err := a.insertUserBootstrap(ctx, u, requireFirst); err != nil {
		if errors.Is(err, errEmailTaken) {
			return nil, ErrEmailExists
		}
		return nil, err // ErrRegistrationClosed (lost the bootstrap race) passes through
	}
	return u, nil
}

// userCount is the read-only population probe for the closed-policy
// first-run pre-check. It is a hint only — insertUserBootstrap re-derives
// "first account ever" under the write lock, so racing registrants cannot
// both bootstrap.
func (a *Auth) userCount(ctx context.Context) (int, error) {
	var n int
	if err := a.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// errEmailTaken marks a UNIQUE collision on users.email inside the bootstrap
// transaction; callers translate it to their own sentinel.
var errEmailTaken = errors.New("auth: email already taken")

// insertUserBootstrap atomically inserts u and applies the first-account
// admin bootstrap. The old Count()==0 → insert scheme raced: two concurrent
// first users could both observe an empty table and both become admin. Here
// BEGIN IMMEDIATE takes the write lock up front and a conditional UPDATE
// promotes the row only while it is the only account that has ever existed,
// so exactly one first user can win. On success u.Role reflects the stored
// (possibly promoted) role.
//
// While the lock is held the same transaction also stamps first_run_done for
// the very first account ever (docs/AUTHGATE-CONTRACT.md §2), so
// /api/auth/status flips out of first_run atomically with the bootstrap.
// requireFirst marks callers that may ONLY register as that first account
// (closed Registration policy): when other users exist the transaction
// aborts with ErrRegistrationClosed and the row vanishes with the rollback.
func (a *Auth) insertUserBootstrap(ctx context.Context, u *store.User, requireFirst bool) error {
	if u.ID == "" {
		u.ID = uuid.NewString()
	}
	if u.CreatedAt == 0 {
		u.CreatedAt = time.Now().Unix()
	}
	if u.Role == "" {
		u.Role = store.RoleUser
	}

	conn, err := a.Store.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	var pw any
	if u.PasswordHash != nil {
		pw = *u.PasswordHash
	}
	_, err = conn.ExecContext(ctx,
		`INSERT INTO users (id, email, name, role, password_hash, avatar_url, created_at, last_active_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
		u.ID, u.Email, u.Name, u.Role, pw, u.AvatarURL, u.CreatedAt, u.LastActiveAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.email") {
			return fmt.Errorf("%w (%v)", errEmailTaken, err)
		}
		return err
	}

	if u.Role != store.RoleAdmin {
		// Promote only when this row is the first account (count includes it);
		// the write lock makes the check-then-update race-free.
		if _, err := conn.ExecContext(ctx,
			`UPDATE users SET role = 'admin' WHERE id = ?1
			 AND (SELECT COUNT(*) FROM users) = 1`, u.ID); err != nil {
			return err
		}
		var role string
		if err := conn.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?1`, u.ID).Scan(&role); err != nil {
			return err
		}
		u.Role = role
	}

	// First-run stamp (AUTHGATE §2): still holding the write lock, check
	// whether any OTHER account has ever existed. If not, this insert owns
	// the instance — set first_run_done in the same transaction (covers the
	// open/approve and OIDC first users too, not just the closed bootstrap).
	var others int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id <> ?1`, u.ID).Scan(&others); err != nil {
		return err
	}
	if others == 0 {
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO settings (key, value) VALUES (?1, ?2)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			SettingFirstRunDone, "true"); err != nil {
			return err
		}
		u.Role = store.RoleAdmin // the first account is the admin by definition
	} else if requireFirst {
		// Closed policy and no longer first: lost the bootstrap race (or the
		// pre-check raced). ROLLBACK via the deferred guard, answer 403.
		return ErrRegistrationClosed
	}

	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

func validEmail(email string) bool {
	if email == "" || len(email) > 254 || strings.ContainsAny(email, " \t") {
		return false
	}
	_, err := mail.ParseAddress(email)
	return err == nil && strings.Contains(email, "@")
}
