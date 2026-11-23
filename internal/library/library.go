// Package library is the BE-SOCIAL feature plane (docs/FEATURES-V2.md
// §BE-SOCIAL): prompt library, message ratings, chat forking and export.
//
// It owns its DDL (created via Init on store.DB — NEVER via store's
// migration files, per the coordination law) and mounts every route through
// Register(mux, Deps); the integrator wires it next to apichat/apimgr.
//
// Handlers: use chatgo/internal/api/httpx for JSON (NOT internal/api —
// import cycle); get the caller via d.Auth.UserFrom(r.Context()).
package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"chatgo/internal/api/httpx"
	"chatgo/internal/auth"
	"chatgo/internal/store"
)

// Pagination defaults (contract §Non-negotiables: every list paginated,
// default 30, cap 100).
const (
	defaultPer = 30
	maxPer     = 100
)

// schema is this package's own DDL (coordination law: features own their
// tables, CREATE TABLE IF NOT EXISTS, run in Init). Timestamps are unix
// seconds like every other table; ids are uuid v4 TEXT.
const schema = `
CREATE TABLE IF NOT EXISTS prompts (
	id TEXT PRIMARY KEY,
	user_id TEXT REFERENCES users(id) ON DELETE CASCADE, -- NULL = admin-curated global
	title TEXT NOT NULL DEFAULT '',
	content TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_prompts_user ON prompts(user_id);

CREATE TABLE IF NOT EXISTS ratings (
	id TEXT PRIMARY KEY,
	message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
	chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	value INTEGER NOT NULL CHECK (value IN (-1, 1)),
	comment TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL DEFAULT 0,
	UNIQUE (message_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_ratings_chat ON ratings(chat_id);
CREATE INDEX IF NOT EXISTS idx_ratings_user ON ratings(user_id);
`

// Init creates the prompts/ratings tables on the shared handle. Idempotent;
// Register calls it, so the integrator normally only calls Register.
func Init(db *sql.DB) error {
	if db == nil {
		return errors.New("library: Init requires a non-nil *sql.DB")
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

// Register mounts the whole BE-SOCIAL surface on the shared mux.
//
// CONTRACT-DEVIATION: Register returns an error (schema init can fail) —
// apimgr/apichat Register are void because their DDL is store-owned; feature
// packages that own DDL must surface Init failures to startup instead of
// panicking.
func Register(mux *http.ServeMux, d *Deps) error {
	if d == nil || d.Store == nil {
		return errors.New("library: Register requires Deps.Store")
	}
	if err := Init(d.Store.DB); err != nil {
		return fmt.Errorf("library: init schema: %w", err)
	}
	registerPrompts(mux, d)
	registerRatings(mux, d)
	registerRatingsAdmin(mux, d)
	registerFork(mux, d)
	registerExport(mux, d)
	return nil
}

// --- shared user lookup (same pattern as internal/apichat/chats.go) --------

// ctxKeyLocalUser is a library-local context key checked only when the auth
// middleware found nothing. CONTRACT-DEVIATION: the scaffold's auth ctx keys
// are unexported (internal/auth/middleware.go), so handler tests cannot forge
// a logged-in request without them; this fallback keeps tests hermetic. The
// real auth middleware is consulted FIRST, so production behavior is
// unchanged (nothing else can plant this key — it is package-private).
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

// requireUser resolves the session user attached by auth middleware —
// defense-in-depth even though api/server.go AuthPolicy gates /api/*.
func (d *Deps) requireUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u, ok := d.userFrom(r)
	if !ok || u.ID == "" {
		httpx.ErrorJSON(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	return u, true
}

// --- shared error mapping (mirrors apichat) ---------------------------------

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
	default:
		httpx.InternalError(w, r, err)
	}
}

// ownChat loads a live chat and enforces ownership (foreign → ErrNotFound,
// contract §Non-negotiables: never 403, no existence leak).
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

// --- shared JSON shapes (mirror the frozen apichat DTOs = web/src/lib/api.ts)

// chatJSON mirrors apichat's chatJSON so fork/export answer the exact shape
// of GET /api/chats/{id}. CONTRACT-DEVIATION: apichat's type is unexported
// and importing apichat from a feature package is a layering inversion, so
// the shape is mirrored here (forked chats carry no tags — tags omitempty).
type chatJSON struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	ProviderID string         `json:"provider_id"`
	ModelID    string         `json:"model_id"`
	Params     map[string]any `json:"params,omitempty"`
	Pinned     bool           `json:"pinned"`
	FolderID   *string        `json:"folder_id,omitempty"`
	ShareToken *string        `json:"share_token,omitempty"`
	CreatedAt  int64          `json:"created_at"`
	UpdatedAt  int64          `json:"updated_at"`
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

type sourceJSON struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type messageJSON struct {
	ID        string       `json:"id"`
	ChatID    string       `json:"chat_id"`
	Role      string       `json:"role"`
	Content   string       `json:"content"`
	Sources   []sourceJSON `json:"sources,omitempty"`
	Error     *string      `json:"error,omitempty"`
	TokensIn  int          `json:"tokens_in,omitempty"`
	TokensOut int          `json:"tokens_out,omitempty"`
	CreatedAt int64        `json:"created_at"`
}

func toMessageJSON(m *store.Message) *messageJSON {
	j := &messageJSON{
		ID: m.ID, ChatID: m.ChatID, Role: m.Role, Content: m.Content,
		Error: m.Error, TokensIn: m.TokensIn, TokensOut: m.TokensOut,
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
