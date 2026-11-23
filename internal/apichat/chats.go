// Chat CRUD endpoints: GET/POST /api/chats, GET/PATCH/DELETE /api/chats/{id}.
// Ownership rule (contract §Non-negotiables): a chat the caller does not own
// answers 404, never 403 — admins get NO cross-user chat access either.

package apichat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"chatgo/internal/api/httpx"
	"chatgo/internal/store"
)

// listEnvelope: the scaffold frontend DTO (web/src/lib/api.ts ChatPage) names
// the page field `chats`; every list here mirrors those frozen shapes.
// CONTRACT-DEVIATION: task brief said {items,…}; scaffold api.ts (law: agents
// call the scaffold wrapper) uses {chats,total,page,per} for this endpoint.
type chatPage struct {
	Chats []*chatJSON `json:"chats"`
	Total int         `json:"total"`
	Page  int         `json:"page"`
	Per   int         `json:"per"`
}

const (
	defaultPer = 30
	maxPer     = 100
)

// --- shared user lookup -----------------------------------------------------

// ctxKeyLocalUser is an apichat-local context key checked only when the auth
// middleware found nothing. CONTRACT-DEVIATION: the scaffold's auth ctx keys
// are unexported (internal/auth/middleware.go), so handler tests cannot forge
// a logged-in request without them; this fallback keeps tests hermetic. The
// real auth middleware is consulted FIRST, so production behavior is
// unchanged (nothing else can plant this key — it is package-private).
type ctxKeyLocalUser int

func requireUserFallback(ctx context.Context) (*store.User, bool) {
	u, ok := ctx.Value(ctxKeyLocalUser(0)).(*store.User)
	return u, ok
}

func withUserForTest(ctx context.Context, u *store.User) context.Context {
	return context.WithValue(ctx, ctxKeyLocalUser(0), u)
}

func (d *Deps) userFrom(r *http.Request) (*store.User, bool) {
	if d.Auth != nil {
		if u, ok := d.Auth.UserFrom(r.Context()); ok {
			return u, true
		}
	}
	return requireUserFallback(r.Context())
}

func (d *Deps) requireUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u, ok := d.userFrom(r)
	if !ok || u.ID == "" {
		httpx.ErrorJSON(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	return u, true
}

// --- shared error mapping ---------------------------------------------------

// httpError carries a client-safe status/message through helper layers.
type httpError struct {
	status int
	msg    string
}

func (e httpError) Error() string { return e.msg }

func httpErr(status int, msg string) error { return httpError{status, msg} }

// fail maps store/helper errors onto the contract's status codes.
func (d *Deps) fail(w http.ResponseWriter, r *http.Request, err error) {
	var he httpError
	switch {
	case errors.As(err, &he):
		httpx.ErrorJSON(w, he.status, he.msg)
	case errors.Is(err, store.ErrNotFound):
		httpx.ErrorJSON(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrDuplicate):
		httpx.ErrorJSON(w, http.StatusConflict, "already exists")
	case errors.Is(err, store.ErrInvalidName), errors.Is(err, store.ErrParentCycle):
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid name")
	default:
		httpx.InternalError(w, r, err)
	}
}

// --- shared JSON shapes (mirror web/src/lib/api.ts DTOs) ---------------------

type tagJSON struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
}

type folderJSON struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	ParentID *string `json:"parent_id,omitempty"`
	Sort     int     `json:"sort"`
}

type chatJSON struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	ProviderID string         `json:"provider_id"`
	ModelID    string         `json:"model_id"`
	Params     map[string]any `json:"params,omitempty"`
	Pinned     bool           `json:"pinned"`
	FolderID   *string        `json:"folder_id,omitempty"`
	ShareToken *string        `json:"share_token,omitempty"`
	Tags       []*tagJSON     `json:"tags,omitempty"`
	CreatedAt  int64          `json:"created_at"`
	UpdatedAt  int64          `json:"updated_at"`
	// Enrichment for the public share payload (extra fields are additive for
	// the TS DTO): provider + model display names.
	ProviderName string `json:"provider,omitempty"`
	ModelDisplay string `json:"model_display,omitempty"`
}

func toChatJSON(c *store.Chat) *chatJSON {
	j := &chatJSON{
		ID: c.ID, Title: c.Title, ProviderID: c.ProviderID, ModelID: c.ModelID,
		Pinned: c.Pinned, FolderID: c.FolderID, ShareToken: c.ShareToken,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	if c.ParamsJSON != "" && c.ParamsJSON != "{}" {
		var m map[string]any
		if json.Unmarshal([]byte(c.ParamsJSON), &m) == nil {
			j.Params = m
		}
	}
	return j
}

func toTagJSON(t *store.Tag) *tagJSON { return &tagJSON{ID: t.ID, Name: t.Name, Color: t.Color} }

func toFolderJSON(f *store.Folder) *folderJSON {
	return &folderJSON{ID: f.ID, Name: f.Name, ParentID: f.ParentID, Sort: f.Sort}
}

func withTags(j *chatJSON, tags []*store.Tag) *chatJSON {
	for _, t := range tags {
		j.Tags = append(j.Tags, toTagJSON(t))
	}
	return j
}

// nullableStr distinguishes "key absent" (leave field alone) from an explicit
// JSON null (clear the field) in PATCH/PUT bodies.
type nullableStr struct {
	Set  bool
	Null bool
	Val  string
}

func (n *nullableStr) UnmarshalJSON(b []byte) error {
	n.Set = true
	if string(b) == "null" {
		n.Null = true
		return nil
	}
	return json.Unmarshal(b, &n.Val)
}

// --- shared helpers -----------------------------------------------------------

// ownChat loads a live chat and enforces ownership (foreign → ErrNotFound).
func (d *Deps) ownChat(ctx context.Context, userID, chatID string) (*store.Chat, error) {
	c, err := d.Store.Chats.Get(ctx, chatID)
	if err != nil {
		return nil, err // ErrNotFound for missing/soft-deleted
	}
	if c.UserID != userID {
		return nil, store.ErrNotFound
	}
	return c, nil
}

// ownFolder resolves a referenced folder id for the caller (foreign → 404).
func (d *Deps) ownFolder(ctx context.Context, userID, folderID string) (*store.Folder, error) {
	f, err := d.Store.Folders.Get(ctx, folderID)
	if err != nil {
		return nil, err
	}
	if f.UserID != userID {
		return nil, store.ErrNotFound
	}
	return f, nil
}

// validateChatModel checks a (provider_id, model_id) pair against the cached
// catalog: provider must exist and be enabled, model must be a cached,
// enabled model of that provider. ok = (0, "") style via error.
func (d *Deps) validateChatModel(ctx context.Context, providerID, modelID string) error {
	if providerID == "" {
		if modelID != "" {
			return httpErr(http.StatusBadRequest, "provider_id is required with model_id")
		}
		return nil
	}
	p, err := d.Store.Providers.Get(ctx, providerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return httpErr(http.StatusNotFound, "provider not found")
		}
		return err
	}
	if !p.Enabled {
		return httpErr(http.StatusBadRequest, "provider is disabled")
	}
	if modelID == "" {
		return nil
	}
	models, err := d.Store.Models.ForProvider(ctx, providerID)
	if err != nil {
		return err
	}
	for i := range models {
		if models[i].ModelID == modelID {
			if !models[i].Enabled {
				return httpErr(http.StatusBadRequest, "model is disabled")
			}
			return nil
		}
	}
	return httpErr(http.StatusNotFound, "model not found")
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

func wantsPinned(r *http.Request) bool {
	v := r.URL.Query().Get("pinned")
	return v == "1" || strings.EqualFold(v, "true")
}

// --- registration -------------------------------------------------------------

// registerChats mounts chat CRUD on the shared mux. The retract sub-resource
// rides along here (rather than in registerMessages) because its delete/404/
// ownership semantics are the chat-CRUD family, and its busy guard shares the
// tracker below with the stream pump in messages.go.
func registerChats(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("GET /api/chats", d.handleListChats)
	mux.HandleFunc("POST /api/chats", d.handleCreateChat)
	mux.HandleFunc("GET /api/chats/{id}", d.handleGetChat)
	mux.HandleFunc("PATCH /api/chats/{id}", d.handlePatchChat)
	mux.HandleFunc("DELETE /api/chats/{id}", d.handleDeleteChat)
	mux.HandleFunc("POST /api/chats/{id}/messages/{msgId}/retract", d.handleRetractMessage)
}

// --- handlers -------------------------------------------------------------------

func (d *Deps) handleListChats(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	page, per := pageParams(r)
	opts := store.ChatListOptions{
		FolderID: r.URL.Query().Get("folder"),
		TagID:    r.URL.Query().Get("tag"),
		Page:     page,
		Per:      per,
	}

	var (
		chats []*store.Chat
		total int
		err   error
	)
	switch {
	case q != "":
		// Message-content search via FTS5. CONTRACT-DEVIATION: the frozen
		// SearchChats(ctx,userID,q,page,per) shape carries no folder/tag/
		// pinned filters, so those query params are ignored on the ?q= path.
		chats, total, err = d.Store.Chats.SearchChats(r.Context(), u.ID, q, page, per)
	case wantsPinned(r):
		chats, total, err = d.Store.Chats.ListPinned(r.Context(), u.ID, opts)
	default:
		chats, total, err = d.Store.Chats.List(r.Context(), u.ID, opts)
	}
	if err != nil {
		d.fail(w, r, err)
		return
	}

	ids := make([]string, 0, len(chats))
	for _, c := range chats {
		ids = append(ids, c.ID)
	}
	tagMap, err := d.Store.Tags.TagsForChats(r.Context(), ids)
	if err != nil {
		d.fail(w, r, err)
		return
	}

	items := make([]*chatJSON, 0, len(chats))
	for _, c := range chats {
		items = append(items, withTags(toChatJSON(c), tagMap[c.ID]))
	}
	httpx.JSON(w, http.StatusOK, chatPage{Chats: items, Total: total, Page: page, Per: per})
}

type chatCreateReq struct {
	Title      string          `json:"title"`
	ProviderID string          `json:"provider_id"`
	ModelID    string          `json:"model_id"`
	FolderID   *string         `json:"folder_id"`
	Params     *map[string]any `json:"params"`
	Pinned     bool            `json:"pinned"`
}

func (d *Deps) handleCreateChat(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	var body chatCreateReq
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := d.validateChatModel(r.Context(), body.ProviderID, body.ModelID); err != nil {
		d.fail(w, r, err)
		return
	}
	c := &store.Chat{
		UserID:     u.ID,
		Title:      strings.TrimSpace(body.Title),
		ProviderID: body.ProviderID,
		ModelID:    body.ModelID,
		Pinned:     body.Pinned,
	}
	if body.FolderID != nil && *body.FolderID != "" {
		if _, err := d.ownFolder(r.Context(), u.ID, *body.FolderID); err != nil {
			d.fail(w, r, err)
			return
		}
		c.FolderID = body.FolderID
	}
	if body.Params != nil {
		pj, err := marshalParams(*body.Params)
		if err != nil {
			d.fail(w, r, err)
			return
		}
		c.ParamsJSON = pj
	}
	if err := d.Store.Chats.Create(r.Context(), c); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toChatJSON(c))
}

func (d *Deps) handleGetChat(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	tagMap, err := d.Store.Tags.TagsForChats(r.Context(), []string{c.ID})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, withTags(toChatJSON(c), tagMap[c.ID]))
}

type chatPatchReq struct {
	Title      *string         `json:"title"`
	ProviderID *string         `json:"provider_id"`
	ModelID    nullableStr     `json:"model_id"`
	FolderID   nullableStr     `json:"folder_id"`
	Params     *map[string]any `json:"params"`
	Pinned     *bool           `json:"pinned"`
}

func (d *Deps) handlePatchChat(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	var body chatPatchReq
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Title != nil {
		t := strings.TrimSpace(*body.Title)
		if t == "" {
			httpx.ErrorJSON(w, http.StatusBadRequest, "title must not be empty")
			return
		}
		c.Title = t
	}
	if body.ProviderID != nil {
		c.ProviderID = *body.ProviderID
	}
	if body.ModelID.Set {
		c.ModelID = body.ModelID.Val // null → "" (unsets)
	}
	if err := d.validateChatModel(r.Context(), c.ProviderID, c.ModelID); err != nil {
		d.fail(w, r, err)
		return
	}
	if body.FolderID.Set {
		if body.FolderID.Null || body.FolderID.Val == "" {
			c.FolderID = nil
		} else {
			if _, err := d.ownFolder(r.Context(), u.ID, body.FolderID.Val); err != nil {
				d.fail(w, r, err)
				return
			}
			v := body.FolderID.Val
			c.FolderID = &v
		}
	}
	if body.Params != nil {
		pj, err := marshalParams(*body.Params)
		if err != nil {
			d.fail(w, r, err)
			return
		}
		c.ParamsJSON = pj
	}
	if body.Pinned != nil {
		c.Pinned = *body.Pinned
	}

	if err := d.Store.Chats.Update(r.Context(), c); err != nil {
		d.fail(w, r, err)
		return
	}
	tagMap, err := d.Store.Tags.TagsForChats(r.Context(), []string{c.ID})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, withTags(toChatJSON(c), tagMap[c.ID]))
}

func (d *Deps) handleDeleteChat(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if err := d.Store.Chats.Delete(r.Context(), c.ID); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusNoContent, nil)
}

// marshalParams caps serialized params and rejects non-object payloads.
func marshalParams(m map[string]any) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", httpErr(http.StatusBadRequest, "invalid params")
	}
	if len(b) > 64<<10 {
		return "", httpErr(http.StatusBadRequest, "params too large")
	}
	return string(b), nil
}

// --- retract (Esc-Esc cancelled-exchange cleanup) -----------------------------

// chatTurns marks chats whose streamed turn has not fully settled server-side.
// The chat service persists the assistant row on a context DETACHED from the
// request (fireCtx, internal/chat/service.go), so an aborted client can see
// its partial appended moments AFTER the SSE request died. A retract landing
// inside that window would delete the exchange only for the late partial to
// re-litter the chat underneath, so retract refuses (409) while the turn is
// live. apichat-local by necessity: Deps is a frozen field set and the
// service's own turn accounting is per-user and unexported (and internal/chat
// is off-limits here).
var chatTurns = struct {
	mu   sync.Mutex
	live map[string]int
}{live: map[string]int{}}

// trackChatTurn marks chatID busy until the returned release runs. streamSend
// releases only after the service event channel has fully drained — i.e. once
// run() has persisted any partial — so the guard spans the whole late-write
// window, not just the live HTTP request. Reference-counted like the service's
// per-user tally: same-chat overlap is theoretical (per-user cap) but cheap.
func trackChatTurn(chatID string) (release func()) {
	chatTurns.mu.Lock()
	chatTurns.live[chatID]++
	chatTurns.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			chatTurns.mu.Lock()
			if n := chatTurns.live[chatID] - 1; n > 0 {
				chatTurns.live[chatID] = n
			} else {
				delete(chatTurns.live, chatID)
			}
			chatTurns.mu.Unlock()
		})
	}
}

func chatTurnLive(chatID string) bool {
	chatTurns.mu.Lock()
	defer chatTurns.mu.Unlock()
	return chatTurns.live[chatID] > 0
}

// handleRetractMessage deletes a cancelled exchange: the target message, the
// user turn it answered and everything hanging below them — the server half
// of the composer's Esc-Esc gesture (stop, delete, edit, resend). Internals
// mirror regenerate's ?from= contract: ownChat ownership (foreign/unknown
// chat → 404, never 403), 404 for an unknown/foreign message — but nothing is
// re-sent, the turn just vanishes.
//
// Branch-aware: the whole version slot vanishes — every sibling version of
// the cancelled user turn (and each version's continuations) is deleted as a
// subtree, and the chat's leaf lands on the deepest child of the surviving
// parent, so an untouched sibling branch keeps the conversation intact.
//
// Keyed by the ASSISTANT message id, because that is the only server id the
// SSE `meta` event exposes: the user row's id never crosses the wire. When
// the id names an assistant row, its parent (or the row directly before it,
// legacy chats) is the exchange head; any other role retracts from the row
// itself.
//
// Ordering: auth 401 → chat ownership 404 → live-stream guard 409 (checked
// BEFORE touching rows: a delete during the late-persist window is the exact
// corruption this guard exists to prevent) → message 404. The response counts
// what actually vanished ({deleted: N}).
func (d *Deps) handleRetractMessage(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	msgID := r.PathValue("msgId")
	if msgID == "" {
		httpx.ErrorJSON(w, http.StatusNotFound, "not found")
		return
	}
	if chatTurnLive(c.ID) {
		httpx.ErrorJSON(w, http.StatusConflict, "stream still running for this chat")
		return
	}
	m, err := d.Store.Messages.Get(r.Context(), c.ID, msgID)
	if err != nil {
		d.fail(w, r, err) // unknown id or a row from another chat → 404 (Get is chat-scoped)
		return
	}

	// Exchange head: the user row of the cancelled pair.
	head := m
	if m.Role == "assistant" {
		if p, perr := d.Store.Messages.Get(r.Context(), c.ID, m.ParentID); perr == nil && p.Role == "user" {
			head = p
		} else if prev, lerr := d.Store.Messages.List(r.Context(), c.ID, 1, m.ID); lerr == nil &&
			len(prev) == 1 && prev[0].Role == "user" {
			head = prev[0] // legacy row without a parent pointer
		}
	}

	// Everything from the retracted turn forward goes. Branch data: every
	// sibling version of the turn plus its continuations (later turns live
	// inside the sub-tree). Legacy linear rows: truncate inclusive.
	before, err := d.Store.Messages.Count(r.Context(), c.ID)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if head.ParentID == "" {
		if err := d.Store.Messages.DeleteFrom(r.Context(), c.ID, head.ID); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			d.fail(w, r, err)
			return
		}
	} else {
		var targets []*store.Message
		siblings, err := d.Store.Messages.Children(r.Context(), c.ID, head.ParentID)
		if err != nil {
			d.fail(w, r, err)
			return
		}
		headSeen := false
		for _, s := range siblings {
			if s.Role != head.Role {
				continue // only versions of the retracted turn itself vanish
			}
			if s.ID == head.ID {
				headSeen = true
			}
			targets = append(targets, s)
		}
		if !headSeen { // head not among siblings (stale parent pointer)
			targets = append(targets, head)
		}
		for _, s := range targets {
			if _, derr := d.Store.Messages.DeleteSubtree(r.Context(), c.ID, s.ID); derr != nil {
				d.fail(w, r, derr)
				return
			}
		}
	}
	after, err := d.Store.Messages.Count(r.Context(), c.ID)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	deleted := before - after

	// Leaf: newest surviving continuation of the parent turn (or the parent
	// itself once its whole version slot is gone), else an empty chat.
	newLeaf := ""
	if after > 0 && head.ParentID != "" {
		if leaf, lerr := d.Store.Messages.DeepestChild(r.Context(), c.ID, head.ParentID); lerr == nil {
			newLeaf = leaf
		}
		if newLeaf == "" {
			newLeaf = head.ParentID
		}
	}
	if err := d.Store.Chats.SetLeaf(r.Context(), c.ID, newLeaf); err != nil && !errors.Is(err, store.ErrNotFound) {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int{"deleted": deleted})
}
