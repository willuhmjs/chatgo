// Shared test helpers for the library package tests. Prefix: TestLib.

package library

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgo/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open("file:memlib_"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared",
		[]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	d := &Deps{Store: s}
	mux := http.NewServeMux()
	if err := Register(mux, d); err != nil {
		t.Fatalf("library.Register: %v", err)
	}
	return s
}

func testUser(t *testing.T, s *store.Store, email string) *store.User {
	t.Helper()
	u := &store.User{Email: email, Name: "T " + email, Role: store.RoleUser}
	if err := s.Users.Insert(t.Context(), u); err != nil {
		t.Fatalf("Users.Insert: %v", err)
	}
	return u
}

func testAdmin(t *testing.T, s *store.Store, email string) *store.User {
	t.Helper()
	u := testUser(t, s, email)
	if err := s.Users.UpdateRole(t.Context(), u.ID, store.RoleAdmin); err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	u.Role = store.RoleAdmin
	return u
}

func testChat(t *testing.T, s *store.Store, u *store.User, title string) *store.Chat {
	t.Helper()
	c := &store.Chat{UserID: u.ID, Title: title}
	if err := s.Chats.Create(t.Context(), c); err != nil {
		t.Fatalf("Chats.Create: %v", err)
	}
	return c
}

func testMsg(t *testing.T, s *store.Store, chatID, role, content string, createdAt int64) *store.Message {
	t.Helper()
	m := &store.Message{ChatID: chatID, Role: role, Content: content, CreatedAt: createdAt}
	if err := s.Messages.Append(t.Context(), m); err != nil {
		t.Fatalf("Messages.Append: %v", err)
	}
	return m
}

// serve drives the library route group through the real mux; the caller is
// faked through withUserForTest (auth ctx keys are unexported — see the
// CONTRACT-DEVIATION note in library.go).
func serve(t *testing.T, d *Deps, u *store.User, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	if err := Register(mux, d); err != nil {
		t.Fatalf("library.Register: %v", err)
	}

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

func mustStatus(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("%s: want %d, got %d (%s)", rec.Result().Request.URL.Path, code, rec.Code, rec.Body.String())
	}
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return v
}
