package store

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	// Named in-memory DB per test (shared cache) — isolates tests from each
	// other even though :memory: dbs with cache=shared share one process-wide
	// instance.
	s, err := Open("file:mem_"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared",
		testSecret())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustUser(t *testing.T, s *Store, email, role string) *User {
	t.Helper()
	u := &User{Email: email, Name: "Test " + email, Role: role}
	if err := s.Users.Insert(context.Background(), u); err != nil {
		t.Fatalf("Users.Insert: %v", err)
	}
	return u
}

// TestMigratePragmasAndTables verifies schema + FTS objects and file-backed pragmas.
func TestMigratePragmasAndTables(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	want := []string{"users", "sessions", "settings", "providers", "models", "chats",
		"messages", "message_images", "folders", "tags", "chat_tags", "files",
		"api_tokens", "summaries", "messages_fts"}
	for _, tbl := range want {
		var name string
		err := s.DB.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE name = ?1`, tbl).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", tbl, err)
		}
	}
	// triggers exist
	var n int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'messages_fts_%'`).Scan(&n); err != nil || n != 3 {
		t.Errorf("expected 3 fts triggers, got %d (err %v)", n, err)
	}

	// File-backed DB must come up in WAL with busy_timeout + foreign_keys.
	t.Run("pragmas", func(t *testing.T) {
		dir := t.TempDir()
		fs, err := Open(dir+"/chatgo.db", []byte("0123456789abcdef0123456789abcdef"))
		if err != nil {
			t.Fatalf("Open file: %v", err)
		}
		defer fs.Close()
		var mode string
		if err := fs.DB.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
			t.Errorf("journal_mode = %q, want wal (err %v)", mode, err)
		}
		var bt int
		if err := fs.DB.QueryRow(`PRAGMA busy_timeout`).Scan(&bt); err != nil || bt != 5000 {
			t.Errorf("busy_timeout = %d, want 5000 (err %v)", bt, err)
		}
		var fk int
		if err := fs.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
			t.Errorf("foreign_keys = %d, want 1 (err %v)", fk, err)
		}
	})
}

func TestUsersCRUD(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	u := mustUser(t, s, "Alice@Example.com", RoleUser)
	if u.ID == "" {
		t.Fatal("expected generated id")
	}

	got, err := s.Users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Role != RoleUser {
		t.Errorf("role = %q", got.Role)
	}
	// email lookup is case-insensitive (COLLATE NOCASE)
	if _, err := s.Users.GetByEmail(ctx, "alice@example.com"); err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}

	if err := s.Users.SetPassword(ctx, u.ID, "$2a$12$hash"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	got, _ = s.Users.GetByID(ctx, u.ID)
	if !got.HasPassword() {
		t.Error("password hash not stored")
	}

	pending := mustUser(t, s, "bob@example.com", RolePending)
	pend, err := s.Users.ListPending(ctx)
	if err != nil || len(pend) != 1 || pend[0].ID != pending.ID {
		t.Fatalf("ListPending = %v, %v", pend, err)
	}
	if err := s.Users.UpdateRole(ctx, pending.ID, RoleAdmin); err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if pend, _ := s.Users.ListPending(ctx); len(pend) != 0 {
		t.Errorf("still pending after role update")
	}
	all, err := s.Users.List(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("List = %d users, err %v", len(all), err)
	}
	if err := s.Users.Touch(ctx, u.ID, 1234); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, _ = s.Users.GetByID(ctx, u.ID)
	if got.LastActiveAt != 1234 {
		t.Errorf("LastActiveAt = %d", got.LastActiveAt)
	}
	if _, err := s.Users.GetByID(ctx, "nope"); err != ErrNotFound {
		t.Errorf("GetByID(missing) err = %v, want ErrNotFound", err)
	}
}

func TestProviderKeyRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	p := &Provider{Name: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-secret-123", Enabled: true}
	if err := s.Providers.Insert(ctx, p); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := s.Providers.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.APIKey != "sk-secret-123" {
		t.Errorf("APIKey = %q, want round-trip", got.APIKey)
	}

	// Stored blob must NOT contain the plaintext key.
	var raw []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT api_key_enc FROM providers WHERE id = ?1`, p.ID).Scan(&raw); err != nil {
		t.Fatalf("raw select: %v", err)
	}
	if len(raw) == 0 || bytes.Contains(raw, []byte("sk-secret-123")) {
		t.Fatal("api_key_enc missing or plaintext")
	}

	// Update round-trip + clear.
	got.Name = "renamed"
	got.APIKey = "sk-new"
	if err := s.Providers.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = s.Providers.Get(ctx, p.ID)
	if got.APIKey != "sk-new" || got.Name != "renamed" {
		t.Errorf("after update: key=%q name=%q", got.APIKey, got.Name)
	}
	got.APIKey = ""
	if err := s.Providers.Update(ctx, got); err != nil {
		t.Fatalf("Update clear: %v", err)
	}
	got, _ = s.Providers.Get(ctx, p.ID)
	if got.APIKey != "" {
		t.Errorf("cleared key = %q", got.APIKey)
	}

	// A store with a different secret must fail to decrypt.
	s2, err := Open("file:mem_wrongkey?mode=memory&cache=shared", []byte("ffffffffffffffffffffffffffffffff"))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.Providers.Insert(ctx, p); err != nil {
		t.Fatal(err)
	}
	// A store with a different secret must NOT fail Get: the row comes back
	// with an empty key and KeyUnreadable so the admin UI stays operable.
	if _, err := s2.DB.Exec(`UPDATE providers SET api_key_enc = ?1 WHERE id = ?2`, raw, p.ID); err != nil {
		t.Fatal(err)
	}
	// copy sealed blob (still holding the sk-secret-123 ciphertext from the
	// initial Insert) into the second store's row and read:
	bad, err := s2.Providers.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get with rotated secret should not error, got %v", err)
	}
	if bad.APIKey != "" || !bad.KeyUnreadable {
		t.Errorf("unreadable row = key %q flag %v, want \"\" + true", bad.APIKey, bad.KeyUnreadable)
	}
	// List keeps working and surfaces the flag; the broken row is deletable.
	list, err := s2.Providers.List(ctx)
	if err != nil || len(list) != 1 || !list[0].KeyUnreadable {
		t.Errorf("List = %+v, %v", list, err)
	}
	if err := s2.Providers.Delete(ctx, p.ID); err != nil {
		t.Errorf("Delete unreadable row: %v", err)
	}

	if err := s.Providers.Delete(ctx, p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Providers.Get(ctx, p.ID); err != ErrNotFound {
		t.Errorf("Get(deleted) err = %v", err)
	}
}

func TestChatListPaginationAndSoftDelete(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := mustUser(t, s, "chatter@example.com", RoleUser)

	for i := 0; i < 5; i++ {
		c := &Chat{UserID: u.ID, Title: "chat", UpdatedAt: int64(1000 + i)}
		if err := s.Chats.Create(ctx, c); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	// Pin one to test ordering.
	first, _, err := s.Chats.List(ctx, u.ID, ChatListOptions{Page: 1, Per: 10})
	if err != nil {
		t.Fatal(err)
	}

	page1, total, err := s.Chats.List(ctx, u.ID, ChatListOptions{Page: 1, Per: 2})
	if err != nil || total != 5 || len(page1) != 2 {
		t.Fatalf("page1: len=%d total=%d err=%v", len(page1), total, err)
	}
	page3, total, err := s.Chats.List(ctx, u.ID, ChatListOptions{Page: 3, Per: 2})
	if err != nil || total != 5 || len(page3) != 1 {
		t.Fatalf("page3: len=%d total=%d err=%v", len(page3), total, err)
	}
	if page1[0].UpdatedAt <= page3[0].UpdatedAt {
		t.Error("expected updated_at desc")
	}
	_ = first

	// Soft delete: hidden from List but row remains.
	if err := s.Chats.Delete(ctx, page1[0].ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, total, err = s.Chats.List(ctx, u.ID, ChatListOptions{Page: 1, Per: 10})
	if err != nil || total != 4 {
		t.Fatalf("after soft delete total = %d, want 4 (err %v)", total, err)
	}
	if _, err := s.Chats.Get(ctx, page1[0].ID); err != ErrNotFound {
		t.Errorf("Get(soft-deleted) = %v, want ErrNotFound", err)
	}
	var alive int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM chats WHERE id = ?1`, page1[0].ID).Scan(&alive); err != nil || alive != 1 {
		t.Errorf("soft-deleted row should still exist (found %d, err %v)", alive, err)
	}

	// Share token round-trip.
	if err := s.Chats.SetShare(ctx, page1[1].ID, "tok123"); err != nil {
		t.Fatal(err)
	}
	if c, err := s.Chats.GetByShare(ctx, "tok123"); err != nil || c.ID != page1[1].ID {
		t.Errorf("GetByShare = %v, %v", c, err)
	}
	// q filter (title)
	list, _, err := s.Chats.List(ctx, u.ID, ChatListOptions{Q: "chat", Page: 1, Per: 10})
	if err != nil || len(list) != 4 {
		t.Errorf("q filter = %d, want 4 (err %v)", len(list), err)
	}
}

func TestMessagesAppendCursorDeleteFromAndFTS(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := mustUser(t, s, "msgs@example.com", RoleUser)
	c := &Chat{UserID: u.ID, Title: "c"}
	if err := s.Chats.Create(ctx, c); err != nil {
		t.Fatal(err)
	}

	texts := []string{"hello world", "bananas are yellow", "the end"}
	ids := make([]string, 0, len(texts))
	for _, txt := range texts {
		m := &Message{ChatID: c.ID, Role: "user", Content: txt}
		if err := s.Messages.Append(ctx, m); err != nil {
			t.Fatalf("Append: %v", err)
		}
		ids = append(ids, m.ID)
	}

	ftsCount := func(term string) int {
		var n int
		if err := s.DB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH ?1`, term).Scan(&n); err != nil {
			t.Fatalf("fts match %q: %v", term, err)
		}
		return n
	}
	if n := ftsCount("bananas"); n != 1 {
		t.Fatalf("FTS should find 'bananas' once, got %d", n)
	}

	msgs, err := s.Messages.List(ctx, c.ID, 10, "")
	if err != nil || len(msgs) != 3 {
		t.Fatalf("List = %d, err %v", len(msgs), err)
	}
	if msgs[0].Content != "hello world" || msgs[2].Content != "the end" {
		t.Errorf("wrong order: %s … %s", msgs[0].Content, msgs[2].Content)
	}
	// before-cursor: everything strictly before the 2nd message
	page, err := s.Messages.List(ctx, c.ID, 10, ids[1])
	if err != nil || len(page) != 1 || page[0].ID != ids[0] {
		t.Fatalf("before-cursor List = %+v, err %v", page, err)
	}
	// limit + reverse-order check
	page, _ = s.Messages.List(ctx, c.ID, 2, "")
	if len(page) != 2 || page[0].Content != "bananas are yellow" {
		t.Errorf("limit page wrong: %+v", page)
	}
	if n, err := s.Messages.Count(ctx, c.ID); err != nil || n != 3 {
		t.Errorf("Count = %d, err %v", n, err)
	}

	// DeleteFrom removes msg2 + msg3 (regenerate semantics) and FTS follows.
	if err := s.Messages.DeleteFrom(ctx, c.ID, ids[1]); err != nil {
		t.Fatalf("DeleteFrom: %v", err)
	}
	if n, _ := s.Messages.Count(ctx, c.ID); n != 1 {
		t.Errorf("after DeleteFrom count = %d, want 1", n)
	}
	if n := ftsCount("bananas"); n != 0 {
		t.Errorf("FTS still contains 'bananas' after delete (%d)", n)
	}
	if n := ftsCount("hello"); n != 1 {
		t.Errorf("FTS lost unrelated row: %d", n)
	}
	if err := s.Messages.DeleteFrom(ctx, c.ID, "missing"); err != ErrNotFound {
		t.Errorf("DeleteFrom(missing) = %v, want ErrNotFound", err)
	}

	// FTS tracks UPDATE via the au trigger.
	if _, err := s.DB.ExecContext(ctx, `UPDATE messages SET content = ?1 WHERE id = ?2`, "changed to kiwi", ids[0]); err != nil {
		t.Fatal(err)
	}
	if n := ftsCount("kiwi"); n != 1 {
		t.Errorf("after update FTS 'kiwi' = %d, want 1", n)
	}
	if n := ftsCount("hello"); n != 0 {
		t.Errorf("after update FTS 'hello' = %d, want 0", n)
	}
	var _ *sql.DB
}

func TestSettingsAndSessions(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := mustUser(t, s, "s@example.com", RoleUser)

	v, err := s.Settings.Get(ctx, "theme")
	if err != nil || v != nil {
		t.Fatalf("Get(missing) = %v, %v", v, err)
	}
	if err := s.Settings.Set(ctx, "theme", "dark"); err != nil {
		t.Fatal(err)
	}
	if err := s.Settings.Set(ctx, "theme", "light"); err != nil {
		t.Fatal(err) // upsert
	}
	v, _ = s.Settings.Get(ctx, "theme")
	if v == nil || *v != "light" {
		t.Errorf("Get = %v", v)
	}
	all, _ := s.Settings.All(ctx)
	if all["theme"] != "light" {
		t.Errorf("All = %v", all)
	}

	sess := &Session{Token: "tok", UserID: u.ID, CreatedAt: 1, ExpiresAt: 100, UserAgent: "ua", IP: "::1"}
	if err := s.Sessions.Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.Sessions.Get(ctx, "tok", 50)
	if err != nil || got.UserID != u.ID {
		t.Fatalf("Sessions.Get = %v, %v", got, err)
	}
	if _, err := s.Sessions.Get(ctx, "tok", 200); err != ErrNotFound {
		t.Error("expired session should not be returned")
	}
	if err := s.Sessions.Touch(ctx, "tok", 60); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Sessions.DeleteExpired(ctx, 150); err != nil || n != 1 {
		t.Errorf("DeleteExpired = %d, %v", n, err)
	}
}
