// Suggest endpoint tests (BE-apichat owned). Prefix: TestAPI. Covers the
// auth/ownership walls, the capped empty-ish body, the typed 503 mapping
// and the per-user in-flight 429 guard.

package apichat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgo/internal/chat"
	"chatgo/internal/config"
	"chatgo/internal/providers"
	"chatgo/internal/store"
)

// suggestTestProv is a controllable providers.Client for the task model.
type suggestTestProv struct {
	mu      sync.Mutex
	text    string
	err     error
	calls   int
	block   chan struct{} // when non-nil, Complete signals entry here
	unblock chan struct{} // when non-nil, Complete waits until it closes
}

func (p *suggestTestProv) ListModels(context.Context, string, string) ([]providers.Model, error) {
	return nil, errors.New("unused")
}

func (p *suggestTestProv) StreamChat(context.Context, providers.Provider, providers.StreamOpts) (<-chan providers.Chunk, error) {
	return nil, errors.New("unused")
}

func (p *suggestTestProv) Complete(ctx context.Context, _ providers.Provider, _ string, _ []providers.ChatMessage) (string, error) {
	p.mu.Lock()
	p.calls++
	text, err := p.text, p.err
	p.mu.Unlock()
	if p.block != nil {
		select {
		case p.block <- struct{}{}:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if p.unblock != nil {
		select {
		case <-p.unblock:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err != nil {
		return "", err
	}
	return text, nil
}

func (p *suggestTestProv) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// suggestEnv wires a real chat.Service (task model p1:tm1) over the shared
// api test store, with alice owning a chat seeded with one exchange.
func suggestEnv(t *testing.T, prov providers.Client, taskModel string, withMessages bool) (*Deps, *store.User, *store.User, *store.Chat) {
	t.Helper()
	s := apiTestStore(t)
	ctx := t.Context()
	if err := s.Providers.Insert(ctx, &store.Provider{ID: "p1", Name: "fake", BaseURL: "http://127.0.0.1:1", APIKey: "sk-test", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Models.ReplaceForProvider(ctx, "p1",
		[]store.ModelRow{{ModelID: "tm1", Display: "TM1", ContextLen: 4096, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	alice := apiTestUser(t, s, "sug-a@x.io")
	bob := apiTestUser(t, s, "sug-b@x.io")
	c := apiTestChat(t, s, alice, "suggest me")
	if withMessages {
		if err := s.Messages.Append(ctx, &store.Message{ChatID: c.ID, Role: "user", Content: "hello"}); err != nil {
			t.Fatal(err)
		}
		if err := s.Messages.Append(ctx, &store.Message{ChatID: c.ID, Role: "assistant", Content: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	d := &Deps{Store: s, Chat: chat.New(&config.Config{TaskModel: taskModel}, s, prov, nil)}
	return d, alice, bob, c
}

func TestAPI_SuggestNextHappyAndWalls(t *testing.T) {
	prov := &suggestTestProv{text: "How do I deploy it?\n2) What about rollback?\n✨ Which ports?"}
	d, alice, bob, c := suggestEnv(t, prov, "p1:tm1", true)
	url := "/api/chats/" + c.ID + "/suggest"

	type resp struct {
		Suggestions []string `json:"suggestions"`
	}

	// Empty body is fine (the chat is the input).
	rec := apiServe(t, d, alice, "POST", url, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("suggest: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeJSON[resp](t, rec).Suggestions
	want := []string{"How do I deploy it?", "What about rollback?", "Which ports?"}
	if len(got) != len(want) {
		t.Fatalf("suggestions = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d = %q, want %q", i, got[i], want[i])
		}
	}
	// "{}" body works too.
	if rec := apiServe(t, d, alice, "POST", url, "{}"); rec.Code != http.StatusOK {
		t.Errorf("empty-object body: %d", rec.Code)
	}

	// Auth wall: no user in context → 401.
	if rec := apiServe(t, d, nil, "POST", url, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous suggest: %d, want 401", rec.Code)
	}
	// Ownership: foreign chat and unknown chat are both 404, never 403.
	if rec := apiServe(t, d, bob, "POST", url, nil); rec.Code != http.StatusNotFound {
		t.Errorf("foreign suggest: %d, want 404", rec.Code)
	}
	if rec := apiServe(t, d, alice, "POST", "/api/chats/ghost/suggest", nil); rec.Code != http.StatusNotFound {
		t.Errorf("ghost chat suggest: %d, want 404", rec.Code)
	}

	// Body cap (~1 KiB) and malformed JSON → 400.
	big := `{"junk":"` + strings.Repeat("x", 2048) + `"}`
	if rec := apiServe(t, d, alice, "POST", url, big); rec.Code != http.StatusBadRequest {
		t.Errorf("oversized body: %d, want 400", rec.Code)
	}
	if rec := apiServe(t, d, alice, "POST", url, "not json"); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body: %d, want 400", rec.Code)
	}
}

func TestAPI_SuggestNextUnavailable503(t *testing.T) {
	suggestURL := func(id string) string { return "/api/chats/" + id + "/suggest" }

	t.Run("no-task-model", func(t *testing.T) {
		prov := &suggestTestProv{text: "one?\ntwo?"}
		d, alice, _, c := suggestEnv(t, prov, "", true)
		if rec := apiServe(t, d, alice, "POST", suggestURL(c.ID), nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("no task model: %d, want 503", rec.Code)
		}
		if prov.callCount() != 0 {
			t.Errorf("upstream called %d times without a task model", prov.callCount())
		}
	})

	t.Run("upstream-error", func(t *testing.T) {
		// Upstream failure → 503 (typed, not 500).
		d, alice, _, c := suggestEnv(t, &suggestTestProv{err: errors.New("upstream 500")}, "p1:tm1", true)
		if rec := apiServe(t, d, alice, "POST", suggestURL(c.ID), nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("upstream error: %d, want 503", rec.Code)
		}
	})

	t.Run("too-few-lines", func(t *testing.T) {
		// Model returns a single usable line → 503 (ErrNoSuggestions).
		d, alice, _, c := suggestEnv(t, &suggestTestProv{text: "Just one question?"}, "p1:tm1", true)
		if rec := apiServe(t, d, alice, "POST", suggestURL(c.ID), nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("one-line answer: %d, want 503", rec.Code)
		}
	})

	t.Run("empty-chat", func(t *testing.T) {
		// Empty chat → 503 before any upstream call.
		prov := &suggestTestProv{text: "one?\ntwo?"}
		d, alice, _, c := suggestEnv(t, prov, "p1:tm1", false)
		if rec := apiServe(t, d, alice, "POST", suggestURL(c.ID), nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("empty chat: %d, want 503", rec.Code)
		}
		if prov.callCount() != 0 {
			t.Errorf("empty chat hit upstream %d times", prov.callCount())
		}
	})
}

// The per-user guard must refuse the second concurrent suggest with 429
// "already suggesting" WITHOUT reaching the task model, then release once
// the first request completes.
func TestAPI_SuggestNextBusy429(t *testing.T) {
	block := make(chan struct{}, 1)
	unblock := make(chan struct{})
	prov := &suggestTestProv{
		text:    "Q one?\nQ two?\nQ three?",
		block:   block,
		unblock: unblock,
	}
	d, alice, _, c := suggestEnv(t, prov, "p1:tm1", true)
	url := "/api/chats/" + c.ID + "/suggest"

	// The blocking call runs on a helper goroutine; apiServe(t) only uses
	// t.Helper on this path (no body ⇒ no t.Fatalf inside).
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- apiServe(t, d, alice, "POST", url, nil) }()

	select {
	case <-block:
	case <-time.After(5 * time.Second):
		t.Fatal("first suggest never reached the task model")
	}

	rec2 := apiServe(t, d, alice, "POST", url, nil)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrent suggest: %d, want 429", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "already suggesting") {
		t.Errorf("429 body = %s", rec2.Body.String())
	}

	close(unblock)
	var rec1 *httptest.ResponseRecorder
	select {
	case rec1 = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("first suggest never finished")
	}
	if rec1.Code != http.StatusOK {
		t.Fatalf("first suggest: %d %s", rec1.Code, rec1.Body.String())
	}

	// Guard released: the next suggest runs again.
	if rec := apiServe(t, d, alice, "POST", url, nil); rec.Code != http.StatusOK {
		t.Errorf("post-release suggest: %d, want 200", rec.Code)
	}
	// Exactly two upstream calls: the rejected one never reached the model.
	if n := prov.callCount(); n != 2 {
		t.Errorf("task model calls = %d, want 2 (429 must not dial upstream)", n)
	}
}
