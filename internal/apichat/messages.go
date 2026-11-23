// Message endpoints: list, send (SSE) and regenerate (SSE).
// SSE protocol per docs/CONTRACTS.md: event names meta|title|sources|token|
// done|error; httpx.NewSSE sets headers + flushes; client disconnect cancels
// r.Context(), which is handed to chat.Service.Send so upstream stops too.

package apichat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"chatgo/internal/api/httpx"
	"chatgo/internal/chat"
	"chatgo/internal/providers"
	"chatgo/internal/store"
)

// registerMessages mounts message list + SSE send/regenerate.
// CONTRACT-DEVIATION: the scaffold stub comment also listed the files
// endpoints (POST /api/files, GET /files/{id}/{name}, GET /api/files) here;
// my assigned scope (task §3) covers messages/regenerate only, and the list
// endpoint needs the BE6 Files repo which is owned elsewhere — left to the
// files owner to wire up.
func registerMessages(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("GET /api/chats/{id}/messages", d.handleListMessages)
	mux.HandleFunc("POST /api/chats/{id}/messages", d.handleSendMessage)
	mux.HandleFunc("POST /api/chats/{id}/regenerate", d.handleRegenerate)
	mux.HandleFunc("POST /api/chats/{id}/branch", d.handleSwitchBranch)
}

// messageStreamer is the chat.Service.Send surface used by the SSE handlers.
// CONTRACT-DEVIATION: unexported seam so the pump is unit-testable while
// chat.Service.Send is still the scaffold stub; production resolves d.Chat.
type messageStreamer interface {
	Send(ctx context.Context, userID, chatID string, userMsg providers.ChatMessage, opts chat.SendOpts) (<-chan chat.Event, error)
}

var chatStreamerOf = func(d *Deps) messageStreamer { return d.Chat }

type messageJSON struct {
	ID        string       `json:"id"`
	ChatID    string       `json:"chat_id"`
	Role      string       `json:"role"`
	ParentID  string       `json:"parent_id,omitempty"`
	IsCurrent bool         `json:"is_current,omitempty"` // on the active branch (leaf chain)
	Content   string       `json:"content"`
	Sources   []sourceJSON `json:"sources,omitempty"`
	Error     *string      `json:"error,omitempty"`
	TokensIn  int          `json:"tokens_in,omitempty"`
	TokensOut int          `json:"tokens_out,omitempty"`
	CreatedAt int64        `json:"created_at"`
}

type sourceJSON struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

func toMessageJSON(m *store.Message) *messageJSON {
	j := &messageJSON{
		ID: m.ID, ChatID: m.ChatID, Role: m.Role, ParentID: m.ParentID,
		Content: m.Content,
		Error:   m.Error, TokensIn: m.TokensIn, TokensOut: m.TokensOut,
		CreatedAt: m.CreatedAt,
	}
	if m.SourcesJSON != "" {
		var src []sourceJSON
		if json.Unmarshal([]byte(m.SourcesJSON), &src) == nil {
			j.Sources = src
		}
	}
	return j
}

// handleListMessages returns one page of a chat's history. The page is the
// newest `limit` rows before ?before= (rowid cursor), returned chronologically
// (store.Messages.List already reverses); bare JSON array per api.ts.
func (d *Deps) handleListMessages(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	limit := 200
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	msgs, err := d.Store.Messages.List(r.Context(), c.ID, limit, r.URL.Query().Get("before"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	out := make([]*messageJSON, 0, len(msgs))
	for _, m := range msgs {
		j := toMessageJSON(m)
		j.IsCurrent = c.LeafID != "" && m.ID == c.LeafID
		out = append(out, j)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// sendMessageReq mirrors api.ts SendMessageBody.
type sendMessageReq struct {
	Content string             `json:"content"`
	Images  []sendMessageImage `json:"images"`
	Search  bool               `json:"search"`
	Branch  bool               `json:"branch"` // regenerate: insert as sibling, keep the tail
}

type sendMessageImage struct {
	FileID string `json:"file_id"`
}

// maxMessageImages bounds vision payloads per message (DoS guard: each image
// is an ≤8MB base64 blob sent upstream).
const maxMessageImages = 4

// resolveImages turns uploaded file ids into data: URLs for the vision
// payload via files.Service.DataURL (contract §files).
//
// SECURITY: every id must belong to userID. Ownership is checked against the
// row BEFORE DataURL and only owned files get the markdown reference, so a
// foreign file_id can neither feed another user's bytes to the model nor leak
// its name/id into the caller's message. Unknown and foreign ids answer the
// same 400 (no existence leak).
func (d *Deps) resolveImages(ctx context.Context, userID string, imgs []sendMessageImage) ([]string, string, error) {
	if len(imgs) == 0 {
		return nil, "", nil
	}
	if len(imgs) > maxMessageImages {
		return nil, "", httpErr(http.StatusBadRequest, fmt.Sprintf("at most %d images per message", maxMessageImages))
	}
	if d.Files == nil {
		return nil, "", httpErr(http.StatusInternalServerError, "files service unavailable")
	}
	out := make([]string, 0, len(imgs))
	var md strings.Builder
	for _, im := range imgs {
		if im.FileID == "" {
			return nil, "", httpErr(http.StatusBadRequest, "images[].file_id is required")
		}
		f, err := d.Store.Files.Get(ctx, im.FileID)
		if err != nil || f.UserID != userID {
			return nil, "", httpErr(http.StatusBadRequest, "invalid image file_id")
		}
		url, err := d.Files.DataURL(ctx, userID, im.FileID)
		if err != nil {
			// File unreadable/too big/not an image — treat as bad input.
			return nil, "", httpErr(http.StatusBadRequest, "invalid image file_id")
		}
		out = append(out, url)
		// Markdown reference so the image stays visible in history when the
		// message is reloaded (served same-origin by handleFileGet).
		fmt.Fprintf(&md, "\n![%s](/files/%s/%s)", f.Name, f.ID, url2escape(f.Name))
	}
	return out, md.String(), nil
}

func url2escape(name string) string { return url.PathEscape(name) }

func (d *Deps) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	var body sendMessageReq
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	images, imgMD, err := d.resolveImages(r.Context(), u.ID, body.Images)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if body.Content == "" && len(images) == 0 {
		httpx.ErrorJSON(w, http.StatusBadRequest, "content or images required")
		return
	}

	d.streamSend(w, r, u.ID, c.ID, providers.ChatMessage{Role: "user", Content: body.Content + imgMD, Images: images},
		chat.SendOpts{Search: body.Search, Images: images})
}

// handleRegenerate re-runs the turn. Default mode: ?from={msgID} deletes that
// message and everything after it (chat.Service semantics via SendOpts.From).
// Branch mode (body {"branch":true} or ?branch=1): the anchor and its tail
// stay — the new version is appended beside the anchor and the chat's leaf
// advances onto it (version pager semantics; see handleSwitchBranch).
func (d *Deps) handleRegenerate(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	from := r.URL.Query().Get("from")
	if from != "" {
		if _, err := d.Store.Messages.Get(r.Context(), c.ID, from); err != nil {
			d.fail(w, r, err) // unknown from-id in this chat → 404
			return
		}
	}
	var body sendMessageReq
	if err := httpx.DecodeLoose(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	images, imgMD, err := d.resolveImages(r.Context(), u.ID, body.Images)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	branch := body.Branch || r.URL.Query().Get("branch") == "1"

	d.streamSend(w, r, u.ID, c.ID,
		providers.ChatMessage{Role: "user", Content: body.Content + imgMD, Images: images},
		chat.SendOpts{Search: body.Search, Images: images, From: from, Branch: branch})
}

// switchBranchReq is the POST /api/chats/{id}/branch body.
type switchBranchReq struct {
	MessageID string `json:"message_id"`
}

// handleSwitchBranch moves the chat's active branch to the version named by
// message_id — landing on that version's newest continuation, mirroring Open
// WebUI's getDeepestChildId: paging ‹1/2› mid-conversation keeps everything
// below the paged message. Response carries the new leaf so the client can
// recompute the visible path from the message tree it already holds.
func (d *Deps) handleSwitchBranch(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	var body switchBranchReq
	if err := httpx.Decode(r, &body); err != nil || body.MessageID == "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, "message_id required")
		return
	}
	leaf, err := d.Store.Messages.DeepestChild(r.Context(), c.ID, body.MessageID)
	if err != nil {
		d.fail(w, r, err) // unknown/foreign message → 404
		return
	}
	if err := d.Store.Chats.SetLeaf(r.Context(), c.ID, leaf); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"leaf_id": leaf})
}

// streamSend starts the upstream turn and pumps chat.Service events onto the
// SSE wire. Any failure before the stream starts answers plain JSON; once
// headers are out, `error` events carry the bad news.
func (d *Deps) streamSend(w http.ResponseWriter, r *http.Request, userID, chatID string,
	userMsg providers.ChatMessage, opts chat.SendOpts) {

	events, err := chatStreamerOf(d).Send(r.Context(), userID, chatID, userMsg, opts)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	// Retract guard (see trackChatTurn in chats.go): the chat stays marked busy
	// until the drain defer below completes — a canceled client's partial is
	// persisted on a detached context AFTER r.Context() is done, and retract
	// must not delete the exchange underneath that late write. Deferred first,
	// so LIFO it releases last.
	releaseTurn := trackChatTurn(chatID)
	defer releaseTurn()
	sse, err := httpx.NewSSE(r, w)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	defer sse.Close()
	// Contract: Send closes its channel after the terminal event; draining on
	// the way out unblocks the producer if we bail early (client gone).
	defer func() {
		for range events {
		}
	}()

	terminal := false
	for {
		select {
		case <-r.Context().Done():
			return // client disconnected; ctx cancel stops upstream too
		case ev, ok := <-events:
			if !ok {
				if !terminal { // defensive: never leave the client without a terminator
					_ = sse.Event("done", map[string]any{"usage": nil})
				}
				return
			}
			if err := sse.Event(ev.Type, ev.Data); err != nil {
				return // write failed or client gone
			}
			switch ev.Type {
			case "done", "error":
				terminal = true
				return
			}
		}
	}
}
