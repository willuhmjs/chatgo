// Tags repo tests (BE-apichat owned). Prefix: TestTags.

package store

import (
	"context"
	"errors"
	"testing"
)

func TestTagsRepoCRUDAndWalls(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	alice := mustUser(t, s, "at@example.com", RoleUser)
	bob := mustUser(t, s, "bt@example.com", RoleUser)

	t1 := &Tag{UserID: alice.ID, Name: " Go ", Color: "#6366f1"}
	if err := s.Tags.Create(ctx, t1); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if t1.Name != "Go" {
		t.Errorf("name not trimmed: %q", t1.Name)
	}
	if err := s.Tags.Create(ctx, &Tag{UserID: alice.ID, Name: "Go"}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate err = %v, want ErrDuplicate", err)
	}
	// Same name is fine for a different user.
	if err := s.Tags.Create(ctx, &Tag{UserID: bob.ID, Name: "Go"}); err != nil {
		t.Errorf("cross-user same name: %v", err)
	}

	t1.Color = "#22c55e"
	if err := s.Tags.Update(ctx, t1); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// Cross-user mutation must miss (user-scoped UPDATE).
	steal := *t1
	steal.UserID = bob.ID
	steal.Name = "stolen"
	if err := s.Tags.Update(ctx, &steal); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user Update err = %v, want ErrNotFound", err)
	}
	if err := s.Tags.Delete(ctx, t1.ID, bob.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user Delete err = %v, want ErrNotFound", err)
	}

	got, err := s.Tags.Get(ctx, t1.ID)
	if err != nil || got.Color != "#22c55e" {
		t.Errorf("after update: %+v, %v", got, err)
	}
}

func TestTagsSetChatTagsAndTagsForChats(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	alice := mustUser(t, s, "as@example.com", RoleUser)
	bob := mustUser(t, s, "bs@example.com", RoleUser)

	chat := &Chat{UserID: alice.ID, Title: "tagged"}
	if err := s.Chats.Create(ctx, chat); err != nil {
		t.Fatal(err)
	}
	t1 := &Tag{UserID: alice.ID, Name: "one"}
	t2 := &Tag{UserID: alice.ID, Name: "two"}
	bobTag := &Tag{UserID: bob.ID, Name: "mine"}
	for _, tg := range []*Tag{t1, t2, bobTag} {
		if err := s.Tags.Create(ctx, tg); err != nil {
			t.Fatal(err)
		}
	}

	// Happy path (duplicate input id deduped).
	if err := s.Tags.SetChatTags(ctx, chat.ID, alice.ID, []string{t1.ID, t2.ID, t1.ID, ""}); err != nil {
		t.Fatalf("SetChatTags: %v", err)
	}
	m, err := s.Tags.TagsForChats(ctx, []string{chat.ID})
	if err != nil {
		t.Fatalf("TagsForChats: %v", err)
	}
	if len(m[chat.ID]) != 2 {
		t.Fatalf("want 2 tags, got %d", len(m[chat.ID]))
	}

	// Replace with a smaller set.
	if err := s.Tags.SetChatTags(ctx, chat.ID, alice.ID, []string{t2.ID}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	m, _ = s.Tags.TagsForChats(ctx, []string{chat.ID})
	if len(m[chat.ID]) != 1 || m[chat.ID][0].ID != t2.ID {
		t.Errorf("replace failed: %+v", m[chat.ID])
	}

	// Foreign tag id → ErrNotFound, original set untouched.
	if err := s.Tags.SetChatTags(ctx, chat.ID, alice.ID, []string{t1.ID, bobTag.ID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign tag err = %v, want ErrNotFound", err)
	}
	m, _ = s.Tags.TagsForChats(ctx, []string{chat.ID})
	if len(m[chat.ID]) != 1 {
		t.Errorf("failed SetChatTags must not mutate: %+v", m[chat.ID])
	}
	// Foreign chat → ErrNotFound; bob cannot tag alice's chat with his own tag.
	if err := s.Tags.SetChatTags(ctx, chat.ID, bob.ID, []string{bobTag.ID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign chat err = %v, want ErrNotFound", err)
	}
	// Unknown tag id → ErrNotFound.
	if err := s.Tags.SetChatTags(ctx, chat.ID, alice.ID, []string{"nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown tag err = %v, want ErrNotFound", err)
	}

	// Empty set clears; soft-deleted chat rejected.
	if err := s.Tags.SetChatTags(ctx, chat.ID, alice.ID, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := s.Chats.Delete(ctx, chat.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Tags.SetChatTags(ctx, chat.ID, alice.ID, []string{t1.ID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted chat err = %v, want ErrNotFound", err)
	}

	// Deleting a tag cascades out of chat_tags.
	c2 := &Chat{UserID: alice.ID, Title: "cascade"}
	if err := s.Chats.Create(ctx, c2); err != nil {
		t.Fatal(err)
	}
	if err := s.Tags.SetChatTags(ctx, c2.ID, alice.ID, []string{t1.ID, t2.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.Tags.Delete(ctx, t1.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	m, err = s.Tags.TagsForChats(ctx, []string{c2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(m[c2.ID]) != 1 || m[c2.ID][0].ID != t2.ID {
		t.Errorf("cascade failed: %+v", m[c2.ID])
	}

	// Empty input → empty map, no query.
	if empty, err := s.Tags.TagsForChats(ctx, nil); err != nil || len(empty) != 0 {
		t.Errorf("TagsForChats(nil) = %v, %v", empty, err)
	}
}
