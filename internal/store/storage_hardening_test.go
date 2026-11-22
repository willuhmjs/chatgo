package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/hkdf"
)

// testSecret is the secret testStore opens with (store_test.go).
func testSecret() []byte { return []byte("0123456789abcdef0123456789abcdef") }

func insertSealedProvider(t *testing.T, s *Store, id string, enc []byte) {
	t.Helper()
	_, err := s.DB.Exec(
		`INSERT INTO providers (id, name, base_url, api_key_enc, enabled, is_default, sort, created_at)
		 VALUES (?1, ?2, '', ?3, 1, 0, 0, 1)`, id, "prov-"+id, enc)
	if err != nil {
		t.Fatalf("raw provider insert: %v", err)
	}
}

// TestProviderKeyDerivationHKDF verifies the at-rest key is
// HKDF-SHA256(secret, nil, "chatgo:providers-atrest/v1") — a blob sealed with
// that independently derived key decrypts through the repo, and a blob sealed
// with the OLD raw SHA-256 derivation no longer does.
func TestProviderKeyDerivationHKDF(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	secret := testSecret()

	hkdfKey := make([]byte, 32)
	if _, err := hkdf.New(sha256.New, secret, nil,
		[]byte("chatgo:providers-atrest/v1")).Read(hkdfKey); err != nil {
		// Read never returns err for the short length here.
		t.Fatalf("hkdf: %v", err)
	}
	if want := sha256.Sum256(secret); string(hkdfKey) == string(want[:]) {
		t.Fatal("HKDF key must differ from the old raw SHA-256 derivation")
	}

	insertSealedProvider(t, s, "hkdf-ok", storeKey(hkdfKey).mustEncrypt(t, "sk-via-hkdf"))
	got, err := s.Providers.Get(ctx, "hkdf-ok")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.APIKey != "sk-via-hkdf" || got.KeyUnreadable {
		t.Errorf("HKDF-sealed blob: key=%q unreadable=%v", got.APIKey, got.KeyUnreadable)
	}

	// Legacy raw-SHA256-sealed blob is now unreadable, not a hard error.
	legacy := sha256.Sum256(secret)
	insertSealedProvider(t, s, "legacy", storeKey(legacy[:]).mustEncrypt(t, "sk-old"))
	got, err = s.Providers.Get(ctx, "legacy")
	if err != nil {
		t.Fatalf("Get legacy: %v", err)
	}
	if got.APIKey != "" || !got.KeyUnreadable {
		t.Errorf("legacy blob: key=%q unreadable=%v, want \"\" + true", got.APIKey, got.KeyUnreadable)
	}
}

// TestProviderKeyUnreadableRoundTrip covers finding 4: a row whose sealed key
// cannot be decrypted still reads (flagged), can be repaired via Update, and
// can be deleted.
func TestProviderKeyUnreadableRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	foreign := make([]byte, 32)
	for i := range foreign {
		foreign[i] = byte(i * 7)
	}
	insertSealedProvider(t, s, "broken", storeKey(foreign).mustEncrypt(t, "sk-elsewhere"))

	got, err := s.Providers.Get(ctx, "broken")
	if err != nil {
		t.Fatalf("Get unreadable: %v", err)
	}
	if got.APIKey != "" || !got.KeyUnreadable {
		t.Fatalf("got key=%q unreadable=%v, want \"\" + true", got.APIKey, got.KeyUnreadable)
	}

	// Repair: Update writes a fresh, readable seal.
	got.APIKey = "sk-repaired"
	if err := s.Providers.Update(ctx, got); err != nil {
		t.Fatalf("Update repair: %v", err)
	}
	got, err = s.Providers.Get(ctx, "broken")
	if err != nil {
		t.Fatalf("Get after repair: %v", err)
	}
	if got.APIKey != "sk-repaired" || got.KeyUnreadable {
		t.Errorf("after repair: key=%q unreadable=%v", got.APIKey, got.KeyUnreadable)
	}

	// And the unreadable row is deletable without decrypting.
	if err := s.Providers.Delete(ctx, "broken"); err != nil {
		t.Errorf("Delete: %v", err)
	}
}

// TestSessionTokenHashedAtRest covers finding 5: the sessions.token column
// holds hex sha256(token), while the repo API keeps round-tripping plaintext.
func TestSessionTokenHashedAtRest(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := mustUser(t, s, "hash@example.com", RoleUser)

	const plain = "plain-cookie-token-abc"
	sess := &Session{Token: plain, UserID: u.ID, CreatedAt: 1, ExpiresAt: 100, UserAgent: "ua", IP: "::1"}
	if err := s.Sessions.Create(ctx, sess); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Token != plain {
		t.Errorf("Create mutated Token to %q; caller needs the plaintext for the cookie", sess.Token)
	}

	var stored string
	if err := s.DB.QueryRowContext(ctx, `SELECT token FROM sessions WHERE user_id = ?1`, u.ID).Scan(&stored); err != nil {
		t.Fatalf("raw select: %v", err)
	}
	if stored == plain {
		t.Fatal("sessions.token stores the plaintext token")
	}
	if want := HashToken(plain); stored != want {
		t.Errorf("stored token = %q, want sha256 hex %q", stored, want)
	}

	got, err := s.Sessions.Get(ctx, plain, 50)
	if err != nil || got.UserID != u.ID {
		t.Fatalf("Get: %v, %v", got, err)
	}
	if got.Token != plain {
		t.Errorf("Get returned Token %q, want the presented plaintext", got.Token)
	}

	// Round-trip: the token from Get works for Touch and Delete.
	if err := s.Sessions.Touch(ctx, got.Token, 60); err != nil {
		t.Fatalf("Touch via round-tripped token: %v", err)
	}
	if refreshed, err := s.Sessions.Get(ctx, plain, 70); err != nil || refreshed.LastSeenAt != 60 {
		t.Errorf("Touch did not land: %+v, %v", refreshed, err)
	}

	// Expired lookups stay correct, and DeleteExpired purges by time.
	if _, err := s.Sessions.Get(ctx, plain, 200); err != ErrNotFound {
		t.Errorf("expired Get = %v, want ErrNotFound", err)
	}
	if err := s.Sessions.Delete(ctx, got.Token); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Sessions.Get(ctx, plain, 50); err != ErrNotFound {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
	if n, err := s.Sessions.DeleteExpired(ctx, 1<<40); err != nil || n != 0 {
		t.Errorf("DeleteExpired on empty = %d, %v", n, err)
	}
}

// TestAppendContentTooLong covers finding 6: exactly 64 KiB passes, 64 KiB+1
// is rejected with ErrContentTooLong and nothing is written.
func TestAppendContentTooLong(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := mustUser(t, s, "big@example.com", RoleUser)
	c := &Chat{UserID: u.ID, Title: "c"}
	if err := s.Chats.Create(ctx, c); err != nil {
		t.Fatal(err)
	}

	big := &Message{ChatID: c.ID, Role: "user", Content: strings.Repeat("x", MaxContentLen)}
	if err := s.Messages.Append(ctx, big); err != nil {
		t.Fatalf("Append(64 KiB) = %v, want success at the boundary", err)
	}
	if back, err := s.Messages.Get(ctx, c.ID, big.ID); err != nil || len(back.Content) != MaxContentLen {
		t.Fatalf("boundary message not stored: %v, %v", back, err)
	}

	over := &Message{ChatID: c.ID, Role: "user", Content: strings.Repeat("x", MaxContentLen+1)}
	err := s.Messages.Append(ctx, over)
	if !errors.Is(err, ErrContentTooLong) {
		t.Fatalf("Append(64 KiB + 1) = %v, want ErrContentTooLong", err)
	}
	if n, err := s.Messages.Count(ctx, c.ID); err != nil || n != 1 {
		t.Errorf("rejected Append must not write: count = %d (err %v)", n, err)
	}
}

// TestOpenFilePermissions covers finding 1: file-backed Open leaves the DB
// 0600 (sidecars too, when present) and creates missing dirs 0700.
func TestOpenFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data", "chatgo.db")

	s, err := Open(path, testSecret())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Write through the repo so WAL sidecars exist, then close cleanly.
	if err := s.Settings.Set(context.Background(), "k", "v"); err != nil {
		t.Fatalf("Settings.Set: %v", err)
	}

	if st, err := os.Stat(path); err != nil {
		t.Fatalf("stat db: %v", err)
	} else if st.Mode().Perm() != 0o600 {
		t.Errorf("db mode = %v, want -rw-------", st.Mode().Perm())
	}
	if st, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("stat dir: %v", err)
	} else if st.Mode().Perm() != 0o700 {
		t.Errorf("DataDir mode = %v, want drwx------", st.Mode().Perm())
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if st, err := os.Stat(path + suffix); err == nil && st.Mode().Perm() != 0o600 {
			t.Errorf("sidecar %s mode = %v, want -rw-------", suffix, st.Mode().Perm())
		}
	}
	s.Close()
}

// mustEncrypt is a test helper around Store.encrypt.
func (s *Store) mustEncrypt(t *testing.T, plaintext string) []byte {
	t.Helper()
	blob, err := s.encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return blob
}
