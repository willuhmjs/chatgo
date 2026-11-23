// v1 → v2 upgrade regression (message branching). The live rollout proved the
// bug class: schema.sql must NOT reference columns the v2 step has not added
// yet on pre-existing databases (CREATE TABLE IF NOT EXISTS is a no-op there,
// and CREATE INDEX on messages(parent_id) failed with "no such column").
// This test builds a byte-faithful v1 database and runs the real Open path.

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

const v1Schema = `
CREATE TABLE users (
	id TEXT PRIMARY KEY,
	email TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL DEFAULT 'user',
	status TEXT NOT NULL DEFAULT 'active',
	created_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE folders (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	created_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE chats (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	folder_id TEXT REFERENCES folders(id) ON DELETE SET NULL,
	title TEXT NOT NULL DEFAULT 'New chat',
	provider_id TEXT NOT NULL DEFAULT '',
	model_id TEXT NOT NULL DEFAULT '',
	params_json TEXT NOT NULL DEFAULT '{}',
	pinned INTEGER NOT NULL DEFAULT 0,
	share_token TEXT UNIQUE,
	deleted_at INTEGER,
	created_at INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS messages (
	id TEXT PRIMARY KEY,
	chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
	role TEXT NOT NULL CHECK (role IN ('user','assistant','system')),
	content TEXT NOT NULL DEFAULT '',
	sources_json TEXT NOT NULL DEFAULT '',
	error TEXT,
	tokens_in INTEGER NOT NULL DEFAULT 0,
	tokens_out INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages(chat_id);
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5 (
	content,
	content='messages',
	content_rowid='rowid'
);
CREATE TRIGGER IF NOT EXISTS messages_fts_ai AFTER INSERT ON messages BEGIN
	INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
END;
CREATE TRIGGER IF NOT EXISTS messages_fts_ad AFTER DELETE ON messages BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.rowid, old.content);
END;
CREATE TRIGGER IF NOT EXISTS messages_fts_au AFTER UPDATE ON messages BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.rowid, old.content);
	INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
END;
PRAGMA user_version = 1;
`

func TestMigrateV1ToV2UpgradePath(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chatgo.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(v1Schema); err != nil {
		t.Fatalf("seed v1 schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('u1', 'u1@x.dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO chats (id, user_id) VALUES ('c1', 'u1')`); err != nil {
		t.Fatal(err)
	}
	for i, role := range []string{"user", "assistant", "user"} {
		if _, err := db.Exec(`INSERT INTO messages (id, chat_id, role, content, created_at) VALUES (?1, 'c1', ?2, ?3, ?4)`,
			string(rune('a'+i)), role, "msg", i); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// The real production entry point: Open runs schema.sql + version steps.
	st, err := Open(path, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("open upgraded db: %v", err)
	}
	defer st.Close()

	// The linear chain must be backfilled: a←b←c, leaf at c.
	msgs, err := st.Messages.List(ctx, "c1", 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 || msgs[0].ParentID != "" || msgs[1].ParentID != msgs[0].ID || msgs[2].ParentID != msgs[1].ID {
		t.Fatalf("parents not backfilled: %+v", msgs)
	}
	c, err := st.Chats.Get(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if c.LeafID != msgs[2].ID {
		t.Fatalf("leaf = %q, want %q", c.LeafID, msgs[2].ID)
	}
	// The parent index exists (EXPLAIN resolves it; a missing column/index errors).
	var got string
	if err := st.DB.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='index' AND name='idx_messages_chat_parent')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "1" {
		t.Fatal("idx_messages_chat_parent missing after migration")
	}
	// Branching writes work on the upgraded schema.
	m := &Message{ChatID: "c1", Role: "assistant", Content: "v2 sibling", ParentID: msgs[1].ID}
	if err := st.Messages.Append(ctx, m); err != nil {
		t.Fatalf("append on upgraded schema: %v", err)
	}
	path2, err := st.Messages.Path(ctx, "c1", m.ID)
	if err != nil || len(path2) != 3 || path2[2].ID != m.ID {
		t.Fatalf("path after sibling append: len=%d err=%v", len(path2), err)
	}
	// And the version landed where it should.
	var v int
	if err := st.DB.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != schemaVersion {
		t.Fatalf("user_version=%d err=%v, want %d", v, err, schemaVersion)
	}
}
