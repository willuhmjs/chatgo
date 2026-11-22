package apichat

import "net/http"

// CRUD /api/folders + /api/tags (per-user). BE-apichat implements against
// store.Folders/Tags (BE4 stores).
func registerFoldersTags(mux *http.ServeMux, d *Deps) {}
