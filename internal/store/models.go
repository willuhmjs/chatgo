package store

// Row structs mirroring schema.sql. Pointers = NULL-able columns.
// Timestamps are unix seconds.

// User roles (contract §DB schema).
const (
	RoleAdmin   = "admin"
	RoleUser    = "user"
	RolePending = "pending"
)

// File kinds.
const (
	FileKindUpload   = "upload"
	FileKindArtifact = "artifact"
	FileKindImage    = "image"
)

// User is a row in `users`.
type User struct {
	ID           string
	Email        string
	Name         string
	Role         string // admin|user|pending
	PasswordHash *string
	AvatarURL    string
	CreatedAt    int64
	LastActiveAt int64
}

// IsAdmin reports the admin role.
func (u *User) IsAdmin() bool { return u != nil && u.Role == RoleAdmin }

// HasPassword reports whether local login is possible.
func (u *User) HasPassword() bool { return u != nil && u.PasswordHash != nil && *u.PasswordHash != "" }

// Session is a row in `sessions`; Token is the opaque 32B url-safe cookie value.
type Session struct {
	Token      string
	UserID     string
	CreatedAt  int64
	ExpiresAt  int64
	LastSeenAt int64
	UserAgent  string
	IP         string
}

// Provider is a row in `providers`. APIKey is PLAINTEXT in memory; the repo
// encrypts to api_key_enc (AES-256-GCM, key = HKDF-SHA256(SESSION_SECRET)) on
// write and decrypts on read. Never log this struct.
type Provider struct {
	ID        string
	Name      string
	BaseURL   string
	APIKey    string
	Enabled   bool
	IsDefault bool
	Sort      int
	CreatedAt int64

	// KeyUnreadable is true when the stored api_key_enc blob fails to
	// decrypt (SESSION_SECRET rotated or blob corrupted): APIKey is "" but
	// the row stays listable so the admin UI can still render it, set a new
	// key, or delete it. Callers that actually need the key must refuse the
	// dispatch outright (SECREV4 M3b) — never send unauthenticated and
	// blame the provider for its 401.
	KeyUnreadable bool

	// sealedKey carries the raw api_key_enc blob exactly as Get/List read
	// it. Update rewrites it verbatim when the caller holds no new key to
	// seal for a KeyUnreadable row, so an unrelated admin edit (rename,
	// enable, sort, default flip) can never NULL the column and destroy the
	// only copy of a key the current SESSION_SECRET merely can't decrypt
	// (SECREV4 M1). Never log, serialize, or send it anywhere.
	sealedKey []byte
}

// ModelRow is a row in `models` (cache of a provider's /v1/models).
type ModelRow struct {
	ProviderID string
	ModelID    string
	Display    string
	ContextLen int
	Enabled    bool
	// MinRole gates visibility: '' = every authed user, 'admin' = admins only.
	// Preserved across re-sync exactly like Enabled (a fresh model lands at '').
	MinRole string
}

// Chat is a row in `chats`. LeafID is the id of the active branch's newest
// message ("" on legacy chats whose history is still rowid-linear).
type Chat struct {
	ID         string
	UserID     string
	FolderID   *string
	Title      string
	ProviderID string
	ModelID    string
	ParamsJSON string // default "{}"
	Pinned     bool
	ShareToken *string
	LeafID     string
	ArchivedAt *int64 // non-nil = archived (hidden from the default list)
	DeletedAt  *int64
	CreatedAt  int64
	UpdatedAt  int64
}

// Message is a row in `messages`. SourcesJSON is a JSON array of
// {title,url} (SSE `sources` items); "" when none. ParentID links the
// message into the chat's branch tree ("" = root/legacy-linear).
type Message struct {
	ID          string
	ChatID      string
	Role        string // user|assistant|system
	ParentID    string
	Content     string
	SourcesJSON string
	Error       *string
	TokensIn    int
	TokensOut   int
	CreatedAt   int64
}

// Folder is a row in `folders` (repo owned by BE4).
type Folder struct {
	ID        string
	UserID    string
	Name      string
	ParentID  *string
	Sort      int
	CreatedAt int64
}

// Tag is a row in `tags` (repo owned by BE4).
type Tag struct {
	ID        string
	UserID    string
	Name      string
	Color     string
	CreatedAt int64
}

// File is a row in `files` (repo owned by BE6). Path is relative to
// DataDir (e.g. "files/<uuid>.png").
type File struct {
	ID        string
	UserID    string
	ChatID    *string
	Name      string
	Mime      string
	Size      int64
	Path      string
	Kind      string // upload|artifact|image
	CreatedAt int64
}

// APIToken is a row in `api_tokens` (repo owned by BE5). KeyHash is
// sha256 hex of the token; the plaintext is shown exactly once at creation.
type APIToken struct {
	ID         string
	UserID     string
	Name       string
	KeyHash    string
	CreatedAt  int64
	LastUsedAt *int64
}

// Summary is a row in `summaries` (written by internal/chat/summarize.go).
type Summary struct {
	ID            string
	ChatID        string
	UpToMessageID string
	Text          string
	CreatedAt     int64
}
