// Share create/remove + public read tests (BE-apichat owned). Prefix: TestAPI.

package apichat

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"chatgo/internal/store"
)

func TestAPI_ShareLifecycle(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s}
	alice := apiTestUser(t, s, "sh@x.io")
	bob := apiTestUser(t, s, "sh2@x.io")
	c := apiTestChat(t, s, alice, "shared chat")

	if err := s.Messages.Append(t.Context(), &store.Message{ChatID: c.ID, Role: "user",
		Content: "public question"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Messages.Append(t.Context(), &store.Message{ChatID: c.ID, Role: "assistant",
		Content: "with sources", SourcesJSON: `[{"title":"Src","url":"https://src"}]`}); err != nil {
		t.Fatal(err)
	}

	// Foreign chat → 404, never a token.
	if rec := apiServe(t, d, bob, "POST", "/api/chats/"+c.ID+"/share", nil); rec.Code != http.StatusNotFound {
		t.Errorf("foreign share: %d", rec.Code)
	}

	rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/share", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("share: %d %s", rec.Code, rec.Body.String())
	}
	tok := decodeJSON[map[string]string](t, rec)["token"]
	if !regexp.MustCompile(`^[0-9a-f]{24}$`).MatchString(tok) {
		t.Fatalf("token %q is not 24-char hex", tok)
	}
	// Idempotent: second POST returns the same token.
	rec = apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/share", nil)
	if again := decodeJSON[map[string]string](t, rec)["token"]; again != tok {
		t.Errorf("not idempotent: %q vs %q", again, tok)
	}

	// Public reader works with no user at all.
	rec = apiServe(t, d, nil, "GET", "/api/share/"+tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("public get: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Chat     chatJSON      `json:"chat"`
		Messages []messageJSON `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Chat.Title != "shared chat" || payload.Chat.ShareToken != nil {
		t.Errorf("chat payload: %+v", payload.Chat)
	}
	if len(payload.Messages) != 2 || payload.Messages[1].Content != "with sources" {
		t.Fatalf("messages: %+v", payload.Messages)
	}
	if len(payload.Messages[1].Sources) != 1 || payload.Messages[1].Sources[0].URL != "https://src" {
		t.Errorf("share keeps source urls: %+v", payload.Messages[1])
	}
	// Payload must not leak the owner.
	raw := rec.Body.String()
	if strings.Contains(raw, alice.ID) || strings.Contains(raw, "user_id") {
		t.Errorf("share payload leaks owner id: %s", raw)
	}

	// Unknown / revoked tokens 404.
	if rec := apiServe(t, d, nil, "GET", "/api/share/deadbeefdeadbeefdead", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown token: %d", rec.Code)
	}
	if rec := apiServe(t, d, bob, "DELETE", "/api/chats/"+c.ID+"/share", nil); rec.Code != http.StatusNotFound {
		t.Errorf("foreign unshare: %d", rec.Code)
	}
	if rec := apiServe(t, d, alice, "DELETE", "/api/chats/"+c.ID+"/share", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("unshare: %d", rec.Code)
	}
	if rec := apiServe(t, d, nil, "GET", "/api/share/"+tok, nil); rec.Code != http.StatusNotFound {
		t.Errorf("token still live after revoke: %d", rec.Code)
	}

	// Soft-deleted chat kills its share too.
	rec = apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/share", nil)
	tok = decodeJSON[map[string]string](t, rec)["token"]
	if err := s.Chats.Delete(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	if rec := apiServe(t, d, nil, "GET", "/api/share/"+tok, nil); rec.Code != http.StatusNotFound {
		t.Errorf("share on deleted chat: %d", rec.Code)
	}
}

// The PUBLIC share payload must not hand anonymous readers file ids/names
// embedded as image markdown; owner views keep the reference intact.
func TestAPI_SharePublicStripsFileRefs(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s}
	alice := apiTestUser(t, s, "shref@x.io")
	c := apiTestChat(t, s, alice, "refs chat")

	const fid = "6f9c1c2a-1111-2222-3333-444455556666"
	content := "look ![pic.png](/files/" + fid + "/pic.png) done"
	if err := s.Messages.Append(t.Context(), &store.Message{ChatID: c.ID, Role: "user",
		Content: content}); err != nil {
		t.Fatal(err)
	}

	rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/share", nil)
	tok := decodeJSON[map[string]string](t, rec)["token"]

	rec = apiServe(t, d, nil, "GET", "/api/share/"+tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("public get: %d", rec.Code)
	}
	raw := rec.Body.String()
	if strings.Contains(raw, "/files/") || strings.Contains(raw, fid) {
		t.Errorf("public share leaks file refs: %s", raw)
	}
	if !strings.Contains(raw, "[attachment]") {
		t.Errorf("public share missing placeholder: %s", raw)
	}
	payload := decodeJSON[sharePayload](t, rec)
	if len(payload.Messages) != 1 || payload.Messages[0].Content != "look [attachment] done" {
		t.Errorf("stripped content wrong: %+v", payload.Messages)
	}

	// Owner view is untouched.
	rec = apiServe(t, d, alice, "GET", "/api/chats/"+c.ID+"/messages", nil)
	if !strings.Contains(rec.Body.String(), "/files/"+fid+"/pic.png") {
		t.Errorf("owner view was stripped: %s", rec.Body.String())
	}
}
