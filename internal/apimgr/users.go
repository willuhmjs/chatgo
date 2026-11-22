package apimgr

import "net/http"

// GET /api/users (admin), POST /api/users/{id}/approve,
// PUT /api/users/{id}/role. BE-apimgr implements.
func registerUsers(mux *http.ServeMux, d *Deps) {}
