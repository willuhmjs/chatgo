package apimgr

import "net/http"

// GET/POST /api/providers, PUT/DELETE /api/providers/{id} (admin),
// POST /api/providers/{id}/sync (refresh models cache via Providers.ListModels),
// GET /api/models. BE-apimgr implements.
func registerProviders(mux *http.ServeMux, d *Deps) {}
