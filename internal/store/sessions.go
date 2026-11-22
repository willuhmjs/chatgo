package store

import (
	"context"
	"database/sql"
	"errors"
)

// Create stores a session; Token must already be generated (see auth.NewToken).
func (r *SessionsRepo) Create(ctx context.Context, s *Session) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, created_at, expires_at, last_seen_at, user_agent, ip)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)`,
		s.Token, s.UserID, s.CreatedAt, s.ExpiresAt, s.LastSeenAt, s.UserAgent, s.IP)
	return err
}

// Get fetches a session by token (expired sessions are filtered out).
func (r *SessionsRepo) Get(ctx context.Context, token string, now int64) (*Session, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT token, user_id, created_at, expires_at, last_seen_at, user_agent, ip
		 FROM sessions WHERE token = ?1 AND expires_at > ?2`, token, now)
	s := &Session{}
	err := row.Scan(&s.Token, &s.UserID, &s.CreatedAt, &s.ExpiresAt, &s.LastSeenAt, &s.UserAgent, &s.IP)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

// Touch bumps last_seen_at.
func (r *SessionsRepo) Touch(ctx context.Context, token string, at int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ?2 WHERE token = ?1`, token, at)
	return affected(res, err)
}

// Delete removes a session (logout).
func (r *SessionsRepo) Delete(ctx context.Context, token string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?1`, token)
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
