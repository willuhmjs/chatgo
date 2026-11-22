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

const messageCols = "id, chat_id, role, content, sources_json, error, tokens_in, tokens_out, created_at"

func scanMessage(scan func(...any) error) (*Message, error) {
	m := &Message{}
	var errMsg sql.NullString
	if err := scan(&m.ID, &m.ChatID, &m.Role, &m.Content, &m.SourcesJSON,
		&errMsg, &m.TokensIn, &m.TokensOut, &m.CreatedAt); err != nil {
		return nil, err
	}
	if errMsg.Valid {
		m.Error = &errMsg.String
	}
	return m, nil
}

// Append inserts a message; fills ID/CreatedAt when zero.
func (r *MessagesRepo) Append(ctx context.Context, m *Message) error {
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = nowUnix()
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO messages (id, chat_id, role, content, sources_json, error, tokens_in, tokens_out, created_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)`,
		m.ID, m.ChatID, m.Role, m.Content, m.SourcesJSON, nullStr(m.Error), m.TokensIn, m.TokensOut, m.CreatedAt)
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
