// Package audit records security-relevant events to the self-trimming
// audit_log table (docs/FEATURES-V2.md §BE-SEC). Design rules:
//
//   - fire-and-safe: Record NEVER fails the calling request; DB errors are
//     logged with slog and dropped. Inserts run on a detached context so a
//     client disconnect cannot swallow the row.
//   - bounded: after every insert the table trims itself to the newest
//     DefaultRetain+1 rows (DELETE everything below max_id - retain).
//   - The DDL belongs to this package (feature-owned schema, coordination
//     law): Init runs CREATE TABLE IF NOT EXISTS via the caller's *sql.DB;
//     nothing in internal/store is touched.
//
// Import discipline: audit must not import internal/auth (auth imports audit
// for its own events → cycle). Handlers that need the caller identity take
// the tiny Resolver interface instead; *auth.Auth satisfies it.
package audit

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"chatgo/internal/store"
)

// DefaultRetain is the trim budget: rows with id < max_id-DefaultRetain are
// deleted after each insert, so the table holds at most DefaultRetain+1 rows
// (~ docs/FEATURES-V2.md "DELETE everything below (max_id - 10_000)").
const DefaultRetain = 10_000

// recordTimeout bounds a detached insert so a wedged SQLite writer can never
// pin the connection pool behind a finished request.
const recordTimeout = 5 * time.Second

// Canonical action strings — auth-owned events. Keep in sync with the
// integrator's apimgr call sites (provider.*, user.*, settings.update,
// token.*, password.change).
const (
	ActionLoginSuccess   = "login.success"
	ActionLoginFail      = "login.fail"
	ActionTOTPChallenge  = "login.totp_challenge"
	ActionTOTPVerifyOK   = "totp.verify.success"
	ActionTOTPVerifyFail = "totp.verify.fail"
	Action2FASetup       = "2fa.setup"
	Action2FAEnable      = "2fa.enable"
	Action2FADisable     = "2fa.disable"
	ActionLogout         = "logout"
)

const ddl = `CREATE TABLE IF NOT EXISTS audit_log (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	ts      INTEGER NOT NULL,
	user_id TEXT NOT NULL DEFAULT '',
	email   TEXT NOT NULL DEFAULT '',
	action  TEXT NOT NULL DEFAULT '',
	detail  TEXT NOT NULL DEFAULT '',
	ip      TEXT NOT NULL DEFAULT ''
)`

// Init creates the audit table (idempotent). Call once at startup; internal/
// auth's New runs it defensively so Record works even if the integrator
// forgets audit.Register.
func Init(db *sql.DB) error {
	if _, err := db.Exec(ddl); err != nil {
		return err
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_action_ts ON audit_log(action, ts)`)
	return err
}

// Record logs one event for user u (nil = anonymous, e.g. failed login).
// It never returns an error: failures are logged, never fatal (fire-and-safe).
func Record(ctx context.Context, db *sql.DB, u *store.User, action, detail, ip string) {
	var userID, email string
	if u != nil {
		userID, email = u.ID, u.Email
	}
	RecordWithRetain(ctx, db, userID, email, action, detail, ip, DefaultRetain)
}

// RecordIdentity is Record for events where no user row exists (unknown
// email at login). detail must be free of secrets — the email column carries
// the identity, so keep detail structural ("invalid credentials").
func RecordIdentity(ctx context.Context, db *sql.DB, userID, email, action, detail, ip string) {
	RecordWithRetain(ctx, db, userID, email, action, detail, ip, DefaultRetain)
}

// RecordWithRetain is the parametric core (retain is the trim window; tests
// shrink it to verify self-trimming without inserting 10k rows).
func RecordWithRetain(ctx context.Context, db *sql.DB, userID, email, action, detail, ip string, retain int) {
	// Detach from the request context: the response may already be written
	// (or the client gone) while this insert matters. Bounded so a wedged
	// DB can never leak the goroutine/conn beyond recordTimeout.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO audit_log (ts, user_id, email, action, detail, ip) VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
		time.Now().Unix(), userID, email, action, detail, ip); err != nil {
		slog.Warn("audit: insert failed", "action", action, "err", err)
		return
	}
	if retain > 0 {
		if _, err := db.ExecContext(ctx,
			`DELETE FROM audit_log WHERE id < (SELECT COALESCE(MAX(id), 0) FROM audit_log) - ?1`,
			retain); err != nil {
			slog.Warn("audit: trim failed", "err", err)
		}
	}
}

// Entry is one audit_log row as serialized by GET /api/audit.
type Entry struct {
	ID     int64  `json:"id"`
	Ts     int64  `json:"ts"`
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Action string `json:"action"`
	Detail string `json:"detail"`
	IP     string `json:"ip"`
}
