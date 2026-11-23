package store

import (
	_ "embed"
	"fmt"
)

// schemaSQL is the single source of truth for the DB schema
// (docs/CONTRACTS.md §DB schema). Idempotent: IF NOT EXISTS everywhere.
//
//go:embed schema.sql
var schemaSQL string

// migrate applies the embedded schema. v1 uses idempotent DDL; a user_version
// based stepwise migration can be layered on later without changing callers.
func (s *Store) migrate() error {
	if _, err := s.DB.Exec(schemaSQL); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}
