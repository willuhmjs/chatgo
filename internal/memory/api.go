// HTTP surface (user-owned; api/server.go's AuthPolicy already wraps /api/*
// with auth.Require — handlers still re-check the session defensively).

package memory

import (
	"net/http"
	"strconv"
	"strings"

	"chatgo/internal/api/httpx"
)

// listResponse is GET /api/me/memories' payload.
type listResponse struct {
	Items []Memory `json:"items"`
	Total int      `json:"total"`
	Limit int      `json:"limit"` // per-user cap, so the UI can show n/200
}

// GET /api/me/memories?page=&per= → newest-first page + total.
func (d *Deps) handleList(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	per := parseIntDefault(r.URL.Query().Get("per"), defaultPerPage)
	if per < 1 {
		per = 1
	}
	if per > maxPerPage {
		per = maxPerPage // contract: lists capped at 100/page
	}
	page := parseIntDefault(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	items, total, err := listMemories(r.Context(), d.Store.DB, u.ID, per, (page-1)*per)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse{Items: items, Total: total, Limit: MaxPerUser})
}

// POST /api/me/memories {content} → 201 {memory}.
func (d *Deps) handleCreate(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "body must be a JSON object with a \"content\" string")
		return
	}
	content, msg := sanitizeContent(in.Content)
	if msg != "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, msg)
		return
	}
	n, err := countMemories(r.Context(), d.Store.DB, u.ID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if n >= MaxPerUser {
		d.fail(w, r, ErrLimit)
		return
	}
	m := &Memory{Content: content, Source: SourceManual, Enabled: true}
	if err := insertMemory(r.Context(), d.Store.DB, u.ID, m); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, m)
}

// PUT /api/me/memories/{id} {content?, enabled?} → 200 {memory}.
func (d *Deps) handleUpdate(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Content *string `json:"content"`
		Enabled *bool   `json:"enabled"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "body must be a JSON object with \"content\" and/or \"enabled\"")
		return
	}
	if in.Content == nil && in.Enabled == nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "provide \"content\" and/or \"enabled\"")
		return
	}
	if in.Content != nil {
		c, msg := sanitizeContent(*in.Content)
		if msg != "" {
			httpx.ErrorJSON(w, http.StatusBadRequest, msg)
			return
		}
		in.Content = &c
	}
	m, err := updateMemory(r.Context(), d.Store.DB, u.ID, r.PathValue("id"), in.Content, in.Enabled)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, m)
}

// DELETE /api/me/memories/{id} → 204.
func (d *Deps) handleDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	if err := deleteMemory(r.Context(), d.Store.DB, u.ID, r.PathValue("id")); err != nil {
		d.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sanitizeContent flattens one memory to a single trimmed line and applies
// the length rule. It returns the client-safe error message ("" = valid).
// Newlines inside content would break the "About the user:\n- …" injection
// block, so they are flattened rather than rejected.
func sanitizeContent(raw string) (string, string) {
	c := strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		return r
	}, raw)
	c = strings.Join(strings.Fields(c), " ")
	if c == "" {
		return "", "content must not be empty"
	}
	if r := []rune(c); len(r) > MaxContentRunes {
		return "", "content exceeds " + strconv.Itoa(MaxContentRunes) + " characters"
	}
	return c, ""
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
