// Export tests: md/json single-chat shapes, format validation, ownership
// walls, and the streamed /api/me/export zip (entry list + settings redaction).

package library

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestLib_ExportChatMarkdownAndJSON(t *testing.T) {
	s := testStore(t)
	d := &Deps{Store: s}
	alice := testUser(t, s, "a@x.io")
	bob := testUser(t, s, "b@x.io")
	chat := testChat(t, s, alice, "Trip — 2026!")
	chat.ProviderID, chat.ModelID = "p1", "gpt-x"
	if err := s.Chats.Update(t.Context(), chat); err != nil {
		t.Fatalf("Chats.Update: %v", err)
	}
	m1 := testMsg(t, s, chat.ID, "user", "hello there", 1_700_000_000)
	if _, err := s.DB.ExecContext(t.Context(),
		`UPDATE messages SET sources_json = ?1 WHERE id = ?2`,
		`[{"title":"Wiki","url":"https://example.test/w"}]`, m1.ID); err != nil {
		t.Fatalf("set sources: %v", err)
	}
	m2 := testMsg(t, s, chat.ID, "assistant", "general kenobi", 1_700_000_060)
	if _, err := s.DB.ExecContext(t.Context(),
		`UPDATE messages SET error = ?1 WHERE id = ?2`, "upstream 502", m2.ID); err != nil {
		t.Fatalf("set error: %v", err)
	}
	secretChat := testChat(t, s, bob, "Bobs private")
	testMsg(t, s, secretChat.ID, "user", "bob was here", 1_700_000_000)

	// --- markdown -------------------------------------------------------------
	rec := serve(t, d, alice, "GET", "/api/chats/"+chat.ID+"/export", nil) // default = md
	mustStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("md content-type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") ||
		!strings.Contains(cd, "Trip-2026") || !strings.HasSuffix(cd, ".md\"") {
		t.Fatalf("md disposition = %q", cd)
	}
	md := rec.Body.String()
	for _, want := range []string{
		"# Trip — 2026!",
		"- Model: `p1/gpt-x`",
		"## user · 2023-11-14T22:13:20Z",
		"hello there",
		"## assistant · 2023-11-14T22:14:20Z",
		"general kenobi",
		"> error: upstream 502",
		"Source: [Wiki](https://example.test/w)",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("md missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "bob was here") {
		t.Fatal("md leaked another user's message")
	}

	// --- json -----------------------------------------------------------------
	rec = serve(t, d, alice, "GET", "/api/chats/"+chat.ID+"/export?format=json", nil)
	mustStatus(t, rec, http.StatusOK)
	var doc struct {
		Chat     chatJSON `json:"chat"`
		Messages []struct {
			ID      string `json:"id"`
			Role    string `json:"role"`
			Content string `json:"content"`
			Error   string `json:"error"`
			Sources []struct {
				Title string `json:"title"`
				URL   string `json:"url"`
			} `json:"sources"`
			CreatedAt int64 `json:"created_at"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("json export decode: %v\n%s", err, rec.Body.String())
	}
	if doc.Chat.ID != chat.ID || doc.Chat.Title != chat.Title ||
		doc.Chat.ProviderID != "p1" || doc.Chat.ModelID != "gpt-x" {
		t.Fatalf("json chat = %+v", doc.Chat)
	}
	if len(doc.Messages) != 2 {
		t.Fatalf("json messages = %d, want 2", len(doc.Messages))
	}
	if doc.Messages[0].ID != m1.ID || doc.Messages[0].Content != "hello there" ||
		doc.Messages[1].ID != m2.ID || doc.Messages[1].Error != "upstream 502" {
		t.Fatalf("json messages = %+v", doc.Messages)
	}
	if len(doc.Messages[0].Sources) != 1 || doc.Messages[0].Sources[0].URL != "https://example.test/w" {
		t.Fatalf("json sources = %+v", doc.Messages[0].Sources)
	}

	// --- walls + bad format ---------------------------------------------------
	mustStatus(t, serve(t, d, alice, "GET", "/api/chats/"+chat.ID+"/export?format=xml", nil), http.StatusBadRequest)
	mustStatus(t, serve(t, d, bob, "GET", "/api/chats/"+chat.ID+"/export", nil), http.StatusNotFound)
	mustStatus(t, serve(t, d, nil, "GET", "/api/chats/"+chat.ID+"/export", nil), http.StatusUnauthorized)
	mustStatus(t, serve(t, d, nil, "GET", "/api/me/export", nil), http.StatusUnauthorized)
}

func TestLib_ExportMeZip(t *testing.T) {
	s := testStore(t)
	d := &Deps{Store: s}
	alice := testUser(t, s, "a@x.io")
	bob := testUser(t, s, "b@x.io")
	c1 := testChat(t, s, alice, "One")
	c2 := testChat(t, s, alice, "Two")
	bChat := testChat(t, s, bob, "Bobs")
	testMsg(t, s, c1.ID, "user", "a1", 100)
	testMsg(t, s, c2.ID, "user", "a2", 101)
	testMsg(t, s, bChat.ID, "user", "b1", 102)
	// Soft-deleted chats must not appear.
	if err := s.Chats.Delete(t.Context(), c2.ID); err != nil {
		t.Fatalf("Chats.Delete: %v", err)
	}

	// me-settings: prefixed rows; secret-bearing key must be dropped.
	for k, v := range map[string]string{
		"me." + alice.ID + ".theme":   "dark",
		"me." + alice.ID + ".api_key": "sk-should-never-ship",
		"me." + bob.ID + ".theme":     "light",
		"some.global.setting":         "nope",
	} {
		if err := s.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatalf("Settings.Set(%s): %v", k, err)
		}
	}

	rec := serve(t, d, alice, "GET", "/api/me/export", nil)
	mustStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content-type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "chatgo-export-") || !strings.HasSuffix(cd, ".zip\"") {
		t.Fatalf("disposition = %q", cd)
	}

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	want := map[string]bool{"chats/" + c1.ID + ".json": true, "me-settings.json": true}
	for n := range want {
		if !names[n] {
			t.Fatalf("zip missing %s; has %v", n, names)
		}
	}
	if names["chats/"+c2.ID+".json"] {
		t.Fatal("soft-deleted chat exported")
	}
	if names["chats/"+bChat.ID+".json"] {
		t.Fatal("another user's chat exported")
	}

	// Chat entry decodes to the per-chat json document.
	f, err := zr.Open("chats/" + c1.ID + ".json")
	if err != nil {
		t.Fatalf("open entry: %v", err)
	}
	var doc struct {
		Chat     chatJSON       `json:"chat"`
		Messages []*messageJSON `json:"messages"`
	}
	if err := json.NewDecoder(f).Decode(&doc); err != nil {
		t.Fatalf("decode entry: %v", err)
	}
	f.Close()
	if doc.Chat.ID != c1.ID || len(doc.Messages) != 1 || doc.Messages[0].Content != "a1" {
		t.Fatalf("entry doc = %+v", doc)
	}

	// Settings entry: "me.<uid>." stripped, secret-ish key excluded, other
	// users' and global rows absent.
	f, err = zr.Open("me-settings.json")
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	var prefs map[string]string
	if err := json.NewDecoder(f).Decode(&prefs); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	f.Close()
	if prefs["theme"] != "dark" {
		t.Fatalf("prefs = %+v", prefs)
	}
	for k := range prefs {
		if strings.Contains(strings.ToLower(k), "key") || strings.Contains(strings.ToLower(k), "secret") ||
			strings.Contains(strings.ToLower(k), "token") || strings.Contains(strings.ToLower(k), "password") {
			t.Fatalf("secret-looking key %q shipped in export", k)
		}
		if strings.HasPrefix(k, "me.") {
			t.Fatalf("prefix not stripped: %q", k)
		}
	}
	body := rec.Body.String()
	if strings.Contains(body, "sk-should-never-ship") {
		t.Fatal("secret value present in zip bytes")
	}
}
