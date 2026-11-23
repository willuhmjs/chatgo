// Tests for BE-chat: Service.Send orchestration (persistence, ordering,
// compaction, regenerate, titles, summaries) against a fake providers.Client
// and a real in-memory SQLite store.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgo/internal/config"
	"chatgo/internal/providers"
	"chatgo/internal/search"
	"chatgo/internal/store"
)

// ---------- fake providers.Client ----------

type fakeClient struct {
	mu        sync.Mutex
	chunks    []providers.Chunk
	streamErr error

	streamCalls int
	lastOpts    providers.StreamOpts

	completeCalls  int
	completeDelay  time.Duration
	streamDelay    time.Duration
	hold           <-chan struct{} // when non-nil, streams stall until it closes
	completePrompt []string        // concatenated prompts, in call order
}

func (f *fakeClient) ListModels(context.Context, string, string) ([]providers.Model, error) {
	return nil, errors.New("not used")
}

func (f *fakeClient) StreamChat(ctx context.Context, p providers.Provider, opts providers.StreamOpts) (<-chan providers.Chunk, error) {
	f.mu.Lock()
	f.streamCalls++
	f.lastOpts = opts
	hold := f.hold
	f.mu.Unlock()
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	out := make(chan providers.Chunk)
	go func() {
		defer close(out)
		if hold != nil {
			select {
			case <-hold:
			case <-ctx.Done():
				return
			}
		}
		for _, c := range f.chunks {
			if f.streamDelay > 0 {
				select {
				case <-time.After(f.streamDelay):
				case <-ctx.Done():
					return
				}
			}
			select {
			case out <- c:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (f *fakeClient) Complete(ctx context.Context, _ providers.Provider, model string, msgs []providers.ChatMessage) (string, error) {
	f.mu.Lock()
	f.completeCalls++
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(m.Role)
		sb.WriteByte('\n')
		sb.WriteString(m.Content)
	}
	f.completePrompt = append(f.completePrompt, sb.String())
	delay := f.completeDelay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	_ = model
	text := sb.String()
	switch {
	case strings.Contains(text, "3-5 word title for:"):
		return "Greet Helper Bot", nil
	case strings.Contains(text, "Summarize the conversation"):
		return "Fake summary text", nil
	}
	return "unexpected complete prompt", nil
}

func (f *fakeClient) snapshot() (providers.StreamOpts, int, int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := make([]string, len(f.completePrompt))
	copy(p, f.completePrompt)
	return f.lastOpts, f.streamCalls, f.completeCalls, p
}

// ---------- env helpers ----------

func newEnv(t *testing.T, fc *fakeClient, contextLen int, taskModel string) (*Service, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	name := "file:memchat_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	st, err := store.Open(name, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if err := st.Users.Insert(ctx, &store.User{ID: "u1", Email: "u1@x.dev", Role: store.RoleUser}); err != nil {
		t.Fatal(err)
	}
	if err := st.Users.Insert(ctx, &store.User{ID: "u2", Email: "u2@x.dev", Role: store.RoleUser}); err != nil {
		t.Fatal(err)
	}
	if err := st.Providers.Insert(ctx, &store.Provider{ID: "p1", Name: "fake", BaseURL: "http://127.0.0.1:1", APIKey: "sk-test", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.Models.ReplaceForProvider(ctx, "p1", []store.ModelRow{{ModelID: "m1", Display: "M1", ContextLen: contextLen, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	chat := &store.Chat{ID: "c1", UserID: "u1", ProviderID: "p1", ModelID: "m1"}
	if err := st.Chats.Create(ctx, chat); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{TaskModel: taskModel}
	svc := New(cfg, st, fc, search.New(&config.Config{})) // search disabled by default
	return svc, st, "c1"
}

// seed appends messages exactly the way production writes them: each row
// parents the previous one and the chat's leaf lands on the newest row.
func seed(t *testing.T, st *store.Store, chatID string, msgs ...[2]string) []*store.Message {
	t.Helper()
	var out []*store.Message
	var prev string
	for _, p := range msgs {
		m := &store.Message{ChatID: chatID, Role: p[0], Content: p[1], ParentID: prev}
		if err := st.Messages.Append(context.Background(), m); err != nil {
			t.Fatal(err)
		}
		prev = m.ID
		out = append(out, m)
	}
	if prev != "" {
		if err := st.Chats.SetLeaf(context.Background(), chatID, prev); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func collect(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var evs []Event
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
		case <-deadline:
			t.Fatal("timed out waiting for events")
		}
	}
}

func typesOf(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

// idxOfType returns event indexes with the given type, in order.
func idxOfType(evs []Event, typ string) []int {
	var out []int
	for i, e := range evs {
		if e.Type == typ {
			out = append(out, i)
		}
	}
	return out
}

func tokenText(evs []Event) string {
	var sb strings.Builder
	for _, e := range evs {
		if e.Type == EventToken {
			sb.WriteString(e.Data.(TokenData).Content)
		}
	}
	return sb.String()
}

func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func sayChunks(parts ...string) []providers.Chunk {
	var out []providers.Chunk
	for _, p := range parts {
		out = append(out, providers.Chunk{Content: p})
	}
	return out
}

func stopChunk(in, out int) providers.Chunk {
	return providers.Chunk{FinishReason: "stop", Usage: &providers.Usage{In: in, Out: out}}
}

// ---------- tests ----------

func TestSendHappyFlow(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("Hel", "lo"), stopChunk(11, 7))}
	svc, st, chatID := newEnv(t, fc, 4096, "p1:m1")

	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "Hi"}, SendOpts{}))

	types := typesOf(evs)
	if types[0] != EventMeta || types[len(types)-1] != EventDone {
		t.Fatalf("event order = %v, want meta…done", types)
	}
	if got := tokenText(evs); got != "Hello" {
		t.Fatalf("token text = %q, want Hello", got)
	}
	meta := evs[0].Data.(MetaData)
	if meta.ChatID != chatID || meta.MessageID == "" {
		t.Fatalf("bad meta %+v", meta)
	}
	done := evs[len(evs)-1].Data.(DoneData)
	if done.Usage == nil || done.Usage.In != 11 || done.Usage.Out != 7 {
		t.Fatalf("done usage = %+v", done.Usage)
	}

	msgs, err := st.Messages.List(context.Background(), chatID, 500, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("persisted %d messages, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "Hi" {
		t.Fatalf("user msg = %+v", msgs[0])
	}
	a := msgs[1]
	if a.ID != meta.MessageID || a.Role != "assistant" || a.Content != "Hello" || a.Error != nil {
		t.Fatalf("assistant msg = %+v", a)
	}
	if a.TokensIn != 11 || a.TokensOut != 7 {
		t.Fatalf("assistant usage = %d/%d, want 11/7", a.TokensIn, a.TokensOut)
	}
}

func TestSendOwnershipNoExistenceLeak(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("x"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 4096, "")

	// Foreign chat and non-existent chat must be indistinguishable.
	ch1, err1 := svc.Send(context.Background(), "u2", chatID, providers.ChatMessage{Content: "hey"}, SendOpts{})
	if ch1 != nil || !errors.Is(err1, ErrChatNotFound) {
		t.Fatalf("foreign chat: got (%v, %v), want (nil, ErrChatNotFound)", ch1, err1)
	}
	_, err2 := svc.Send(context.Background(), "u1", "does-not-exist", providers.ChatMessage{Content: "hey"}, SendOpts{})
	if !errors.Is(err2, ErrChatNotFound) {
		t.Fatalf("missing chat: got %v, want ErrChatNotFound", err2)
	}

	// Soft-deleted chat is invisible too.
	if err := st.Chats.Delete(context.Background(), chatID); err != nil {
		t.Fatal(err)
	}
	_, err3 := svc.Send(context.Background(), "u1", chatID, providers.ChatMessage{Content: "hey"}, SendOpts{})
	if !errors.Is(err3, ErrChatNotFound) {
		t.Fatalf("deleted chat: got %v, want ErrChatNotFound", err3)
	}
}

func TestRegenerateDeletesTailSkipsUserPersist(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("new answer"), stopChunk(5, 2))}
	svc, st, chatID := newEnv(t, fc, 4096, "")
	seeded := seed(t, st, chatID, [2]string{"user", "one"}, [2]string{"assistant", "reply1"}, [2]string{"user", "two"})

	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "two"}, SendOpts{From: seeded[2].ID}))
	if types := typesOf(evs); types[0] != EventMeta || types[len(types)-1] != EventDone {
		t.Fatalf("event order = %v", types)
	}

	msgs, err := st.Messages.List(context.Background(), chatID, 500, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("messages after regenerate = %d (%v), want 3", len(msgs), msgs)
	}
	if msgs[0].Content != "one" || msgs[1].Content != "reply1" || msgs[2].Role != "assistant" {
		t.Fatalf("tail not replaced: %+v", msgs)
	}
	// The regenerated user turn stays prompt-only (never double-persisted).
	for _, m := range msgs {
		if m.Role == "user" && m.Content == "two" {
			t.Fatal("user message re-persisted on regenerate")
		}
	}
	opts, _, _, _ := fc.snapshot()
	last := opts.Messages[len(opts.Messages)-1]
	if last.Role != "user" || last.Content != "two" {
		t.Fatalf("prompt does not end with regenerated user turn: %+v", opts.Messages)
	}

	// A regenerate anchor from another chat (or bogus) is 404-ish.
	_, err = svc.Send(context.Background(), "u1", chatID, providers.ChatMessage{Content: "x"}, SendOpts{From: "bogus"})
	if !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("bogus From: got %v, want ErrMessageNotFound", err)
	}
}

func TestCompactionSmallContext(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("ok"), stopChunk(1, 1))}
	long := strings.Repeat("x", 400) // ~100 est. tokens per message
	svc, st, chatID := newEnv(t, fc, 512, "p1:m1")
	var seedMsgs [][2]string
	for i := 0; i < 12; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		seedMsgs = append(seedMsgs, [2]string{role, fmt.Sprintf("%s %d", long, i)})
	}
	seed(t, st, chatID, seedMsgs...)

	// Context 512 → trigger 358; seeded history is ~1200 tokens (+ system
	// prompt with its date anchor). Compact hard.
	collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "go on"}, SendOpts{}))

	opts, _, _, _ := fc.snapshot()
	if len(opts.Messages) >= 8 {
		t.Fatalf("prompt not compacted: %d messages", len(opts.Messages))
	}
	first := opts.Messages[0]
	// content = base prompt + date anchor suffix (see systemPrompt)
	if first.Role != "system" || !strings.HasPrefix(first.Content, defaultSystemPrompt) {
		t.Fatalf("msg[0] not base system prompt: %+v", first)
	}
	if opts.Messages[1].Content != seedMsgs[0][1] || opts.Messages[2].Content != seedMsgs[1][1] {
		t.Fatal("first two history messages not kept")
	}
	last := opts.Messages[len(opts.Messages)-1]
	if last.Role != "user" || last.Content != "go on" {
		t.Fatalf("live user message not kept: %+v", last)
	}
	total := estimateTokens(opts.Messages)
	if total > int(0.61*400) {
		t.Fatalf("compacted prompt still %d est tokens (budget 240)", total)
	}
}

func TestSearchEventOrder(t *testing.T) {
	// SearXNG stand-in on loopback; the service is built through search.New
	// with AllowLoopback so the real SSRF-guarded transport is exercised.
	// Ordering contract must hold: meta → sources → tokens → done.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"title":"Go lang","url":"https://go.example","content":"snippet"},`+
			`{"title":"evil","url":"javascript:alert(1)","content":"x"}]}`)
	}))
	defer srv.Close()

	fc := &fakeClient{chunks: append(sayChunks("a"), stopChunk(1, 1))}
	svc, _, chatID := newEnv(t, fc, 4096, "")
	svc.Search = search.New(&config.Config{SearXNGURL: srv.URL, AllowLoopback: true})

	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "weather"}, SendOpts{Search: true}))

	sources := idxOfType(evs, EventSources)
	if len(sources) != 1 {
		t.Fatalf("sources events = %d, want exactly 1 (%v)", len(sources), typesOf(evs))
	}
	tokens := idxOfType(evs, EventToken)
	done := idxOfType(evs, EventDone)
	if len(tokens) == 0 || len(done) != 1 {
		t.Fatalf("tokens/done missing: %v", typesOf(evs))
	}
	if !(0 < sources[0] && sources[0] < tokens[0] && tokens[len(tokens)-1] < done[0]) {
		t.Fatalf("event order violated: meta < sources < tokens < done (%v)", typesOf(evs))
	}
	sd, ok := evs[sources[0]].Data.(SourcesData)
	if !ok {
		t.Fatalf("sources payload = %T", evs[sources[0]].Data)
	}
	if len(sd.Items) != 1 || sd.Items[0].URL != "https://go.example" {
		t.Fatalf("sources items = %+v, want only the http(s) hit", sd.Items)
	}
	// The upstream turn must carry the untrusted-data framing (it is sent as
	// a USER turn by providers.buildMessages, never a system message).
	opts, _, _, _ := fc.snapshot()
	if !strings.Contains(opts.SearchCtx, "UNTRUSTED") || !strings.Contains(opts.SearchCtx, "https://go.example") {
		t.Fatalf("SearchCtx lacks untrusted-data framing: %q", opts.SearchCtx)
	}
}

func TestTitleOnceOnlyWithTaskModel(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("hi"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 4096, "p1:m1")

	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "hello there"}, SendOpts{}))
	title := idxOfType(evs, EventTitle)
	if len(title) != 1 {
		t.Fatalf("title events = %d, want 1 (%v)", len(title), typesOf(evs))
	}
	tk := idxOfType(evs, EventToken)
	if len(tk) > 0 && title[0] > tk[0] {
		t.Fatal("title event must precede tokens")
	}
	if got := evs[title[0]].Data.(TitleData).Title; got != "Greet Helper Bot" {
		t.Fatalf("title = %q", got)
	}
	chat, err := st.Chats.Get(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if chat.Title != "Greet Helper Bot" {
		t.Fatalf("persisted title = %q", chat.Title)
	}

	// Second turn: no re-titling.
	evs = collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "again"}, SendOpts{}))
	if n := len(idxOfType(evs, EventTitle)); n != 0 {
		t.Fatalf("second send retitled (%v)", typesOf(evs))
	}
}

func TestTitleTaskModelSettingOverride(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("hi"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 4096, "") // cfg.TaskModel unset
	if err := st.Settings.Set(context.Background(), "task_model", "p1:m1"); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "hello"}, SendOpts{}))
	if n := len(idxOfType(evs, EventTitle)); n != 1 {
		t.Fatalf("settings task_model did not produce title event (%v)", typesOf(evs))
	}
}

func TestTitleFallbackWhenNoTaskModel(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("hi"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 4096, "")
	long := strings.Repeat("word ", 40) // way past 48 runes

	collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: long}, SendOpts{}))

	chat, err := st.Chats.Get(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	want := string([]rune(strings.Join(strings.Fields(long), " "))[:48])
	if chat.Title != want {
		t.Fatalf("fallback title = %q, want %q", chat.Title, want)
	}
	if _, _, calls, _ := fc.snapshot(); calls != 0 {
		t.Fatalf("Complete called %d times without TaskModel", calls)
	}
}

func TestTitleSkippedWhenSlowButPersistedLater(t *testing.T) {
	old := titleEmitBudget
	titleEmitBudget = time.Millisecond
	defer func() { titleEmitBudget = old }()

	fc := &fakeClient{
		chunks:        append(sayChunks("hi"), stopChunk(1, 1)),
		completeDelay: 150 * time.Millisecond,
	}
	svc, st, chatID := newEnv(t, fc, 4096, "p1:m1")

	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "hello"}, SendOpts{}))
	if n := len(idxOfType(evs, EventTitle)); n != 0 {
		t.Fatalf("slow title must be skipped, got %d events (%v)", n, typesOf(evs))
	}
	waitFor(t, func() bool {
		c, err := st.Chats.Get(context.Background(), chatID)
		return err == nil && c.Title == "Greet Helper Bot"
	})
}

func TestStreamStartError(t *testing.T) {
	fc := &fakeClient{streamErr: errors.New("dial up: refused")}
	svc, st, chatID := newEnv(t, fc, 4096, "")

	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "hi"}, SendOpts{}))
	types := typesOf(evs)
	if types[0] != EventMeta || types[len(types)-1] != EventError {
		t.Fatalf("events = %v, want meta…error", types)
	}
	msgs, _ := st.Messages.List(context.Background(), chatID, 500, "")
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("only user message should persist, got %+v", msgs)
	}
}

func TestUpstreamErrorMidStreamPersistsPartial(t *testing.T) {
	fc := &fakeClient{chunks: sayChunks("partial answ", "er")} // no finish chunk → died mid-stream
	svc, st, chatID := newEnv(t, fc, 4096, "")

	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "hi"}, SendOpts{}))
	types := typesOf(evs)
	if types[len(types)-1] != EventError {
		t.Fatalf("last event = %s, want error (%v)", types[len(types)-1], types)
	}
	if got := tokenText(evs); got != "partial answer" {
		t.Fatalf("partial tokens = %q", got)
	}
	msgs, _ := st.Messages.List(context.Background(), chatID, 500, "")
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want user+partial assistant", len(msgs))
	}
	a := msgs[1]
	if a.Role != "assistant" || a.Content != "partial answer" || a.Error == nil {
		t.Fatalf("partial assistant not persisted with error: %+v", a)
	}
}

func TestSummarizeFiresAndFeedsNextPrompt(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("ok"), stopChunk(1, 1))}
	long := strings.Repeat("y", 300) // ~75 tokens per message
	svc, st, chatID := newEnv(t, fc, 300, "p1:m1")
	seeded := seed(t, st, chatID,
		[2]string{"user", long + " 0"}, [2]string{"assistant", long + " 1"},
		[2]string{"user", long + " 2"}, [2]string{"assistant", long + " 3"})

	collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "continue"}, SendOpts{}))

	var sum *store.Summary
	waitFor(t, func() bool {
		var err error
		sum, err = svc.latestSummary(context.Background(), chatID)
		return err == nil && sum != nil && sum.Text == "Fake summary text"
	})
	if sum.UpToMessageID == "" {
		t.Fatal("summary row has no up_to_message_id")
	}
	// Window must stop short of the newest (current user) message.
	var known bool
	for _, m := range seeded {
		if m.ID == sum.UpToMessageID {
			known = true
		}
	}
	if !known {
		t.Fatalf("summary up_to %q is not a seeded message", sum.UpToMessageID)
	}

	// Next turn must splice the summary in as system context.
	collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "and then?"}, SendOpts{}))
	opts, _, _, _ := fc.snapshot()
	var found bool
	for _, m := range opts.Messages {
		if m.Role == "system" && strings.Contains(m.Content, "Fake summary text") {
			found = true
		}
	}
	if !found {
		t.Fatal("summary not spliced into next prompt")
	}
}

func TestDoneUsageNullWhenUpstreamSilent(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("x"), providers.Chunk{FinishReason: "stop"})}
	svc, _, chatID := newEnv(t, fc, 4096, "")
	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "hi"}, SendOpts{}))
	done := evs[len(evs)-1]
	if done.Type != EventDone {
		t.Fatalf("last = %s", done.Type)
	}
	if u := done.Data.(DoneData).Usage; u != nil {
		t.Fatalf("usage = %+v, want null", u)
	}
	b, _ := json.Marshal(done.Data)
	if !strings.Contains(string(b), `"usage":null`) {
		t.Fatalf("done JSON = %s, want usage:null", b)
	}
}

func TestSplitTaskModel(t *testing.T) {
	if p, m, ok := splitTaskModel("prov:model:x"); !ok || p != "prov" || m != "model:x" {
		t.Fatalf("splitTaskModel(prov:model:x) = %q,%q,%v", p, m, ok)
	}
	for _, bad := range []string{"", "model", ":model", "prov:", " : "} {
		if _, _, ok := splitTaskModel(strings.TrimSpace(bad)); ok {
			t.Fatalf("splitTaskModel(%q) unexpectedly ok", bad)
		}
	}
}

func mustSend(t *testing.T, svc *Service, userID, chatID string, um providers.ChatMessage, opts SendOpts) <-chan Event {
	t.Helper()
	ch, err := svc.Send(context.Background(), userID, chatID, um, opts)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return ch
}

// ---------- concurrency caps ----------

func wantRejected(t *testing.T, ch <-chan Event) {
	t.Helper()
	evs := collect(t, ch)
	if len(evs) != 1 || evs[0].Type != EventError {
		t.Fatalf("over-limit send = %v, want single error event", typesOf(evs))
	}
	if msg := evs[0].Data.(ErrorData).Message; msg != "too many concurrent requests" {
		t.Fatalf("rejection message = %q", msg)
	}
}

func TestSendPerUserConcurrencyCap(t *testing.T) {
	hold := make(chan struct{})
	fc := &fakeClient{
		chunks: append(sayChunks("x"), stopChunk(1, 1)),
		hold:   hold,
	}
	svc, _, chatID := newEnv(t, fc, 4096, "")

	var inflight []<-chan Event
	for i := 0; i < maxConcurrentPerUser; i++ {
		inflight = append(inflight, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "go"}, SendOpts{}))
	}
	waitFor(t, func() bool {
		_, calls, _, _ := fc.snapshot()
		return calls >= maxConcurrentPerUser
	})

	// The (N+1)-th concurrent send from the same user is refused as an SSE
	// error event without ever touching the upstream.
	wantRejected(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "one too many"}, SendOpts{}))
	_, calls, _, _ := fc.snapshot()
	if calls != maxConcurrentPerUser {
		t.Fatalf("rejected send reached upstream: %d stream calls", calls)
	}

	close(hold) // let the turns finish…
	for _, ch := range inflight {
		collect(t, ch)
	}
	// …and the slot frees up: the next send completes normally.
	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "now?"}, SendOpts{}))
	if last := typesOf(evs)[len(evs)-1]; last != EventDone {
		t.Fatalf("post-cap send ended with %s (%v)", last, typesOf(evs))
	}
}

func TestSendGlobalConcurrencyCap(t *testing.T) {
	oldP, oldG := maxConcurrentPerUser, maxConcurrentGlobal
	maxConcurrentPerUser, maxConcurrentGlobal = 2, 2
	defer func() { maxConcurrentPerUser, maxConcurrentGlobal = oldP, oldG }()

	hold := make(chan struct{})
	fc := &fakeClient{chunks: append(sayChunks("x"), stopChunk(1, 1)), hold: hold}
	svc, st, c1 := newEnv(t, fc, 4096, "")
	if err := st.Chats.Create(context.Background(),
		&store.Chat{ID: "c2", UserID: "u2", ProviderID: "p1", ModelID: "m1"}); err != nil {
		t.Fatal(err)
	}

	// Two users, one turn each: under both caps, both run.
	ch1 := mustSend(t, svc, "u1", c1, providers.ChatMessage{Content: "a"}, SendOpts{})
	ch2 := mustSend(t, svc, "u2", "c2", providers.ChatMessage{Content: "b"}, SendOpts{})
	waitFor(t, func() bool {
		_, calls, _, _ := fc.snapshot()
		return calls >= 2
	})

	// Global cap reached — a turn from a user with per-user headroom still
	// gets rejected (429-style).
	wantRejected(t, mustSend(t, svc, "u1", c1, providers.ChatMessage{Content: "c"}, SendOpts{}))

	close(hold)
	collect(t, ch1)
	collect(t, ch2)
}

// ---------- provider enable flag ----------

func TestSendRejectsDisabledProvider(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("x"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 4096, "")

	row, err := st.Providers.Get(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	row.Enabled = false
	if err := st.Providers.Update(context.Background(), row); err != nil {
		t.Fatal(err)
	}

	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "hi"}, SendOpts{}))
	types := typesOf(evs)
	if types[0] != EventMeta || types[len(types)-1] != EventError {
		t.Fatalf("events = %v, want meta…error", types)
	}
	if msg := evs[len(evs)-1].Data.(ErrorData).Message; msg != "model provider disabled" {
		t.Fatalf("error message = %q, want %q", msg, "model provider disabled")
	}
	if _, calls, _, _ := fc.snapshot(); calls != 0 {
		t.Fatalf("disabled provider reached upstream (%d calls)", calls)
	}
	// Same error-flow as other pre-upstream failures: user turn persists.
	msgs, _ := st.Messages.List(context.Background(), chatID, 500, "")
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("messages = %+v, want only the user turn", msgs)
	}
}

// ---------- search_url setting fallback ----------

func TestSearchURLSettingUsedWhenCfgEmpty(t *testing.T) {
	queries := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries <- r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"title":"Doc","url":"https://doc.example","content":"s"}]}`)
	}))
	defer srv.Close()

	fc := &fakeClient{chunks: append(sayChunks("ok"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 4096, "")
	// cfg.SearXNGURL == "" → service disabled; guard config (AllowLoopback)
	// exists so the setting-provided URL can still be dialled.
	svc.Search = search.New(&config.Config{AllowLoopback: true})
	if svc.Search.Enabled {
		t.Fatal("service should be disabled with SEARXNG_URL unset")
	}

	// No setting configured: search toggle degrades with NO sources event.
	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "q?"}, SendOpts{Search: true}))
	if n := len(idxOfType(evs, EventSources)); n != 0 {
		t.Fatalf("sources events without any target = %d (%v)", n, typesOf(evs))
	}
	select {
	case q := <-queries:
		t.Fatalf("search ran against %q with no target configured", q)
	default:
	}

	// Admin writes search_url → the very next send must dial it.
	if err := st.Settings.Set(context.Background(), "search_url", srv.URL); err != nil {
		t.Fatal(err)
	}
	evs = collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "weather"}, SendOpts{Search: true}))
	sources := idxOfType(evs, EventSources)
	if len(sources) != 1 {
		t.Fatalf("sources events = %d, want 1 (%v)", len(sources), typesOf(evs))
	}
	items := evs[sources[0]].Data.(SourcesData).Items
	if len(items) != 1 || items[0].URL != "https://doc.example" {
		t.Fatalf("sources items = %+v", items)
	}
	select {
	case q := <-queries:
		if q != "weather" {
			t.Fatalf("setting-provided search got q=%q, want %q", q, "weather")
		}
	default:
		t.Fatal("search_url setting was never dialled")
	}
	opts, _, _, _ := fc.snapshot()
	if !strings.Contains(opts.SearchCtx, "UNTRUSTED") {
		t.Fatalf("SearchCtx lacks untrusted framing: %q", opts.SearchCtx)
	}
}

// ---------- fireCtx goroutine retention ----------

func TestFireCtxSpawnsNoWatcherGoroutines(t *testing.T) {
	// The old fireCtx spawned a `<-ctx.Done(); cancel()` watcher per call
	// that lived the full 60s deadline (callers cannot cancel by design).
	// fireCtx must now be pure: detach + deadline, cancel handed to the
	// worker goroutine.
	time.Sleep(50 * time.Millisecond) // let earlier tests' workers drain
	base := runtime.NumGoroutine()
	var cancels []context.CancelFunc
	for range 200 {
		_, cancel := fireCtx(context.Background())
		cancels = append(cancels, cancel) // deliberately not cancelled below
	}
	if n := runtime.NumGoroutine(); n > base+5 {
		t.Fatalf("fireCtx retained %d extra goroutines (watcher leak)", n-base)
	}
	for _, c := range cancels {
		c()
	}
}

func TestTurnWorkersReleaseGoroutines(t *testing.T) {
	// End-to-end: turns with title + summarize + persist workers must not
	// leave goroutines behind once the stream completes.
	fc := &fakeClient{chunks: append(sayChunks("ok"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 300, "p1:m1") // tiny ctx → compaction → summarize
	long := strings.Repeat("z", 300)
	seed(t, st, chatID,
		[2]string{"user", long + " 0"}, [2]string{"assistant", long + " 1"},
		[2]string{"user", long + " 2"}, [2]string{"assistant", long + " 3"})

	time.Sleep(50 * time.Millisecond)
	base := runtime.NumGoroutine()
	for i := 0; i < 10; i++ {
		collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: fmt.Sprintf("q %d", i)}, SendOpts{}))
	}
	time.Sleep(100 * time.Millisecond) // let async summarize/title workers exit
	if n := runtime.NumGoroutine(); n > base+8 {
		t.Fatalf("goroutine count grew by %d across 10 turns (retention leak?)", n-base)
	}
}

// TestSendBranching pins the ChatGPT-style version tree: regenerate lands as
// a SIBLING (nothing is deleted), the leaf follows the newest turn, and
// DeepestChild re-lands a branch switch on its newest continuation (OWUI's
// getDeepestChildId).
func TestSendBranching(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("answer two v2"), stopChunk(5, 2))}
	svc, st, chatID := newEnv(t, fc, 4096, "")
	ctx := context.Background()
	seed(t, st, chatID,
		[2]string{"user", "one"}, [2]string{"assistant", "reply1"},
		[2]string{"user", "two"}, [2]string{"assistant", "reply2"})
	msgs, err := st.Messages.List(ctx, chatID, 500, "")
	if err != nil {
		t.Fatal(err)
	}
	u2, a2 := msgs[2], msgs[3]

	// OWUI-style assistant regeneration (branch): a sibling answer under the
	// same user row; the old version and everything else survive.
	collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "two"},
		SendOpts{From: a2.ID, Branch: true}))
	msgs, _ = st.Messages.List(ctx, chatID, 500, "")
	if len(msgs) != 5 {
		t.Fatalf("rows after sibling regen = %d, want 5 (nothing deleted)", len(msgs))
	}
	a3 := msgs[4]
	if a3.ParentID != u2.ID || a3.Content != "answer two v2" {
		t.Fatalf("sibling answer = %+v (parent %q, want %q)", a3, a3.ParentID, u2.ID)
	}
	if leaf := chatLeafID(t, st, chatID); leaf != a3.ID {
		t.Fatalf("leaf after regen = %q, want %q", leaf, a3.ID)
	}
	path, err := st.Messages.Path(ctx, chatID, a3.ID)
	if err != nil || len(path) != 4 || path[3].ID != a3.ID || path[2].ID != u2.ID {
		t.Fatalf("branch path wrong: len=%d err=%v", len(path), err)
	}
	if _, err := st.Messages.Get(ctx, chatID, a2.ID); err != nil {
		t.Fatalf("old version deleted: %v", err)
	}

	// User-message edit (branch): the replacement siblings the edited user row
	// and carries a fresh answer; the old exchange stays intact.
	fc.chunks = append(sayChunks("replied"), stopChunk(1, 1))
	collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "two (edited)"},
		SendOpts{From: u2.ID, Branch: true}))
	msgs, _ = st.Messages.List(ctx, chatID, 500, "")
	if len(msgs) != 7 {
		t.Fatalf("rows after user edit = %d, want 7", len(msgs))
	}
	u2b, a2b := msgs[5], msgs[6]
	if u2b.ParentID != u2.ParentID || u2b.Role != "user" {
		t.Fatalf("edited user not a sibling of the original: %+v", u2b)
	}
	if a2b.ParentID != u2b.ID {
		t.Fatalf("answer parent = %q, want %q", a2b.ParentID, u2b.ID)
	}

	// Switching versions moves the chat leaf; OWUI's getDeepestChildId means
	// landing on a user version continues into its NEWEST answer (a3 under
	// u2), so a switch first resolves the sibling's deepest continuation.
	switchTo, err := st.Messages.DeepestChild(ctx, chatID, u2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if switchTo != a3.ID {
		t.Fatalf("DeepestChild(u2) = %q, want %q (newest answer under v1 user row)", switchTo, a3.ID)
	}
	if err := st.Chats.SetLeaf(ctx, chatID, switchTo); err != nil {
		t.Fatal(err)
	}
	path, err = st.Messages.Path(ctx, chatID, switchTo)
	if err != nil || len(path) != 4 || path[3].ID != a3.ID || path[2].ID != u2.ID {
		t.Fatalf("switched path wrong: len=%d err=%v", len(path), err)
	}

	// Continuing from the switched leaf hangs the new turn under a3 and
	// leaves the other branch untouched.
	collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "three"}, SendOpts{}))
	msgs, _ = st.Messages.List(ctx, chatID, 500, "")
	if len(msgs) != 9 {
		t.Fatalf("rows = %d, want 9", len(msgs))
	}
	if msgs[7].ParentID != a3.ID {
		t.Fatalf("continuation parent = %q, want %q", msgs[7].ParentID, a3.ID)
	}
	if leaf := chatLeafID(t, st, chatID); leaf != msgs[8].ID {
		t.Fatalf("leaf = %q, want %q", leaf, msgs[8].ID)
	}
}

func chatLeafID(t *testing.T, st *store.Store, chatID string) string {
	t.Helper()
	c, err := st.Chats.Get(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	return c.LeafID
}

// TestContinueResponse pins the OWUI continue flow: a branch regenerate
// anchored on an assistant row replays that answer in the prompt and follows
// it with the directive as prompt-only turns — no user row is persisted and
// the replacement lands as a sibling of the anchor.
func TestContinueResponse(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks(" — continued"), stopChunk(4, 3))}
	svc, st, chatID := newEnv(t, fc, 4096, "")
	ctx := context.Background()
	seed(t, st, chatID, [2]string{"user", "tell me more"}, [2]string{"assistant", "once upon a"})
	msgs, _ := st.Messages.List(ctx, chatID, 500, "")
	a := msgs[1]

	collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "continue please"},
		SendOpts{From: a.ID, Branch: true}))

	opts, _, _, _ := fc.snapshot()
	n := len(opts.Messages)
	if n < 3 || opts.Messages[n-2].Role != "assistant" || opts.Messages[n-2].Content != "once upon a" ||
		opts.Messages[n-1].Role != "user" || opts.Messages[n-1].Content != "continue please" {
		t.Fatalf("prompt does not replay the anchor + directive: %+v", opts.Messages)
	}

	msgs, _ = st.Messages.List(ctx, chatID, 500, "")
	if len(msgs) != 3 {
		t.Fatalf("rows=%d, want 3 (sibling answer only, no user row)", len(msgs))
	}
	a2 := msgs[2]
	if a2.Role != "assistant" || a2.ParentID != a.ParentID || a2.Content != " — continued" {
		t.Fatalf("replacement = %+v, want sibling assistant under %q", a2, a.ParentID)
	}
	if leaf := chatLeafID(t, st, chatID); leaf != a2.ID {
		t.Fatalf("leaf=%q, want %q", leaf, a2.ID)
	}
}

func TestSearchNPrecedence(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("ok"), stopChunk(1, 1))}
	svc, st, _ := newEnv(t, fc, 4096, "")
	ctx := context.Background()

	if got := svc.searchN(ctx); got != search.MaxResults {
		t.Fatalf("default searchN = %d, want %d", got, search.MaxResults)
	}
	svc.Cfg.SearchMaxResults = 10
	if got := svc.searchN(ctx); got != 10 {
		t.Fatalf("env searchN = %d, want 10", got)
	}
	// Admin setting beats env; whitespace tolerated; junk ignored; absurd
	// value clamps to the hard cap instead of erroring the turn.
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"3", 3}, {" 8 ", 8}, {"999", search.HardCap}, {"junk", 10}, {"", 10},
	} {
		if err := st.Settings.Set(ctx, "search_max_results", tc.in); err != nil {
			t.Fatal(err)
		}
		if got := svc.searchN(ctx); got != tc.want {
			t.Fatalf("searchN with setting %q = %d, want %d", tc.in, got, tc.want)
		}
	}
}
