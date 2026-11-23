// Message ratings: POST/DELETE /api/chats/{id}/messages/{mid}/rating and
// GET /api/chats/{id}/ratings. One rating per (message, user) — the POST
// upserts (flip = same call again), the DELETE clears. Caller must own the
// chat (foreign → 404, contract §Non-negotiables) and the message must live
// in it. The GET returns the CALLER's own ratings keyed by message id so the
// FE can paint the thumb buttons when painting a transcript; admin-side
// aggregate stats are wave 2 (FEATURES-V2 §BE-SOCIAL).

package library

import (
	"net/http"
	"time"

	"chatgo/internal/api/httpx"

	"github.com/google/uuid"
)

// maxRatingComment caps the optional free-text note (kept small: it ships
// whole in the per-chat ratings map).
const maxRatingComment = 4 << 10

// ratingValueError is the single client-safe message for bad votes.
const ratingValueError = `value must be 1 (thumbs up) or -1 (thumbs down)`

func registerRatings(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("POST /api/chats/{id}/messages/{mid}/rating", d.handleRateMessage)
	mux.HandleFunc("DELETE /api/chats/{id}/messages/{mid}/rating", d.handleDeleteRating)
	mux.HandleFunc("GET /api/chats/{id}/ratings", d.handleChatRatings)
}

type ratingReq struct {
	Value   *int   `json:"value"`
	Comment string `json:"comment"`
}

type ratingJSON struct {
	Value   int    `json:"value"`
	Comment string `json:"comment"`
}

// POST /api/chats/{id}/messages/{mid}/rating {value:-1|1, comment?} → upsert.
func (d *Deps) handleRateMessage(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	mid := r.PathValue("mid")
	if _, err := d.Store.Messages.Get(r.Context(), c.ID, mid); err != nil {
		d.fail(w, r, err) // foreign chat already 404'd; stray mid → ErrNotFound
		return
	}

	var body ratingReq
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Value == nil || (*body.Value != 1 && *body.Value != -1) {
		httpx.ErrorJSON(w, http.StatusBadRequest, ratingValueError)
		return
	}
	if len(body.Comment) > maxRatingComment {
		httpx.ErrorJSON(w, http.StatusBadRequest, "comment too long (max 4KB)")
		return
	}

	// Upsert on (message_id, user_id): a second vote flips the first in
	// place; created_at keeps the original timestamp.
	if _, err := d.Store.DB.ExecContext(r.Context(),
		`INSERT INTO ratings (id, message_id, chat_id, user_id, value, comment, created_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)
		 ON CONFLICT (message_id, user_id) DO UPDATE
		 SET value = excluded.value, comment = excluded.comment`,
		uuid.NewString(), mid, c.ID, u.ID, *body.Value, body.Comment, time.Now().Unix()); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ratingJSON{Value: *body.Value, Comment: body.Comment})
}

// DELETE /api/chats/{id}/messages/{mid}/rating → clear the caller's rating
// (idempotent: 204 whether or not one existed).
func (d *Deps) handleDeleteRating(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if _, err := d.Store.DB.ExecContext(r.Context(),
		`DELETE FROM ratings WHERE chat_id = ?1 AND message_id = ?2 AND user_id = ?3`,
		c.ID, r.PathValue("mid"), u.ID); err != nil {
		d.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/chats/{id}/ratings → {"<message_id>":{"value":-1|1,"comment":…}}
// of the caller's own ratings in this chat (empty object when none).
func (d *Deps) handleChatRatings(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	rows, err := d.Store.DB.QueryContext(r.Context(),
		`SELECT message_id, value, comment FROM ratings WHERE chat_id = ?1 AND user_id = ?2`,
		c.ID, u.ID)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	defer rows.Close()

	out := map[string]ratingJSON{}
	for rows.Next() {
		var mid string
		var v ratingJSON
		if err := rows.Scan(&mid, &v.Value, &v.Comment); err != nil {
			d.fail(w, r, err)
			return
		}
		out[mid] = v
	}
	if err := rows.Err(); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
