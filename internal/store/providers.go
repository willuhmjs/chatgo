package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

// Providers store LLM API keys; APIKey is encrypted at rest (crypto.go).
// Never log a Provider struct or its APIKey.

const providerCols = "id, name, base_url, api_key_enc, enabled, is_default, sort, created_at"

// Insert creates a provider; fills ID/CreatedAt when zero and encrypts APIKey.
func (r *ProvidersRepo) Insert(ctx context.Context, p *Provider) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.CreatedAt == 0 {
		p.CreatedAt = nowUnix()
	}
	enc, err := r.seal(p.APIKey)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO providers (id, name, base_url, api_key_enc, enabled, is_default, sort, created_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
		p.ID, p.Name, p.BaseURL, enc, boolInt(p.Enabled), boolInt(p.IsDefault), p.Sort, p.CreatedAt)
	return err
}

// Get fetches a provider with its APIKey decrypted in memory. A row whose
// sealed key cannot be decrypted is returned with APIKey == "" and
// KeyUnreadable == true (no error), so a rotated SESSION_SECRET never bricks
// the admin UI; chat callers that need the key treat the flag like "no key
// configured" and send the upstream request unauthenticated.
func (r *ProvidersRepo) Get(ctx context.Context, id string) (*Provider, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+providerCols+` FROM providers WHERE id = ?1`, id)
	p, _, err := r.scanWithKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// List returns providers ordered by (sort, name); APIKeys decrypted, except
// unreadable blobs (see Get). A single undecryptable row never fails the list.
func (r *ProvidersRepo) List(ctx context.Context) ([]*Provider, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+providerCols+` FROM providers ORDER BY sort ASC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Provider
	for rows.Next() {
		p, _, err := r.scanWithKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Update overwrites name/base_url/api_key_enc/enabled/is_default/sort.
// An empty APIKey means "clear the stored key"; set it from Get() output
// when you want to keep the existing key.
func (r *ProvidersRepo) Update(ctx context.Context, p *Provider) error {
	enc, err := r.seal(p.APIKey)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE providers SET name = ?2, base_url = ?3, api_key_enc = ?4, enabled = ?5, is_default = ?6, sort = ?7
		 WHERE id = ?1`,
		p.ID, p.Name, p.BaseURL, enc, boolInt(p.Enabled), boolInt(p.IsDefault), p.Sort)
	return affected(res, err)
}

// Delete removes a provider (its cached models cascade).
func (r *ProvidersRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM providers WHERE id = ?1`, id)
	return affected(res, err)
}

// scanWithKey reads a provider row and decrypts its api key into APIKey.
// A failed decryption (auth-tag mismatch = SESSION_SECRET changed, or a
// corrupted blob) does NOT fail the row: it comes back with APIKey == "" and
// KeyUnreadable == true so admin List/Update/Delete keep working and the
// broken row can be repaired or removed.
func (r *ProvidersRepo) scanWithKey(row rowScanner) (*Provider, []byte, error) {
	p := &Provider{}
	var enc []byte
	var enabled, isDefault int
	if err := row.Scan(&p.ID, &p.Name, &p.BaseURL, &enc, &enabled, &isDefault, &p.Sort, &p.CreatedAt); err != nil {
		return nil, nil, err
	}
	p.Enabled = enabled != 0
	p.IsDefault = isDefault != 0
	if len(enc) > 0 {
		plain, err := storeKey(r.key).decrypt(enc)
		if err != nil {
			p.APIKey = ""
			p.KeyUnreadable = true
			return p, enc, nil
		}
		p.APIKey = plain
	}
	return p, enc, nil
}

// seal encrypts an API key (nil for empty so the column stays NULL).
func (r *ProvidersRepo) seal(apiKey string) ([]byte, error) {
	if apiKey == "" {
		return nil, nil
	}
	return storeKey(r.key).encrypt(apiKey)
}

// rowScanner unifies *sql.Row and *sql.Rows Scan.
type rowScanner interface {
	Scan(dest ...any) error
}

// storeKey builds a throwaway Store for the shared AES-GCM helpers without
// duplicating crypto code. The DB handle is unused by encrypt/decrypt.
func storeKey(key []byte) *Store { return &Store{key: key} }
