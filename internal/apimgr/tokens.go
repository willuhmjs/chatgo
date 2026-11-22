package apimgr

import "net/http"

// CRUD /api/tokens — create returns the full ck_… token once
// (store only keeps sha256). BE-apimgr implements against store.Tokens (BE5).
func registerTokens(mux *http.ServeMux, d *Deps) {}
