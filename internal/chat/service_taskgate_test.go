// SECREV4 M2: task-model dispatch (title / summarize) must honor the admin
// kill switches — provider disabled, rotated-secret unreadable key, model row
// disabled — instead of silently bypassing them the way the send path refuses
// to. Policy (decided for this finding): a refusal falls back to the local
// title/summary and NEVER fails the user's turn, because these calls run
// after the turn is already complete. "Not cached" is allowed: the task model
// is pinned by id (onboarding set it), so an unsynced row must not disable a
// working task model.
package chat

import (
	"context"
	"testing"

	"chatgo/internal/providers"
	"chatgo/internal/store"
)

// seedTaskProvider adds a second provider (p2) with one model row (tm1); the
// task model is "p2:tm1".
func seedTaskProvider(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.Providers.Insert(ctx, &store.Provider{ID: "p2", Name: "task", BaseURL: "http://127.0.0.1:2", APIKey: "sk-task", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.Models.ReplaceForProvider(ctx, "p2",
		[]store.ModelRow{{ModelID: "tm1", Display: "TM1", ContextLen: 4096, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
}

// Task provider disabled → title falls back locally; the turn still completes
// and the refusal never reaches the user's event stream.
func TestTaskProviderDisabledFallsBackNotFails(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("pong"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 4096, "p2:tm1")
	seedTaskProvider(t, st)

	p2, err := st.Providers.Get(context.Background(), "p2")
	if err != nil {
		t.Fatal(err)
	}
	p2.Enabled = false
	if err := st.Providers.Update(context.Background(), p2); err != nil {
		t.Fatal(err)
	}

	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "prove gating"}, SendOpts{}))
	types := typesOf(evs)
	if last := types[len(types)-1]; last != EventDone {
		t.Fatalf("turn ended %s (%v), want done — gating must not fail the turn", last, types)
	}
	waitFor(t, func() bool {
		c, _ := st.Chats.Get(context.Background(), chatID)
		return c != nil && c.Title == fallbackTitle("prove gating")
	})
	if _, _, calls, _ := fc.snapshot(); calls != 0 {
		t.Fatalf("Complete called %d times for a disabled task provider, want 0", calls)
	}
}

// Admin disables the task model row itself → same fallback behavior.
func TestTaskModelRowDisabledFallsBack(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("pong"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 4096, "p2:tm1")
	seedTaskProvider(t, st)

	if err := st.Models.ReplaceForProvider(context.Background(), "p2",
		[]store.ModelRow{{ModelID: "tm1", Display: "TM1", ContextLen: 4096, Enabled: false}}); err != nil {
		t.Fatal(err)
	}

	collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "model off"}, SendOpts{}))
	waitFor(t, func() bool {
		c, _ := st.Chats.Get(context.Background(), chatID)
		return c != nil && c.Title == fallbackTitle("model off")
	})
	if _, _, calls, _ := fc.snapshot(); calls != 0 {
		t.Fatalf("Complete called %d times for a disabled task model row, want 0", calls)
	}
}

// A task model that has no cached row is NOT refused (pinned by id; rows
// follow provider syncs): the title still asks upstream.
func TestTaskModelUncachedStillDispatches(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("pong"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 4096, "p2:tm1")
	seedTaskProvider(t, st)
	if err := st.Models.ReplaceForProvider(context.Background(), "p2", nil); err != nil {
		t.Fatal(err)
	}

	collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "uncached ok"}, SendOpts{}))
	waitFor(t, func() bool {
		c, _ := st.Chats.Get(context.Background(), chatID)
		return c != nil && c.Title == "Greet Helper Bot"
	})
	if _, _, calls, _ := fc.snapshot(); calls == 0 {
		t.Fatal("uncached task model was refused; want it to pass through")
	}
}

// KeyUnreadable (rotated SESSION_SECRET) on the task provider → the completion
// is refused pre-dial (no plaintext key exists to send) and the title falls
// back. Re-sealed rows keep working.
func TestTaskProviderKeyUnreadableFallsBack(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("pong"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 4096, "p2:tm1")
	seedTaskProvider(t, st)

	// Reopen the SAME shared-cache DB with a foreign secret and point the
	// service at it: provider rows now read KeyUnreadable.
	st2, err := store.Open("file:memchat_"+t.Name()+"?mode=memory&cache=shared",
		[]byte("ffffffffffffffffffffffffffffffff"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })
	svc.Store = st2

	collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "secret churn"}, SendOpts{}))
	waitFor(t, func() bool {
		c, _ := st2.Chats.Get(context.Background(), chatID)
		return c != nil && c.Title == fallbackTitle("secret churn")
	})
	if _, _, calls, _ := fc.snapshot(); calls != 0 {
		t.Fatalf("Complete called %d times with an unreadable task key, want 0", calls)
	}
}
