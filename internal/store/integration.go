package store

import "context"

// Count returns the number of user accounts. Used for the first-user-becomes-
// admin bootstrap (integration): the very first account — local or OIDC — is
// promoted to admin so the instance is never ownerless.
func (r UsersRepo) Count(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// DeleteAllForUser revokes every session of a user (password rotation,
// "sign out everywhere"). Returns nil when the user had no sessions.
func (r SessionsRepo) DeleteAllForUser(ctx context.Context, userID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}
