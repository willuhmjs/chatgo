// Package apimgr registers the management-plane HTTP surface (auth API,
// admin, providers, users, tokens, /v1 proxy).
//
// Handlers: use chatgo/internal/api/httpx for JSON (NOT internal/api —
// import cycle); get the caller via d.Auth.UserFrom(r.Context()).
package apimgr

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

// Register mounts every management-plane group on the shared mux.
func Register(mux *http.ServeMux, d *Deps) {
	registerAuth(mux, d)
	registerAdmin(mux, d)
	registerProviders(mux, d)
	registerUsers(mux, d)
	registerTokens(mux, d)
	registerProxy(mux, d)
}
