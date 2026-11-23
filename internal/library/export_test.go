// Export tests: md/json single-chat shapes, format validation, ownership
// walls, the streamed /api/me/export zip (entry list + settings redaction
// manifest), and the C1 hardening matrix (share-token scrub, title
// flattening, source-link safety — L8-L2, L3-L1, L3-L2, L2-I1).

package library

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"chatgo/internal/store"
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
		"Source: [Wiki](<https://example.test/w>)",
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
	// A LIVE share token on an exported chat must not ride along in the zip (L8-L2).
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE chats SET share_token = ?1 WHERE id = ?2`,
		"tok-live-share-999999", c1.ID); err != nil {
		t.Fatalf("set share_token: %v", err)
	}
	testMsg(t, s, c1.ID, "user", "a1", 100)
	testMsg(t, s, c2.ID, "user", "a2", 101)
	testMsg(t, s, bChat.ID, "user", "b1", 102)
	// Soft-deleted chats must not appear.
	if err := s.Chats.Delete(t.Context(), c2.ID); err != nil {
		t.Fatalf("Chats.Delete: %v", err)
	}

	// me-settings: prefixed rows; secret-NAMED keys are excluded AND named in
	// the redacted manifest; benign values that merely contain secret-ish words
	// ship normally (L2-I1 regression: "all good no secrets" used to vanish).
	for k, v := range map[string]string{
		"me." + alice.ID + ".theme":     "dark",
		"me." + alice.ID + ".mood":      "all good no secrets",
		"me." + alice.ID + ".api_key":   "sk-should-never-ship",
		"me." + alice.ID + ".passwords": "hunter2",
		"me." + bob.ID + ".theme":       "light",
		"some.global.setting":           "nope",
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

	// Settings entry: "me.<uid>." stripped, secret-NAMED keys excluded, benign
	// values containing secret-ish words kept, other users' and global rows
	// absent.
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
	if prefs["mood"] != "all good no secrets" {
		t.Fatalf("benign value dropped from own export (L2-I1 regression): %+v", prefs)
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

	// Redacted manifest names every excluded key — nothing vanishes silently.
	f, err = zr.Open("redacted-settings.json")
	if err != nil {
		t.Fatalf("open redacted manifest: %v", err)
	}
	var manifest struct {
		Redacted []string `json:"redacted"`
		Reason   string   `json:"reason"`
	}
	if err := json.NewDecoder(f).Decode(&manifest); err != nil {
		t.Fatalf("decode redacted manifest: %v", err)
	}
	f.Close()
	if strings.Join(manifest.Redacted, ",") != "api_key,passwords" || manifest.Reason == "" {
		t.Fatalf("redacted manifest = %+v", manifest)
	}

	body := rec.Body.String()
	if strings.Contains(body, "sk-should-never-ship") || strings.Contains(body, "hunter2") {
		t.Fatal("secret value present in zip bytes")
	}
	if strings.Contains(body, "tok-live-share-999999") {
		t.Fatal("live share token present in zip bytes (L8-L2)")
	}
}

// TestLib_ExportSanitization is the C1 hardening matrix: one hostile chat
// (live share token, newline-injecting title, breaky + internal-scheme source
// URLs) run through every export format.
func TestLib_ExportSanitization(t *testing.T) {
	const token = "tok-live-share-abc123"
	const evilTitle = "Evil\n# Injected [x](http://evil)"

	newStore := func(t *testing.T, email, token string) (*Deps, *store.Chat, *store.User) {
		t.Helper()
		s := testStore(t)
		d := &Deps{Store: s}
		alice := testUser(t, s, email)
		chat := testChat(t, s, alice, evilTitle)
		if _, err := s.DB.ExecContext(t.Context(), `UPDATE chats SET share_token = ?1 WHERE id = ?2`,
			token, chat.ID); err != nil {
			t.Fatalf("set share_token: %v", err)
		}
		m := testMsg(t, s, chat.ID, "user", "body text", 1_700_000_000)
		sources := `[{"title":"Web [bad]{x} title","url":"https://example.test/a)b c\n# Fake"},` +
			`{"title":"guide.md","url":"kb://deadbeef"},` +
			`{"title":"","url":"javascript:alert(1)"}]`
		if _, err := s.DB.ExecContext(t.Context(),
			`UPDATE messages SET sources_json = ?1 WHERE id = ?2`, sources, m.ID); err != nil {
			t.Fatalf("set sources: %v", err)
		}
		return d, chat, alice
	}

	// --- share token scrubbed from EVERY serialization (L8-L2) --------------
	d, chat, alice := newStore(t, "a1@x.io", token)
	paths := []struct{ name, path string }{
		{"md", "/api/chats/" + chat.ID + "/export"},
		{"json", "/api/chats/" + chat.ID + "/export?format=json"},
	}
	for _, tc := range paths {
		t.Run("token-absent-"+tc.name, func(t *testing.T) {
			rec := serve(t, d, alice, "GET", tc.path, nil)
			mustStatus(t, rec, http.StatusOK)
			if strings.Contains(rec.Body.String(), token) || strings.Contains(rec.Body.String(), "share_token") {
				t.Fatalf("%s export embeds share token:\n%s", tc.name, rec.Body.String())
			}
		})
	}
	t.Run("token-absent-zip", func(t *testing.T) {
		rec := serve(t, d, alice, "GET", "/api/me/export", nil)
		mustStatus(t, rec, http.StatusOK)
		zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
		if err != nil {
			t.Fatalf("zip.NewReader: %v", err)
		}
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open %s: %v", f.Name, err)
			}
			blob, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatalf("read %s: %v", f.Name, err)
			}
			if strings.Contains(string(blob), token) || strings.Contains(string(blob), "share_token") {
				t.Fatalf("zip member %s embeds share token:\n%s", f.Name, blob)
			}
		}
	})

	// --- md structure hardening (L3-L1 + L3-L2) ------------------------------
	rec := serve(t, d, alice, "GET", "/api/chats/"+chat.ID+"/export", nil)
	mustStatus(t, rec, http.StatusOK)
	md := rec.Body.String()

	if md == "" {
		t.Fatal("empty md")
	}
	lines := strings.Split(md, "\n")
	h1, h2 := 0, 0
	for i, ln := range lines {
		if strings.HasPrefix(ln, "# ") && i != 0 {
			t.Fatalf("fabricated top-level heading at line %d (title escaped its header):\n%s", i, md)
		}
		if strings.HasPrefix(ln, "# ") {
			h1++
		}
		if strings.HasPrefix(ln, "## ") {
			h2++
		}
	}
	if h1 != 1 || h2 != 1 { // exactly the real title + the one message turn
		t.Fatalf("heading counts h1=%d h2=%d, want 1/1:\n%s", h1, h2, md)
	}
	if lines[0] != "# Evil # Injected [x](http://evil)" {
		t.Fatalf("title not flattened to one line: %q", lines[0])
	}

	for _, want := range []string{
		// breaky URL: label escaped, dest <>-wrapped + percent-encoded, no raw ") "
		// (# kept raw — percent-encoding it would move bytes out of the fragment)
		`Source: [Web \[bad\]\{x\} title](<https://example.test/a%29b%20c#%20Fake>)`,
		// kb:// internal scheme → plain-text source name, no link
		"Source: guide.md\n",
		// javascript: URL → plain text, never a link
		"Source: javascript:alert(1)\n",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("md missing %q:\n%s", want, md)
		}
	}
	for _, banned := range []string{"kb://deadbeef", "](<javascript:", "](&lt;"} {
		if strings.Contains(md, banned) {
			t.Fatalf("md contains %q:\n%s", banned, md)
		}
	}

	// --- json stays structured: raw title preserved, still valid JSON --------
	d2, chat2, alice2 := newStore(t, "a2@x.io", "tok-live-share-two")
	rec = serve(t, d2, alice2, "GET", "/api/chats/"+chat2.ID+"/export?format=json", nil)
	mustStatus(t, rec, http.StatusOK)
	var doc struct {
		Chat     chatJSON       `json:"chat"`
		Messages []*messageJSON `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("json export decode: %v\n%s", err, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tok-live-share-two") {
		t.Fatal("json export embeds share token (L8-L2)")
	}
	if doc.Chat.Title != evilTitle {
		t.Fatalf("json title should stay verbatim (structured format), got %q", doc.Chat.Title)
	}
	if len(doc.Messages) != 1 || len(doc.Messages[0].Sources) != 3 ||
		doc.Messages[0].Sources[1].URL != "kb://deadbeef" {
		t.Fatalf("json sources should stay verbatim data: %+v", doc.Messages)
	}

	// --- exportChatShape unit check: token zeroed, rest of shape intact ------
	d3, chat3, _ := newStore(t, "a3@x.io", "tok-live-share-three")
	live, err := d3.Store.Chats.Get(t.Context(), chat3.ID)
	if err != nil {
		t.Fatalf("Chats.Get: %v", err)
	}
	if live.ShareToken == nil || *live.ShareToken != "tok-live-share-three" {
		t.Fatalf("fixture lost its share token: %+v", live.ShareToken)
	}
	blob, err := json.Marshal(exportChatShape(live))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "share_token") || strings.Contains(string(blob), "tok-live-share-three") {
		t.Fatalf("exportChatShape kept the token: %s", blob)
	}
	if ref, _ := json.Marshal(toChatJSON(live)); !strings.Contains(string(ref), "tok-live-share-three") {
		t.Fatal("toChatJSON (chat API/fork shape) must keep sharing untouched")
	}
}

// TestLib_ExportMeSettingsKeyVerdict pins the L2-I1 verdict: exports carry
// only the caller's own me.* namespace rows; exclusion is KEY-signature only,
// excluded names are reported, and benign values survive. Provider/API keys
// never enter the namespace at all (PUT /api/me/settings allowlists the keys —
// see internal/apimgr meSettingKeys), so no value-redaction machinery exists.
func TestLib_ExportMeSettingsKeyVerdict(t *testing.T) {
	s := testStore(t)
	d := &Deps{Store: s}
	alice := testUser(t, s, "a@x.io")
	bob := testUser(t, s, "b@x.io")
	for k, v := range map[string]string{
		"me." + alice.ID + ".theme":    "dark",
		"me." + alice.ID + ".mood":     "all good no secrets",  // value word match: must survive
		"me." + alice.ID + ".api_key":  "sk-alice-never-ships", // name match: dropped + listed
		"me." + alice.ID + ".my.token": "tok-alice",
		"me." + bob.ID + ".theme":      "light",
		"provider.alice.api_key":       "sk-server-side",
		"search_url":                   "https://sx.internal",
	} {
		if err := s.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatalf("Settings.Set(%s): %v", k, err)
		}
	}
	prefs, redacted, err := d.meSettings(t.Context(), alice.ID)
	if err != nil {
		t.Fatalf("meSettings: %v", err)
	}
	if prefs["theme"] != "dark" || prefs["mood"] != "all good no secrets" {
		t.Fatalf("prefs = %+v", prefs)
	}
	if strings.Join(redacted, ",") != "api_key,my.token" {
		t.Fatalf("redacted = %v, want [api_key my.token]", redacted)
	}
	blob, err := json.Marshal(prefs)
	if err != nil {
		t.Fatalf("marshal prefs: %v", err)
	}
	if strings.Contains(string(blob), "sk-alice-never-ships") || strings.Contains(string(blob), "tok-alice") ||
		strings.Contains(string(blob), "sk-server-side") || strings.Contains(string(blob), "light") ||
		strings.Contains(string(blob), "https://sx.internal") {
		t.Fatalf("prefs leaked foreign/secret-bearing rows: %s", blob)
	}
}
