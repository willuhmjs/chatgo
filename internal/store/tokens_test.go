package store

import (
	"context"
	"strings"
	"testing"
)

// TokensRepo (api_tokens) — BE-apimgr-owned file/test (contract §Store BE5).

func TestTokensCreateLookupAndLastUsed(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := mustUser(t, s, "tok@example.com", RoleUser)

	plain := "ck_" + strings.Repeat("ab", 16)
	tok := &APIToken{UserID: u.ID, Name: "ci", KeyHash: HashToken(plain)}
	if err := s.Tokens.Create(ctx, tok); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tok.ID == "" || tok.CreatedAt == 0 {
		t.Fatalf("Create did not fill ID/CreatedAt: %+v", tok)
	}
	if tok.LastUsedAt != nil {
		t.Errorf("fresh token has last_used %v", *tok.LastUsedAt)
	}

	// Lookup resolves the OWNING USER and stamps last_used_at.
	gotU, gotT, err := s.Tokens.HashLookup(ctx, HashToken(plain))
	if err != nil {
		t.Fatalf("HashLookup: %v", err)
	}
	if gotU.ID != u.ID || gotU.Email != "tok@example.com" || gotU.Role != RoleUser {
		t.Errorf("HashLookup user = %+v", gotU)
	}
	if gotT.ID != tok.ID || gotT.Name != "ci" {
		t.Errorf("HashLookup token = %+v", gotT)
	}
	if gotT.LastUsedAt == nil || *gotT.LastUsedAt == 0 {
		t.Error("last_used_at not stamped by HashLookup")
	}
	var stored int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT last_used_at FROM api_tokens WHERE id = ?1`, tok.ID).Scan(&stored); err != nil || stored == 0 {
		t.Errorf("last_used_at in DB = %d (err %v)", stored, err)
	}

	// Unknown / empty hash → ErrNotFound.
	if _, _, err := s.Tokens.HashLookup(ctx, HashToken("ck_nope")); err != ErrNotFound {
		t.Errorf("HashLookup(unknown) = %v, want ErrNotFound", err)
	}
	if _, _, err := s.Tokens.HashLookup(ctx, ""); err != ErrNotFound {
		t.Errorf("HashLookup(empty) = %v, want ErrNotFound", err)
	}

	// List is owner-scoped and omits nothing else.
	list, err := s.Tokens.List(ctx, u.ID)
	if err != nil || len(list) != 1 || list[0].ID != tok.ID {
		t.Fatalf("List = %+v, %v", list, err)
	}
	other := mustUser(t, s, "tok2@example.com", RoleUser)
	if l2, _ := s.Tokens.List(ctx, other.ID); len(l2) != 0 {
		t.Errorf("other user sees %d tokens", len(l2))
	}

	// Delete is owner-scoped: foreign delete 404s, own delete works.
	if err := s.Tokens.Delete(ctx, tok.ID, other.ID); err != ErrNotFound {
		t.Errorf("Delete(other owner) = %v, want ErrNotFound", err)
	}
	if err := s.Tokens.Delete(ctx, tok.ID, u.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := s.Tokens.HashLookup(ctx, HashToken(plain)); err != ErrNotFound {
		t.Error("deleted token still resolves")
	}
}

func TestTokensCreateRejectsMissingHash(t *testing.T) {
	s := testStore(t)
	u := mustUser(t, s, "tok3@example.com", RoleUser)
	if err := s.Tokens.Create(context.Background(), &APIToken{UserID: u.ID, Name: "x"}); err == nil {
		t.Fatal("Create without KeyHash should fail")
	}
}
