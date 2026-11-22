package apimgr

import "net/http"

// POST /api/auth/login, POST /api/auth/register, POST /api/auth/logout,
// GET /api/auth/me, GET /api/auth/config,
// GET /api/auth/oidc/login →302, GET /api/auth/oidc/callback.
// File lives here but is OWNED BY BE-APICHAT (contract §Agent ownership —
// it has the session ctx); BE-auth owns the middleware internals.
func registerAuth(mux *http.ServeMux, d *Deps) {}
