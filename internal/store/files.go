package store

// Files repo (BE-data, contract §Store): INSERT/GET/LIST/DELETE over the
// `files` table. Disk side-effects (writing/removing bytes under DataDir) are
// the files service's job (internal/files), not this repo's.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const fileCols = "id, user_id, chat_id, name, mime, size, path, kind, created_at"

func scanFile(scan func(...any) error) (*File, error) {
	f := &File{}
	var chat sql.NullString
	if err := scan(&f.ID, &f.UserID, &chat, &f.Name, &f.Mime, &f.Size, &f.Path, &f.Kind, &f.CreatedAt); err != nil {
		return nil, err
	}
	if chat.Valid {
		f.ChatID = &chat.String
	}
	return f, nil
}

// Insert stores a file row; fills ID/CreatedAt when zero. Path is expected
// relative to DataDir (e.g. "files/<uuid>.png").
func (r *FilesRepo) Insert(ctx context.Context, f *File) error {
	if f.ID == "" {
		f.ID = uuid.NewString()
	}
	if f.CreatedAt == 0 {
		f.CreatedAt = nowUnix()
	}
	if f.Kind == "" {
		f.Kind = FileKindUpload
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO files (id, user_id, chat_id, name, mime, size, path, kind, created_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)`,
		f.ID, f.UserID, nullStr(f.ChatID), f.Name, f.Mime, f.Size, f.Path, f.Kind, f.CreatedAt)
	return err
}

// Get fetches a file row by id.
func (r *FilesRepo) Get(ctx context.Context, id string) (*File, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+fileCols+` FROM files WHERE id = ?1`, id)
	f, err := scanFile(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

// ListByUser returns a user's files newest-first, paginated (per defaults 30,
// capped at 100 — contract non-negotiable). kind == "" lists all kinds;
// otherwise filter to one of FileKind*. It returns the page plus the total
// count matching the filter.
func (r *FilesRepo) ListByUser(ctx context.Context, userID, kind string, page, per int) ([]*File, int, error) {
	if page < 1 {
		page = 1
	}
	if per <= 0 {
		per = defaultPerPage
	}
	if per > maxPerPage {
		per = maxPerPage
	}

	where := []string{"user_id = ?1"}
	args := []any{userID}
	if kind != "" {
		args = append(args, kind)
		where = append(where, fmt.Sprintf("kind = ?%d", len(args)))
	}
	join := strings.Join(where, " AND ")

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE `+join, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, per, (page-1)*per)
	query := fmt.Sprintf(`SELECT `+fileCols+` FROM files WHERE %s ORDER BY created_at DESC, id DESC LIMIT ?%d OFFSET ?%d`,
		join, len(args)-1, len(args))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*File
	for rows.Next() {
		f, err := scanFile(rows.Scan)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, f)
	}
	return out, total, rows.Err()
}

// Delete removes a file row. Callers must delete the on-disk bytes themselves
// (the files service does row-then-disk so a failed unlink never orphans a row).
func (r *FilesRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM files WHERE id = ?1`, id)
	return affected(res, err)
}
