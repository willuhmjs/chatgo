// Admin evaluations endpoint tests: 403 for non-admins, totals + per-model
// rollup + recent feed shape. Self-contained mux (registers only the admin
// ratings route on top of library.Init's schema).

package library

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"chatgo/internal/store"

	"github.com/google/uuid"
)

func TestAPI_AdminRatingsSummary(t *testing.T) {
	s, err := store.Open(":memory:", []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	if err := Init(s.DB); err != nil {
		t.Fatalf("library.Init: %v", err)
	}
	ctx := context.Background()

	mk := func(email, role string) *store.User {
		u := &store.User{Email: email, Name: "R " + email, Role: role}
		if err := s.Users.Insert(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
		return u
	}
	admin := mk("boss@x.io", store.RoleAdmin)
	usr := mk("dev@x.io", store.RoleUser)

	// One chat with one message; two ratings (👍 and 👎 with comment).
	chat := &store.Chat{UserID: usr.ID, Title: "rated chat", ModelID: "gpt-x", ProviderID: "p1"}
	if err := s.Chats.Create(ctx, chat); err != nil {
		t.Fatalf("chat: %v", err)
	}
	msg := &store.Message{ID: uuid.NewString(), ChatID: chat.ID, Role: "assistant", Content: "answer"}
	if err := s.Messages.Append(ctx, msg); err != nil {
		t.Fatalf("append: %v", err)
	}
	rate := func(userID string, value int, comment string) {
		_, err := s.DB.ExecContext(ctx,
			`INSERT INTO ratings (id, message_id, chat_id, user_id, value, comment, created_at)
			 VALUES (?1, ?2, ?3, ?4, ?5, ?6, 1700000000)`,
			uuid.NewString(), msg.ID, chat.ID, userID, value, comment)
		if err != nil {
			t.Fatalf("rating: %v", err)
		}
	}
	rate(admin.ID, 1, "")
	rate(usr.ID, -1, "too verbose")

	d := &Deps{Store: s}
	mux := http.NewServeMux()
	registerRatingsAdmin(mux, d)

	serveAs := func(u *store.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/admin/ratings", nil)
		if u != nil {
			req = req.WithContext(withUserForTest(req.Context(), u))
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := serveAs(nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon: %d", rec.Code)
	}
	if rec := serveAs(usr); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin: %d", rec.Code)
	}
	rec := serveAs(admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Totals struct{ Up, Down, Score int } `json:"totals"`
		Models []struct {
			ModelID    string `json:"model_id"`
			ProviderID string `json:"provider_id"`
			Up         int    `json:"up"`
			Down       int    `json:"down"`
		} `json:"models"`
		Recent []struct {
			Value     int    `json:"value"`
			Comment   string `json:"comment"`
			ChatTitle string `json:"chat_title"`
			Excerpt   string `json:"excerpt"`
			ModelID   string `json:"model_id"`
			UserName  string `json:"user_name"`
		} `json:"recent"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Totals.Up != 1 || out.Totals.Down != 1 || out.Totals.Score != 0 || out.Total != 2 {
		t.Fatalf("totals: %+v", out.Totals)
	}
	if len(out.Models) != 1 || out.Models[0].ModelID != "gpt-x" || out.Models[0].Up != 1 || out.Models[0].Down != 1 {
		t.Fatalf("models: %+v", out.Models)
	}
	if len(out.Recent) != 2 {
		t.Fatalf("recent: %+v", out.Recent)
	}
	var commentSeen bool
	for _, f := range out.Recent {
		if f.Comment == "too verbose" {
			commentSeen = true
		}
		if f.ChatTitle != "rated chat" || f.Excerpt != "answer" || f.ModelID != "gpt-x" {
			t.Fatalf("feedback row: %+v", f)
		}
	}
	if !commentSeen {
		t.Fatal("comment missing from feed")
	}
}
