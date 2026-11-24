// Suggest-next endpoint: POST /api/chats/{id}/suggest →
// {"suggestions": ["…", "…"]} (the Open WebUI-style sparkle button). Follows
// the messages.go handler pattern: requireUser → ownChat (foreign → 404,
// never 403) → tiny capped body → service call. Task-model failures come
// back as chat.ErrSuggestUnavailable / chat.ErrNoSuggestions and answer a
// clean 503 — never a 500, never internals.

package apichat

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"chatgo/internal/api/httpx"
	"chatgo/internal/chat"
)

// registerSuggest mounts the suggest sub-resource on the shared mux. Wired
// from registerChats (chats.go) — the one added line there.
func registerSuggest(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("POST /api/chats/{id}/suggest", d.handleSuggestNext)
}

// suggestBodyCap bounds the request body: suggestions take no input — the
// chat is the input — so the body is an empty-ish JSON object at most.
const suggestBodyCap = 1 << 10 // 1 KiB

// suggestBusy is the per-user in-flight guard: one suggest per user at a
// time (click-spamming the sparkle must not fan out task-model calls).
// Bounded twice over — entries live only for the duration of a request AND
// the map is hard-capped — so it can never grow without limit.
var suggestBusy = struct {
	mu sync.Mutex
	m  map[string]bool
}{m: make(map[string]bool)}

const suggestMaxBusy = 256

func beginSuggest(userID string) bool {
	suggestBusy.mu.Lock()
	defer suggestBusy.mu.Unlock()
	if suggestBusy.m[userID] || len(suggestBusy.m) >= suggestMaxBusy {
		return false
	}
	suggestBusy.m[userID] = true
	return true
}

func endSuggest(userID string) {
	suggestBusy.mu.Lock()
	delete(suggestBusy.m, userID)
	suggestBusy.mu.Unlock()
}

// handleSuggestNext answers 3-5 short follow-up questions for the chat.
// Ordering: auth 401 → chat ownership 404 → body cap 400 → per-user
// in-flight guard 429 → service (typed errors → 503; a chat that vanished
// between the ownership check and here still answers 404, never 500).
func (d *Deps) handleSuggestNext(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if !decodeEmptyBody(w, r) {
		return
	}
	if !beginSuggest(u.ID) {
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "already suggesting")
		return
	}
	defer endSuggest(u.ID)

	if d.Chat == nil {
		httpx.ErrorJSON(w, http.StatusServiceUnavailable, "suggestions are unavailable")
		return
	}
	items, err := d.Chat.SuggestNext(r.Context(), c.ID, u.ID)
	if err != nil {
		switch {
		case errors.Is(err, chat.ErrChatNotFound):
			httpx.ErrorJSON(w, http.StatusNotFound, "not found")
		case errors.Is(err, chat.ErrSuggestUnavailable):
			httpx.ErrorJSON(w, http.StatusServiceUnavailable, "the task model is not available for suggestions")
		case errors.Is(err, chat.ErrNoSuggestions):
			httpx.ErrorJSON(w, http.StatusServiceUnavailable, "the model returned no usable suggestions")
		default:
			httpx.InternalError(w, r, err)
		}
		return
	}
	httpx.JSON(w, http.StatusOK, map[string][]string{"suggestions": items})
}

// decodeEmptyBody accepts an absent/empty/JSON-object body up to
// suggestBodyCap bytes ("{}" or nothing at all), answering 400 itself for
// oversized or malformed input. Returns whether the request may proceed.
func decodeEmptyBody(w http.ResponseWriter, r *http.Request) bool {
	body := http.MaxBytesReader(w, r.Body, suggestBodyCap)
	defer body.Close()
	var v any
	if err := json.NewDecoder(body).Decode(&v); err != nil && !errors.Is(err, io.EOF) {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}
