package store

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// Store is the single SQLite handle plus repos (method groups, contract §Store).
type Store struct {
	DB  *sql.DB
	key []byte // AES-256-GCM key = SHA-256(SESSION_SECRET)

	Users     UsersRepo
	Sessions  SessionsRepo
	Settings  SettingsRepo
	Providers ProvidersRepo
	Models    ModelsRepo
	Chats     ChatsRepo
	Messages  MessagesRepo

	// Declared by scaffold so owning agents add methods in their OWN files
	// without touching this file (contract §Store):
	Folders FoldersRepo // BE4 (internal/store/folders.go)
	Tags    TagsRepo    // BE4 (internal/store/tags.go)
	Tokens  TokensRepo  // BE5 (internal/store/tokens.go)
	Files   FilesRepo   // BE6 (internal/store/files.go)
}

// Open opens (creating if needed) the SQLite database at path, applies the
// pragmas from the contract (WAL, busy_timeout=5000, foreign_keys=ON) and runs
// migrations. secret is the raw (decoded) SESSION_SECRET; it derives the
// at-rest key for provider API keys. Pass path=":memory:" for tests.
func Open(path string, secret []byte) (*Store, error) {
	if len(secret) == 0 {
		return nil, errors.New("store: secret must not be empty")
	}

	dsn, singleConn, err := dsnFor(path)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if singleConn {
		// :memory: DBs are per-connection; serialize to keep one instance.
		db.SetMaxOpenConns(1)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}

	s := &Store{DB: db, key: sha256sum(secret)}
	s.initRepos()

	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// dsnFor builds a modernc.org/sqlite DSN with per-connection pragmas.
// "file:"-prefixed paths pass through, letting tests use distinct named
// in-memory DBs (file:memX?mode=memory&cache=shared) for isolation.
func dsnFor(path string) (dsn string, singleConn bool, err error) {
	const pragmas = "busy_timeout(5000)&_pragma=foreign_keys(ON)"
	if path == ":memory:" {
		// Default in-memory DB is per-connection; serialize onto one.
		return "file::memory:?cache=shared&_pragma=" + pragmas, true, nil
	}
	if strings.HasPrefix(path, "file:") {
		u, err := url.Parse(path)
		if err != nil {
			return "", false, fmt.Errorf("store: bad dsn %q: %w", path, err)
		}
		q := u.Query()
		q.Add("_pragma", "busy_timeout(5000)")
		q.Add("_pragma", "foreign_keys(ON)")
		u.RawQuery = q.Encode()
		return u.String(), false, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return "", false, fmt.Errorf("store: mkdir %s: %w", dir, err)
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(ON)")
	u.RawQuery = q.Encode()
	return u.String(), false, nil
}

func (s *Store) initRepos() {
	s.Users.db = s.DB
	s.Sessions.db = s.DB
	s.Settings.db = s.DB
	s.Providers.db = s.DB
	s.Providers.key = s.key
	s.Models.db = s.DB
	s.Chats.db = s.DB
	s.Messages.db = s.DB
	s.Folders.db = s.DB
	s.Tags.db = s.DB
	s.Tokens.db = s.DB
	s.Files.db = s.DB
}

// Close closes the underlying handle.
func (s *Store) Close() error { return s.DB.Close() }

func sha256sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
