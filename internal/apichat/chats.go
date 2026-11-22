package apichat

import "net/http"

// CRUD /api/chats (list: ?q=&folder=&tag=&page=&per= default 30),
// GET/DELETE/PATCH /api/chats/{id}, PUT /api/chats/{id}/folder,
// PUT /api/chats/{id}/tags, POST /api/chats (create).
// BE-apichat implements against store.Chats + store.Folders/Tags.
func registerChats(mux *http.ServeMux, d *Deps) {}
