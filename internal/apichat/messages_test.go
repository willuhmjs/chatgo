// Message list + SSE send/regenerate tests (BE-apichat owned). Prefix: TestAPI.

package apichat

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"chatgo/internal/chat"
	"chatgo/internal/config"
	"chatgo/internal/files"
	"chatgo/internal/providers"
	"chatgo/internal/store"
)

// fakeStream implements messageStreamer; useFake swaps it in.
type fakeStream struct {
	events  []chat.Event
	sendErr error
	gotUser string
	gotChat string
	gotMsg  providers.ChatMessage
	gotOpts chat.SendOpts
}

func (f *fakeStream) Send(ctx context.Context, userID, chatID string, m providers.ChatMessage, o chat.SendOpts) (<-chan chat.Event, error) {
	f.gotUser, f.gotChat, f.gotMsg, f.gotOpts = userID, chatID, m, o
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	ch := make(chan chat.Event, len(f.events))
	for _, e := range f.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func useFakeStream(t *testing.T, f *fakeStream) {
	t.Helper()
	old := chatStreamerOf
	chatStreamerOf = func(*Deps) messageStreamer { return f }
	t.Cleanup(func() { chatStreamerOf = old })
}

func TestAPI_MessageListPaging(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s}
	alice := apiTestUser(t, s, "ml@x.io")
	bob := apiTestUser(t, s, "ml2@x.io")
	c := apiTestChat(t, s, alice, "hist")

	var ids []string
	for _, txt := range []string{"one", "two", "three"} {
		m := &store.Message{ChatID: c.ID, Role: "user", Content: txt}
		if err := s.Messages.Append(t.Context(), m); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}

	rec := apiServe(t, d, alice, "GET", "/api/chats/"+c.ID+"/messages", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	msgs := decodeJSON[[]messageJSON](t, rec)
	if len(msgs) != 3 || msgs[0].Content != "one" || msgs[2].Content != "three" {
		t.Fatalf("not chronological: %+v", msgs)
	}
	// Newest-first page (limit 2) returned chronological.
	rec = apiServe(t, d, alice, "GET", "/api/chats/"+c.ID+"/messages?limit=2", nil)
	msgs = decodeJSON[[]messageJSON](t, rec)
	if len(msgs) != 2 || msgs[0].Content != "two" || msgs[1].Content != "three" {
		t.Errorf("limit page: %+v", msgs)
	}
	// Cursor ?before= second message → only the first.
	rec = apiServe(t, d, alice, "GET", "/api/chats/"+c.ID+"/messages?before="+ids[1], nil)
	msgs = decodeJSON[[]messageJSON](t, rec)
	if len(msgs) != 1 || msgs[0].ID != ids[0] {
		t.Errorf("before cursor: %+v", msgs)
	}
	// Sources round-trip.
	if err := s.Messages.Append(t.Context(), &store.Message{ChatID: c.ID, Role: "assistant",
		Content: "cited", SourcesJSON: `[{"title":"T","url":"https://u"}]`}); err != nil {
		t.Fatal(err)
	}
	rec = apiServe(t, d, alice, "GET", "/api/chats/"+c.ID+"/messages", nil)
	msgs = decodeJSON[[]messageJSON](t, rec)
	last := msgs[len(msgs)-1]
	if len(last.Sources) != 1 || last.Sources[0].URL != "https://u" {
		t.Errorf("sources not decoded: %+v", last)
	}

	if rec := apiServe(t, d, bob, "GET", "/api/chats/"+c.ID+"/messages", nil); rec.Code != http.StatusNotFound {
		t.Errorf("foreign chat messages: %d", rec.Code)
	}
}

func TestAPI_SendMessageSSE(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s}
	alice := apiTestUser(t, s, "sse@x.io")
	c := apiTestChat(t, s, alice, "stream me")

	f := &fakeStream{events: []chat.Event{
		{Type: "meta", Data: map[string]string{"message_id": "m1", "chat_id": c.ID}},
		{Type: "title", Data: map[string]string{"title": "Hi"}},
		{Type: "token", Data: map[string]string{"content": "hel"}},
		{Type: "token", Data: map[string]string{"content": "lo"}},
		{Type: "done", Data: map[string]any{"usage": nil}},
	}}
	useFakeStream(t, f)

	rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/messages",
		map[string]any{"content": "hello there", "search": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}
	if xa := rec.Header().Get("X-Accel-Buffering"); xa != "no" {
		t.Errorf("X-Accel-Buffering = %q", xa)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"event: meta\n", "event: title\n", "event: token\n", "event: done\n",
		`"content":"hel"`, `"chat_id":"` + c.ID + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SSE body missing %q:\n%s", want, body)
		}
	}
	// Exact protocol order (first occurrence, terminal last).
	idx := func(ev string) int { return strings.Index(body, "event: "+ev+"\n") }
	if !(idx("meta") < idx("title") && idx("title") < idx("token") && idx("token") < idx("done")) {
		t.Errorf("event order wrong:\n%s", body)
	}
	if strings.Contains(body, "event: token") && idx("done") > idx("token") && strings.Contains(body[idx("done"):], "event: token") {
		t.Error("token after terminal event")
	}
	if f.gotUser != alice.ID || f.gotChat != c.ID || f.gotMsg.Content != "hello there" || !f.gotOpts.Search {
		t.Errorf("Send args: %+v", f)
	}

	// Upstream error before stream start → 500 JSON, no SSE headers.
	f2 := &fakeStream{sendErr: context.Canceled}
	useFakeStream(t, f2)
	rec = apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/messages", map[string]any{"content": "x"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("send error: %d", rec.Code)
	}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Error("SSE headers set on pre-stream failure")
	}

	// Real scaffold stub service → also 500 (Send not implemented yet).
	dStub := &Deps{Store: s}
	if rec := apiServe(t, dStub, alice, "POST", "/api/chats/"+c.ID+"/messages", map[string]any{"content": "x"}); rec.Code != http.StatusInternalServerError {
		t.Errorf("stub service: %d", rec.Code)
	}

	// Validation + walls.
	if rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/messages", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Errorf("empty content: %d", rec.Code)
	}
	f3 := &fakeStream{}
	useFakeStream(t, f3)
	if rec := apiServe(t, d, alice, "POST", "/api/chats/ghost/messages", map[string]any{"content": "x"}); rec.Code != http.StatusNotFound {
		t.Errorf("ghost chat: %d", rec.Code)
	}
	// Unknown file_id resolves through the ownership check → 400, never a crash.
	dFiles := &Deps{Store: s, Files: files.New(&config.Config{DataDir: t.TempDir()}, s)}
	if rec := apiServe(t, dFiles, alice, "POST", "/api/chats/"+c.ID+"/messages",
		map[string]any{"content": "x", "images": []map[string]any{{"file_id": "f1"}}}); rec.Code != http.StatusBadRequest {
		t.Errorf("stubbed DataURL image: %d, want 400", rec.Code)
	}
	// No files service at all → 500 (not a panic).
	if rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/messages",
		map[string]any{"content": "x", "images": []map[string]any{{"file_id": "f1"}}}); rec.Code != http.StatusInternalServerError {
		t.Errorf("nil files svc image: %d, want 500", rec.Code)
	}
}

func TestAPI_RegenerateParamParse(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s}
	alice := apiTestUser(t, s, "re@x.io")
	bob := apiTestUser(t, s, "re2@x.io")
	c := apiTestChat(t, s, alice, "regen")
	m := &store.Message{ChatID: c.ID, Role: "assistant", Content: "old answer"}
	if err := s.Messages.Append(t.Context(), m); err != nil {
		t.Fatal(err)
	}

	// from-id must exist in this chat.
	if rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/regenerate?from=nope", map[string]any{}); rec.Code != http.StatusNotFound {
		t.Errorf("bogus from: %d, want 404", rec.Code)
	}
	// Other user's chat → 404 regardless.
	if rec := apiServe(t, d, bob, "POST", "/api/chats/"+c.ID+"/regenerate?from="+m.ID, map[string]any{}); rec.Code != http.StatusNotFound {
		t.Errorf("foreign regen: %d", rec.Code)
	}

	f := &fakeStream{events: []chat.Event{
		{Type: "meta", Data: map[string]string{"message_id": "m2", "chat_id": c.ID}},
		{Type: "error", Data: map[string]string{"message": "upstream blew up"}},
	}}
	useFakeStream(t, f)
	rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/regenerate?from="+m.ID,
		map[string]any{"search": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("regen: %d %s", rec.Code, rec.Body.String())
	}
	if f.gotOpts.From != m.ID {
		t.Errorf("From = %q, want %q", f.gotOpts.From, m.ID)
	}
	if !f.gotOpts.Search {
		t.Error("search flag dropped")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: meta") || !strings.Contains(body, "event: error") ||
		strings.Contains(body[strings.Index(body, "event: error"):], "event: token") {
		t.Errorf("bad regen stream:\n%s", body)
	}

	// No from param → From == "" (regenerate latest).
	f2 := &fakeStream{}
	useFakeStream(t, f2)
	if rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/regenerate", map[string]any{}); rec.Code != http.StatusOK {
		t.Fatalf("regen latest: %d", rec.Code)
	}
	if f2.gotOpts.From != "" {
		t.Errorf("From = %q, want empty", f2.gotOpts.From)
	}
}

// saveTestPNG stores a tiny png owned by uid through the real files service.
func saveTestPNG(t *testing.T, svc *files.Service, uid string) *files.Meta {
	t.Helper()
	var imgB bytes.Buffer
	if err := png.Encode(&imgB, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "pic.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(imgB.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	form, err := multipart.NewReader(&body, mw.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { form.RemoveAll() })
	meta, err := svc.Save(context.Background(), uid, nil, form.File["file"][0], store.FileKindImage)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return meta
}

// Vision-input guards: ≤4 images per message, and file ids must belong to the
// caller (foreign ids never reach the model nor the message markdown).
func TestAPI_SendImageGuards(t *testing.T) {
	s := apiTestStore(t)
	dir := t.TempDir()
	svc := files.New(&config.Config{DataDir: dir}, s)
	d := &Deps{Store: s, Files: svc}
	alice := apiTestUser(t, s, "ig@x.io")
	bob := apiTestUser(t, s, "ig2@x.io")
	c := apiTestChat(t, s, alice, "imgs")

	f := &fakeStream{events: []chat.Event{{Type: "done", Data: map[string]any{"usage": nil}}}}
	useFakeStream(t, f)

	// >4 images → 400 before anything resolves.
	many := make([]map[string]any, maxMessageImages+1)
	for i := range many {
		many[i] = map[string]any{"file_id": "f" + strconv.Itoa(i)}
	}
	if rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/messages",
		map[string]any{"content": "x", "images": many}); rec.Code != http.StatusBadRequest {
		t.Errorf("%d images: %d, want 400", len(many), rec.Code)
	}
	// Regenerate path enforces the same cap.
	if rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/regenerate",
		map[string]any{"content": "x", "images": many}); rec.Code != http.StatusBadRequest {
		t.Errorf("regen %d images: %d, want 400", len(many), rec.Code)
	}

	// Foreign-owned file id → 400 (row exists, owner mismatch).
	foreign := &store.File{ID: "foreign-img", UserID: bob.ID, Name: "victim.png",
		Mime: "image/png", Size: 8, Path: "files/foreign-img.png", Kind: store.FileKindImage}
	if err := s.Files.Insert(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	if rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/messages",
		map[string]any{"content": "x", "images": []map[string]any{{"file_id": foreign.ID}}}); rec.Code != http.StatusBadRequest {
		t.Errorf("foreign file_id: %d, want 400", rec.Code)
	}
	if strings.Contains(f.gotMsg.Content, foreign.ID) || strings.Contains(f.gotMsg.Content, "victim") {
		t.Error("foreign file id/name leaked into streamed message")
	}

	// Happy path: own file → data URL + markdown reference.
	meta := saveTestPNG(t, svc, alice.ID)
	f2 := &fakeStream{events: []chat.Event{{Type: "done", Data: map[string]any{"usage": nil}}}}
	useFakeStream(t, f2)
	rec := apiServe(t, d, alice, "POST", "/api/chats/"+c.ID+"/messages",
		map[string]any{"content": "look", "images": []map[string]any{{"file_id": meta.ID}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("send with own image: %d %s", rec.Code, rec.Body.String())
	}
	if len(f2.gotMsg.Images) != 1 || !strings.HasPrefix(f2.gotMsg.Images[0], "data:image/png;base64,") {
		t.Errorf("vision payload images: %+v", f2.gotMsg.Images)
	}
	wantRef := "\n![pic.png](/files/" + meta.ID + "/pic.png)"
	if !strings.HasSuffix(f2.gotMsg.Content, wantRef) {
		t.Errorf("content = %q, want suffix %q", f2.gotMsg.Content, wantRef)
	}
}
