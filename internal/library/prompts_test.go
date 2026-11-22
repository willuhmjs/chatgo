// Prompt CRUD tests: per-user isolation, admin-only globals, ownership walls.

package library

import (
	"net/http"
	"testing"
)

type promptResp struct {
	ID        string  `json:"id"`
	UserID    *string `json:"user_id"`
	Global    bool    `json:"global"`
	Title     string  `json:"title"`
	Content   string  `json:"content"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

type promptListResp struct {
	Prompts []promptResp `json:"prompts"`
	Total   int          `json:"total"`
	Page    int          `json:"page"`
	Per     int          `json:"per"`
}

func TestLib_PromptIsolationAndGlobals(t *testing.T) {
	s := testStore(t)
	d := &Deps{Store: s}
	alice := testUser(t, s, "a@x.io")
	bob := testUser(t, s, "b@x.io")
	root := testAdmin(t, s, "root@x.io")

	// Anonymous → 401 across the group.
	for _, r := range [][2]string{
		{"GET", "/api/prompts"}, {"POST", "/api/prompts"},
		{"PUT", "/api/prompts/x"}, {"DELETE", "/api/prompts/x"},
	} {
		mustStatus(t, serve(t, d, nil, r[0], r[1], nil), http.StatusUnauthorized)
	}

	// alice creates a private prompt.
	rec := serve(t, d, alice, "POST", "/api/prompts", map[string]any{"title": "Hi", "content": "there"})
	mustStatus(t, rec, http.StatusCreated)
	p := decodeJSON[promptResp](t, rec)
	if p.Global || p.UserID == nil || *p.UserID != alice.ID || p.Title != "Hi" || p.Content != "there" {
		t.Fatalf("create echoed wrong row: %+v", p)
	}

	// Isolation: bob's list never sees alice's row; alice's does.
	if l := decodeJSON[promptListResp](t, serve(t, d, bob, "GET", "/api/prompts", nil)); l.Total != 0 {
		t.Fatalf("bob sees %d prompts, want 0", l.Total)
	}
	l := decodeJSON[promptListResp](t, serve(t, d, alice, "GET", "/api/prompts", nil))
	if l.Total != 1 || l.Prompts[0].ID != p.ID || l.Page != 1 || l.Per != defaultPer {
		t.Fatalf("alice list = %+v", l)
	}

	// bob cannot mutate alice's prompt — 404, never 403 (no existence leak).
	mustStatus(t, serve(t, d, bob, "PUT", "/api/prompts/"+p.ID, map[string]any{"title": "x"}), http.StatusNotFound)
	mustStatus(t, serve(t, d, bob, "DELETE", "/api/prompts/"+p.ID, nil), http.StatusNotFound)

	// Global creation is admin-only.
	mustStatus(t, serve(t, d, bob, "POST", "/api/prompts",
		map[string]any{"title": "G", "content": "g", "global": true}), http.StatusForbidden)
	rec = serve(t, d, root, "POST", "/api/prompts",
		map[string]any{"title": "G", "content": "g", "global": true})
	mustStatus(t, rec, http.StatusCreated)
	g := decodeJSON[promptResp](t, rec)
	if !g.Global || g.UserID != nil {
		t.Fatalf("global row wrong: %+v", g)
	}

	// Both users now see own + global.
	if l := decodeJSON[promptListResp](t, serve(t, d, bob, "GET", "/api/prompts", nil)); l.Total != 1 || !l.Prompts[0].Global {
		t.Fatalf("bob should see only the global: %+v", l)
	}
	if l := decodeJSON[promptListResp](t, serve(t, d, alice, "GET", "/api/prompts", nil)); l.Total != 2 {
		t.Fatalf("alice should see 2: %+v", l)
	}

	// Global writes are admin-only; reads stay open.
	mustStatus(t, serve(t, d, alice, "PUT", "/api/prompts/"+g.ID, map[string]any{"content": "hack"}), http.StatusForbidden)
	mustStatus(t, serve(t, d, alice, "DELETE", "/api/prompts/"+g.ID, nil), http.StatusForbidden)
	rec = serve(t, d, root, "PUT", "/api/prompts/"+g.ID, map[string]any{"content": "v2"})
	mustStatus(t, rec, http.StatusOK)
	if got := decodeJSON[promptResp](t, rec); got.Content != "v2" || !got.Global {
		t.Fatalf("admin update wrong: %+v", got)
	}

	// alice updates her own; then deletes it.
	rec = serve(t, d, alice, "PUT", "/api/prompts/"+p.ID, map[string]any{"title": "Hi2"})
	mustStatus(t, rec, http.StatusOK)
	if got := decodeJSON[promptResp](t, rec); got.Title != "Hi2" || got.Content != "there" {
		t.Fatalf("owner update wrong: %+v", got)
	}
	mustStatus(t, serve(t, d, alice, "DELETE", "/api/prompts/"+p.ID, nil), http.StatusNoContent)
	if l := decodeJSON[promptListResp](t, serve(t, d, alice, "GET", "/api/prompts", nil)); l.Total != 1 {
		t.Fatalf("after delete alice sees %+v, want just the global", l)
	}
	mustStatus(t, serve(t, d, root, "DELETE", "/api/prompts/"+g.ID, nil), http.StatusNoContent)
}

func TestLib_PromptValidation(t *testing.T) {
	s := testStore(t)
	d := &Deps{Store: s}
	alice := testUser(t, s, "a@x.io")

	mustStatus(t, serve(t, d, alice, "POST", "/api/prompts", map[string]any{"title": "", "content": "x"}), http.StatusBadRequest)
	mustStatus(t, serve(t, d, alice, "POST", "/api/prompts", map[string]any{"title": "t", "content": ""}), http.StatusBadRequest)
	mustStatus(t, serve(t, d, alice, "POST", "/api/prompts",
		map[string]any{"title": "t", "content": string(make([]byte, maxPromptContent+1))}), http.StatusBadRequest)
	mustStatus(t, serve(t, d, alice, "PUT", "/api/prompts/does-not-exist", map[string]any{"title": "t"}), http.StatusNotFound)

	rec := serve(t, d, alice, "POST", "/api/prompts", map[string]any{"title": "t", "content": "c"})
	mustStatus(t, rec, http.StatusCreated)
	id := decodeJSON[promptResp](t, rec).ID
	mustStatus(t, serve(t, d, alice, "PUT", "/api/prompts/"+id, map[string]any{}), http.StatusBadRequest)
	mustStatus(t, serve(t, d, alice, "PUT", "/api/prompts/"+id, map[string]any{"title": " "}), http.StatusBadRequest)
}
