// Tags repo (BE-apichat / BE4 per docs/CONTRACTS.md §Store): per-user tag
// CRUD plus the chat↔tag join (SetChatTags / TagsForChats).

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// MaxTagNameLen caps tag names (names are chips in the sidebar; short by design).
const MaxTagNameLen = 64

// ErrDuplicate signals a (user_id, name) uniqueness conflict on tags.
var ErrDuplicate = errors.New("store: duplicate")

const tagCols = "id, user_id, name, color, created_at"

func scanTag(scan func(...any) error) (*Tag, error) {
	t := &Tag{}
	if err := scan(&t.ID, &t.UserID, &t.Name, &t.Color, &t.CreatedAt); err != nil {
		return nil, err
	}
	return t, nil
}

// Create inserts a tag; returns ErrDuplicate when the user already has that
// name (UNIQUE(user_id, name)), ErrInvalidName when blank/oversized.
func (r *TagsRepo) Create(ctx context.Context, t *Tag) error {
	name := strings.TrimSpace(t.Name)
	if name == "" || len([]rune(name)) > MaxTagNameLen {
		return ErrInvalidName
	}
	t.Name = name
	if exists, err := r.byName(ctx, t.UserID, name); err != nil {
		return err
	} else if exists != nil {
		return ErrDuplicate
	}
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = nowUnix()
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO tags (id, user_id, name, color, created_at) VALUES (?1, ?2, ?3, ?4, ?5)`,
		t.ID, t.UserID, t.Name, t.Color, t.CreatedAt)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrDuplicate // lost a race against an identical insert
	}
	return err
}

// Get fetches a tag by id (ownership checked by caller or scoped methods).
func (r *TagsRepo) Get(ctx context.Context, id string) (*Tag, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+tagCols+` FROM tags WHERE id = ?1`, id)
	t, err := scanTag(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// List returns a user's tags ordered by name.
func (r *TagsRepo) List(ctx context.Context, userID string) ([]*Tag, error) {
	return r.queryTags(ctx, `SELECT `+tagCols+` FROM tags WHERE user_id = ?1 ORDER BY name COLLATE NOCASE ASC, id ASC`, userID)
}

func (r *TagsRepo) byName(ctx context.Context, userID, name string) (*Tag, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+tagCols+` FROM tags WHERE user_id = ?1 AND name = ?2`, userID, name)
	t, err := scanTag(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func (r *TagsRepo) queryTags(ctx context.Context, query string, args ...any) ([]*Tag, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Tag
	for rows.Next() {
		t, err := scanTag(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Update writes name/color, owner-scoped (foreign owner → ErrNotFound).
// Renaming onto an existing (user, name) returns ErrDuplicate.
func (r *TagsRepo) Update(ctx context.Context, t *Tag) error {
	name := strings.TrimSpace(t.Name)
	if name == "" || len([]rune(name)) > MaxTagNameLen {
		return ErrInvalidName
	}
	t.Name = name
	if other, err := r.byName(ctx, t.UserID, name); err != nil {
		return err
	} else if other != nil && other.ID != t.ID {
		return ErrDuplicate
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE tags SET name = ?2, color = ?3 WHERE id = ?1 AND user_id = ?4`,
		t.ID, t.Name, t.Color, t.UserID)
	return affected(res, err)
}

// Delete removes an own tag; chat_tags rows follow via ON DELETE CASCADE.
func (r *TagsRepo) Delete(ctx context.Context, id, userID string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM tags WHERE id = ?1 AND user_id = ?2`, id, userID)
	return affected(res, err)
}

// SetChatTags replaces the full tag set of a chat. chatID must be a live
// chat owned by userID and every tagID must be a tag of the same user —
// otherwise ErrNotFound (handlers map that to 404, no existence leak).
func (r *TagsRepo) SetChatTags(ctx context.Context, chatID, userID string, tagIDs []string) error {
	ids := dedupe(tagIDs)

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort after commit

	var owner string
	err = tx.QueryRowContext(ctx,
		`SELECT user_id FROM chats WHERE id = ?1 AND deleted_at IS NULL`, chatID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if owner != userID {
		return ErrNotFound
	}

	if len(ids) > 0 {
		ph := inPlaceholders(len(ids))
		args := append([]any{userID}, toArgs(ids)...)
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tags WHERE user_id = ?1 AND id IN (`+ph+`)`, args...).Scan(&n); err != nil {
			return err
		}
		if n != len(ids) {
			return ErrNotFound // unknown or foreign-owned tag id
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM chat_tags WHERE chat_id = ?1`, chatID); err != nil {
		return err
	}
	for _, tagID := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chat_tags (chat_id, tag_id) VALUES (?1, ?2)`, chatID, tagID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TagsForChats batch-loads tags for the given chats (list endpoint enrichment).
// Chats without tags are absent from the map.
func (r *TagsRepo) TagsForChats(ctx context.Context, chatIDs []string) (map[string][]*Tag, error) {
	out := map[string][]*Tag{}
	ids := dedupe(chatIDs)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT ct.chat_id, `+tagCols+`
		 FROM chat_tags ct JOIN tags t ON t.id = ct.tag_id
		 WHERE ct.chat_id IN (`+inPlaceholders(len(ids))+`)
		 ORDER BY t.name COLLATE NOCASE ASC, t.id ASC`, toArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var chatID string
		t, err := scanTag(func(dest ...any) error { return rows.Scan(append([]any{&chatID}, dest...)...) })
		if err != nil {
			return nil, err
		}
		out[chatID] = append(out[chatID], t)
	}
	return out, rows.Err()
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// inPlaceholders builds "?,?,?" for an IN (...) list (always parameterized).
func inPlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func toArgs(ids []string) []any {
	args := make([]any, len(ids))
	for i, s := range ids {
		args[i] = s
	}
	return args
}
