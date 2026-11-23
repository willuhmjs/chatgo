// Package store owns SQLite access. repos.go declares the repo types that
// hang off *Store as method groups (contract: `Users: Insert/GetByID/...`
// becomes `store.Users.Insert(...)`). Owning agents implement methods in
// their own files; only the struct declarations here are frozen.
package store

// FTS/keyword search over message content (BE-apichat adds Search methods on
// MessagesRepo in internal/store/ftssearch.go; scaffold keeps the FTS table +
// triggers in sync via schema.sql).

import (
	"database/sql"
)

// dbHandle aliases *sql.DB so repo structs stay tiny; all queries run
// through it parameterized (contract: no fmt.Sprintf into SQL, ever).
type dbHandle = *sql.DB

// UsersRepo: user CRUD (scaffold-implemented, users.go).
type UsersRepo struct{ db dbHandle }

// SessionsRepo: opaque session tokens (scaffold-implemented, sessions.go).
type SessionsRepo struct{ db dbHandle }

// SettingsRepo: global key/value settings (scaffold-implemented, settings.go).
type SettingsRepo struct{ db dbHandle }

// ProvidersRepo: LLM providers; api_key_enc AES-256-GCM round-trip
// (scaffold-implemented, providers.go).
type ProvidersRepo struct {
	db  dbHandle
	key []byte
}

// ModelsRepo: cached provider /v1/models results (scaffold-implemented, models_cache.go).
type ModelsRepo struct{ db dbHandle }

// ChatsRepo: chats incl. list filters, soft delete, share tokens
// (scaffold-implemented, chats.go).
type ChatsRepo struct{ db dbHandle }

// MessagesRepo: chat messages with rowid-based before-cursor pagination
// (scaffold-implemented, messages.go).
type MessagesRepo struct{ db dbHandle }

// FoldersRepo is populated by BE4 in folders.go: CRUD per user.
type FoldersRepo struct{ db dbHandle }

// TagsRepo is populated by BE4 in tags.go: CRUD + SetChatTags/TagsForChats.
type TagsRepo struct{ db dbHandle }

// TokensRepo is populated by BE5 in tokens.go: Create/List/HashLookup.
type TokensRepo struct{ db dbHandle }

// FilesRepo is populated by BE6 in files.go: Insert/Get/ListByUser/DeleteByUser.
type FilesRepo struct{ db dbHandle }
