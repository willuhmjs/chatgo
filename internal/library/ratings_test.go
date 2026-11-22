// Rating tests: upsert/flip/delete semantics, value validation, chat-ownership
// walls (foreign chat → 404 per contract §Non-negotiables), and the ratings
// map endpoint.

package library

import (
	"net/http"
	"strings"
	"testing"
)

func TestLib_RatingUpsertFlipDelete(t *testing.T) {
	s := testStore(t)
	d := &Deps{Store: s}
	alice := testUser(t, s, "a@x.io")
	chat := testChat(t, s, alice, "C")
	m := testMsg(t, s, chat.ID, "assistant", "answer", 100)

	// Anonymous → 401.
	mustStatus(t, serve(t, d, nil, "POST", "/api/chats/"+chat.ID+"/messages/"+m.ID+"/rating",
		map[string]any{"value": 1}), http.StatusUnauthorized)

	// First vote.
	ratingPath := "/api/chats/" + chat.ID + "/messages/" + m.ID + "/rating"
	rec := serve(t, d, alice, "POST", ratingPath, map[string]any{"value": 1})
	mustStatus(t, rec, http.StatusOK)
	if got := decodeJSON[ratingJSON](t, rec); got.Value != 1 || got.Comment != "" {
		t.Fatalf("first vote resp = %+v", got)
	}
	mustStatus(t, serve(t, d, alice, "POST", ratingPath, map[string]any{"value": -1, "comment": "bad"}), http.StatusOK)

	// Flip updated in place — exactly one row per (message, user).
	var n int
	if err := s.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM ratings`).Scan(&n); err != nil {
		t.Fatalf("count ratings: %v", err)
	}
	if n != 1 {
		t.Fatalf("ratings rows = %d, want 1 (upsert must not insert a second)", n)
	}

	// Map endpoint for FE painting.
	ratingsPath := "/api/chats/" + chat.ID + "/ratings"
	rec = serve(t, d, alice, "GET", ratingsPath, nil)
	mustStatus(t, rec, http.StatusOK)
	got := decodeJSON[map[string]ratingJSON](t, rec)
	if len(got) != 1 || got[m.ID].Value != -1 || got[m.ID].Comment != "bad" {
		t.Fatalf("ratings map = %+v", got)
	}

	// Delete clears; idempotent.
	mustStatus(t, serve(t, d, alice, "DELETE", ratingPath, nil), http.StatusNoContent)
	rec = serve(t, d, alice, "GET", ratingsPath, nil)
	mustStatus(t, rec, http.StatusOK)
	if m2 := decodeJSON[map[string]ratingJSON](t, rec); len(m2) != 0 {
		t.Fatalf("ratings after delete = %+v", m2)
	}
	mustStatus(t, serve(t, d, alice, "DELETE", ratingPath, nil), http.StatusNoContent)

	// Value validation.
	for _, v := range []any{0, 2, -2, "up", nil} {
		mustStatus(t, serve(t, d, alice, "POST", ratingPath, map[string]any{"value": v}), http.StatusBadRequest)
	}
	mustStatus(t, serve(t, d, alice, "POST", ratingPath,
		map[string]any{"value": 1, "comment": strings.Repeat("x", maxRatingComment+1)}), http.StatusBadRequest)
}

func TestLib_RatingOwnershipWalls(t *testing.T) {
	s := testStore(t)
	d := &Deps{Store: s}
	alice := testUser(t, s, "a@x.io")
	bob := testUser(t, s, "b@x.io")
	chat := testChat(t, s, alice, "Alices")
	other := testChat(t, s, alice, "Alices2")
	m := testMsg(t, s, chat.ID, "user", "hi", 100)
	stray := testMsg(t, s, other.ID, "user", "elsewhere", 101)

	// Foreign chat → 404 on all three routes (never 403, no existence leak).
	for _, r := range [][3]string{
		{"POST", "/api/chats/" + chat.ID + "/messages/" + m.ID + "/rating", `{"value":1}`},
		{"DELETE", "/api/chats/" + chat.ID + "/messages/" + m.ID + "/rating", ""},
		{"GET", "/api/chats/" + chat.ID + "/ratings", ""},
	} {
		mustStatus(t, serve(t, d, bob, r[0], r[1], r[2]), http.StatusNotFound)
	}

	// Message must belong to the addressed chat (same owner, wrong chat → 404).
	mustStatus(t, serve(t, d, alice, "POST",
		"/api/chats/"+chat.ID+"/messages/"+stray.ID+"/rating", map[string]any{"value": 1}), http.StatusNotFound)

	// Ratings are per-user: bob cannot see alice's even if inserted directly.
	if _, err := s.DB.ExecContext(t.Context(),
		`INSERT INTO ratings (id, message_id, chat_id, user_id, value, comment, created_at)
		 VALUES ('r1', ?1, ?2, ?3, 1, 'note', 1)`, m.ID, chat.ID, alice.ID); err != nil {
		t.Fatalf("insert rating: %v", err)
	}
	rec := serve(t, d, alice, "GET", "/api/chats/"+chat.ID+"/ratings", nil)
	mustStatus(t, rec, http.StatusOK)
	if got := decodeJSON[map[string]ratingJSON](t, rec); len(got) != 1 {
		t.Fatalf("alice map = %+v", got)
	}
}
