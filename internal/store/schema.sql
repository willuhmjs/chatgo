-- ChatGo schema — owned by internal/store/migrate.go (docs/CONTRACTS.md §DB schema).
-- All ids TEXT (uuid v4). All timestamps unix seconds INTEGER.

CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	email TEXT NOT NULL UNIQUE COLLATE NOCASE,
	name TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin','user','pending')),
	password_hash TEXT,
	avatar_url TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL DEFAULT 0,
	last_active_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS sessions (
	token TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL DEFAULT 0,
	expires_at INTEGER NOT NULL DEFAULT 0,
	last_seen_at INTEGER NOT NULL DEFAULT 0,
	user_agent TEXT NOT NULL DEFAULT '',
	ip TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS providers (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	base_url TEXT NOT NULL DEFAULT '',
	api_key_enc BLOB, -- AES-256-GCM, key = SHA-256(SESSION_SECRET)
	enabled INTEGER NOT NULL DEFAULT 1,
	is_default INTEGER NOT NULL DEFAULT 0,
	sort INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS models (
	provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
	model_id TEXT NOT NULL,
	display TEXT NOT NULL DEFAULT '',
	context_len INTEGER NOT NULL DEFAULT 0,
	enabled INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (provider_id, model_id)
);

CREATE TABLE IF NOT EXISTS folders (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	parent_id TEXT REFERENCES folders(id) ON DELETE SET NULL,
	sort INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_folders_user ON folders(user_id);

CREATE TABLE IF NOT EXISTS tags (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	color TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL DEFAULT 0,
	UNIQUE (user_id, name)
);

CREATE TABLE IF NOT EXISTS chats (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	folder_id TEXT REFERENCES folders(id) ON DELETE SET NULL,
	title TEXT NOT NULL DEFAULT 'New chat',
	provider_id TEXT NOT NULL DEFAULT '',
	model_id TEXT NOT NULL DEFAULT '',
	params_json TEXT NOT NULL DEFAULT '{}',
	pinned INTEGER NOT NULL DEFAULT 0,
	share_token TEXT UNIQUE,
	leaf_id TEXT NOT NULL DEFAULT '',
	deleted_at INTEGER,
	created_at INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_chats_user ON chats(user_id, deleted_at, updated_at);
CREATE INDEX IF NOT EXISTS idx_chats_folder ON chats(folder_id);

CREATE TABLE IF NOT EXISTS chat_tags (
	chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
	tag_id TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
	PRIMARY KEY (chat_id, tag_id)
);

CREATE TABLE IF NOT EXISTS messages (
	id TEXT PRIMARY KEY,
	chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
	role TEXT NOT NULL CHECK (role IN ('user','assistant','system')),
	parent_id TEXT NOT NULL DEFAULT '',
	content TEXT NOT NULL DEFAULT '',
	sources_json TEXT NOT NULL DEFAULT '',
	error TEXT,
	tokens_in INTEGER NOT NULL DEFAULT 0,
	tokens_out INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages(chat_id);
CREATE INDEX IF NOT EXISTS idx_messages_chat_parent ON messages(chat_id, parent_id);
-- read order is by rowid (insertion order); the chat_id index narrows the scan.
-- parent_id forms a tree per chat (regenerate/edit = sibling, Open WebUI model):
-- the active conversation is the chain chats.leaf_id → … → root; messages
-- without parents (legacy rows, parent_id='') fall back to rowid order.

CREATE TABLE IF NOT EXISTS message_images (
	id TEXT PRIMARY KEY,
	message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
	file_id TEXT NOT NULL,
	created_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_message_images_msg ON message_images(message_id);

CREATE TABLE IF NOT EXISTS files (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL,
	name TEXT NOT NULL DEFAULT '',
	mime TEXT NOT NULL DEFAULT '',
	size INTEGER NOT NULL DEFAULT 0,
	path TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL DEFAULT 'upload' CHECK (kind IN ('upload','artifact','image')),
	created_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_files_user ON files(user_id, kind);

CREATE TABLE IF NOT EXISTS api_tokens (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL DEFAULT '',
	key_hash TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL DEFAULT 0,
	last_used_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id);

CREATE TABLE IF NOT EXISTS summaries (
	id TEXT PRIMARY KEY,
	chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
	up_to_message_id TEXT NOT NULL DEFAULT '',
	text TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_summaries_chat ON summaries(chat_id, created_at);

-- FTS5 external-content index over messages.content, kept in sync by triggers.
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
