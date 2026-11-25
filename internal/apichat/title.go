// Regenerate-title endpoint: POST /api/chats/{id}/title → {"title": "…"}.
// The Open WebUI "Regenerate Title" action, reusing the auto-title machinery
// (task model + cleanTitle) but overwriting the existing title instead of
// only filling the default. Handler shape mirrors suggest.go: requireUser →
// ownChat (foreign → 404, never 403) → empty-body decode → service call;
// task-model failures answer a clean 503, never a 500.

package apichat

import (
	"errors"
	"net/http"

	"chatgo/internal/api/httpx"
	"chatgo/internal/chat"
)

// registerTitle mounts the title sub-resource on the shared mux. Wired from
// registerChats (chats.go) — the one added line there.
func registerTitle(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("POST /api/chats/{id}/title", d.handleRegenerateTitle)
}

func (d *Deps) handleRegenerateTitle(w http.ResponseWriter, r *http.Request) {
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
	if d.Chat == nil {
		httpx.ErrorJSON(w, http.StatusServiceUnavailable, "title generation is unavailable")
		return
	}
	title, err := d.Chat.RegenerateTitle(r.Context(), c.ID, u.ID)
	if err != nil {
		switch {
		case errors.Is(err, chat.ErrChatNotFound):
			httpx.ErrorJSON(w, http.StatusNotFound, "not found")
		case errors.Is(err, chat.ErrTitleUnavailable):
			httpx.ErrorJSON(w, http.StatusServiceUnavailable, "the task model is not available for titling")
		default:
			httpx.InternalError(w, r, err)
		}
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"title": title})
}
