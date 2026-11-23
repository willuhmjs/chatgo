// Retract endpoint tests — the Esc-Esc cancelled-exchange cleanup
// (POST /api/chats/{id}/messages/{msgId}/retract). Prefix: TestAPI.

package apichat

import (
	"net/http"
	"testing"

	"chatgo/internal/chat"
	"chatgo/internal/store"
)

func TestAPI_RetractMessage(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s}
	alice := apiTestUser(t, s, "rt@x.io")
	bob := apiTestUser(t, s, "rt2@x.io")
	c := apiTestChat(t, s, alice, "retract me")

	appendMsg := func(role, content string, msgErr *string) string {
		t.Helper()
		m := &store.Message{ChatID: c.ID, Role: role, Content: content, Error: msgErr}
		if err := s.Messages.Append(t.Context(), m); err != nil {
			t.Fatalf("Append %s: %v", content, err)
		}
		return m.ID
	}
	deref := func(s string) *string { return &s }

	// Kept exchange, then the cancelled one (user + partial assistant killed
	// mid-stream), then a later tail proving truncate-INCLUSIVE semantics.
	keepU := appendMsg("user", "old question", nil)
	appendMsg("assistant", "old answer", nil)
	cancelU := appendMsg("user", "cancel this", nil)
	cancelA := appendMsg("assistant", "hel", deref("stream canceled"))
	appendMsg("user", "next", nil)
	appendMsg("assistant", "next answer", nil)

	path := func(msgID string) string {
		return "/api/chats/" + c.ID + "/messages/" + msgID + "/retract"
	}
	count := func() int {
		t.Helper()
		n, err := s.Messages.Count(t.Context(), c.ID)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Auth wall.
	if rec := apiServe(t, d, nil, "POST", path("whatever"), nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anon: got %d, want 401", rec.Code)
	}

	// Foreign chat → 404 (ownChat first; existence never leaks, not even via
	// a valid message id from the caller's point of view).
	if rec := apiServe(t, d, bob, "POST", path(cancelA), nil); rec.Code != http.StatusNotFound {
		t.Errorf("foreign chat: got %d, want 404", rec.Code)
	}
	// Unknown chat → 404.
	if rec := apiServe(t, d, alice, "POST", "/api/chats/nope/messages/x/retract", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown chat: got %d, want 404", rec.Code)
	}
	// Unknown message id in an owned chat → 404.
	if rec := apiServe(t, d, alice, "POST", path("ghost-message"), nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown msg: got %d, want 404", rec.Code)
	}
	// A message id that lives in SOMEONE ELSE's chat → 404 (Get is chat-scoped).
	bobChat := apiTestChat(t, s, bob, "bob's")
	bobMsg := &store.Message{ChatID: bobChat.ID, Role: "assistant", Content: "not yours"}
	if err := s.Messages.Append(t.Context(), bobMsg); err != nil {
		t.Fatal(err)
	}
	if rec := apiServe(t, d, alice, "POST", path(bobMsg.ID), nil); rec.Code != http.StatusNotFound {
		t.Errorf("foreign msg id: got %d, want 404", rec.Code)
	}
	if n := count(); n != 6 {
		t.Fatalf("failed retracts must not delete anything: count=%d, want 6", n)
	}

	// Live-stream guard: while the chat's turn is un-drained, retract is 409
	// and NOTHING is deleted (a delete inside the late-persist window is the
	// corruption the guard exists to prevent). Guard is checked before the
	// message lookup, so even a bogus id answers 409-while-busy.
	release := trackChatTurn(c.ID)
	if rec := apiServe(t, d, alice, "POST", path(cancelA), nil); rec.Code != http.StatusConflict {
		t.Errorf("busy retract: got %d, want 409", rec.Code)
	}
	if rec := apiServe(t, d, alice, "POST", path("ghost-message"), nil); rec.Code != http.StatusConflict {
		t.Errorf("busy + unknown id: got %d, want 409 (guard precedes lookup)", rec.Code)
	}
	if n := count(); n != 6 {
		t.Fatalf("busy retract deleted rows: count=%d, want 6", n)
	}
	release()

	// The gesture itself: retract the cancelled assistant row → user prompt +
	// partial + everything after vanish; the earlier exchange survives.
	rec := apiServe(t, d, alice, "POST", path(cancelA), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("retract: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeJSON[map[string]int](t, rec)["deleted"]; got != 4 {
		t.Errorf("deleted=%d, want 4 (user + partial + tail)", got)
	}
	if n := count(); n != 2 {
		t.Fatalf("count after retract=%d, want 2", n)
	}
	left, err := s.Messages.List(t.Context(), c.ID, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 2 || left[0].ID != keepU || left[1].Role != "assistant" {
		t.Errorf("survivors wrong: %+v", left)
	}
	if _, err := s.Messages.Get(t.Context(), c.ID, cancelU); err == nil {
		t.Errorf("cancelled user row survived the retract")
	}

	// Double-retract the same id → honest 404 (row is gone; idempotent in
	// effect, never re-truncates the survivors).
	if rec := apiServe(t, d, alice, "POST", path(cancelA), nil); rec.Code != http.StatusNotFound {
		t.Errorf("double retract: got %d, want 404", rec.Code)
	}
	if n := count(); n != 2 {
		t.Fatalf("double retract deleted more: count=%d, want 2", n)
	}

	// Non-assistant anchor truncates from the row itself (regenerate-style
	// anchor, generalizes the endpoint).
	uRec := apiServe(t, d, alice, "POST", path(keepU), nil)
	if uRec.Code != http.StatusOK {
		t.Fatalf("user-anchor retract: %d %s", uRec.Code, uRec.Body.String())
	}
	if got := decodeJSON[map[string]int](t, uRec)["deleted"]; got != 2 {
		t.Errorf("user-anchor deleted=%d, want 2", got)
	}
	if n := count(); n != 0 {
		t.Errorf("chat should be empty now: count=%d", n)
	}

	// Assistant row with NO user row before it stands alone: only it + tail go.
	loneA := appendMsg("assistant", "orphan", nil)
	appendMsg("user", "after it", nil)
	if rec := apiServe(t, d, alice, "POST", path(loneA), nil); rec.Code != http.StatusOK {
		t.Fatalf("lone assistant: %d %s", rec.Code, rec.Body.String())
	}
	if n := count(); n != 0 {
		t.Errorf("lone retract tail: count=%d, want 0", n)
	}
}

// TestAPI_RetractBusyGuardLifecycle pins the streamSend wiring: the guard is
// armed while a turn streams and is released once the service channel has
// drained (the late-persist window is over) — observed from outside, after
// the SSE handler returns.
func TestAPI_RetractBusyGuardLifecycle(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s}
	alice := apiTestUser(t, s, "rtg@x.io")
	c := apiTestChat(t, s, alice, "guard lifecycle")

	useFakeStream(t, &fakeStream{events: []chat.Event{
		{Type: "meta", Data: map[string]string{"message_id": "a1", "chat_id": c.ID}},
		{Type: "token", Data: map[string]string{"content": "hi"}},
		{Type: "done", Data: map[string]any{"usage": nil}},
	}})

	rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/messages", map[string]any{"content": "hello"})
	if rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	if chatTurnLive(c.ID) {
		t.Errorf("chat still marked busy after the turn fully drained")
	}
	// Not busy → retract answers the honest 404 (the fake streamer persists
	// nothing; "a1" is not a real row), NOT a stale 409.
	if rec := apiServe(t, d, alice, "POST", pathOf(c.ID, "a1"), nil); rec.Code != http.StatusNotFound {
		t.Errorf("post-stream retract: got %d, want 404", rec.Code)
	}
}

func pathOf(chatID, msgID string) string {
	return "/api/chats/" + chatID + "/messages/" + msgID + "/retract"
}
