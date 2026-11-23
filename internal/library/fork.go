// Chat forking: POST /api/chats/{id}/fork {message_id} duplicates the chat —
// new id, title "Fork of <title>", same provider/model/params — and copies
// every message up to AND INCLUDING the target (rowid order, the same order
// GET /api/chats/{id}/messages uses). Store-level copy only; the completion
// pipeline is never touched.
//
// Transaction choice (documented per task brief): ONE transaction wraps the
// chat insert + all message inserts, so a fork is all-or-nothing (no orphan
// half-chat on mid-copy failure). The messages_fts triggers are ordinary
// table triggers, so the FTS index stays in sync inside the tx too. Messages
// are read into a slice before inserting (interleaving row iteration with
// writes on the single SQLite writer would serialize worse and risks conn
// reentrancy on :memory: pools); memory is bounded by the source chat size,
// and every content was already capped at store.MaxContentLen at Append time.

package library

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"chatgo/internal/api/httpx"
	"chatgo/internal/store"

	"github.com/google/uuid"
)

// maxForkTitleLen caps the generated title (chats.title is uncapped in the
// schema; 300 runes keeps list rows sane).
const maxForkTitleLen = 300

const forkMessageCols = `id, role, content, sources_json, error, tokens_in, tokens_out, created_at`

func registerFork(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("POST /api/chats/{id}/fork", d.handleForkChat)
}

type forkReq struct {
	MessageID string `json:"message_id"`
}

// POST /api/chats/{id}/fork {message_id} → 201 + the new chat in the exact
// GET /api/chats/{id} shape. Foreign chat → 404; message not in the chat →
// 404.
func (d *Deps) handleForkChat(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	var body forkReq
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.MessageID == "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, "message_id is required")
		return
	}
	nc, err := d.forkChat(r.Context(), c, body.MessageID)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toChatJSON(nc))
}

// forkChat copies src (owned by the caller) into a fresh chat containing the
// messages with rowid ≤ the target's, in insertion order.
func (d *Deps) forkChat(ctx context.Context, src *store.Chat, targetMsgID string) (*store.Chat, error) {
	tx, err := d.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	var targetRowid int64
	err = tx.QueryRowContext(ctx,
		`SELECT rowid FROM messages WHERE id = ?1 AND chat_id = ?2`, targetMsgID, src.ID).
		Scan(&targetRowid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpErr(http.StatusNotFound, "message not found in chat")
	}
	if err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT `+forkMessageCols+` FROM messages WHERE chat_id = ?1 AND rowid <= ?2 ORDER BY rowid`,
		src.ID, targetRowid)
	if err != nil {
		return nil, err
	}
	type copyMsg struct {
		role, content, sources string
		errStr                 *string
		tIn, tOut, createdAt   int64
	}
	var msgs []copyMsg
	for rows.Next() {
		var m copyMsg
		var e sql.NullString
		if err := rows.Scan(new(string), &m.role, &m.content, &m.sources, &e, &m.tIn, &m.tOut, &m.createdAt); err != nil {
			rows.Close()
			return nil, err
		}
		if e.Valid {
			m.errStr = &e.String
		}
		// Defensive re-check of the store-wide cap (rows already satisfy it
		// via Messages.Append; never write an over-cap row through the
		// side door).
		if len(m.content) > store.MaxContentLen {
			rows.Close()
			return nil, httpErr(http.StatusConflict, "message exceeds size limit")
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	now := time.Now().Unix()
	nc := &store.Chat{
		ID: uuid.NewString(), UserID: src.UserID, Title: forkTitle(src.Title),
		ProviderID: src.ProviderID, ModelID: src.ModelID, ParamsJSON: src.ParamsJSON,
		CreatedAt: now, UpdatedAt: now,
	}
	if nc.ParamsJSON == "" {
		nc.ParamsJSON = "{}"
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chats (id, user_id, folder_id, title, provider_id, model_id, params_json, pinned, share_token, deleted_at, created_at, updated_at)
		 VALUES (?1, ?2, NULL, ?3, ?4, ?5, ?6, 0, NULL, NULL, ?7, ?8)`,
		nc.ID, nc.UserID, nc.Title, nc.ProviderID, nc.ModelID, nc.ParamsJSON,
		nc.CreatedAt, nc.UpdatedAt); err != nil {
		return nil, err
	}
	for i := range msgs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO messages (id, chat_id, role, content, sources_json, error, tokens_in, tokens_out, created_at)
			 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)`,
			uuid.NewString(), nc.ID, msgs[i].role, msgs[i].content, msgs[i].sources,
			msgs[i].errStr, msgs[i].tIn, msgs[i].tOut, msgs[i].createdAt); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return nc, nil
}

// forkTitle builds "Fork of <title>" with a rune-safe cap.
func forkTitle(orig string) string {
	t := "Fork of " + orig
	if r := []rune(t); len(r) > maxForkTitleLen {
		t = string(r[:maxForkTitleLen])
	}
	return t
}
