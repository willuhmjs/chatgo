// HTTP tests for the notes plane: CRUD + ownership walls + share lifecycle +
// public share fetch + caps. Package-local mux (same pattern as library).

package notes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgo/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:", []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testUser(t *testing.T, s *store.Store, email string) *store.User {
	t.Helper()
	u := &store.User{Email: email, Name: "N " + email, Role: store.RoleUser}
	if err := s.Users.Insert(context.Background(), u); err != nil {
		t.Fatalf("Users.Insert: %v", err)
	}
	return u
}

func serve(t *testing.T, d *Deps, u *store.User, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	if err := Register(mux, d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var rdr *bytes.Reader = nil
	if body != nil {
		var raw []byte
		switch b := body.(type) {
		case string:
			raw = []byte(b)
		default:
			var err error
			raw, err = json.Marshal(b)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
		}
		rdr = bytes.NewReader(raw)
	}
	var req *http.Request
	if rdr != nil {
		req = httptest.NewRequest(method, target, rdr)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if u != nil {
		req = req.WithContext(withUserForTest(req.Context(), u))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return v
}

func TestNotes_CRUDShareAndWalls(t *testing.T) {
	s := testStore(t)
	d := &Deps{Store: s}
	alice := testUser(t, s, "a@x.io")
	bob := testUser(t, s, "b@x.io")

	// Anon → 401.
	if rec := serve(t, d, nil, "GET", "/api/notes", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon list: %d", rec.Code)
	}
	// Create + defaults.
	rec := serve(t, d, alice, "POST", "/api/notes", map[string]any{"content": "# hello"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	n := decode[noteJSON](t, rec)
	if n.ID == "" || n.Title != "Untitled note" || n.Content != "# hello" {
		t.Fatalf("create result: %+v", n)
	}
	// Ownership wall: bob can neither read, edit, share nor delete alice's note.
	for _, r := range [][3]string{
		{"GET", "/api/notes/" + n.ID, ""},
		{"PUT", "/api/notes/" + n.ID, `{"title":"stolen"}`},
		{"POST", "/api/notes/" + n.ID + "/share", `{"on":true}`},
		{"DELETE", "/api/notes/" + n.ID, ""},
	} {
		var body any
		if r[2] != "" {
			body = r[2]
		}
		if rec := serve(t, d, bob, r[0], r[1], body); rec.Code != http.StatusNotFound {
			t.Errorf("bob %s %s: %d, want 404", r[0], r[1], rec.Code)
		}
	}
	// Update (owner).
	if rec := serve(t, d, alice, "PUT", "/api/notes/"+n.ID,
		map[string]any{"title": "Plan", "content": "body"}); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	} else if u := decode[noteJSON](t, rec); u.Title != "Plan" || u.Content != "body" {
		t.Fatalf("update result: %+v", u)
	}
	// List envelope.
	if rec := serve(t, d, alice, "GET", "/api/notes", nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	} else if p := decode[struct {
		Notes []noteJSON `json:"notes"`
		Total int        `json:"total"`
	}](t, rec); p.Total != 1 || p.Notes[0].ID != n.ID {
		t.Fatalf("list result: %+v", p)
	}

	// Share → public fetch → unshare → 404 for the old token.
	rec = serve(t, d, alice, "POST", "/api/notes/"+n.ID+"/share", map[string]any{"on": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("share on: %d %s", rec.Code, rec.Body.String())
	}
	shared := decode[noteJSON](t, rec)
	if shared.ShareToken == nil || *shared.ShareToken == "" {
		t.Fatalf("no token: %+v", shared)
	}
	token := *shared.ShareToken
	// Public (no user in context!) read-only view.
	rec = serve(t, d, nil, "GET", "/api/share/note/"+token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("public share: %d", rec.Code)
	}
	if pub := decode[noteJSON](t, rec); pub.Content != "body" || pub.ShareToken != nil {
		t.Fatalf("public payload: %+v", pub)
	}
	if rec := serve(t, d, alice, "POST", "/api/notes/"+n.ID+"/share", map[string]any{"on": false}); rec.Code != http.StatusOK {
		t.Fatalf("share off: %d", rec.Code)
	}
	if rec := serve(t, d, nil, "GET", "/api/share/note/"+token, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("revoked share: %d, want 404", rec.Code)
	}

	// Oversized body → 400.
	if rec := serve(t, d, alice, "PUT", "/api/notes/"+n.ID,
		map[string]any{"content": strings.Repeat("x", maxNoteBody+1)}); rec.Code != http.StatusBadRequest {
		t.Errorf("oversize: %d, want 400", rec.Code)
	}
	// Delete → 404 afterwards.
	if rec := serve(t, d, alice, "DELETE", "/api/notes/"+n.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := serve(t, d, alice, "GET", "/api/notes/"+n.ID, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get deleted: %d, want 404", rec.Code)
	}
}
