// Suggest-next-message tests (BE-chat owned): the defensive line parser,
// the happy-path service flow on the fake providers.Client harness, task
// gating, and the typed error surface the apichat handler maps to 503.
package chat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"chatgo/internal/providers"
	"chatgo/internal/store"
)

// ---------- fake that returns canned suggest output ----------

// suggestClient wraps the service_test.go fakeClient, overriding Complete
// with canned text while still recording the call through the embedded
// fake (completeCalls accounting stays intact).
type suggestClient struct {
	*fakeClient
	mu   sync.Mutex
	text string
	err  error
	last []providers.ChatMessage
}

func (f *suggestClient) Complete(ctx context.Context, p providers.Provider, model string, msgs []providers.ChatMessage) (string, error) {
	f.mu.Lock()
	f.last = append([]providers.ChatMessage(nil), msgs...)
	text, err := f.text, f.err
	f.mu.Unlock()
	_, _ = f.fakeClient.Complete(ctx, p, model, msgs)
	if err != nil {
		return "", err
	}
	return text, nil
}

func (f *suggestClient) snapshotLast() []providers.ChatMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// ---------- parser ----------

func TestParseSuggestions(t *testing.T) {
	long := strings.Repeat("é", 250)
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "plain lines",
			raw:  "How do I deploy it?\nWhat about rollback?\nWhich ports are needed?",
			want: []string{"How do I deploy it?", "What about rollback?", "Which ports are needed?"},
		},
		{
			name: "numbered and list markers stripped",
			raw:  "1. How do I deploy it?\n2) What about rollback?\n3、第三个问题？\n**Why not Podman?**\n- cheaper option?\n* last thought?",
			want: []string{"How do I deploy it?", "What about rollback?", "第三个问题？", "Why not Podman?", "cheaper option?"},
		},
		{
			name: "emoji bullets and quotes stripped",
			raw:  "✨ How do I deploy it?\n💡 \"What about rollback?\"\n• ‘Which ports?’\n❓ 另一件该问什么？",
			want: []string{"How do I deploy it?", "What about rollback?", "Which ports?", "另一件该问什么？"},
		},
		{
			name: "fences and lead-ins dropped, deduped, capped",
			raw:  "```json\nHere are 3 suggestions:\nHow are things?\nHOW ARE THINGS?\n???\n" + long + "?",
			want: []string{"How are things?", strings.Repeat("é", 200)},
		},
		{
			name: "crlf tolerated",
			raw:  "Q one?\r\nQ two?",
			want: []string{"Q one?", "Q two?"},
		},
		{
			name: "one usable line survives a wall of junk",
			raw:  "\n\n  \n```\nSuggestions:\n- 1. ??? \nReal question?\n",
			want: []string{"Real question?"},
		},
		{
			name: "nothing usable",
			raw:  "\n\n   \n```\nHere are some questions:\n- ??? \n",
			want: nil,
		},
		{
			name: "empty output",
			raw:  "",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSuggestions(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("item %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// ---------- service ----------

func TestSuggestNextHappyPath(t *testing.T) {
	fc := &fakeClient{}
	sc := &suggestClient{fakeClient: fc, text: "1. How do I deploy it?\n- What about rollback?\n✨ Which ports are needed?"}
	svc, st, chatID := newEnv(t, fc, 4096, "p1:m1")
	svc.Prov = sc
	huge := strings.Repeat("x", 1000)
	seed(t, st, chatID,
		[2]string{"user", "how do I ship this?"},
		[2]string{"assistant", "docker works"},
		[2]string{"user", huge})

	got, err := svc.SuggestNext(context.Background(), chatID, "u1")
	if err != nil {
		t.Fatalf("SuggestNext: %v", err)
	}
	want := []string{"How do I deploy it?", "What about rollback?", "Which ports are needed?"}
	if len(got) != len(want) {
		t.Fatalf("suggestions = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d = %q, want %q", i, got[i], want[i])
		}
	}
	if _, _, calls, _ := fc.snapshot(); calls != 1 {
		t.Fatalf("Complete calls = %d, want 1", calls)
	}

	// Prompt: instruction system turn + bounded, role-labeled transcript.
	msgs := sc.snapshotLast()
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[0].Content != suggestInstruction {
		t.Fatalf("system turn wrong: %+v", msgs)
	}
	if !strings.Contains(msgs[1].Content, "Assistant: docker works") {
		t.Fatalf("transcript missing assistant turn: %q", msgs[1].Content)
	}
	if !strings.Contains(msgs[1].Content, strings.Repeat("x", suggestMessageRunes)) ||
		strings.Contains(msgs[1].Content, strings.Repeat("x", suggestMessageRunes+1)) {
		t.Fatalf("long message not truncated to %d runes: %q", suggestMessageRunes, msgs[1].Content)
	}
}

func TestSuggestNextNoTaskModel(t *testing.T) {
	fc := &fakeClient{}
	svc, st, chatID := newEnv(t, fc, 4096, "") // cfg + setting both unset
	seed(t, st, chatID, [2]string{"user", "hi"}, [2]string{"assistant", "hello"})

	_, err := svc.SuggestNext(context.Background(), chatID, "u1")
	if !errors.Is(err, ErrSuggestUnavailable) {
		t.Fatalf("err = %v, want ErrSuggestUnavailable", err)
	}
	if _, _, calls, _ := fc.snapshot(); calls != 0 {
		t.Fatalf("Complete called %d times without a task model", calls)
	}
}

func TestSuggestNextRespectsTaskGating(t *testing.T) {
	fc := &fakeClient{}
	sc := &suggestClient{fakeClient: fc, text: "one?\ntwo?"}
	svc, st, chatID := newEnv(t, fc, 4096, "p2:tm1")
	svc.Prov = sc
	seedTaskProvider(t, st)
	seed(t, st, chatID, [2]string{"user", "hi"}, [2]string{"assistant", "hello"})

	p2, err := st.Providers.Get(context.Background(), "p2")
	if err != nil {
		t.Fatal(err)
	}
	p2.Enabled = false
	if err := st.Providers.Update(context.Background(), p2); err != nil {
		t.Fatal(err)
	}

	_, err = svc.SuggestNext(context.Background(), chatID, "u1")
	if !errors.Is(err, ErrSuggestUnavailable) {
		t.Fatalf("err = %v, want ErrSuggestUnavailable for a disabled task provider", err)
	}
	if _, _, calls, _ := fc.snapshot(); calls != 0 {
		t.Fatalf("Complete called %d times for a disabled task provider", calls)
	}
}

func TestSuggestNextUpstreamErrorIsTyped(t *testing.T) {
	fc := &fakeClient{}
	sc := &suggestClient{fakeClient: fc, err: errors.New("upstream exploded")}
	svc, st, chatID := newEnv(t, fc, 4096, "p1:m1")
	svc.Prov = sc
	seed(t, st, chatID, [2]string{"user", "hi"})

	_, err := svc.SuggestNext(context.Background(), chatID, "u1")
	if !errors.Is(err, ErrSuggestUnavailable) {
		t.Fatalf("err = %v, want ErrSuggestUnavailable", err)
	}
}

func TestSuggestNextTooFewLines(t *testing.T) {
	fc := &fakeClient{}
	sc := &suggestClient{fakeClient: fc, text: "Only one question?"}
	svc, st, chatID := newEnv(t, fc, 4096, "p1:m1")
	svc.Prov = sc
	seed(t, st, chatID, [2]string{"user", "hi"})

	_, err := svc.SuggestNext(context.Background(), chatID, "u1")
	if !errors.Is(err, ErrNoSuggestions) {
		t.Fatalf("err = %v, want ErrNoSuggestions", err)
	}
}

func TestSuggestNextOwnershipAndEmptyChat(t *testing.T) {
	fc := &fakeClient{}
	svc, st, chatID := newEnv(t, fc, 4096, "p1:m1")
	seed(t, st, chatID, [2]string{"user", "hi"}, [2]string{"assistant", "hello"})

	// Foreign chat: indistinguishable from a missing one (no existence leak).
	if _, err := svc.SuggestNext(context.Background(), chatID, "u2"); !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("foreign chat err = %v, want ErrChatNotFound", err)
	}
	if _, err := svc.SuggestNext(context.Background(), "ghost", "u1"); !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("missing chat err = %v, want ErrChatNotFound", err)
	}

	// Empty chat: nothing to suggest from — typed error, zero upstream calls.
	empty := &store.Chat{ID: "c-empty", UserID: "u1", ProviderID: "p1", ModelID: "m1"}
	if err := st.Chats.Create(context.Background(), empty); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SuggestNext(context.Background(), empty.ID, "u1"); !errors.Is(err, ErrNoSuggestions) {
		t.Fatalf("empty chat err = %v, want ErrNoSuggestions", err)
	}
	if _, _, calls, _ := fc.snapshot(); calls != 0 {
		t.Fatalf("Complete called %d times with nothing to suggest", calls)
	}
}

func TestSuggestNextSkipsSystemAndFailedRows(t *testing.T) {
	fc := &fakeClient{}
	svc, st, chatID := newEnv(t, fc, 4096, "p1:m1")
	ctx := context.Background()

	// Only a system row and a failed assistant turn exist → no transcript.
	m1 := &store.Message{ChatID: chatID, Role: "system", Content: "ignore me"}
	if err := st.Messages.Append(ctx, m1); err != nil {
		t.Fatal(err)
	}
	e := "upstream died mid-stream"
	m2 := &store.Message{ChatID: chatID, Role: "assistant", Content: "partial", ParentID: m1.ID, Error: &e}
	if err := st.Messages.Append(ctx, m2); err != nil {
		t.Fatal(err)
	}
	if err := st.Chats.SetLeaf(ctx, chatID, m2.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SuggestNext(ctx, chatID, "u1"); !errors.Is(err, ErrNoSuggestions) {
		t.Fatalf("err = %v, want ErrNoSuggestions", err)
	}
	if _, _, calls, _ := fc.snapshot(); calls != 0 {
		t.Fatalf("Complete called %d times for a chat with no real turns", calls)
	}
}
