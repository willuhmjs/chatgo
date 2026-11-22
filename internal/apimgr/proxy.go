package apimgr

import "net/http"

// POST /v1/chat/completions — OpenAI-compatible passthrough, Bearer ck_…
// api-token auth with user_id binding. BE-apimgr implements using
// store.Tokens.HashLookup + providers.Client.
func registerProxy(mux *http.ServeMux, d *Deps) {}
