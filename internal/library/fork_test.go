// Fork tests: content equality + ordering of the copied prefix, source
// immutability, chat-shape of the response, and ownership walls.

package library

import (
	"encoding/json"
	"net/http"
	"testing"

	"chatgo/internal/store"
)

func TestLib_ForkCopiesPrefixKeepsSource(t *testing.T) {
	s := testStore(t)
	d := &Deps{Store: s}
	alice := testUser(t, s, "a@x.io")

	src := testChat(t, s, alice, "Trip planning")
	src.ProviderID, src.ModelID, src.ParamsJSON = "p1", "gpt-x", `{"temperature":0.7}`
	src.Pinned = true
	if err := s.Chats.Update(t.Context(), src); err != nil {
		t.Fatalf("Chats.Update: %v", err)
	}
	m1 := testMsg(t, s, src.ID, "user", "first", 100)
	m2 := testMsg(t, s, src.ID, "assistant", "second", 101)
	m2.SourcesJSON = `[{"title":"W","url":"https://example.test"}]`
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE messages SET sources_json = ?1 WHERE id = ?2`,
		m2.SourcesJSON, m2.ID); err != nil {
		t.Fatalf("set sources: %v", err)
	}
	m3 := testMsg(t, s, src.ID, "assistant", "third", 102)
	e := "upstream blew up"
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE messages SET error = ?1 WHERE id = ?2`, e, m3.ID); err != nil {
		t.Fatalf("set error: %v", err)
	}
	m4 := testMsg(t, s, src.ID, "user", "fourth — NOT copied", 103)

	// alice rates the source chat — ratings are NOT part of the fork.
	if _, err := s.DB.ExecContext(t.Context(),
		`INSERT INTO ratings (id, message_id, chat_id, user_id, value, comment, created_at)
		 VALUES ('r1', ?1, ?2, ?3, 1, '', 1)`, m1.ID, src.ID, alice.ID); err != nil {
		t.Fatalf("insert rating: %v", err)
	}

	rec := serve(t, d, alice, "POST", "/api/chats/"+src.ID+"/fork", map[string]any{"message_id": m3.ID})
	mustStatus(t, rec, http.StatusCreated)
	nc := decodeJSON[chatJSON](t, rec)

	// Response = existing chat GET shape, new id, forked title, model/settings
	// preserved, fresh chat (not pinned, no folder/share).
	if nc.ID == "" || nc.ID == src.ID {
		t.Fatalf("fork id wrong: %+v", nc)
	}
	if nc.Title != "Fork of Trip planning" {
		t.Fatalf("title = %q", nc.Title)
	}
	if nc.ProviderID != "p1" || nc.ModelID != "gpt-x" {
		t.Fatalf("model not preserved: %+v", nc)
	}
	if nc.Params["temperature"] != 0.7 {
		t.Fatalf("params not preserved: %+v", nc.Params)
	}
	if nc.Pinned || nc.FolderID != nil || nc.ShareToken != nil {
		t.Fatalf("fork should be a fresh chat: %+v", nc)
	}

	// Copied content + ORDER, inclusive of the target, excluding m4. Re-read
	// the source rows so the raw UPDATE'd sources/error columns are current.
	var wantSrc []*store.Message
	for _, id := range []string{m1.ID, m2.ID, m3.ID} {
		w, err := s.Messages.Get(t.Context(), src.ID, id)
		if err != nil {
			t.Fatalf("reload source msg %s: %v", id, err)
		}
		wantSrc = append(wantSrc, w)
	}
	got, err := s.Messages.List(t.Context(), nc.ID, 100, "")
	if err != nil {
		t.Fatalf("Messages.List: %v", err)
	}
	if len(got) != len(wantSrc) {
		t.Fatalf("copied %d messages, want %d", len(got), len(wantSrc))
	}
	for i, g := range got {
		w := wantSrc[i]
		if g.Content != w.Content || g.Role != w.Role || g.CreatedAt != w.CreatedAt {
			t.Fatalf("msg %d mismatch: got %+v want role/content/created from %+v", i, g, w)
		}
		if g.ID == w.ID {
			t.Fatalf("msg %d reused source id", i)
		}
		if g.ChatID != nc.ID {
			t.Fatalf("msg %d chat_id = %s, want %s", i, g.ChatID, nc.ID)
		}
		if g.SourcesJSON != w.SourcesJSON {
			t.Fatalf("msg %d sources = %q, want %q", i, g.SourcesJSON, w.SourcesJSON)
		}
		if (g.Error == nil) != (w.Error == nil) || (g.Error != nil && *g.Error != *w.Error) {
			t.Fatalf("msg %d error = %v, want %v", i, g.Error, w.Error)
		}
	}

	// Source chat untouched: same title, same 4 messages, still pinned.
	back, err := s.Chats.Get(t.Context(), src.ID)
	if err != nil || back.Title != "Trip planning" || !back.Pinned {
		t.Fatalf("source chat mutated: %+v err=%v", back, err)
	}
	if n, err := s.Messages.Count(t.Context(), src.ID); err != nil || n != 4 {
		t.Fatalf("source message count = %v (err %v), want 4", n, err)
	}
	if _, err := s.Messages.Get(t.Context(), src.ID, m4.ID); err != nil {
		t.Fatalf("source lost its tail message: %v", err)
	}

	// Ratings did not travel to the fork.
	rec = serve(t, d, alice, "GET", "/api/chats/"+nc.ID+"/ratings", nil)
	mustStatus(t, rec, http.StatusOK)
	if rm := decodeJSON[map[string]ratingJSON](t, rec); len(rm) != 0 {
		t.Fatalf("fork inherited ratings: %+v", rm)
	}

	// Forking the first message yields exactly one copied row.
	rec = serve(t, d, alice, "POST", "/api/chats/"+src.ID+"/fork", map[string]any{"message_id": m1.ID})
	mustStatus(t, rec, http.StatusCreated)
	if n, err := s.Messages.Count(t.Context(), decodeJSON[chatJSON](t, rec).ID); err != nil || n != 1 {
		t.Fatalf("first-msg fork count = %v (err %v), want 1", n, err)
	}

	// FTS triggers fire inside the tx: the copied text is searchable by rowid
	// order via the plain List path used above; double-check raw json shape.
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("fork response is not valid JSON: %s", rec.Body.String())
	}
}

func TestLib_ForkWalls(t *testing.T) {
	s := testStore(t)
	d := &Deps{Store: s}
	alice := testUser(t, s, "a@x.io")
	bob := testUser(t, s, "b@x.io")
	chat := testChat(t, s, alice, "Alices")
	other := testChat(t, s, alice, "Alices2")
	m := testMsg(t, s, chat.ID, "user", "hi", 100)
	stray := testMsg(t, s, other.ID, "user", "elsewhere", 101)

	mustStatus(t, serve(t, d, nil, "POST", "/api/chats/"+chat.ID+"/fork",
		map[string]any{"message_id": m.ID}), http.StatusUnauthorized)
	// Foreign chat → 404.
	mustStatus(t, serve(t, d, bob, "POST", "/api/chats/"+chat.ID+"/fork",
		map[string]any{"message_id": m.ID}), http.StatusNotFound)
	// Message from another (owned) chat → 404, nothing created.
	mustStatus(t, serve(t, d, alice, "POST", "/api/chats/"+chat.ID+"/fork",
		map[string]any{"message_id": stray.ID}), http.StatusNotFound)
	// Missing message_id → 400.
	mustStatus(t, serve(t, d, alice, "POST", "/api/chats/"+chat.ID+"/fork", map[string]any{}), http.StatusBadRequest)

	var nChats int
	if err := s.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM chats`).Scan(&nChats); err != nil {
		t.Fatalf("count chats: %v", err)
	}
	if nChats != 2 {
		t.Fatalf("failed forks left %d chats, want the original 2", nChats)
	}
}
