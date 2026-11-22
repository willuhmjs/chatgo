// Share endpoints: POST/DELETE /api/chats/{id}/share (owner-only) and the
// PUBLIC read-only GET /api/share/{token} (no auth per api/server.go policy).

package apichat

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"chatgo/internal/api/httpx"
)

// shareTokenLen bytes → 24 hex chars (contract task: uuid-hex 24).
const shareTokenLen = 12

// sharePayload mirrors api.ts SharePayload; `chat` carries provider/model
// display enrichment on top of the regular Chat shape.
type sharePayload struct {
	Chat     *chatJSON      `json:"chat"`
	Messages []*messageJSON `json:"messages"`
}

// registerShare mounts share create/remove + the public reader.
func registerShare(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("POST /api/chats/{id}/share", d.handleShareCreate)
	mux.HandleFunc("DELETE /api/chats/{id}/share", d.handleShareDelete)
	mux.HandleFunc("GET /api/share/{token}", d.handleSharePublic)
}

func newShareToken() (string, error) {
	b := make([]byte, shareTokenLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// handleShareCreate is idempotent: an existing live token is returned as-is.
func (d *Deps) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if c.ShareToken != nil && *c.ShareToken != "" {
		httpx.JSON(w, http.StatusOK, map[string]string{"token": *c.ShareToken})
		return
	}
	token, err := newShareToken()
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if err := d.Store.Chats.SetShare(r.Context(), c.ID, token); err != nil {
		// share_token is UNIQUE; on the vanishingly rare collision retry once.
		if token, err = newShareToken(); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
		if err := d.Store.Chats.SetShare(r.Context(), c.ID, token); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"token": token})
}

func (d *Deps) handleShareDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if c.ShareToken != nil && *c.ShareToken != "" {
		if err := d.Store.Chats.SetShare(r.Context(), c.ID, ""); err != nil {
			d.fail(w, r, err)
			return
		}
	}
	httpx.JSON(w, http.StatusNoContent, nil)
}

// handleSharePublic serves a shared chat without any auth (route group is
// "optional" in api/server.go AuthPolicy). Sources stay as stored — the
// contract's share view keeps source urls.
func (d *Deps) handleSharePublic(w http.ResponseWriter, r *http.Request) {
	c, err := d.Store.Chats.GetByShare(r.Context(), r.PathValue("token"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	cj := toChatJSON(c)
	cj.ShareToken = nil // no need to repeat the token back

	if p, err := d.Store.Providers.Get(r.Context(), c.ProviderID); err == nil {
		cj.ProviderName = p.Name
	}
	if ms, err := d.Store.Models.ForProvider(r.Context(), c.ProviderID); err == nil {
		for i := range ms {
			if ms[i].ModelID == c.ModelID {
				if ms[i].Display != "" {
					cj.ModelDisplay = ms[i].Display
				} else {
					cj.ModelDisplay = ms[i].ModelID
				}
				break
			}
		}
	}

	msgs, err := d.Store.Messages.List(r.Context(), c.ID, 500, "")
	if err != nil {
		d.fail(w, r, err)
		return
	}
	out := make([]*messageJSON, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toMessageJSON(m))
	}
	httpx.JSON(w, http.StatusOK, sharePayload{Chat: cj, Messages: out})
}
