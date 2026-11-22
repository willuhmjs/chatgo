package apichat

import "net/http"

// POST /api/chats/{id}/messages {content, images:[{file_id}], search} → SSE
// POST /api/chats/{id}/regenerate?from={msgID} → SSE
// GET  /api/chats/{id}/messages?before=&limit=
// POST /api/files (multipart) + GET /files/{id}/{name} + GET /api/files?kind=
// BE-apichat implements using api.SSEWriter + chat.Service.Send.
func registerMessages(mux *http.ServeMux, d *Deps) {}
