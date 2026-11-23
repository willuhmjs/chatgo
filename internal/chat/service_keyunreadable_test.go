// The lost/rotated SESSION_SECRET incident, pinned at the send boundary:
// sealed provider keys become undecryptable, and silently firing upstream
// with an empty key blamed the provider for its 401 ("invalid API key")
// while the real fault sat in our own at-rest layer. The rows survive
// (KeyUnreadable) so admin can re-enter the key; chat/task-model paths must
// refuse with the honest message instead.
package chat

import (
	"context"
	"strings"
	"testing"

	"chatgo/internal/providers"
	"chatgo/internal/store"
)

// rotateSecret reopens the SAME shared-cache DB under a different secret —
// byte-identical to a server restart after SESSION_SECRET rotation.
func rotateSecret(t *testing.T, svc *Service, name string) {
	t.Helper()
	st2, err := store.Open(name, []byte("ffffffffffffffffffffffffffffffff"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { st2.Close() })
	svc.Store = st2
}

func TestSendRefusesUnreadableProviderKey(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("pong"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 4096, "")
	ctx := context.Background()

	// Sanity: the key reads fine pre-rotation and the chat streams.
	if p, err := st.Providers.Get(ctx, "p1"); err != nil || p.KeyUnreadable {
		t.Fatalf("provider key readable pre-rotation: %+v err=%v", p, err)
	}
	name := "file:memchat_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	rotateSecret(t, svc, name)

	p, err := svc.Store.Providers.Get(ctx, "p1")
	if err != nil || !p.KeyUnreadable || p.APIKey != "" {
		t.Fatalf("post-rotation provider: unreadable=%v key=%q err=%v", p.KeyUnreadable, p.APIKey, err)
	}

	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "still there?"}, SendOpts{}))
	wantModelRefused(t, fc, evs, errMsgKeyUnreadable, 0)
}
