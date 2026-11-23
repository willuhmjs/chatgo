// Session tokens are stored as HashToken(token) (hex SHA-256) so a DB dump
// never leaks live cookies (SECURITY.md). Callers keep passing the PLAINTEXT
// cookie token to Create/Get/Touch/Delete: Create stores only the hash and
// leaves s.Token untouched (CreateSession hands that plaintext to the cookie),
// and Get echoes the presented plaintext back into Session.Token so callers
// can round-trip it into Touch/Delete. Raw queries against sessions.token
// must hash their lookup value first.
package store

import (
	"context"
	"database/sql"
	"errors"
)

// Create stores a session; Token must already be generated (see auth.NewToken).
// The plaintext token is hashed for the token column; s.Token is left as-is.
func (r *SessionsRepo) Create(ctx context.Context, s *Session) error {
	if s.Token == "" {
		return errors.New("store: session token must not be empty")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, created_at, expires_at, last_seen_at, user_agent, ip)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)`,
		HashToken(s.Token), s.UserID, s.CreatedAt, s.ExpiresAt, s.LastSeenAt, s.UserAgent, s.IP)
	return err
}

// Get fetches a session by (plaintext) token; expired sessions are filtered
// out. Session.Token in the result is the presented plaintext token, not the
// stored hash.
func (r *SessionsRepo) Get(ctx context.Context, token string, now int64) (*Session, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	row := r.db.QueryRowContext(ctx,
		`SELECT user_id, created_at, expires_at, last_seen_at, user_agent, ip
		 FROM sessions WHERE token = ?1 AND expires_at > ?2`, HashToken(token), now)
	s := &Session{Token: token}
	err := row.Scan(&s.UserID, &s.CreatedAt, &s.ExpiresAt, &s.LastSeenAt, &s.UserAgent, &s.IP)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

// Touch bumps last_seen_at (token is the plaintext presented by the client).
func (r *SessionsRepo) Touch(ctx context.Context, token string, at int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ?2 WHERE token = ?1`, HashToken(token), at)
	return affected(res, err)
}

// Delete removes a session (logout); token is the plaintext cookie value.
func (r *SessionsRepo) Delete(ctx context.Context, token string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?1`, HashToken(token))
	return err
}

// DeleteExpired purges sessions expired before now; returns rows removed.
func (r *SessionsRepo) DeleteExpired(ctx context.Context, now int64) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?1`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
