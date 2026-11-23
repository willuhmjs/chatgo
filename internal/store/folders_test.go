// Folders repo tests (BE-apichat owned). Prefix: TestFolders.

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFoldersRepoCRUDAndWalls(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	alice := mustUser(t, s, "af@example.com", RoleUser)
	bob := mustUser(t, s, "bf@example.com", RoleUser)

	f := &Folder{UserID: alice.ID, Name: "  Work  "}
	if err := s.Folders.Create(ctx, f); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if f.ID == "" || f.Name != "Work" {
		t.Fatalf("Create filled wrong: id=%q name=%q", f.ID, f.Name)
	}

	long := &Folder{UserID: alice.ID, Name: strings.Repeat("x", MaxFolderNameLen+1)}
	if err := s.Folders.Create(ctx, long); !errors.Is(err, ErrInvalidName) {
		t.Errorf("oversized name err = %v, want ErrInvalidName", err)
	}
	if err := s.Folders.Create(ctx, &Folder{UserID: alice.ID, Name: "   "}); !errors.Is(err, ErrInvalidName) {
		t.Errorf("blank name err = %v, want ErrInvalidName", err)
	}

	// Bob cannot see or mutate Alice's folder.
	if _, err := s.Folders.Get(ctx, f.ID); err != nil {
		t.Fatalf("Get: %v", err)
	}
	other := *f
	other.UserID = bob.ID
	other.Name = "hijacked"
	if err := s.Folders.Update(ctx, &other); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user Update err = %v, want ErrNotFound", err)
	}
	if err := s.Folders.Delete(ctx, f.ID, bob.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user Delete err = %v, want ErrNotFound", err)
	}

	// Nested folder + cycle guard.
	child := &Folder{UserID: alice.ID, Name: "Projects", ParentID: &f.ID}
	if err := s.Folders.Create(ctx, child); err != nil {
		t.Fatalf("Create child: %v", err)
	}
	// Parent owned by bob → ErrNotFound (no existence leak).
	foreignParent := &Folder{UserID: bob.ID, Name: "Bobs"}
	if err := s.Folders.Create(ctx, foreignParent); err != nil {
		t.Fatal(err)
	}
	bad := *child
	bad.ParentID = &foreignParent.ID
	if err := s.Folders.Update(ctx, &bad); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign parent err = %v, want ErrNotFound", err)
	}
	// Cycle: Work under Projects (its own descendant).
	cyc := *f
	cyc.ParentID = &child.ID
	if err := s.Folders.Update(ctx, &cyc); !errors.Is(err, ErrParentCycle) {
		t.Errorf("cycle err = %v, want ErrParentCycle", err)
	}
	// Self-parent.
	self := *f
	self.ParentID = &f.ID
	if err := s.Folders.Update(ctx, &self); !errors.Is(err, ErrParentCycle) {
		t.Errorf("self-parent err = %v, want ErrParentCycle", err)
	}

	list, err := s.Folders.List(ctx, alice.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %d, %v", len(list), err)
	}
	// name order: "Projects" (child of Work) then "Work" (root).
	if list[0].Name != "Projects" || list[0].ParentID == nil || *list[0].ParentID != f.ID {
		t.Errorf("unexpected first folder: %+v", list[0])
	}
	if list[1].Name != "Work" || list[1].ParentID != nil {
		t.Errorf("unexpected second folder: %+v", list[1])
	}

	// Deleting a folder unfiles its chats (FK ON DELETE SET NULL).
	c := &Chat{UserID: alice.ID, Title: "filed", FolderID: &f.ID}
	if err := s.Chats.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.Folders.Delete(ctx, f.ID, alice.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Folders.Get(ctx, f.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(deleted) = %v", err)
	}
	got, err := s.Chats.Get(ctx, c.ID)
	if err != nil || got.FolderID != nil {
		t.Errorf("chat should be unfiled: %+v, %v", got, err)
	}
}
