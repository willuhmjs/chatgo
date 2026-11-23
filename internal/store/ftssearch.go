// FTS-backed chat search + the pinned-only list variant
// (BE-apichat / BE4 per docs/CONTRACTS.md §Store "SearchMsgs").
//
// User input is turned into a single FTS5 *phrase* (double-quoted, embedded
// quotes doubled), so no operator/syntax character can escape it — the MATCH
// value itself is a bound parameter, never string-interpolated.

package store

import (
	"context"
	"fmt"
	"strings"
)

// ftsPhrase renders raw user text as one safe FTS5 phrase query.
// Control characters become spaces; every `"` is doubled per FTS5 quoting
// rules, and the whole string is wrapped in quotes.
func ftsPhrase(raw string) string {
	raw = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, raw)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return `"` + strings.ReplaceAll(raw, `"`, `""`) + `"`
}

// searchMatchCTE restricts matches to live chats owned by the caller and
// collapses to one row per chat, keyed by the newest matching message.
const searchMatchCTE = `WITH match(cid, last_at) AS (
	SELECT m.chat_id, MAX(m.created_at)
	FROM messages_fts fts
	JOIN messages m ON m.rowid = fts.rowid
	JOIN chats c ON c.id = m.chat_id
	WHERE fts.messages_fts MATCH ?1 AND c.user_id = ?2 AND c.deleted_at IS NULL AND c.archived_at IS NULL
	GROUP BY m.chat_id
)`

// SearchChats full-text searches message content for a user and returns the
// distinct chats containing matches, newest-match first. page is 1-based,
// per defaults to 30 and is capped at 100 (contract §Non-negotiables).
func (r *ChatsRepo) SearchChats(ctx context.Context, userID, q string, page, per int) ([]*Chat, int, error) {
	match := ftsPhrase(q)
	if match == "" {
		return nil, 0, nil
	}
	if page < 1 {
		page = 1
	}
	if per <= 0 {
		per = defaultPerPage
	}
	if per > maxPerPage {
		per = maxPerPage
	}

	var total int
	if err := r.db.QueryRowContext(ctx, searchMatchCTE+` SELECT COUNT(*) FROM match`,
		match, userID).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.db.QueryContext(ctx, searchMatchCTE+
		` SELECT `+chatCols+` FROM match JOIN chats c ON c.id = match.cid
		  ORDER BY match.last_at DESC, match.cid DESC LIMIT ?3 OFFSET ?4`,
		match, userID, per, (page-1)*per)
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

// ListPinned is ChatsRepo.List restricted to pinned chats (same filters
// except Q — content search goes through SearchChats).
func (r *ChatsRepo) ListPinned(ctx context.Context, userID string, opts ChatListOptions) ([]*Chat, int, error) {
	if opts.Page < 1 {
		opts.Page = 1
	}
	if opts.Per <= 0 {
		opts.Per = defaultPerPage
	}
	if opts.Per > maxPerPage {
		opts.Per = maxPerPage
	}

	where := []string{"user_id = ?1", "deleted_at IS NULL", "pinned = 1"}
	args := []any{userID}
	add := func(clause string, arg any) {
		args = append(args, arg)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	// Archive lane mirrors List: pinned-archived rows only show in ?archived=1.
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
	join := strings.Join(where, " AND ")

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chats WHERE `+join, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, opts.Per, (opts.Page-1)*opts.Per)
	query := `SELECT ` + chatCols + ` FROM chats WHERE ` + join +
		` ORDER BY updated_at DESC, id DESC LIMIT ?%d OFFSET ?%d`
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
