// Folder/tag CRUD + chat assignment HTTP tests (BE-apichat owned). Prefix: TestAPI.

package apichat

import (
	"net/http"
	"testing"
)

func TestAPI_FolderCRUDAndWalls(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s}
	alice := apiTestUser(t, s, "hf@x.io")
	bob := apiTestUser(t, s, "hf2@x.io")

	// Create + validation.
	rec := apiServe(t, d, alice, "POST", "/api/folders", map[string]any{"name": "Docs"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create folder: %d %s", rec.Code, rec.Body.String())
	}
	folder := decodeJSON[folderJSON](t, rec)
	if folder.ID == "" || folder.Name != "Docs" {
		t.Fatalf("folder: %+v", folder)
	}
	if rec := apiServe(t, d, alice, "POST", "/api/folders", map[string]any{"name": " "}); rec.Code != http.StatusBadRequest {
		t.Errorf("blank folder name: %d", rec.Code)
	}

	// Bob cannot touch Alice's folder.
	if rec := apiServe(t, d, bob, "PUT", "/api/folders/"+folder.ID, map[string]any{"name": "stolen"}); rec.Code != http.StatusNotFound {
		t.Errorf("foreign folder PUT: %d", rec.Code)
	}
	if rec := apiServe(t, d, bob, "DELETE", "/api/folders/"+folder.ID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("foreign folder DELETE: %d", rec.Code)
	}
	// Bob's list stays empty.
	if rec := apiServe(t, d, bob, "GET", "/api/folders", nil); rec.Code != http.StatusOK {
		t.Fatalf("bob folders: %d", rec.Code)
	} else if fs := decodeJSON[[]folderJSON](t, rec); len(fs) != 0 {
		t.Errorf("bob sees folders: %+v", fs)
	}

	// Update + delete (owner).
	if rec := apiServe(t, d, alice, "PUT", "/api/folders/"+folder.ID, map[string]any{"name": "Specs", "sort": 3}); rec.Code != http.StatusOK {
		t.Errorf("folder PUT: %d %s", rec.Code, rec.Body.String())
	} else if f2 := decodeJSON[folderJSON](t, rec); f2.Name != "Specs" || f2.Sort != 3 {
		t.Errorf("folder update result: %+v", f2)
	}
	if rec := apiServe(t, d, alice, "DELETE", "/api/folders/"+folder.ID, nil); rec.Code != http.StatusNoContent {
		t.Errorf("folder DELETE: %d", rec.Code)
	}
	if rec := apiServe(t, d, alice, "GET", "/api/chats", nil); rec.Code != http.StatusOK {
		t.Fatalf("list after delete: %d", rec.Code)
	}
}

func TestAPI_TagCRUDAndChatAssignment(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s}
	alice := apiTestUser(t, s, "ht@x.io")
	bob := apiTestUser(t, s, "ht2@x.io")
	c := apiTestChat(t, s, alice, "tag-target")

	rec := apiServe(t, d, alice, "POST", "/api/tags", map[string]any{"name": "go", "color": "#fff"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tag: %d %s", rec.Code, rec.Body.String())
	}
	tagA := decodeJSON[tagJSON](t, rec)
	if rec := apiServe(t, d, alice, "POST", "/api/tags", map[string]any{"name": "go"}); rec.Code != http.StatusConflict {
		t.Errorf("duplicate tag: %d, want 409", rec.Code)
	}
	rec = apiServe(t, d, bob, "POST", "/api/tags", map[string]any{"name": "go"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("bob same tag name: %d", rec.Code)
	}
	tagB := decodeJSON[tagJSON](t, rec)

	// Tag walls on updates.
	if rec := apiServe(t, d, bob, "PUT", "/api/tags/"+tagA.ID, map[string]any{"name": "hijack"}); rec.Code != http.StatusNotFound {
		t.Errorf("foreign tag PUT: %d", rec.Code)
	}
	if rec := apiServe(t, d, bob, "DELETE", "/api/tags/"+tagA.ID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("foreign tag DELETE: %d", rec.Code)
	}

	// PUT /api/chats/{id}/folder with ownership walls.
	fRec := apiServe(t, d, alice, "POST", "/api/folders", map[string]any{"name": "Work"})
	ownFolder := decodeJSON[folderJSON](t, fRec)
	fRec = apiServe(t, d, bob, "POST", "/api/folders", map[string]any{"name": "BobsWork"})
	bobsFolder := decodeJSON[folderJSON](t, fRec)

	if rec := apiServe(t, d, alice, "PUT", "/api/chats/"+c.ID+"/folder", map[string]any{"folder_id": bobsFolder.ID}); rec.Code != http.StatusNotFound {
		t.Errorf("assign foreign folder: %d, want 404", rec.Code)
	}
	if rec := apiServe(t, d, bob, "PUT", "/api/chats/"+c.ID+"/folder", map[string]any{"folder_id": ownFolder.ID}); rec.Code != http.StatusNotFound {
		t.Errorf("foreign chat folder assign: %d", rec.Code)
	}
	if rec := apiServe(t, d, alice, "PUT", "/api/chats/"+c.ID+"/folder", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Errorf("missing folder_id key: %d, want 400", rec.Code)
	}
	if rec := apiServe(t, d, alice, "PUT", "/api/chats/"+c.ID+"/folder", map[string]any{"folder_id": ownFolder.ID}); rec.Code != http.StatusOK {
		t.Errorf("assign own folder: %d %s", rec.Code, rec.Body.String())
	}
	if rec := apiServe(t, d, alice, "PUT", "/api/chats/"+c.ID+"/folder", map[string]any{"folder_id": nil}); rec.Code != http.StatusOK {
		t.Errorf("unfile: %d", rec.Code)
	}

	// PUT /api/chats/{id}/tags (both tag_ids and tags alias).
	if rec := apiServe(t, d, alice, "PUT", "/api/chats/"+c.ID+"/tags",
		map[string]any{"tag_ids": []string{tagA.ID}}); rec.Code != http.StatusOK {
		t.Errorf("set tags: %d %s", rec.Code, rec.Body.String())
	} else if j := decodeJSON[chatJSON](t, rec); len(j.Tags) != 1 || j.Tags[0].ID != tagA.ID {
		t.Errorf("set tags result: %+v", j.Tags)
	}
	if rec := apiServe(t, d, alice, "PUT", "/api/chats/"+c.ID+"/tags",
		map[string]any{"tags": []string{tagA.ID, tagB.ID}}); rec.Code != http.StatusNotFound {
		t.Errorf("mixed-ownership tag ids: %d, want 404", rec.Code)
	}
	if rec := apiServe(t, d, bob, "PUT", "/api/chats/"+c.ID+"/tags",
		map[string]any{"tag_ids": []string{tagB.ID}}); rec.Code != http.StatusNotFound {
		t.Errorf("foreign chat tagging: %d, want 404", rec.Code)
	}
	// Clearing works.
	if rec := apiServe(t, d, alice, "PUT", "/api/chats/"+c.ID+"/tags",
		map[string]any{"tag_ids": []string{}}); rec.Code != http.StatusOK {
		t.Errorf("clear tags: %d", rec.Code)
	} else if j := decodeJSON[chatJSON](t, rec); len(j.Tags) != 0 {
		t.Errorf("tags not cleared: %+v", j.Tags)
	}

	// Tag delete removes it from chats.
	if rec := apiServe(t, d, alice, "DELETE", "/api/tags/"+tagA.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("tag delete: %d", rec.Code)
	}
	m, err := s.Tags.TagsForChats(t.Context(), []string{c.ID})
	if err != nil || len(m[c.ID]) != 0 {
		t.Errorf("tag rows after delete: %v, %v", m[c.ID], err)
	}
}
