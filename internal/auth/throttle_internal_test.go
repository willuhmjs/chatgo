package auth

// White-box test for the ≥60s touch throttle (contract §auth) — needs the
// unexported lastTouch map, hence an in-package test file.

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"chatgo/internal/config"
	"chatgo/internal/store"
)

func TestLoginAllowedBucket(t *testing.T) {
	a := &Auth{limiters: map[string]*loginBucket{}}
	for i := range loginBurst {
		if !a.loginAllowed("1.2.3.4") {
			t.Fatalf("attempt %d denied, want allow (burst 5)", i+1)
		}
	}
	if a.loginAllowed("1.2.3.4") {
		t.Error("6th attempt in burst window allowed, want block")
	}
	if !a.loginAllowed("5.6.7.8") {
		t.Error("other IP blocked, want independent bucket")
	}
}

func TestTouchThrottle(t *testing.T) {
	name := "file:mem_auth_internal_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared"
	st, err := store.Open(name, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		SessionSecret: hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
		Registration:  "approve",
	}
	a, err := New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	u := &store.User{Email: "touch@example.com", Role: store.RoleUser}
	if err := st.Users.Insert(ctx, u); err != nil {
		t.Fatal(err)
	}
	lastActive := func() int64 {
		got, err := st.Users.GetByID(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.LastActiveAt
	}

	// First touch writes "now".
	a.touch(u.ID, "tok-1")
	if v := lastActive(); v == 0 {
		t.Fatal("first touch did not stamp last_active_at")
	}

	// Immediate second touch is throttled (row stays pinned at 5).
	if _, err := st.DB.Exec(`UPDATE users SET last_active_at = 5 WHERE id = ?1`, u.ID); err != nil {
		t.Fatal(err)
	}
	a.touch(u.ID, "tok-1")
	if v := lastActive(); v != 5 {
		t.Errorf("touch within throttle window rewrote last_active_at to %d, want 5", v)
	}

	// After ≥60s the window expires and the next touch writes again.
	if _, err := st.DB.Exec(`UPDATE users SET last_active_at = 5 WHERE id = ?1`, u.ID); err != nil {
		t.Fatal(err)
	}
	a.lastTouch.Store("user:"+u.ID, time.Now().Unix()-120)
	a.touch(u.ID, "tok-1")
	if v := lastActive(); v < time.Now().Unix()-5 {
		t.Errorf("touch after throttle window = %d, want ~now", v)
	}

	// Session activity uses a separate key so one window cannot mask the other.
	if _, err := st.DB.Exec(`UPDATE users SET last_active_at = 5 WHERE id = ?1`, u.ID); err != nil {
		t.Fatal(err)
	}
	a.lastTouch.Store("sess:tok-1", time.Now().Unix()-120)
	a.touch(u.ID, "tok-1")
	if _, ok := a.lastTouch.Load("sess:tok-1"); !ok {
		t.Error("session throttle key missing")
	}
	if v, _ := a.lastTouch.Load("sess:tok-1"); v.(int64) < time.Now().Unix()-5 {
		t.Errorf("stale session key was not refreshed: %v", v)
	}
	if v := lastActive(); v != 5 {
		t.Errorf("user touch leaked session window: last_active_at = %d, want 5 (user key still fresh)", v)
	}
}
