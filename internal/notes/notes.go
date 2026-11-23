// Package notes is the Notes feature plane: Open WebUI-style scratchpads —
// lightweight markdown documents a user creates, edits, optionally shares
// publicly (read-only at /n/<token>, same anonymous lane as chat shares).
//
// It owns its DDL (created via Init on store.DB — NEVER via store's migration
// files, per the coordination law) and mounts every route through
// Register(mux, Deps); the integrator wires it next to library/kb/memory.
package notes

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"chatgo/internal/api/httpx"
	"chatgo/internal/auth"
	"chatgo/internal/store"

	"github.com/google/uuid"
)

// Limits keep notes cheap to hold in memory: they ride whole in list
// responses, so the caps are the feature, not a polite suggestion.
const (
	maxNotesPerUser = 500
	maxNoteTitle    = 300
	maxNoteBody     = 128 << 10
)

const schema = `
CREATE TABLE IF NOT EXISTS notes (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	title TEXT NOT NULL DEFAULT '',
	content TEXT NOT NULL DEFAULT '',
	share_token TEXT UNIQUE,
	created_at INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_notes_user ON notes(user_id, updated_at);
`

// Init creates the notes table on the shared handle. Idempotent; Register
// calls it, so the integrator normally only calls Register.
func Init(db *sql.DB) error {
	if db == nil {
		return errors.New("notes: Init requires a non-nil *sql.DB")
	}
	_, err := db.Exec(schema)
	return err
}

// Deps is the small struct this feature needs (coordination law: features
// define their own Deps; the integrator wires it in server.go).
type Deps struct {
	Store *store.Store
	Auth  *auth.Auth // optional; tests use the local ctx fallback below
}

// Register mounts the notes surface on the shared mux. Register returns an
// error because the DDL is owned here (same CONTRACT-DEVIATION as library).
func Register(mux *http.ServeMux, d *Deps) error {
	if d == nil || d.Store == nil {
		return errors.New("notes: Register requires Deps.Store")
	}
	if err := Init(d.Store.DB); err != nil {
		return fmt.Errorf("notes: init schema: %w", err)
	}
	mux.HandleFunc("GET /api/notes", d.handleList)
	mux.HandleFunc("POST /api/notes", d.handleCreate)
	mux.HandleFunc("GET /api/notes/{id}", d.handleGet)
	mux.HandleFunc("PUT /api/notes/{id}", d.handlePut)
	mux.HandleFunc("DELETE /api/notes/{id}", d.handleDelete)
	mux.HandleFunc("POST /api/notes/{id}/share", d.handleShare)
	// Public read-only view (AuthPolicy already treats /api/share/ as public).
	mux.HandleFunc("GET /api/share/note/{token}", d.handleShared)
	return nil
}

// --- shared user lookup (same pattern as internal/library/library.go) -------

type ctxKeyLocalUser int

func withUserForTest(ctx context.Context, u *store.User) context.Context {
	return context.WithValue(ctx, ctxKeyLocalUser(0), u)
}

func (d *Deps) userFrom(r *http.Request) (*store.User, bool) {
	if d.Auth != nil {
		if u, ok := d.Auth.UserFrom(r.Context()); ok {
			return u, true
		}
	}
	u, ok := r.Context().Value(ctxKeyLocalUser(0)).(*store.User)
	return u, ok
}

func (d *Deps) requireUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u, ok := d.userFrom(r)
	if !ok || u.ID == "" {
		httpx.ErrorJSON(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	return u, true
}

// --- DTO ---------------------------------------------------------------------

type noteJSON struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Content    string  `json:"content"`
	ShareToken *string `json:"share_token,omitempty"`
	CreatedAt  int64   `json:"created_at"`
	UpdatedAt  int64   `json:"updated_at"`
}

type noteRow struct {
	noteJSON
	userID string
}

func (d *Deps) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		httpx.ErrorJSON(w, http.StatusNotFound, "not found")
		return
	}
	httpx.InternalError(w, r, err)
}

func (d *Deps) scanNote(rows *sql.Rows) (*noteRow, error) {
	var n noteRow
	var share sql.NullString
	if err := rows.Scan(&n.ID, &n.userID, &n.Title, &n.Content, &share,
		&n.CreatedAt, &n.UpdatedAt); err != nil {
		return nil, err
	}
	if share.Valid {
		t := share.String
		n.ShareToken = &t
	}
	return &n, nil
}

const noteCols = "id, user_id, title, content, share_token, created_at, updated_at"

// ownNote loads a note enforcing ownership (foreign → 404, never 403).
func (d *Deps) ownNote(ctx context.Context, userID, id string) (*noteRow, error) {
	row := d.Store.DB.QueryRowContext(ctx, `SELECT `+noteCols+` FROM notes WHERE id = ?1`, id)
	var n noteRow
	var share sql.NullString
	if err := row.Scan(&n.ID, &n.userID, &n.Title, &n.Content, &share,
		&n.CreatedAt, &n.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	if share.Valid {
		t := share.String
		n.ShareToken = &t
	}
	if n.userID != userID {
		return nil, store.ErrNotFound
	}
	return &n, nil
}

// --- handlers ------------------------------------------------------------------

// GET /api/notes → newest-updated first, whole bodies included (caps bound
// the payload: 500 notes × 128 KiB worst case, 30-per-page list is the FE
// contract — this list is small enough to ship whole).
func (d *Deps) handleList(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	rows, err := d.Store.DB.QueryContext(r.Context(),
		`SELECT `+noteCols+` FROM notes WHERE user_id = ?1
		 ORDER BY updated_at DESC, id DESC LIMIT 1000`, u.ID)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []*noteJSON{}
	for rows.Next() {
		n, err := d.scanNote(rows)
		if err != nil {
			d.fail(w, r, err)
			return
		}
		j := n.noteJSON
		out = append(out, &j)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"notes": out, "total": len(out)})
}

type noteReq struct {
	Title   *string `json:"title"`
	Content *string `json:"content"`
}

func (n *noteReq) validate() string {
	if n.Title != nil && len(*n.Title) > maxNoteTitle {
		return "title too long"
	}
	if n.Content != nil && len(*n.Content) > maxNoteBody {
		return "note too large (max 128 KiB)"
	}
	return ""
}

func (d *Deps) handleCreate(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	var body noteReq
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := body.validate(); msg != "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, msg)
		return
	}
	var count int
	if err := d.Store.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM notes WHERE user_id = ?1`, u.ID).Scan(&count); err != nil {
		d.fail(w, r, err)
		return
	}
	if count >= maxNotesPerUser {
		httpx.ErrorJSON(w, http.StatusConflict, "note limit reached")
		return
	}
	n := &noteRow{}
	n.ID = uuid.NewString()
	n.userID = u.ID
	n.Title = strings.TrimSpace(str(body.Title))
	n.Content = str(body.Content)
	if n.Title == "" {
		n.Title = "Untitled note"
	}
	now := time.Now().Unix()
	n.CreatedAt, n.UpdatedAt = now, now
	if _, err := d.Store.DB.ExecContext(r.Context(),
		`INSERT INTO notes (id, user_id, title, content, created_at, updated_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
		n.ID, n.userID, n.Title, n.Content, n.CreatedAt, n.UpdatedAt); err != nil {
		d.fail(w, r, err)
		return
	}
	j := n.noteJSON
	httpx.JSON(w, http.StatusCreated, &j)
}

func (d *Deps) handleGet(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	n, err := d.ownNote(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	j := n.noteJSON
	httpx.JSON(w, http.StatusOK, &j)
}

func (d *Deps) handlePut(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	n, err := d.ownNote(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	var body noteReq
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := body.validate(); msg != "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, msg)
		return
	}
	if body.Title != nil {
		t := strings.TrimSpace(*body.Title)
		if t != "" {
			n.Title = t
		}
	}
	if body.Content != nil {
		n.Content = *body.Content
	}
	n.UpdatedAt = time.Now().Unix()
	if _, err := d.Store.DB.ExecContext(r.Context(),
		`UPDATE notes SET title = ?2, content = ?3, updated_at = ?4 WHERE id = ?1`,
		n.ID, n.Title, n.Content, n.UpdatedAt); err != nil {
		d.fail(w, r, err)
		return
	}
	j := n.noteJSON
	httpx.JSON(w, http.StatusOK, &j)
}

func (d *Deps) handleDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	n, err := d.ownNote(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if _, err := d.Store.DB.ExecContext(r.Context(), `DELETE FROM notes WHERE id = ?1`, n.ID); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusNoContent, nil)
}

// POST /api/notes/{id}/share {on: bool} → share token minted (stable across
// toggles? no: OFF→ON mints a fresh token, old links die) or cleared.
func (d *Deps) handleShare(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	n, err := d.ownNote(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	var body struct {
		On *bool `json:"on"`
	}
	if err := httpx.Decode(r, &body); err != nil || body.On == nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, `body must be {"on": true|false}`)
		return
	}
	var tok any
	if *body.On {
		t, err := newShareToken()
		if err != nil {
			httpx.InternalError(w, r, err)
			return
		}
		tok = t
		n.ShareToken = &t
	} else {
		n.ShareToken = nil
	}
	if _, err := d.Store.DB.ExecContext(r.Context(),
		`UPDATE notes SET share_token = ?2 WHERE id = ?1`, n.ID, tok); err != nil {
		d.fail(w, r, err)
		return
	}
	j := n.noteJSON
	httpx.JSON(w, http.StatusOK, &j)
}

// GET /api/share/note/{token} — public, read-only.
func (d *Deps) handleShared(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	row := d.Store.DB.QueryRowContext(r.Context(),
		`SELECT `+noteCols+` FROM notes WHERE share_token = ?1`, token)
	var n noteRow
	var share sql.NullString
	if err := row.Scan(&n.ID, &n.userID, &n.Title, &n.Content, &share,
		&n.CreatedAt, &n.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.ErrorJSON(w, http.StatusNotFound, "not found")
			return
		}
		httpx.InternalError(w, r, err)
		return
	}
	j := n.noteJSON
	j.ShareToken = nil // the public payload never re-publishes the token
	httpx.JSON(w, http.StatusOK, &j)
}

func newShareToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
