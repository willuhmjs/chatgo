package apichat

import "net/http"

// POST/DELETE /api/chats/{id}/share → {token} ; GET /api/share/{token}
// (public read-only chat payload). BE-apichat implements.
func registerShare(mux *http.ServeMux, d *Deps) {}
