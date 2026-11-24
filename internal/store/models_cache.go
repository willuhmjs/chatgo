package store

import (
	"context"
	"database/sql"
	"errors"
)

// NOTE: file named models_cache.go; the ModelRow struct lives in models.go.

// ReplaceForProvider atomically swaps the cached model list for a provider.
func (r *ModelsRepo) ReplaceForProvider(ctx context.Context, providerID string, ms []ModelRow) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort after commit

	if _, err := tx.ExecContext(ctx, `DELETE FROM models WHERE provider_id = ?1`, providerID); err != nil {
		return err
	}
	for i := range ms {
		m := ms[i]
		m.ProviderID = providerID
		if m.Display == "" {
			m.Display = m.ModelID
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO models (provider_id, model_id, display, context_len, enabled, min_role)
			 VALUES (?1, ?2, ?3, ?4, ?5, ?6)
			 ON CONFLICT(provider_id, model_id) DO UPDATE SET display = excluded.display,
			 	context_len = excluded.context_len, enabled = excluded.enabled,
			 	min_role = excluded.min_role`,
			m.ProviderID, m.ModelID, m.Display, m.ContextLen, boolInt(m.Enabled), m.MinRole); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ForProvider returns the cached models of one provider (id order).
func (r *ModelsRepo) ForProvider(ctx context.Context, providerID string) ([]ModelRow, error) {
	return r.queryModels(ctx, `SELECT provider_id, model_id, display, context_len, enabled, min_role
		FROM models WHERE provider_id = ?1 ORDER BY model_id`, providerID)
}

// ModelState reports whether (providerID, modelID) has a row in the admin
// models cache and whether that row is enabled. found == false means the
// pair was never synced — the cache has no verdict, and the CALLER decides
// what that means for its path (send refuses; task-model dispatches only
// gate on an explicit enabled = 0 row, because task models are pinned by id
// in settings and may legitimately never appear in a synced catalog).
// Single indexed read; shared by every kill-switch enforcement point
// (SECREV4 F-1) so the paths cannot drift apart.
func (r *ModelsRepo) ModelState(ctx context.Context, providerID, modelID string) (found, enabled bool, err error) {
	var en int
	err = r.db.QueryRowContext(ctx,
		`SELECT enabled FROM models WHERE provider_id = ?1 AND model_id = ?2`, providerID, modelID).Scan(&en)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, en != 0, nil
}

// EnabledModels returns every enabled model of an enabled provider,
// shaped for GET /api/models.
type ModelWithProvider struct {
	ProviderID   string
	ProviderName string
	ModelID      string
	Display      string
	ContextLen   int
	// MinRole mirrors models.min_role ('' = every authed user, 'admin' =
	// admins only). GET /api/models hides MinRole=='admin' rows from non-admin
	// callers; internal dispatch paths (proxy/imggen) read past it.
	MinRole string
}

// EnabledModels lists enabled models whose provider is enabled.
func (r *ModelsRepo) EnabledModels(ctx context.Context) ([]ModelWithProvider, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT m.provider_id, p.name, m.model_id, m.display, m.context_len, m.min_role
		 FROM models m JOIN providers p ON p.id = m.provider_id
		 WHERE m.enabled = 1 AND p.enabled = 1
		 ORDER BY p.sort ASC, p.name ASC, m.display ASC, m.model_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelWithProvider
	for rows.Next() {
		var m ModelWithProvider
		if err := rows.Scan(&m.ProviderID, &m.ProviderName, &m.ModelID, &m.Display, &m.ContextLen, &m.MinRole); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *ModelsRepo) queryModels(ctx context.Context, query string, arg string) ([]ModelRow, error) {
	rows, err := r.db.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelRow
	for rows.Next() {
		var m ModelRow
		var enabled int
		if err := rows.Scan(&m.ProviderID, &m.ModelID, &m.Display, &m.ContextLen, &enabled, &m.MinRole); err != nil {
			return nil, err
		}
		m.Enabled = enabled != 0
		out = append(out, m)
	}
	return out, rows.Err()
}
