package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

// schemaSQL is the single source of truth for the DB schema
// (docs/CONTRACTS.md §DB schema). Idempotent: IF NOT EXISTS everywhere.
//
//go:embed schema.sql
var schemaSQL string

// schemaVersion is the user_version this build migrates to. Steps are
// idempotent so re-running on a current DB is a no-op.
const schemaVersion = 4

// migrate applies the embedded schema, then steps user_version up to
// schemaVersion. Fresh DBs land at the latest version immediately (the DDL
// above already contains every column), so backfills only ever touch
// pre-existing data.
func (s *Store) migrate() error {
	if _, err := s.DB.Exec(schemaSQL); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	var v int
	if err := s.DB.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return fmt.Errorf("store: migrate: user_version: %w", err)
	}
	for ; v < schemaVersion; v++ {
		step := migrations[v]
		if step == nil {
			continue
		}
		if err := step(s.DB); err != nil {
			return fmt.Errorf("store: migrate v%d: %w", v+1, err)
		}
		if _, err := s.DB.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			return fmt.Errorf("store: migrate: set user_version: %w", err)
		}
	}
	return nil
}

// migrations[i] upgrades user_version i+1 → i+2. Indexes live in schema.sql
// (IF NOT EXISTS re-runs are free), so steps only carry ALTER/UPDATE work.
var migrations = [...]func(*sql.DB) error{
	1: migrateV2MessageParents, // v1 → v2: message branching (parentId tree + chat leaf)
	2: migrateV3ChatArchive,    // v2 → v3: chat archive flag (chats.archived_at)
	3: migrateV4ModelMinRole,   // v3 → v4: per-model access floor (models.min_role)
}

// migrateV4ModelMinRole adds models.min_role (” = every authed user, 'admin'
// = admins only) to pre-existing databases. Fresh DBs already carry the column
// from schema.sql, so existing rows land at ” (visible to all) — never a
// silent lockdown. Idempotent: PRAGMA table_info is the probe.
func migrateV4ModelMinRole(db *sql.DB) error {
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(models)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	have := false
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "min_role" {
			have = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if have {
		return nil
	}
	_, err = db.ExecContext(ctx, `ALTER TABLE models ADD COLUMN min_role TEXT NOT NULL DEFAULT ''`)
	return err
}

// migrateV3ChatArchive adds chats.archived_at (nullable unix seconds; NULL =
// not archived) to pre-existing databases. Fresh DBs already carry the column
// from schema.sql. Idempotent: PRAGMA table_info is the probe.
func migrateV3ChatArchive(db *sql.DB) error {
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(chats)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	have := false
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "archived_at" {
			have = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if have {
		return nil
	}
	_, err = db.ExecContext(ctx, `ALTER TABLE chats ADD COLUMN archived_at INTEGER`)
	return err
}

// migrateV2MessageParents adds messages.parent_id + chats.leaf_id to
// pre-existing databases and backfills a linear chain per chat so old
// conversations keep working under the branch model: every message gets the
// previous row (rowid order) as its parent and the chat's leaf is its newest
// row. Idempotent column adds: PRAGMA table_info is the single probe.
func migrateV2MessageParents(db *sql.DB) error {
	ctx := context.Background()
	added := func(table, col string) error {
		rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
		if err != nil {
			return err
		}
		defer rows.Close()
		have := false
		for rows.Next() {
			var (
				cid     int
				name    string
				ctype   string
				notnull int
				dflt    sql.NullString
				pk      int
			)
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				return err
			}
			if name == col {
				have = true
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if have {
			return nil
		}
		_, err = db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+col+` TEXT NOT NULL DEFAULT ''`)
		return err
	}
	if err := added("messages", "parent_id"); err != nil {
		return err
	}
	if err := added("chats", "leaf_id"); err != nil {
		return err
	}
	// Built here, not in schema.sql: on pre-existing databases the column only
	// exists after the ALTER above (schema.sql runs before the version steps).
	if _, err := db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_messages_chat_parent ON messages(chat_id, parent_id)`); err != nil {
		return err
	}
	// Heal the FTS index against its content table before the backfill's
	// UPDATEs fire the AU triggers: an out-of-step external-content index
	// answers those with SQLITE_CORRUPT.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO messages_fts(messages_fts) VALUES ('rebuild')`); err != nil {
		return err
	}

	// Backfill parents: one pass per chat that still has un-parented rows
	// (fresh DBs have no messages at all, so this is dead code for them).
	// Chats are bounded by design (a chat with millions of messages would
	// already have lost on history assembly), so walking in Go keeps the
	// statement plans trivial.
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT chat_id FROM messages WHERE parent_id = ''`)
	if err != nil {
		return err
	}
	var chatIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		chatIDs = append(chatIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	msgs, err := tx.PrepareContext(ctx, `SELECT id FROM messages WHERE chat_id = ?1 ORDER BY rowid`)
	if err != nil {
		return err
	}
	defer msgs.Close()
	setParent, err := tx.PrepareContext(ctx, `UPDATE messages SET parent_id = ?1 WHERE id = ?2`)
	if err != nil {
		return err
	}
	defer setParent.Close()
	setLeaf, err := tx.PrepareContext(ctx, `UPDATE chats SET leaf_id = ?1 WHERE id = ?2 AND leaf_id = ''`)
	if err != nil {
		return err
	}
	defer setLeaf.Close()

	for _, chatID := range chatIDs {
		r, err := msgs.QueryContext(ctx, chatID)
		if err != nil {
			return err
		}
		var ids []string
		for r.Next() {
			var id string
			if err := r.Scan(&id); err != nil {
				r.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := r.Err(); err != nil {
			r.Close()
			return err
		}
		r.Close()
		for i := 1; i < len(ids); i++ {
			if _, err := setParent.Exec(ids[i-1], ids[i]); err != nil {
				return err
			}
		}
		if len(ids) > 0 {
			if _, err := setLeaf.Exec(ids[len(ids)-1], chatID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
