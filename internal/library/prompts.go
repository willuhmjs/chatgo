// Prompt library: CRUD /api/prompts. Prompts are private per user; rows with
// user_id NULL are admin-curated globals readable by everyone but writable
// only by admins. Ownership rule (contract §Non-negotiables): another user's
// private prompt answers 404, never 403; global prompts answer 403 for
// non-admin writes (they are public rows, so no existence leaks either way).

package library

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chatgo/internal/api/httpx"
	"chatgo/internal/store"

	"github.com/google/uuid"
)

const (
	// maxPromptTitle caps prompt titles (defensive: bodies are already
	// 1 MiB-capped by httpx.Decode).
	maxPromptTitle = 200
	// maxPromptContent caps prompt content (same order as
	// store.MaxContentLen per-message text, kept smaller: prompts travel
	// whole in every list page).
	maxPromptContent = 32 << 10
)

// promptPage is the frozen list envelope shape used across this codebase
// (mirrors apichat's chatPage: items,total,page,per — contract requires every
// list endpoint to be paginated).
type promptPage struct {
	Prompts []*promptJSON `json:"prompts"`
	Total   int           `json:"total"`
	Page    int           `json:"page"`
	Per     int           `json:"per"`
}

type promptJSON struct {
	ID        string  `json:"id"`
	UserID    *string `json:"user_id,omitempty"`
	Global    bool    `json:"global"`
	Title     string  `json:"title"`
	Content   string  `json:"content"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

const promptCols = `id, user_id, title, content, created_at, updated_at`

func scanPrompt(scan func(...any) error) (*promptJSON, error) {
	p := &promptJSON{}
	var uid sql.NullString
	if err := scan(&p.ID, &uid, &p.Title, &p.Content, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if uid.Valid {
		p.UserID = &uid.String
	} else {
		p.Global = true
	}
	return p, nil
}

func registerPrompts(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("GET /api/prompts", d.handleListPrompts)
	mux.HandleFunc("POST /api/prompts", d.handleCreatePrompt)
	mux.HandleFunc("PUT /api/prompts/{id}", d.handleUpdatePrompt)
	mux.HandleFunc("DELETE /api/prompts/{id}", d.handleDeletePrompt)
}

// GET /api/prompts?page=&per= → caller's own prompts + global ones.
func (d *Deps) handleListPrompts(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	page, per := pageParams(r)

	var total int
	err := d.Store.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM prompts WHERE user_id IS NULL OR user_id = ?1`, u.ID).Scan(&total)
	if err != nil {
		d.fail(w, r, err)
		return
	}

	rows, err := d.Store.DB.QueryContext(r.Context(),
		`SELECT `+promptCols+` FROM prompts
		 WHERE user_id IS NULL OR user_id = ?1
		 ORDER BY created_at DESC, id DESC LIMIT ?2 OFFSET ?3`,
		u.ID, per, (page-1)*per)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	defer rows.Close()

	out := make([]*promptJSON, 0, per)
	for rows.Next() {
		p, err := scanPrompt(rows.Scan)
		if err != nil {
			d.fail(w, r, err)
			return
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, promptPage{Prompts: out, Total: total, Page: page, Per: per})
}

type promptCreateReq struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Global  bool   `json:"global"`
}

// POST /api/prompts {title,content,global?} → creates an own prompt;
// global:true requires admin (403 otherwise).
func (d *Deps) handleCreatePrompt(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	var body promptCreateReq
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, "title must not be empty")
		return
	}
	if msg := validatePromptFields(title, &body.Content); msg != "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, msg)
		return
	}
	if body.Global && !u.IsAdmin() {
		httpx.ErrorJSON(w, http.StatusForbidden, "only admins can create global prompts")
		return
	}

	now := time.Now().Unix()
	p := &promptJSON{
		ID: uuid.NewString(), Title: title, Content: body.Content,
		Global: body.Global, CreatedAt: now, UpdatedAt: now,
	}
	var uid any
	if !p.Global {
		uid = u.ID
		p.UserID = &u.ID
	}
	if _, err := d.Store.DB.ExecContext(r.Context(),
		`INSERT INTO prompts (id, user_id, title, content, created_at, updated_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
		p.ID, uid, p.Title, p.Content, p.CreatedAt, p.UpdatedAt); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, p)
}

type promptUpdateReq struct {
	Title   *string `json:"title"`
	Content *string `json:"content"`
}

// PUT /api/prompts/{id} {title?,content?} → owner edit (own prompts) or
// admin edit (global prompts); foreign private → 404.
func (d *Deps) handleUpdatePrompt(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	p, err := d.loadPrompt(r.Context(), r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if err := d.mayMutatePrompt(u, p); err != nil {
		d.fail(w, r, err)
		return
	}
	var body promptUpdateReq
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Title == nil && body.Content == nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "provide title and/or content")
		return
	}
	title := p.Title
	if body.Title != nil {
		title = strings.TrimSpace(*body.Title)
		if title == "" {
			httpx.ErrorJSON(w, http.StatusBadRequest, "title must not be empty")
			return
		}
	}
	if msg := validatePromptFields(title, body.Content); msg != "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, msg)
		return
	}
	if body.Content != nil {
		p.Content = *body.Content
	}
	p.Title = title
	p.UpdatedAt = time.Now().Unix()
	if _, err := d.Store.DB.ExecContext(r.Context(),
		`UPDATE prompts SET title = ?2, content = ?3, updated_at = ?4 WHERE id = ?1`,
		p.ID, p.Title, p.Content, p.UpdatedAt); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

// DELETE /api/prompts/{id} → owner delete (own) or admin delete (global);
// foreign private → 404.
func (d *Deps) handleDeletePrompt(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	p, err := d.loadPrompt(r.Context(), r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if err := d.mayMutatePrompt(u, p); err != nil {
		d.fail(w, r, err)
		return
	}
	if _, err := d.Store.DB.ExecContext(r.Context(), `DELETE FROM prompts WHERE id = ?1`, p.ID); err != nil {
		d.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ----------------------------------------------------------------

func (d *Deps) loadPrompt(ctx context.Context, id string) (*promptJSON, error) {
	row := d.Store.DB.QueryRowContext(ctx, `SELECT `+promptCols+` FROM prompts WHERE id = ?1`, id)
	p, err := scanPrompt(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// mayMutatePrompt enforces the write policy: own row → owner; global row →
// admin only (403, visible to everyone so no existence leak); another user's
// private row → 404 (no existence leak).
func (d *Deps) mayMutatePrompt(u *store.User, p *promptJSON) error {
	if p.Global {
		if !u.IsAdmin() {
			return httpErr(http.StatusForbidden, "only admins can modify global prompts")
		}
		return nil
	}
	if p.UserID != nil && *p.UserID == u.ID {
		return nil
	}
	return store.ErrNotFound
}

func validatePromptFields(title string, content *string) string {
	if len(title) > maxPromptTitle {
		return "title too long (max 200 characters)"
	}
	if content != nil {
		if *content == "" {
			return "content must not be empty"
		}
		if len(*content) > maxPromptContent {
			return "content too long (max 32KB)"
		}
	}
	return ""
}

func pageParams(r *http.Request) (page, per int) {
	page, per = 1, defaultPer
	if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v > 0 {
		page = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("per")); err == nil && v > 0 {
		per = v
		if per > maxPer {
			per = maxPer
		}
	}
	return page, per
}
