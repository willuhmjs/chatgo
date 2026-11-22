package store

import (
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
	key []byte // AES-256-GCM key = HKDF-SHA256(SESSION_SECRET), see deriveKey

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
// migrations. secret is the raw (decoded) SESSION_SECRET; the at-rest key for
// provider API keys is derived from it via HKDF-SHA256 (see deriveKey). File
// paths get owner-only modes: directories 0700, the DB (and its -wal/-shm
// sidecars) 0600. Pass path=":memory:" for tests.
func Open(path string, secret []byte) (*Store, error) {
	if len(secret) == 0 {
		return nil, errors.New("store: secret must not be empty")
	}
	key, err := deriveKey(secret)
	if err != nil {
		return nil, err
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
	} else {
		// File-backed (and named shared-cache in-memory) DBs: cap the pool.
		// Each modernc connection buffers ~1-2 MB and SQLite has a single
		// writer, so an unbounded pool just piles buffered conns up behind
		// that writer. 4 keeps shared-cache :memory: tests (≥2 conns) fine.
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(4)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}

	s := &Store{DB: db, key: key}
	s.initRepos()

	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}

	// The migration just created/touched the DB file and its WAL sidecars;
	// tighten them to owner-only regardless of umask (data is not encrypted
	// at rest, so a world-readable file leaks every row).
	if err := restrictFilePerms(path); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// restrictFilePerms chmods a file-backed DB plus its -wal/-shm sidecars (when
// present) to 0600. In-memory and "file:"-prefixed DSNs are skipped. It must
// run after the first write (migration) so the sidecars already exist.
func restrictFilePerms(path string) error {
	if path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for _, p := range []string{abs, abs + "-wal", abs + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("store: chmod %s: %w", p, err)
		}
	}
	return nil
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
		// DataDir holds the (unencrypted-at-rest) DB: owner-only.
		if err := os.MkdirAll(dir, 0o700); err != nil {
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
