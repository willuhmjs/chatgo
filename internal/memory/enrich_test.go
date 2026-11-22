package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"chatgo/internal/store"
)

// seed writes rows straight through the storage helper (bypassing HTTP so
// tests can control timestamps/enabled state directly).
func (e *env) seed(userID, content string, enabled bool, source string) {
	e.t.Helper()
	m := &Memory{Content: content, Source: source, Enabled: enabled}
	if err := insertMemory(context.Background(), e.store.DB, userID, m); err != nil {
		e.t.Fatalf("insertMemory: %v", err)
	}
}

func TestEnrichOrderingAndEnabledFilter(t *testing.T) {
	e := newEnv(t)
	a := &store.User{Email: "alice@example.com", Name: "A", Role: store.RoleUser}
	b := &store.User{Email: "bob@example.com", Name: "B", Role: store.RoleUser}
	ctx := context.Background()
	if err := e.store.Users.Insert(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Users.Insert(ctx, b); err != nil {
		t.Fatal(err)
	}

	// Insertion order = chronological: first, second, third.
	e.seed(a.ID, "First fact", true, SourceManual)
	e.seed(a.ID, "Disabled fact", false, SourceManual)
	e.seed(a.ID, "Second fact", true, SourceExtracted)
	e.seed(a.ID, "Third fact", true, SourceManual)
	e.seed(b.ID, "Bob only", true, SourceManual)

	// Newest-first, disabled excluded, header present.
	srcs, block, err := Enrich(ctx, e.store, a.ID, "whatever the user asked")
	if err != nil {
		t.Fatal(err)
	}
	if srcs != nil {
		t.Fatalf("memory enrichment must not cite sources, got %+v", srcs)
	}
	want := "About the user:\n- Third fact\n- Second fact\n- First fact"
	if block != want {
		t.Fatalf("block =\n%q\nwant\n%q", block, want)
	}

	// Per-user isolation.
	_, blockB, err := Enrich(ctx, e.store, b.ID, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if blockB != "About the user:\n- Bob only" {
		t.Fatalf("bob block = %q", blockB)
	}

	// No enabled memories → clean no-op (empty block, no error).
	u3 := &store.User{Email: "carol@example.com", Name: "C", Role: store.RoleUser}
	if err := e.store.Users.Insert(ctx, u3); err != nil {
		t.Fatal(err)
	}
	srcs, block, err = Enrich(ctx, e.store, u3.ID, "hi")
	if err != nil || srcs != nil || block != "" {
		t.Fatalf("empty enrich = (%+v, %q, %v)", srcs, block, err)
	}
	// Nil store / empty user → no-op, never a panic.
	if _, block, err := Enrich(ctx, nil, a.ID, "hi"); err != nil || block != "" {
		t.Fatalf("nil-store enrich = (%q, %v)", block, err)
	}
	if _, block, err := Enrich(ctx, e.store, "", "hi"); err != nil || block != "" {
		t.Fatalf("empty-user enrich = (%q, %v)", block, err)
	}
}

func TestEnrichHardCutAt2000Chars(t *testing.T) {
	e := newEnv(t)
	u := &store.User{Email: "alice@example.com", Name: "A", Role: store.RoleUser}
	ctx := context.Background()
	if err := e.store.Users.Insert(ctx, u); err != nil {
		t.Fatal(err)
	}

	// 30 memories of 94 chars each; header+newline costs 16, each line 97.
	pad := strings.Repeat("a", 91)
	var contents []string
	for i := 0; i < 30; i++ {
		c := fmt.Sprintf("%s-%02d", pad, i) // contents[i], i=0 oldest … 29 newest
		if utf8.RuneCountInString(c) != 94 {
			t.Fatalf("seed content width changed: %d", utf8.RuneCountInString(c))
		}
		e.seed(u.ID, c, true, SourceManual)
		contents = append(contents, c)
	}

	_, block, err := Enrich(ctx, e.store, u.ID, "q")
	if err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(block); n > MaxInjectRunes {
		t.Fatalf("block %d runes exceeds %d cap", n, MaxInjectRunes)
	}
	lines := strings.Split(block, "\n")
	got := lines[1:] // drop the header
	// Budget math: floor((2000 - 16) / 97) = 20 lines.
	if len(got) != 20 {
		t.Fatalf("kept %d lines, want 20 (block %q…)", len(got), block[:80])
	}
	// Kept slice is exactly the 20 NEWEST memories, newest-first.
	for i, line := range got {
		wantLine := "- " + contents[len(contents)-1-i]
		if line != wantLine {
			t.Fatalf("line %d = %q, want %q", i, line, wantLine)
		}
	}
	if strings.Contains(block, "- "+contents[0]) { // oldest dropped
		t.Fatal("oldest memory survived the cut")
	}
}

func TestInjectBlockDefensiveTruncation(t *testing.T) {
	// Unreachable through the API (501-char rows are rejected), but a row
	// larger than the block budget must still be cut to it, not overflow.
	oversized := strings.Repeat("z", MaxInjectRunes+100)
	block := injectBlock([]string{oversized, "second"})
	if n := utf8.RuneCountInString(block); n > MaxInjectRunes {
		t.Fatalf("block %d runes exceeds cap", n)
	}
	if lines := strings.Split(block, "\n"); len(lines) != 2 { // header + one truncated line
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if injectBlock(nil) != "" || injectBlock([]string{"", "  "}) != "" {
		t.Fatal("empty inputs must render no block")
	}
}
