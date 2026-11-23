// Chat CRUD handler tests (BE-apichat owned). Prefix: TestAPI.

package apichat

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"chatgo/internal/store"
)

func apiTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open("file:memapi_"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared",
		[]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func apiTestUser(t *testing.T, s *store.Store, email string) *store.User {
	t.Helper()
	u := &store.User{Email: email, Name: "T " + email, Role: store.RoleUser}
	if err := s.Users.Insert(t.Context(), u); err != nil {
		t.Fatalf("Users.Insert: %v", err)
	}
	return u
}

func apiTestChat(t *testing.T, s *store.Store, u *store.User, title string) *store.Chat {
	t.Helper()
	c := &store.Chat{UserID: u.ID, Title: title}
	if err := s.Chats.Create(t.Context(), c); err != nil {
		t.Fatalf("Chats.Create: %v", err)
	}
	return c
}

// apiServe drives the full apichat route group through the real mux; the
// caller is faked through withUserForTest (auth ctx keys are unexported —
// see the CONTRACT-DEVIATION note in chats.go).
func apiServe(t *testing.T, d *Deps, u *store.User, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)

	var rdr io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rdr = strings.NewReader(b)
		case []byte:
			rdr = bytes.NewReader(b)
		default:
			raw, err := json.Marshal(b)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			rdr = bytes.NewReader(raw)
		}
	}
	req := httptest.NewRequest(method, target, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if u != nil {
		req = req.WithContext(withUserForTest(req.Context(), u))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return v
}

func TestAPI_ChatCRUDAndWalls(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s}
	alice := apiTestUser(t, s, "a@x.io")
	bob := apiTestUser(t, s, "b@x.io")

	// No user in context → 401 across the group.
	for _, r := range [][2]string{
		{"GET", "/api/chats"}, {"POST", "/api/chats"}, {"PATCH", "/api/chats/x"},
		{"DELETE", "/api/chats/x"}, {"GET", "/api/chats/x/messages"}, {"POST", "/api/chats/x/share"},
		{"GET", "/api/folders"}, {"GET", "/api/tags"}, {"PUT", "/api/chats/x/folder"},
	} {
		if rec := apiServe(t, d, nil, r[0], r[1], nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: got %d, want 401", r[0], r[1], rec.Code)
		}
	}

	// Create (defaults + explicit).
	rec := apiServe(t, d, alice, "POST", "/api/chats", map[string]any{})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	created := decodeJSON[chatJSON](t, rec)
	if created.Title != "New chat" || created.ID == "" {
		t.Errorf("defaults: %+v", created)
	}
	rec = apiServe(t, d, alice, "POST", "/api/chats", map[string]any{"title": "Hi", "pinned": true})
	pinned := decodeJSON[chatJSON](t, rec)
	if !pinned.Pinned {
		t.Errorf("pinned not stored: %+v", pinned)
	}

	// Read: owner 200, non-owner 404 (not 403!).
	if rec := apiServe(t, d, alice, "GET", "/api/chats/"+created.ID, nil); rec.Code != http.StatusOK {
		t.Errorf("get own: %d", rec.Code)
	}
	if rec := apiServe(t, d, bob, "GET", "/api/chats/"+created.ID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("get foreign: %d, want 404", rec.Code)
	}
	if rec := apiServe(t, d, bob, "PATCH", "/api/chats/"+created.ID, map[string]any{"title": "hax"}); rec.Code != http.StatusNotFound {
		t.Errorf("patch foreign: %d, want 404", rec.Code)
	}

	// PATCH title/params/pinned/folder.
	folderRec := apiServe(t, d, alice, "POST", "/api/folders", map[string]any{"name": "F1"})
	folder := decodeJSON[folderJSON](t, folderRec)
	rec = apiServe(t, d, alice, "PATCH", "/api/chats/"+created.ID, map[string]any{
		"title": "Renamed", "pinned": true, "params": map[string]any{"temperature": 0.7},
		"folder_id": folder.ID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	patched := decodeJSON[chatJSON](t, rec)
	if patched.Title != "Renamed" || !patched.Pinned || patched.Params["temperature"] != 0.7 ||
		patched.FolderID == nil || *patched.FolderID != folder.ID {
		t.Errorf("patch result: %+v", patched)
	}
	// Explicit null folder unsets.
	rec = apiServe(t, d, alice, "PATCH", "/api/chats/"+created.ID, map[string]any{"folder_id": nil})
	if again := decodeJSON[chatJSON](t, rec); again.FolderID != nil {
		t.Errorf("folder_id null did not unset: %+v", again)
	}

	// List: envelope + filters.
	listRec := apiServe(t, d, alice, "GET", "/api/chats?per=1000", nil)
	page := decodeJSON[chatPage](t, listRec)
	if page.Total != 2 || page.Per != maxPer || page.Page != 1 || len(page.Chats) != 2 {
		t.Fatalf("list: %+v", page)
	}
	if pinnedRec := apiServe(t, d, alice, "GET", "/api/chats?pinned=true", nil); true {
		pp := decodeJSON[chatPage](t, pinnedRec)
		if pp.Total != 2 { // both pinned now (Renamed pinned + "Hi" pinned)
			t.Errorf("pinned list total=%d, want 2", pp.Total)
		}
	}

	// Tag filter via store, FTS q filter + quoting safety.
	tag := &store.Tag{UserID: alice.ID, Name: "keep"}
	if err := s.Tags.Create(t.Context(), tag); err != nil {
		t.Fatal(err)
	}
	if err := s.Tags.SetChatTags(t.Context(), created.ID, alice.ID, []string{tag.ID}); err != nil {
		t.Fatal(err)
	}
	if tr := apiServe(t, d, alice, "GET", "/api/chats?tag="+tag.ID, nil); true {
		tp := decodeJSON[chatPage](t, tr)
		if tp.Total != 1 || tp.Chats[0].ID != created.ID || len(tp.Chats[0].Tags) != 1 || tp.Chats[0].Tags[0].Name != "keep" {
			t.Errorf("tag filter/tags: %+v", tp)
		}
	}
	if err := s.Messages.Append(t.Context(),
		&store.Message{ChatID: created.ID, Role: "user", Content: "zebra crossing facts"}); err != nil {
		t.Fatal(err)
	}
	if qr := apiServe(t, d, alice, "GET", "/api/chats?q=zebra", nil); true {
		qp := decodeJSON[chatPage](t, qr)
		if qp.Total != 1 || qp.Chats[0].ID != created.ID {
			t.Errorf("fts q list: %+v", qp)
		}
	}
	if nr := apiServe(t, d, alice, "GET", "/api/chats?q=not%20present%20anywhere", nil); true {
		if np := decodeJSON[chatPage](t, nr); np.Total != 0 {
			t.Errorf("fts miss should be empty: %+v", np)
		}
	}
	// Hostile q must not 500 or damage anything.
	evil := "/api/chats?q=" + url.QueryEscape(`"; DROP TABLE messages;--`)
	if er := apiServe(t, d, alice, "GET", evil, nil); er.Code != http.StatusOK {
		t.Errorf("hostile q: %d %s", er.Code, er.Body.String())
	}
	if _, err := s.Messages.Count(t.Context(), created.ID); err != nil {
		t.Errorf("messages table damaged: %v", err)
	}

	// Soft delete.
	if rec := apiServe(t, d, bob, "DELETE", "/api/chats/"+created.ID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("delete foreign: %d", rec.Code)
	}
	if rec := apiServe(t, d, alice, "DELETE", "/api/chats/"+created.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete own: %d", rec.Code)
	}
	if rec := apiServe(t, d, alice, "GET", "/api/chats/"+created.ID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete: %d", rec.Code)
	}
}

func TestAPI_ChatModelValidation(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s}
	u := apiTestUser(t, s, "mv@x.io")

	prov := &store.Provider{Name: "p1", BaseURL: "https://p1.invalid", Enabled: true}
	off := &store.Provider{Name: "p2", BaseURL: "https://p2.invalid", Enabled: false}
	for _, p := range []*store.Provider{prov, off} {
		if err := s.Providers.Insert(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Models.ReplaceForProvider(t.Context(), prov.ID, []store.ModelRow{
		{ModelID: "gpt-ok", Display: "OK", Enabled: true},
		{ModelID: "gpt-off", Display: "Off", Enabled: false},
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"model without provider", map[string]any{"model_id": "gpt-ok"}, http.StatusBadRequest},
		{"unknown provider", map[string]any{"provider_id": "nope", "model_id": "x"}, http.StatusNotFound},
		{"disabled provider", map[string]any{"provider_id": off.ID}, http.StatusBadRequest},
		{"unknown model", map[string]any{"provider_id": prov.ID, "model_id": "gpt-???"}, http.StatusNotFound},
		{"disabled model", map[string]any{"provider_id": prov.ID, "model_id": "gpt-off"}, http.StatusBadRequest},
		{"valid pair", map[string]any{"provider_id": prov.ID, "model_id": "gpt-ok"}, http.StatusCreated},
	}
	for _, tc := range cases {
		rec := apiServe(t, d, u, "POST", "/api/chats", tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s: got %d want %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}

	// PATCH enforces the same rules on the merged pair.
	c := apiTestChat(t, s, u, "patch-me")
	rec := apiServe(t, d, u, "PATCH", "/api/chats/"+c.ID, map[string]any{"model_id": "gpt-nope"})
	if rec.Code != http.StatusBadRequest { // provider-less chat may not gain a bare model
		t.Errorf("patch model w/o provider: %d, want 400", rec.Code)
	}
	rec = apiServe(t, d, u, "PATCH", "/api/chats/"+c.ID, map[string]any{"provider_id": prov.ID, "model_id": "gpt-ok"})
	if rec.Code != http.StatusOK {
		t.Errorf("patch good pair: %d %s", rec.Code, rec.Body.String())
	}

	// Unknown folder on create → 404.
	if rec := apiServe(t, d, u, "POST", "/api/chats", map[string]any{"folder_id": "ghost"}); rec.Code != http.StatusNotFound {
		t.Errorf("ghost folder: %d", rec.Code)
	}
}
