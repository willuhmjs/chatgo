package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned by Get-style methods when no row matches.
var ErrNotFound = errors.New("store: not found")

func nowUnix() int64 { return time.Now().Unix() }

func nullStr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func scanNullStr(scan func(...any) error, dst *string) error {
	var v sql.NullString
	if err := scan(&v); err != nil {
		return err
	}
	if v.Valid {
		s := v.String
		*dst = s
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

const userCols = "id, email, name, role, password_hash, avatar_url, created_at, last_active_at"

func scanUser(scan func(...any) error) (*User, error) {
	u := &User{}
	var pw sql.NullString
	err := scan(&u.ID, &u.Email, &u.Name, &u.Role, &pw, &u.AvatarURL, &u.CreatedAt, &u.LastActiveAt)
	if err != nil {
		return nil, err
	}
	if pw.Valid {
		u.PasswordHash = &pw.String
	}
	return u, nil
}

// Insert stores a new user; fills ID/CreatedAt when zero.
func (r *UsersRepo) Insert(ctx context.Context, u *User) error {
	if u.ID == "" {
		u.ID = uuid.NewString()
	}
	if u.CreatedAt == 0 {
		u.CreatedAt = nowUnix()
	}
	if u.Role == "" {
		u.Role = RoleUser
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO users (id, email, name, role, password_hash, avatar_url, created_at, last_active_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
		u.ID, u.Email, u.Name, u.Role, nullStr(u.PasswordHash), u.AvatarURL, u.CreatedAt, u.LastActiveAt)
	return err
}

func (r *UsersRepo) get(ctx context.Context, query, arg string) (*User, error) {
	var row *sql.Row
	switch query {
	case "id":
		row = r.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?1`, arg)
	default: // "email"
		row = r.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?1`, arg)
	}
	u, err := scanUser(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// GetByID fetches a user by primary key.
func (r *UsersRepo) GetByID(ctx context.Context, id string) (*User, error) {
	return r.get(ctx, "id", id)
}

// GetByEmail fetches a user by email (case-insensitive via COLLATE NOCASE).
func (r *UsersRepo) GetByEmail(ctx context.Context, email string) (*User, error) {
	return r.get(ctx, "email", email)
}

// UpdateRole sets the role (admin|user|pending).
func (r *UsersRepo) UpdateRole(ctx context.Context, id, role string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE users SET role = ?2 WHERE id = ?1`, id, role)
	return affected(res, err)
}

// List returns all users ordered by creation (newest first).
func (r *UsersRepo) List(ctx context.Context) ([]*User, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListPending returns users awaiting approval.
func (r *UsersRepo) ListPending(ctx context.Context) ([]*User, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+userCols+` FROM users WHERE role = ?1 ORDER BY created_at ASC, id`, RolePending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Touch updates last_active_at.
func (r *UsersRepo) Touch(ctx context.Context, id string, at int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE users SET last_active_at = ?2 WHERE id = ?1`, id, at)
	return affected(res, err)
}

// SetPassword stores (or clears, hash=="") the bcrypt password hash.
func (r *UsersRepo) SetPassword(ctx context.Context, id, hash string) error {
	var h any
	if hash != "" {
		h = hash
	}
	res, err := r.db.ExecContext(ctx, `UPDATE users SET password_hash = ?2 WHERE id = ?1`, id, h)
	return affected(res, err)
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
