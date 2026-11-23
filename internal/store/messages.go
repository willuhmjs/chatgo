package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/google/uuid"
)

// Messages are ordered by SQLite rowid (insertion order) — uuid ids carry no
// order. `beforeID` cursors resolve to the referenced row's rowid.

// MaxContentLen is the maximum size of Message.Content (64 KiB). Enforcing it
// at Append bounds history-load and share-payload amplification at the source.
const MaxContentLen = 64 << 10

// ErrContentTooLong is returned by Append when Message.Content exceeds
// MaxContentLen. Handlers map it to HTTP 400.
var ErrContentTooLong = errors.New("store: message content exceeds 64KiB limit")

const messageCols = "id, chat_id, role, parent_id, content, sources_json, error, tokens_in, tokens_out, created_at"

func scanMessage(scan func(...any) error) (*Message, error) {
	m := &Message{}
	var errMsg sql.NullString
	if err := scan(&m.ID, &m.ChatID, &m.Role, &m.ParentID, &m.Content, &m.SourcesJSON,
		&errMsg, &m.TokensIn, &m.TokensOut, &m.CreatedAt); err != nil {
		return nil, err
	}
	if errMsg.Valid {
		m.Error = &errMsg.String
	}
	return m, nil
}

// Append inserts a message; fills ID/CreatedAt when zero. Content longer
// than MaxContentLen is rejected with ErrContentTooLong (nothing is written).
func (r *MessagesRepo) Append(ctx context.Context, m *Message) error {
	if len(m.Content) > MaxContentLen {
		return ErrContentTooLong
	}
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = nowUnix()
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO messages (id, chat_id, role, parent_id, content, sources_json, error, tokens_in, tokens_out, created_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10)`,
		m.ID, m.ChatID, m.Role, m.ParentID, m.Content, m.SourcesJSON, nullStr(m.Error), m.TokensIn, m.TokensOut, m.CreatedAt)
	return err
}

// List returns up to limit messages of a chat in chronological order.
// beforeID (a message id) pages backwards: only messages inserted before it
// are returned; "" = newest.
func (r *MessagesRepo) List(ctx context.Context, chatID string, limit int, beforeID string) ([]*Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+messageCols+` FROM messages
		 WHERE chat_id = ?1
		   AND (?2 = '' OR rowid < (SELECT rowid FROM messages WHERE id = ?2 AND chat_id = ?1))
		 ORDER BY rowid DESC LIMIT ?3`, chatID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(out) // rows came newest-first; callers want chronological
	return out, nil
}

// Count returns the number of messages in a chat (summarize trigger uses it).
func (r *MessagesRepo) Count(ctx context.Context, chatID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE chat_id = ?1`, chatID).Scan(&n)
	return n, err
}

// DeleteFrom removes msgID and everything after it in the chat (regenerate).
// FTS rows are removed by the AFTER DELETE trigger.
func (r *MessagesRepo) DeleteFrom(ctx context.Context, chatID, msgID string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM messages
		 WHERE chat_id = ?1
		   AND rowid >= (SELECT rowid FROM messages WHERE chat_id = ?1 AND id = ?2)`,
		chatID, msgID)
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

// Get fetches one message by id within a chat.
func (r *MessagesRepo) Get(ctx context.Context, chatID, id string) (*Message, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages WHERE chat_id = ?1 AND id = ?2`, chatID, id)
	m, err := scanMessage(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// maxBranchDepth bounds parent/child walks (cycle breaker and work ceiling).
const maxBranchDepth = 1000

// Path returns the branch chain root→leaf for the message leafID (inclusive),
// walking parent_id up to maxBranchDepth steps. A chat whose messages predate
// branching (all parent_id=”) yields just the leaf row; callers fall back to
// List for those. Unknown leafID → ErrNotFound.
func (r *MessagesRepo) Path(ctx context.Context, chatID, leafID string) ([]*Message, error) {
	rows, err := r.db.QueryContext(ctx,
		`WITH RECURSIVE chain(id, parent, depth) AS (
		   SELECT id, parent_id, 0 FROM messages WHERE chat_id = ?1 AND id = ?2
		   UNION ALL
		   SELECT m.id, m.parent_id, c.depth+1 FROM messages m
		     JOIN chain c ON m.chat_id = ?1 AND m.id = c.parent
		  WHERE c.depth < ?3
		 )
		 SELECT `+`m.id, m.chat_id, m.role, m.parent_id, m.content, m.sources_json, m.error, m.tokens_in, m.tokens_out, m.created_at`+`
		   FROM messages m JOIN chain c ON m.id = c.id ORDER BY c.depth DESC`,
		chatID, leafID, maxBranchDepth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

// CurrentPath returns the chat's active branch root→leaf, capped to the
// newest limit rows; it falls back to the linear List when the chat has no
// leaf or the leaf row is gone (legacy chats, truncate flows, empty chats).
// This is THE read path for anything that renders or replays a conversation:
// share pages, exports, forks, memory extraction, prompt assembly.
func (r *MessagesRepo) CurrentPath(ctx context.Context, chatID, leafID string, limit int) ([]*Message, error) {
	if leafID != "" {
		path, err := r.Path(ctx, chatID, leafID)
		if err == nil {
			if limit > 0 && len(path) > limit {
				path = path[len(path)-limit:]
			}
			return path, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return r.List(ctx, chatID, limit, "")
}

// Children returns the direct children of parentID in insertion order — the
// sibling set a client pages through ("1/2") when parentID names the parent.
func (r *MessagesRepo) Children(ctx context.Context, chatID, parentID string) ([]*Message, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+messageCols+` FROM messages WHERE chat_id = ?1 AND parent_id = ?2 ORDER BY rowid`,
		chatID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeepestChild follows the NEWEST child chain from startID down (the branch a
// version switch lands on, mirroring Open WebUI's getDeepestChildId). Returns
// startID itself when the node is a leaf; cyclic chains stop at the first
// revisit. Unknown startID → ErrNotFound.
func (r *MessagesRepo) DeepestChild(ctx context.Context, chatID, startID string) (string, error) {
	if _, err := r.Get(ctx, chatID, startID); err != nil {
		return "", err
	}
	cur, visited := startID, map[string]bool{startID: true}
	for range maxBranchDepth {
		var next string
		err := r.db.QueryRowContext(ctx,
			`SELECT id FROM messages WHERE chat_id = ?1 AND parent_id = ?2 ORDER BY rowid DESC LIMIT 1`,
			chatID, cur).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			return cur, nil // leaf reached
		}
		if err != nil {
			return "", err
		}
		if visited[next] {
			return cur, nil // cycle — stop on the last distinct node
		}
		visited[next] = true
		cur = next
	}
	return cur, nil
}

// DeleteSubtree removes messageID and every descendant (the whole branch
// hanging off it, including sibling continuations). FTS rows go with the
// AFTER DELETE trigger. Returns the number of rows deleted; unknown root →
// ErrNotFound.
func (r *MessagesRepo) DeleteSubtree(ctx context.Context, chatID, rootID string) (int, error) {
	if _, err := r.Get(ctx, chatID, rootID); err != nil {
		return 0, err
	}
	rows, err := r.db.QueryContext(ctx,
		`WITH RECURSIVE sub(id) AS (
		   SELECT id FROM messages WHERE chat_id = ?1 AND id = ?2
		   UNION ALL
		   SELECT m.id FROM messages m JOIN sub s ON m.parent_id = s.id WHERE m.chat_id = ?1
		 )
		 SELECT id FROM sub LIMIT ?3`, chatID, rootID, maxBranchDepth+1)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, ErrNotFound
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	del, err := tx.PrepareContext(ctx, `DELETE FROM messages WHERE id = ?1 AND chat_id = ?2`)
	if err != nil {
		return 0, err
	}
	defer del.Close()
	n := 0
	for _, id := range ids {
		res, err := del.Exec(id, chatID)
		if err != nil {
			return 0, err
		}
		c, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		n += int(c)
	}
	return n, tx.Commit()
}
