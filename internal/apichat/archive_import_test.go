// Archive lifecycle + JSON-import endpoint tests. Prefix: TestAPI.

package apichat

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAPI_ArchiveLifecycle(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s}
	alice := apiTestUser(t, s, "arch1@x.io")
	bob := apiTestUser(t, s, "arch2@x.io")
	c := apiTestChat(t, s, alice, "keep me")

	if rec := apiServe(t, d, nil, "POST", "/api/chats/"+c.ID+"/archive", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon archive: %d", rec.Code)
	}
	// Foreign chat → 404, never 403.
	if rec := apiServe(t, d, bob, "POST", "/api/chats/"+c.ID+"/archive", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign archive: %d", rec.Code)
	}
	// Archive: leaves the default lane, lands in the archived lane.
	if rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/archive", nil); rec.Code != http.StatusOK {
		t.Fatalf("archive: %d %s", rec.Code, rec.Body.String())
	}
	if rec := apiServe(t, d, alice, "GET", "/api/chats", nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	} else if p := decodeJSON[chatPage](t, rec); len(p.Chats) != 0 {
		t.Fatalf("archived chat still listed: %+v", p.Chats)
	}
	rec := apiServe(t, d, alice, "GET", "/api/chats?archived=1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("archived list: %d", rec.Code)
	}
	page := decodeJSON[chatPage](t, rec)
	if len(page.Chats) != 1 || !page.Chats[0].Archived || page.Chats[0].ID != c.ID {
		t.Fatalf("archived list wrong: %+v", page.Chats)
	}
	// Archived chats stay directly readable (the archive view links them).
	if rec := apiServe(t, d, alice, "GET", "/api/chats/"+c.ID, nil); rec.Code != http.StatusOK {
		t.Fatalf("get archived: %d", rec.Code)
	}
	// Idempotent re-archive keeps the row archived.
	if rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/archive", nil); rec.Code != http.StatusOK {
		t.Fatalf("re-archive: %d", rec.Code)
	}
	// Bob cannot restore Alice's chat.
	if rec := apiServe(t, d, bob, "POST", "/api/chats/"+c.ID+"/unarchive", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign unarchive: %d", rec.Code)
	}
	// Restore → back in the default lane, gone from the archive.
	if rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/unarchive", nil); rec.Code != http.StatusOK {
		t.Fatalf("unarchive: %d", rec.Code)
	}
	if rec := apiServe(t, d, alice, "GET", "/api/chats", nil); rec.Code != http.StatusOK {
		t.Fatalf("list after restore: %d", rec.Code)
	} else if p := decodeJSON[chatPage](t, rec); len(p.Chats) != 1 || p.Chats[0].Archived {
		t.Fatalf("restore did not land: %+v", p.Chats)
	}
	if rec := apiServe(t, d, alice, "GET", "/api/chats?archived=1", nil); rec.Code != http.StatusOK {
		t.Fatalf("archived list after restore: %d", rec.Code)
	} else if p := decodeJSON[chatPage](t, rec); len(p.Chats) != 0 {
		t.Fatalf("still archived: %+v", p.Chats)
	}
}

// 2-message ChatGPT conversation (current_node walk) + 2-message OWUI history
// (parentId walk) import cleanly; junk bodies 400.
func TestAPI_ImportChatGPTAndOWUI(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s}
	alice := apiTestUser(t, s, "imp@x.io")

	gpt := `[{"title":"GPT chat","current_node":"b","mapping":{
	  "root":{"id":"root","parent":null,"children":["a"],"message":null},
	  "a":{"id":"a","parent":"root","children":["b"],"message":{"author":{"role":"user"},
	    "content":{"content_type":"text","parts":["hello"]},"create_time":1700000000}},
	  "b":{"id":"b","parent":"a","children":[],"message":{"author":{"role":"model"},
	    "content":{"content_type":"text","parts":["hi there"]},"model":"gpt-4o","create_time":1700000001}}
	}}]`
	rec := apiServe(t, d, alice, "POST", "/api/chats/import", gpt)
	if rec.Code != http.StatusCreated {
		t.Fatalf("gpt import: %d %s", rec.Code, rec.Body.String())
	}
	res := decodeJSON[struct {
		Imported int      `json:"imported"`
		Messages int      `json:"messages"`
		IDs      []string `json:"ids"`
	}](t, rec)
	if res.Imported != 1 || res.Messages != 2 || len(res.IDs) != 1 {
		t.Fatalf("gpt result: %+v", res)
	}
	// Roles + order survived the mapping walk (model → assistant).
	msgRec := apiServe(t, d, alice, "GET", "/api/chats/"+res.IDs[0]+"/messages", nil)
	if msgRec.Code != http.StatusOK {
		t.Fatalf("imported messages: %d", msgRec.Code)
	}
	var msgs []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(msgRec.Body.Bytes(), &msgs); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[0].Content != "hello" ||
		msgs[1].Role != "assistant" || msgs[1].Content != "hi there" {
		t.Fatalf("imported transcript: %+v", msgs)
	}

	owui := `{"title":"OW chat","chat":{"history":{"currentId":"m2","messages":{
	  "m1":{"id":"m1","role":"user","content":"first","parentId":null},
	  "m2":{"id":"m2","role":"assistant","content":"second","parentId":"m1","models":["llama"]}
	}}}}`
	if rec := apiServe(t, d, alice, "POST", "/api/chats/import", owui); rec.Code != http.StatusCreated {
		t.Fatalf("owui import: %d %s", rec.Code, rec.Body.String())
	}

	for _, junk := range []string{`{"no":"mapping"}`, `[1,2,3]`, `"plain string"`} {
		if rec := apiServe(t, d, alice, "POST", "/api/chats/import", junk); rec.Code != http.StatusBadRequest {
			t.Errorf("junk %q: %d, want 400", junk, rec.Code)
		}
	}
	// Imported chats are ordinary: they show in the default list.
	if rec := apiServe(t, d, alice, "GET", "/api/chats", nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	} else if p := decodeJSON[chatPage](t, rec); len(p.Chats) != 2 {
		t.Fatalf("want 2 imported chats, got %+v", p.Chats)
	}
}
