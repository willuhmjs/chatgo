// SECREV-3 S2-1: the message SEND path must enforce the admin per-model
// enabled flag, not just the provider row (pre-existing chats used to keep
// streaming a model an admin had killed — service.go's own comment claimed
// immediacy the code did not deliver). Policy mirrors validateChatModel in
// internal/apichat/chats.go exactly so create/switch/send agree:
//   - no model on the chat  → no check (create accepts provider-only chats,
//     including providers with zero cached rows);
//   - cached row enabled=0   → refuse with the disabled message;
//   - no cached row at all   → refuse (create answers this 404 "model not
//     found", zero-row providers included — a vanished model never reaches
//     upstream on trust).
package chat

import (
	"context"
	"testing"

	"chatgo/internal/providers"
	"chatgo/internal/store"
)

// disableModel rewrites p1's cached catalog the way
// PUT /api/providers/{id}/models does (whole-set replace), toggling m1.
func disableModel(t *testing.T, st *store.Store, enabled bool) {
	t.Helper()
	err := st.Models.ReplaceForProvider(context.Background(), "p1",
		[]store.ModelRow{{ModelID: "m1", Display: "M1", ContextLen: 4096, Enabled: enabled}})
	if err != nil {
		t.Fatal(err)
	}
}

// wantModelRefused asserts a send was refused pre-upstream exactly like the
// provider-disabled flow: meta…error, the expected user-visible message,
// NO new StreamChat hit beyond wantCalls, and only the user turn persisted.
func wantModelRefused(t *testing.T, fc *fakeClient, evs []Event, wantMsg string, wantCalls int) {
	t.Helper()
	types := typesOf(evs)
	if types[0] != EventMeta || types[len(types)-1] != EventError {
		t.Fatalf("events = %v, want meta…error", types)
	}
	if msg := evs[len(evs)-1].Data.(ErrorData).Message; msg != wantMsg {
		t.Fatalf("error message = %q, want %q", msg, wantMsg)
	}
	for _, e := range evs {
		if e.Type == EventToken || e.Type == EventDone {
			t.Fatalf("refused turn produced %s event (%v)", e.Type, types)
		}
	}
	if _, calls, _, _ := fc.snapshot(); calls != wantCalls {
		t.Fatalf("refused model reached upstream (%d StreamChat calls, want %d)", calls, wantCalls)
	}
}

// PoC of the finding, end to end: chat exists on an enabled model → admin
// PUTs models with enabled=false → send on the SAME chat is refused with an
// error event and no upstream hit → re-enable → the same chat streams again.
func TestSendRejectsAdminDisabledModelAndRecoversOnReEnable(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("pong"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 4096, "")

	disableModel(t, st, false)
	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "proof"}, SendOpts{}))
	wantModelRefused(t, fc, evs, errMsgModelDisabled, 0)

	// Same error-flow as other pre-upstream failures: the user turn persists.
	msgs, err := st.Messages.List(context.Background(), chatID, 500, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("messages = %+v, want only the user turn", msgs)
	}

	// Admin re-enables: the very next send on the same chat works again.
	disableModel(t, st, true)
	evs = collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "again?"}, SendOpts{}))
	if last := typesOf(evs)[len(evs)-1]; last != EventDone {
		t.Fatalf("re-enabled send ended with %s (%v)", last, typesOf(evs))
	}
	if _, calls, _, _ := fc.snapshot(); calls != 1 {
		t.Fatalf("StreamChat calls = %d, want exactly the one re-enabled turn", calls)
	}
}

// Create-parity on the vanished-row verdict: validateChatModel answers a
// model that is not cached for the provider with 404 "model not found" —
// whether other rows exist (sync deleted this one) or the provider has zero
// cached rows at all. Send must agree, so a model removed upstream by a
// later sync stops answering pre-existing chats instead of passing through.
func TestSendRefusesVanishedModelMatchingCreateVerdict(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("ghost"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 4096, "")
	ctx := context.Background()

	// Row deleted from a synced provider (other models remain).
	if err := st.Models.ReplaceForProvider(ctx, "p1",
		[]store.ModelRow{{ModelID: "other", Display: "Other", ContextLen: 4096, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "hi"}, SendOpts{}))
	wantModelRefused(t, fc, evs, errMsgModelGone, 0)

	// Zero-row provider (sync listed nothing): same refusal — the same chat
	// could no longer be created or switched-to under create's 404 either.
	if err := st.Models.ReplaceForProvider(ctx, "p1", nil); err != nil {
		t.Fatal(err)
	}
	evs = collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "hi"}, SendOpts{}))
	wantModelRefused(t, fc, evs, errMsgModelGone, 0)
}

// The create-path accepts exactly two no-cached-row shapes, and send must
// keep working for both: a chat with no model on a synced provider, and a
// provider-only chat on a provider that was never synced (zero rows). The
// model check simply does not apply when the chat pins no model.
func TestSendAllowsUnpinnedModelChatsCreateAlsoAllows(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("ok"), stopChunk(1, 1))}
	svc, st, _ := newEnv(t, fc, 4096, "")
	ctx := context.Background()

	if err := st.Providers.Insert(ctx, &store.Provider{
		ID: "p2", Name: "unsynced", BaseURL: "http://127.0.0.1:1", APIKey: "sk-t", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	chats := []*store.Chat{
		{ID: "c-nomodel", UserID: "u1", ProviderID: "p1", ModelID: ""},
		{ID: "c-unsynced", UserID: "u1", ProviderID: "p2", ModelID: ""},
	}
	for _, c := range chats {
		if err := st.Chats.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
		evs := collect(t, mustSend(t, svc, "u1", c.ID, providers.ChatMessage{Content: "hi"}, SendOpts{}))
		if last := typesOf(evs)[len(evs)-1]; last != EventDone {
			t.Fatalf("provider-only chat %s ended with %s (%v)", c.ID, last, typesOf(evs))
		}
	}
	if _, calls, _, _ := fc.snapshot(); calls != 2 {
		t.Fatalf("StreamChat calls = %d, want 2", calls)
	}

	// …while the SAME unsynced provider still refuses a chat that DOES pin
	// a model it never cached (create would 404 that pair; c-unsynced proves
	// zero-row providers themselves keep working).
	if err := st.Chats.Create(ctx,
		&store.Chat{ID: "c-ghost", UserID: "u1", ProviderID: "p2", ModelID: "never-synced"}); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, mustSend(t, svc, "u1", "c-ghost", providers.ChatMessage{Content: "hi"}, SendOpts{}))
	wantModelRefused(t, fc, evs, errMsgModelGone, 2) // calls must stay at the two good turns
}

// Open WebUI-style per-model access control (models.min_role=='admin'): an
// admin-gated model is cached+enabled, so the enabled check passes — the send
// path must additionally refuse non-admin chat owners and let admins through
// (including a chat that already existed when the gate was set). The verdict
// is per-owner: promoting the owner reopens the very same chat.
func TestSendEnforcesAdminOnlyModelPerOwner(t *testing.T) {
	fc := &fakeClient{chunks: append(sayChunks("ok"), stopChunk(1, 1))}
	svc, st, chatID := newEnv(t, fc, 4096, "")
	ctx := context.Background()

	gate := func(role string) {
		if err := st.Models.ReplaceForProvider(ctx, "p1", []store.ModelRow{
			{ModelID: "m1", Display: "M1", ContextLen: 4096, Enabled: true, MinRole: role},
		}); err != nil {
			t.Fatal(err)
		}
	}

	gate("admin")
	evs := collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "hi"}, SendOpts{}))
	wantModelRefused(t, fc, evs, errMsgModelRestricted, 0)

	// Owner promoted → same chat streams again.
	if err := st.Users.UpdateRole(ctx, "u1", store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	evs = collect(t, mustSend(t, svc, "u1", chatID, providers.ChatMessage{Content: "hi"}, SendOpts{}))
	if last := typesOf(evs)[len(evs)-1]; last != EventDone {
		t.Fatalf("admin-owner send ended with %s (%v)", last, typesOf(evs))
	}

	// Gate cleared again → normal users stream fine (no stray refusal left).
	gate("")
	if err := st.Chats.Create(ctx, &store.Chat{ID: "c-u2", UserID: "u2", ProviderID: "p1", ModelID: "m1"}); err != nil {
		t.Fatal(err)
	}
	evs = collect(t, mustSend(t, svc, "u2", "c-u2", providers.ChatMessage{Content: "hi"}, SendOpts{}))
	if last := typesOf(evs)[len(evs)-1]; last != EventDone {
		t.Fatalf("ungated send ended with %s (%v)", last, typesOf(evs))
	}
}
