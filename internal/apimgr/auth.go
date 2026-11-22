package apimgr

import "net/http"

// POST /api/auth/login, POST /api/auth/register, POST /api/auth/logout,
// GET /api/auth/me, GET /api/auth/config,
// GET /api/auth/oidc/login →302, GET /api/auth/oidc/callback.
//
// Paths match internal/api.AuthPolicy: login/register/config/oidc/* are
// public (Attach only), /me runs behind Require, logout is cookie-carrying
// but anon-tolerant.
//
// CONTRACT-DEVIATION (ownership): docs/CONTRACTS.md §Agent ownership assigns
// this file to BE-apichat; this swarm's task brief assigns it to BE-auth.
// Handlers are thin wiring over the exported *auth.Auth surface (logic and
// tests live in internal/auth, avoiding an auth→apimgr import cycle).
func registerAuth(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("POST /api/auth/login", d.Auth.HandleLogin)
	mux.HandleFunc("POST /api/auth/register", d.Auth.HandleRegister)
	mux.HandleFunc("POST /api/auth/logout", d.Auth.HandleLogout)
	mux.HandleFunc("GET /api/auth/config", d.Auth.HandleConfig)
	mux.HandleFunc("GET /api/auth/me", d.Auth.HandleMe)
	mux.HandleFunc("GET /api/auth/oidc/login", d.Auth.OIDCLogin)
	mux.HandleFunc("GET /api/auth/oidc/callback", d.Auth.OIDCCallback)
}
