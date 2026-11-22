// Folders & tags HTTP surface: CRUD /api/folders, /api/tags plus the chat
// assignments PUT /api/chats/{id}/folder and PUT /api/chats/{id}/tags.
// Everything is per-user; foreign rows answer 404 (contract §Non-negotiables).

package apichat

import (
	"net/http"

	"chatgo/internal/api/httpx"
	"chatgo/internal/store"
)

// registerFoldersTags mounts folder/tag CRUD and chat assignments.
func registerFoldersTags(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("GET /api/folders", d.handleListFolders)
	mux.HandleFunc("POST /api/folders", d.handleCreateFolder)
	mux.HandleFunc("PUT /api/folders/{id}", d.handleUpdateFolder)
	mux.HandleFunc("DELETE /api/folders/{id}", d.handleDeleteFolder)

	mux.HandleFunc("GET /api/tags", d.handleListTags)
	mux.HandleFunc("POST /api/tags", d.handleCreateTag)
	mux.HandleFunc("PUT /api/tags/{id}", d.handleUpdateTag)
	mux.HandleFunc("DELETE /api/tags/{id}", d.handleDeleteTag)

	mux.HandleFunc("PUT /api/chats/{id}/folder", d.handleSetChatFolder)
	mux.HandleFunc("PUT /api/chats/{id}/tags", d.handleSetChatTags)
}

// --- folders ---------------------------------------------------------------

type folderReq struct {
	Name     string      `json:"name"`
	ParentID nullableStr `json:"parent_id"`
	Sort     *int        `json:"sort"`
}

func (d *Deps) handleListFolders(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	fs, err := d.Store.Folders.List(r.Context(), u.ID)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	out := make([]*folderJSON, 0, len(fs))
	for _, f := range fs {
		out = append(out, toFolderJSON(f))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (d *Deps) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	var body folderReq
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	f := &store.Folder{UserID: u.ID, Name: body.Name}
	if body.ParentID.Set && !body.ParentID.Null && body.ParentID.Val != "" {
		pid := body.ParentID.Val
		f.ParentID = &pid
	}
	if body.Sort != nil {
		f.Sort = *body.Sort
	}
	if err := d.Store.Folders.Create(r.Context(), f); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toFolderJSON(f))
}

func (d *Deps) handleUpdateFolder(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	f, err := d.Store.Folders.Get(r.Context(), r.PathValue("id"))
	if err != nil || f.UserID != u.ID {
		httpx.ErrorJSON(w, http.StatusNotFound, "not found")
		return
	}
	var body folderReq
	if err := httpx.DecodeLoose(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name != "" {
		f.Name = body.Name
	}
	if body.ParentID.Set {
		if body.ParentID.Null || body.ParentID.Val == "" {
			f.ParentID = nil
		} else {
			pid := body.ParentID.Val
			f.ParentID = &pid
		}
	}
	if body.Sort != nil {
		f.Sort = *body.Sort
	}
	if err := d.Store.Folders.Update(r.Context(), f); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toFolderJSON(f))
}

func (d *Deps) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	// User-scoped delete: a foreign id can't affect rows → 404.
	if err := d.Store.Folders.Delete(r.Context(), r.PathValue("id"), u.ID); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusNoContent, nil)
}

// --- tags --------------------------------------------------------------------

type tagReq struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (d *Deps) handleListTags(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	ts, err := d.Store.Tags.List(r.Context(), u.ID)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	out := make([]*tagJSON, 0, len(ts))
	for _, t := range ts {
		out = append(out, toTagJSON(t))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (d *Deps) handleCreateTag(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	var body tagReq
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	t := &store.Tag{UserID: u.ID, Name: body.Name, Color: body.Color}
	if err := d.Store.Tags.Create(r.Context(), t); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toTagJSON(t))
}

func (d *Deps) handleUpdateTag(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	t, err := d.Store.Tags.Get(r.Context(), r.PathValue("id"))
	if err != nil || t.UserID != u.ID {
		httpx.ErrorJSON(w, http.StatusNotFound, "not found")
		return
	}
	var body tagReq
	if err := httpx.DecodeLoose(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name != "" {
		t.Name = body.Name
	}
	if body.Color != "" {
		t.Color = body.Color
	}
	if err := d.Store.Tags.Update(r.Context(), t); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTagJSON(t))
}

func (d *Deps) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	if err := d.Store.Tags.Delete(r.Context(), r.PathValue("id"), u.ID); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusNoContent, nil)
}

// --- chat assignments -----------------------------------------------------------

type chatFolderReq struct {
	FolderID nullableStr `json:"folder_id"`
}

// handleSetChatFolder moves a chat to a folder (folder_id null = unfile).
func (d *Deps) handleSetChatFolder(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	var body chatFolderReq
	if err := httpx.Decode(r, &body); err != nil || !body.FolderID.Set {
		httpx.ErrorJSON(w, http.StatusBadRequest, "folder_id required")
		return
	}
	folderID := ""
	if !body.FolderID.Null {
		folderID = body.FolderID.Val
	}
	if folderID != "" {
		if _, err := d.ownFolder(r.Context(), u.ID, folderID); err != nil {
			d.fail(w, r, err) // foreign folder id → 404
			return
		}
	}
	if err := d.Store.Chats.SetFolder(r.Context(), c.ID, folderID); err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toChatJSON(c))
}

// chatTagsReq accepts api.ts's `tag_ids` name plus the `tags` alias from the
// build brief; either (or both, merged) sets the full tag list.
type chatTagsReq struct {
	TagIDs []string `json:"tag_ids"`
	Tags   []string `json:"tags"`
}

// handleSetChatTags replaces the chat's tag set wholesale.
func (d *Deps) handleSetChatTags(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	var body chatTagsReq
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ids := append(append([]string{}, body.TagIDs...), body.Tags...)
	if err := d.Store.Tags.SetChatTags(r.Context(), c.ID, u.ID, ids); err != nil {
		d.fail(w, r, err) // foreign tag id or lost chat → 404
		return
	}
	tagMap, err := d.Store.Tags.TagsForChats(r.Context(), []string{c.ID})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, withTags(toChatJSON(c), tagMap[c.ID]))
}
