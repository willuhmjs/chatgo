package apimgr

import "net/http"

// GET/PUT /api/settings (admin) + GET/PUT /api/me/settings (self).
// BE-apimgr implements against store.Settings.
func registerAdmin(mux *http.ServeMux, d *Deps) {}
