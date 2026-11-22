// Package apichat registers the chat-plane HTTP surface (chats, messages,
// share, folders/tags, and — per contract §Agent ownership — the auth API
// endpoints live in internal/apimgr/auth.go but are owned by BE-apichat).
//
// Handlers: use chatgo/internal/api/httpx for JSON/SSE (NOT internal/api —
// import cycle); get the caller via d.Auth.UserFrom(r.Context()).
package apichat

import (
	"net/http"

	"chatgo/internal/auth"
	"chatgo/internal/chat"
	"chatgo/internal/config"
	"chatgo/internal/files"
	"chatgo/internal/providers"
	"chatgo/internal/search"
	"chatgo/internal/store"
)

// Deps mirrors api.Deps for this route group (frozen field set).
type Deps struct {
	Cfg       *config.Config
	Store     *store.Store
	Auth      *auth.Auth
	Chat      *chat.Service
	Search    *search.Service
	Files     *files.Service
	Providers providers.Client
}

// Register mounts every chat-plane group on the shared mux. Individual
// register funcs are scaffold stubs (empty); BE-apichat fills each file.
func Register(mux *http.ServeMux, d *Deps) {
	registerChats(mux, d)
	registerMessages(mux, d)
	registerShare(mux, d)
	registerFoldersTags(mux, d)
}
