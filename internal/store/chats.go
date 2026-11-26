package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const chatCols = "id, user_id, folder_id, title, provider_id, model_id, params_json, pinned, share_token, leaf_id, archived_at, deleted_at, created_at, updated_at"

func scanChat(scan func(...any) error) (*Chat, error) {
	c := &Chat{}
	var folder, share sql.NullString
	var deleted, archived sql.NullInt64
	var pinned int
	if err := scan(&c.ID, &c.UserID, &folder, &c.Title, &c.ProviderID, &c.ModelID,
		&c.ParamsJSON, &pinned, &share, &c.LeafID, &archived, &deleted, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if folder.Valid {
		c.FolderID = &folder.String
	}
	if share.Valid {
		c.ShareToken = &share.String
	}
	if archived.Valid {
		a := archived.Int64
		c.ArchivedAt = &a
	}
	if deleted.Valid {
		d := deleted.Int64
		c.DeletedAt = &d
	}
	c.Pinned = pinned != 0
	return c, nil
}

// Create inserts a chat; fills ID/timestamps/params defaults.
func (r *ChatsRepo) Create(ctx context.Context, c *Chat) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	if c.CreatedAt == 0 {
		c.CreatedAt = nowUnix()
	}
	if c.UpdatedAt == 0 {
		c.UpdatedAt = c.CreatedAt
	}
	if c.Title == "" {
		c.Title = "New chat"
	}
	if c.ParamsJSON == "" {
		c.ParamsJSON = "{}"
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO chats (id, user_id, folder_id, title, provider_id, model_id, params_json, pinned, share_token, leaf_id, deleted_at, created_at, updated_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, NULL, ?11, ?12)`,
		c.ID, c.UserID, nullStr(c.FolderID), c.Title, c.ProviderID, c.ModelID, c.ParamsJSON,
		boolInt(c.Pinned), nullStr(c.ShareToken), c.LeafID, c.CreatedAt, c.UpdatedAt)
	return err
}

// Get fetches a live (non-soft-deleted) chat by id.
func (r *ChatsRepo) Get(ctx context.Context, id string) (*Chat, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+chatCols+` FROM chats WHERE id = ?1 AND deleted_at IS NULL`, id)
	c, err := scanChat(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// GetByShare fetches a chat by its public share token (must be shared and live).
func (r *ChatsRepo) GetByShare(ctx context.Context, token string) (*Chat, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+chatCols+` FROM chats WHERE share_token = ?1 AND deleted_at IS NULL`, token)
	c, err := scanChat(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// ChatListOptions are the /api/chats list filters. Empty strings = no filter.
// Q matches the chat TITLE only for now (message-content search is the
// SearchMsgs/FTS path owned by BE-apichat).
type ChatListOptions struct {
	Q        string
	FolderID string // "" = any folder
	TagID    string // "" = any tag
	Archived bool   // true = list archived only; false = exclude archived
	Page     int    // 1-based, default 1
	Per      int    // default 30, max 100 (contract non-negotiable)
}

const (
	defaultPerPage = 30
	maxPerPage     = 100
)

// List returns live chats for a user, newest-updated first, pinned on top.
func (r *ChatsRepo) List(ctx context.Context, userID string, opts ChatListOptions) ([]*Chat, int, error) {
	if opts.Page < 1 {
		opts.Page = 1
	}
	if opts.Per <= 0 {
		opts.Per = defaultPerPage
	}
	if opts.Per > maxPerPage {
		opts.Per = maxPerPage
	}

	where := []string{"user_id = ?1", "deleted_at IS NULL"}
	args := []any{userID}
	add := func(clause string, arg any) {
		args = append(args, arg)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	// Archive is a mutually-exclusive lane: the default list is the active
	// inbox (archived_at IS NULL); ?archived=1 flips to the archive itself.
	if opts.Archived {
		where = append(where, "archived_at IS NOT NULL")
	} else {
		where = append(where, "archived_at IS NULL")
	}
	if opts.FolderID != "" {
		add("folder_id = ?%d", opts.FolderID)
	}
	if opts.TagID != "" {
		add("EXISTS (SELECT 1 FROM chat_tags ct WHERE ct.chat_id = chats.id AND ct.tag_id = ?%d)", opts.TagID)
	}
	if opts.Q != "" {
		add("title LIKE ?%d ESCAPE '\\'", likePattern(opts.Q))
	}
	join := strings.Join(where, " AND ")

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chats WHERE `+join, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, opts.Per, (opts.Page-1)*opts.Per)
	query := `SELECT ` + chatCols + ` FROM chats WHERE ` + join +
		` ORDER BY pinned DESC, updated_at DESC, id DESC LIMIT ?%d OFFSET ?%d`
	query = fmt.Sprintf(query, len(args)-1, len(args))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*Chat
	for rows.Next() {
		c, err := scanChat(rows.Scan)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// likePattern escapes LIKE metacharacters in user input.
func likePattern(q string) string {
	q = strings.ReplaceAll(q, `\`, `\\`)
	q = strings.ReplaceAll(q, "%", `\%`)
	q = strings.ReplaceAll(q, "_", `\_`)
	return "%" + q + "%"
}

// Update writes mutable chat fields (title/model/params/pinned/folder).
func (r *ChatsRepo) Update(ctx context.Context, c *Chat) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE chats SET title = ?2, provider_id = ?3, model_id = ?4, params_json = ?5,
			pinned = ?6, folder_id = ?7, updated_at = ?8
		 WHERE id = ?1 AND deleted_at IS NULL`,
		c.ID, c.Title, c.ProviderID, c.ModelID, c.ParamsJSON, boolInt(c.Pinned),
		nullStr(c.FolderID), nowUnix())
	return affected(res, err)
}

// TouchUpdatedAt bumps updated_at (called after appending messages).
func (r *ChatsRepo) TouchUpdatedAt(ctx context.Context, id string, at int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE chats SET updated_at = ?2 WHERE id = ?1`, id, at)
	return affected(res, err)
}

// Delete soft-deletes a chat (sets deleted_at).
func (r *ChatsRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE chats SET deleted_at = ?2 WHERE id = ?1 AND deleted_at IS NULL`, id, nowUnix())
	return affected(res, err)
}

// SetShare sets or clears (token=="" → NULL) the share token.
func (r *ChatsRepo) SetShare(ctx context.Context, id string, token string) error {
	var t any
	if token != "" {
		t = token
	}
	res, err := r.db.ExecContext(ctx, `UPDATE chats SET share_token = ?2 WHERE id = ?1 AND deleted_at IS NULL`, id, t)
	return affected(res, err)
}

// SetFolder moves a chat to a folder (folderID=="" → NULL = no folder).
func (r *ChatsRepo) SetFolder(ctx context.Context, id string, folderID string) error {
	var f any
	if folderID != "" {
		f = folderID
	}
	res, err := r.db.ExecContext(ctx, `UPDATE chats SET folder_id = ?2 WHERE id = ?1 AND deleted_at IS NULL`, id, f)
	return affected(res, err)
}

// SetLeaf points the chat's active branch at leafID ("" resets to legacy
// linear semantics). Unknown chats and deleted chats are no-ops (ErrNotFound).
func (r *ChatsRepo) SetLeaf(ctx context.Context, id, leafID string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE chats SET leaf_id = ?2 WHERE id = ?1 AND deleted_at IS NULL`, id, leafID)
	return affected(res, err)
}

// SetArchived archives a live chat (stamps archived_at) or restores it (NULL).
// Idempotent both ways: re-archiving keeps the original stamp.
func (r *ChatsRepo) SetArchived(ctx context.Context, id string, archived bool) error {
	var at any
	if archived {
		at = nowUnix()
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE chats SET archived_at = CASE WHEN ?2 = 1 AND archived_at IS NOT NULL
		 THEN archived_at ELSE ?3 END
		 WHERE id = ?1 AND deleted_at IS NULL`, id, boolInt(archived), at)
	return affected(res, err)
}

// ArchiveAll archives every live, unarchived chat the user owns and returns
// how many moved. Already-archived chats keep their original stamp.
func (r *ChatsRepo) ArchiveAll(ctx context.Context, userID string) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE chats SET archived_at = ?2
		 WHERE user_id = ?1 AND deleted_at IS NULL AND archived_at IS NULL`, userID, nowUnix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteAll soft-deletes every live chat the user owns in one lane
// (archived=true → only archived chats, false → only the active inbox)
// and returns how many were deleted.
func (r *ChatsRepo) DeleteAll(ctx context.Context, userID string, archived bool) (int64, error) {
	lane := "archived_at IS NULL"
	if archived {
		lane = "archived_at IS NOT NULL"
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE chats SET deleted_at = ?2
		 WHERE user_id = ?1 AND deleted_at IS NULL AND `+lane, userID, nowUnix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
