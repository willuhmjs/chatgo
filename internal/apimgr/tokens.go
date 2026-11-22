package apimgr

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"chatgo/internal/api/httpx"
	"chatgo/internal/store"
)

// /api/tokens CRUD — create mints "ck_"+32hex, stores ONLY sha256(token),
// and returns the plaintext once (api.ts ApiToken.key).

type tokensH struct{ d *Deps }

func registerTokens(mux *http.ServeMux, d *Deps) {
	h := &tokensH{d: d}
	mux.HandleFunc("GET /api/tokens", h.handleList)
	mux.HandleFunc("POST /api/tokens", h.handleCreate)
	mux.HandleFunc("DELETE /api/tokens/{id}", h.handleDelete)
}

// tokenJSON mirrors web/src/lib/api.ts ApiToken. No key_hash is ever emitted;
// "key" appears once in the create response.
type tokenJSON struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt *int64 `json:"last_used_at,omitempty"`
	Key        string `json:"key,omitempty"`
}

func tokenView(t *store.APIToken) tokenJSON {
	return tokenJSON{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt}
}

// handleList: GET /api/tokens — the caller's own tokens (hashes stay in the
// DB; nothing sensitive in the payload).
func (h *tokensH) handleList(w http.ResponseWriter, r *http.Request) {
	u, ok := h.d.requireUser(w, r)
	if !ok {
		return
	}
	h.list(w, r, u.ID)
}

func (h *tokensH) list(w http.ResponseWriter, r *http.Request, userID string) {
	toks, err := h.d.Store.Tokens.List(r.Context(), userID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	out := make([]tokenJSON, 0, len(toks))
	for _, t := range toks {
		out = append(out, tokenView(t))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// handleCreate: POST /api/tokens {name} → 201 with the full "ck_…" key once.
func (h *tokensH) handleCreate(w http.ResponseWriter, r *http.Request) {
	u, ok := h.d.requireUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, `body must be {"name":"…"}`)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		httpx.ErrorJSON(w, http.StatusBadRequest, "name must be 1-100 characters")
		return
	}
	h.create(w, r, u.ID, name)
}

func (h *tokensH) create(w http.ResponseWriter, r *http.Request, userID, name string) {
	plain, err := newTokenSecret()
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	t := &store.APIToken{UserID: userID, Name: name, KeyHash: store.HashToken(plain)}
	if err := h.d.Store.Tokens.Create(r.Context(), t); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	view := tokenView(t)
	view.Key = plain // the only time the plaintext leaves this process
	httpx.JSON(w, http.StatusCreated, view)
}

// handleDelete: DELETE /api/tokens/{id} → 204 (own tokens only; a foreign id
// 404s — no existence leak, contract §Non-negotiables).
func (h *tokensH) handleDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := h.d.requireUser(w, r)
	if !ok {
		return
	}
	h.del(w, r, r.PathValue("id"), u.ID)
}

func (h *tokensH) del(w http.ResponseWriter, r *http.Request, id, userID string) {
	if err := h.d.Store.Tokens.Delete(r.Context(), id, userID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.ErrorJSON(w, http.StatusNotFound, "token not found")
			return
		}
		httpx.InternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// newTokenSecret mints "ck_" + 32 hex chars (16 random bytes).
func newTokenSecret() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "ck_" + hex.EncodeToString(b[:]), nil
}
