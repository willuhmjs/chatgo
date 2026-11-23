// FTS search + pinned-list tests (BE-apichat owned). Prefix: TestFTS.

package store

import (
	"context"
	"strings"
	"testing"
)

func TestFTSSearchChatsScopingAndOrder(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	alice := mustUser(t, s, "fs-a@example.com", RoleUser)
	bob := mustUser(t, s, "fs-b@example.com", RoleUser)

	a1 := &Chat{UserID: alice.ID, Title: "first"}
	a2 := &Chat{UserID: alice.ID, Title: "second"}
	b1 := &Chat{UserID: bob.ID, Title: "bobs"}
	for _, c := range []*Chat{a1, a2, b1} {
		if err := s.Chats.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	add := func(chatID, content string, at int64) {
		m := &Message{ChatID: chatID, Role: "user", Content: content, CreatedAt: at}
		if err := s.Messages.Append(ctx, m); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	add(a1.ID, "pineapple pizza debate", 100)
	add(a2.ID, "another pineapple reference", 200) // newer match → first
	add(b1.ID, "pineapple on everything", 300)     // other user

	chats, total, err := s.Chats.SearchChats(ctx, alice.ID, "pineapple", 1, 10)
	if err != nil {
		t.Fatalf("SearchChats: %v", err)
	}
	if total != 2 || len(chats) != 2 {
		t.Fatalf("alice total=%d len=%d, want 2/2", total, len(chats))
	}
	if chats[0].ID != a2.ID || chats[1].ID != a1.ID {
		t.Errorf("want newest-match-first, got %s %s", chats[0].ID, chats[1].ID)
	}

	// Owner scoping: bob only sees his own hit.
	chats, total, err = s.Chats.SearchChats(ctx, bob.ID, "pineapple", 1, 10)
	if err != nil || total != 1 || chats[0].ID != b1.ID {
		t.Fatalf("bob scope broken: %d %+v %v", total, chats, err)
	}

	// Multi-hit chat appears exactly once (distinct chats).
	add(a1.ID, "pineapple again", 400)
	chats, total, _ = s.Chats.SearchChats(ctx, alice.ID, "pineapple", 1, 10)
	if total != 2 || len(chats) != 2 {
		t.Errorf("distinct chats broken: total=%d len=%d", total, len(chats))
	}
	// …and a1 is now the newest-match chat.
	if chats[0].ID != a1.ID {
		t.Errorf("recount order wrong: %s", chats[0].ID)
	}

	// Soft-deleted chats drop out.
	if err := s.Chats.Delete(ctx, a2.ID); err != nil {
		t.Fatal(err)
	}
	_, total, _ = s.Chats.SearchChats(ctx, alice.ID, "pineapple", 1, 10)
	if total != 1 {
		t.Errorf("deleted chat still matched: total=%d", total)
	}

	// Pagination: per=1 → one hit on page 1, nothing on page 2.
	p1, total, _ := s.Chats.SearchChats(ctx, alice.ID, "pineapple", 1, 1)
	p2, _, _ := s.Chats.SearchChats(ctx, alice.ID, "pineapple", 2, 1)
	if total != 1 || len(p1) != 1 || len(p2) != 0 {
		t.Errorf("pagination: total=%d p1=%d p2=%d", total, len(p1), len(p2))
	}

	// Empty query → no results, no error.
	if chats, total, err := s.Chats.SearchChats(ctx, alice.ID, "   ", 1, 10); err != nil || total != 0 || len(chats) != 0 {
		t.Errorf("blank q: %d %v", total, err)
	}
}

func TestFTSQuotingSafety(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := mustUser(t, s, "inj@example.com", RoleUser)

	evil := []string{
		`"; DROP TABLE messages;--`,
		`"unterminated`,
		`AND OR NEAR NOT`,
		`pine*"apple"`,
		`*`,
		`"quoted "nested" term`,
		"htab\tnewline\nnul\x00byte",
	}
	for i, q := range evil {
		chats, total, err := s.Chats.SearchChats(ctx, u.ID, q, 1, 10)
		if err != nil {
			t.Errorf("evil query %d %q: err = %v", i, q, err)
		}
		if total != 0 || len(chats) != 0 {
			t.Errorf("evil query %d %q matched %d rows", i, q, total)
		}
	}

	// The library survived: schema intact and real search still works.
	m := &Message{ChatID: func() string {
		c := &Chat{UserID: u.ID, Title: "keeper"}
		if err := s.Chats.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}(), Role: "user", Content: `the "pineapple" incident`}
	if err := s.Messages.Append(ctx, m); err != nil {
		t.Fatalf("Append (messages table gone?): %v", err)
	}
	chats, total, err := s.Chats.SearchChats(ctx, u.ID, `"; DROP TABLE messages;--`, 1, 10)
	if err != nil || total != 0 || len(chats) != 0 {
		t.Errorf("SQLi probe should be inert: %d %v", total, err)
	}
	chats, total, err = s.Chats.SearchChats(ctx, u.ID, `pineapple" incident`, 1, 10)
	if err != nil {
		t.Fatalf("phrase search: %v", err)
	}
	if total != 1 || len(chats) != 1 {
		t.Errorf("literal-quote phrase search: total=%d", total)
	}
}

func TestFTSPhraseHelper(t *testing.T) {
	if got := ftsPhrase("plain"); got != `"plain"` {
		t.Errorf("ftsPhrase(plain) = %s", got)
	}
	if got := ftsPhrase(`he said "hi"`); got != `"he said ""hi"""` {
		t.Errorf("ftsPhrase quotes = %s", got)
	}
	if got := ftsPhrase("  "); got != "" {
		t.Errorf("ftsPhrase(blank) = %q", got)
	}
	if strings.ContainsRune(ftsPhrase("a\x00b\x1fc"), '\x00') {
		t.Error("control chars not neutralized")
	}
}

func TestFTSListPinned(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := mustUser(t, s, "pin@example.com", RoleUser)

	pin := &Chat{UserID: u.ID, Title: "pinned", Pinned: true, UpdatedAt: 5}
	plain := &Chat{UserID: u.ID, Title: "plain", UpdatedAt: 9}
	other := &Chat{UserID: u.ID, Title: "gone", Pinned: true}
	folder := &Folder{UserID: u.ID, Name: "F"}
	if err := s.Folders.Create(ctx, folder); err != nil {
		t.Fatal(err)
	}
	inFolder := &Chat{UserID: u.ID, Title: "filed-pin", Pinned: true, FolderID: &folder.ID}
	for _, c := range []*Chat{pin, plain, other, inFolder} {
		if err := s.Chats.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Chats.Delete(ctx, other.ID); err != nil {
		t.Fatal(err)
	}

	pinned, total, err := s.Chats.ListPinned(ctx, u.ID, ChatListOptions{Page: 1, Per: 10})
	if err != nil {
		t.Fatalf("ListPinned: %v", err)
	}
	if total != 2 || len(pinned) != 2 { // 'gone' is soft-deleted
		t.Fatalf("pinned total=%d len=%d, want 2/2", total, len(pinned))
	}

	inFolderOnly, total, err := s.Chats.ListPinned(ctx, u.ID, ChatListOptions{FolderID: folder.ID})
	if err != nil || total != 1 || inFolderOnly[0].ID != inFolder.ID {
		t.Errorf("folder filter: total=%d %v", total, err)
	}

	tag := &Tag{UserID: u.ID, Name: "pinned-tag"}
	if err := s.Tags.Create(ctx, tag); err != nil {
		t.Fatal(err)
	}
	if err := s.Tags.SetChatTags(ctx, inFolder.ID, u.ID, []string{tag.ID}); err != nil {
		t.Fatal(err)
	}
	byTag, total, err := s.Chats.ListPinned(ctx, u.ID, ChatListOptions{TagID: tag.ID})
	if err != nil || total != 1 || byTag[0].ID != inFolder.ID {
		t.Errorf("tag filter: total=%d %v", total, err)
	}
}
