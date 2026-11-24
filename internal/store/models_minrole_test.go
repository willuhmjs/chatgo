// Per-model access floor (models.min_role). Two guarantees are pinned here:
//   - the v3 → v4 ALTER is idempotent (PRAGMA-probe guarded), so a fresh DB
//     (schema.sql already carries the column) and a pre-existing v3 DB (needs
//     the ALTER) both land at v4 with no error, and re-running the step is a
//     no-op;
//   - min_role survives a re-sync exactly like enabled, because ReplaceForProvider
//     carries the caller-supplied value and apimgr re-reads it before re-syncing.

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// v3Schema is a byte-faithful pre-min_role models cache (no min_role column)
// plus the minimal tables Open's migrate path leaves in place. user_version = 3
// so the real migrate step (v3 → v4) is the only thing that runs.
const v3Schema = `
CREATE TABLE providers (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	base_url TEXT NOT NULL DEFAULT '',
	api_key_enc BLOB,
	enabled INTEGER NOT NULL DEFAULT 1,
	is_default INTEGER NOT NULL DEFAULT 0,
	sort INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE models (
	provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
	model_id TEXT NOT NULL,
	display TEXT NOT NULL DEFAULT '',
	context_len INTEGER NOT NULL DEFAULT 0,
	enabled INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (provider_id, model_id)
);
PRAGMA user_version = 3;
`

func TestMigrateV4MinRoleAddIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chatgo.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(v3Schema); err != nil {
		t.Fatalf("seed v3 schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO providers (id, name) VALUES ('p1', 'p1')`); err != nil {
		t.Fatal(err)
	}
	// Pre-existing models land at min_role = '' after the ALTER (never a
	// silent lockdown to admins-only).
	if _, err := db.Exec(`INSERT INTO models (provider_id, model_id, enabled) VALUES ('p1', 'old', 1)`); err != nil {
		t.Fatal(err)
	}

	// The step itself must be safe to run twice (guarded by PRAGMA table_info).
	for i := 0; i < 2; i++ {
		if err := migrateV4ModelMinRole(db); err != nil {
			t.Fatalf("migrateV4ModelMinRole pass %d: %v", i, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// The real production entry point: Open runs schema.sql + version steps and
	// must land at the current version without error.
	st, err := Open(path, testSecret())
	if err != nil {
		t.Fatalf("open upgraded db: %v", err)
	}
	defer st.Close()

	var v int
	if err := st.DB.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != schemaVersion {
		t.Fatalf("user_version=%d err=%v, want %d", v, err, schemaVersion)
	}
	if !hasColumn(t, st.DB, "models", "min_role") {
		t.Fatal("models.min_role missing after migration")
	}
	var role string
	if err := st.DB.QueryRowContext(ctx,
		`SELECT min_role FROM models WHERE provider_id = 'p1' AND model_id = 'old'`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "" {
		t.Errorf("pre-existing row min_role = %q, want '' (visible to all)", role)
	}
}

// TestModelMinRoleRoundTripAndResync verifies ReplaceForProvider persists
// min_role, ForProvider reads it back, and a re-sync that carries the caller's
// value preserves it (a new model with no min_role lands at ”).
func TestModelMinRoleRoundTripAndResync(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	p := &Provider{Name: "ac", BaseURL: "https://x.test/v1", APIKey: "sk", Enabled: true}
	if err := s.Providers.Insert(ctx, p); err != nil {
		t.Fatal(err)
	}

	// Initial cache: one everyone-visible enabled model + one admins-only.
	if err := s.Models.ReplaceForProvider(ctx, p.ID, []ModelRow{
		{ModelID: "pub", Display: "Public", Enabled: true},
		{ModelID: "sec", Display: "Secret", Enabled: true, MinRole: RoleAdmin},
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"pub": "", "sec": RoleAdmin}
	if got := minRoles(t, s, ctx, p.ID); !mapsEqualStr(got, want) {
		t.Fatalf("initial roles = %v, want %v", got, want)
	}

	// EnabledModels surfaces min_role too (the caller filters on it).
	ms, err := s.Models.EnabledModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, m := range ms {
		seen[m.ModelID] = m.MinRole
	}
	if seen["sec"] != RoleAdmin || seen["pub"] != "" {
		t.Errorf("EnabledModels roles = %v, want pub='' sec=admin", seen)
	}

	// Re-sync (what apimgr does): pub still visible-to-all, sec STILL admin
	// (the caller read it before syncing), a brand-new model d defaults to ''.
	if err := s.Models.ReplaceForProvider(ctx, p.ID, []ModelRow{
		{ModelID: "pub", Display: "Public", Enabled: true, MinRole: ""},
		{ModelID: "sec", Display: "Secret", Enabled: true, MinRole: RoleAdmin},
		{ModelID: "d", Display: "D", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	want = map[string]string{"pub": "", "sec": RoleAdmin, "d": ""}
	if got := minRoles(t, s, ctx, p.ID); !mapsEqualStr(got, want) {
		t.Errorf("roles after resync = %v, want %v", got, want)
	}
}

func minRoles(t *testing.T, s *Store, ctx context.Context, providerID string) map[string]string {
	t.Helper()
	rows, err := s.Models.ForProvider(ctx, providerID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, m := range rows {
		out[m.ModelID] = m.MinRole
	}
	return out
}

func hasColumn(t *testing.T, db *sql.DB, table, col string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
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
			t.Fatal(err)
		}
		if strings.EqualFold(name, col) {
			have = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return have
}

func mapsEqualStr(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
