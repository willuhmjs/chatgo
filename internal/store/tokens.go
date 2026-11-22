// TokensRepo (BE5 / BE-apimgr per contract §Store): api_tokens CRUD with
// sha256-hashed keys. The plaintext token is returned exactly once at
// creation; everything after that works on the hash. Never log key_hash.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/google/uuid"
)

// HashToken returns hex sha256(token). Used for api_tokens.key_hash and for
// the sessions.token column at rest (see sessions.go); it is the lookup key
// for HashLookup.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Create stores a new api-token row; fills ID/CreatedAt when zero. KeyHash
// must already be computed via HashToken (Create never sees plaintext).
func (r *TokensRepo) Create(ctx context.Context, t *APIToken) error {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = nowUnix()
	}
	if t.KeyHash == "" {
		return errors.New("store: token KeyHash must be precomputed")
	}
	var lastUsed any
	if t.LastUsedAt != nil {
		lastUsed = *t.LastUsedAt
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO api_tokens (id, user_id, name, key_hash, created_at, last_used_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
		t.ID, t.UserID, t.Name, t.KeyHash, t.CreatedAt, lastUsed)
	return err
}

// List returns every token owned by userID (newest first).
func (r *TokensRepo) List(ctx context.Context, userID string) ([]*APIToken, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, user_id, name, key_hash, created_at, last_used_at
		 FROM api_tokens WHERE user_id = ?1 ORDER BY created_at DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIToken
	for rows.Next() {
		t, err := scanToken(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// HashLookup resolves a key hash to its owning user and token row and bumps
// last_used_at (contract: "HashLookup(name→user, updates last_used)").
// Unknown hashes (or owners whose user row vanished) return ErrNotFound.
func (r *TokensRepo) HashLookup(ctx context.Context, keyHash string) (*User, *APIToken, error) {
	if keyHash == "" {
		return nil, nil, ErrNotFound
	}
	// user_cols without id/email (id comes from t.user_id; email tail-appended)
	row := r.db.QueryRowContext(ctx,
		`SELECT t.id, t.user_id, t.name, t.key_hash, t.created_at, t.last_used_at,
		        u.email, u.name, u.role, u.password_hash, u.avatar_url,
		        u.created_at, u.last_active_at
		 FROM api_tokens t JOIN users u ON u.id = t.user_id
		 WHERE t.key_hash = ?1`, keyHash)

	tok := &APIToken{}
	u := &User{}
	var pw sql.NullString
	if err := row.Scan(&tok.ID, &tok.UserID, &tok.Name, &tok.KeyHash, &tok.CreatedAt,
		&tok.LastUsedAt, &u.Email, &u.Name, &u.Role, &pw, &u.AvatarURL,
		&u.CreatedAt, &u.LastActiveAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	u.ID = tok.UserID
	if pw.Valid {
		u.PasswordHash = &pw.String
	}

	if _, err := r.db.ExecContext(ctx,
		`UPDATE api_tokens SET last_used_at = ?2 WHERE id = ?1`, tok.ID, nowUnix()); err != nil {
		return nil, nil, err
	}
	now := nowUnix()
	tok.LastUsedAt = &now
	return u, tok, nil
}

// Delete removes a token, scoped to its owner so callers cannot leak IDs
// across users (a foreign id reads as ErrNotFound, contract §Non-negotiables).
func (r *TokensRepo) Delete(ctx context.Context, id, userID string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?1 AND user_id = ?2`, id, userID)
	return affected(res, err)
}

func scanToken(scan func(...any) error) (*APIToken, error) {
	t := &APIToken{}
	err := scan(&t.ID, &t.UserID, &t.Name, &t.KeyHash, &t.CreatedAt, &t.LastUsedAt)
	return t, err
}
