package auth

// White-box tests for the throttle machinery (≥60s touch per contract §auth,
// keyed limiter buckets, janitor sweeps) — needs unexported state, hence an
// in-package test file.

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
	for i := range loginPerMin {
		if !a.loginAllowed("1.2.3.4") {
			t.Fatalf("attempt %d denied, want allow (burst %d)", i+1, loginPerMin)
		}
	}
	if a.loginAllowed("1.2.3.4") {
		t.Error("6th attempt in burst window allowed, want block")
	}
	if !a.loginAllowed("5.6.7.8") {
		t.Error("other IP blocked, want independent bucket")
	}
}

func TestAllowKeyedBuckets(t *testing.T) {
	a := &Auth{limiters: map[string]*loginBucket{}}

	// Global register bucket: 10/min burst 10, then block.
	for i := range registerGlobalPerMin {
		if !a.Allow("register:global", registerGlobalPerMin) {
			t.Fatalf("global register attempt %d denied, want allow", i+1)
		}
	}
	if a.Allow("register:global", registerGlobalPerMin) {
		t.Error("global register attempt 11 allowed, want block")
	}

	// A denied global bucket must not starve unrelated keys; the mepass
	// bucket starts at burst 5 (this check consumes the first token).
	if !a.Allow("mepass:sess-1", PasswordChangePerMin) {
		t.Error("unrelated key blocked by register bucket")
	}
	for i := range PasswordChangePerMin - 1 {
		if !a.Allow("mepass:sess-1", PasswordChangePerMin) {
			t.Fatalf("password-change attempt %d denied, want allow", i+2)
		}
	}
	if a.Allow("mepass:sess-1", PasswordChangePerMin) {
		t.Error("6th password-change attempt allowed, want block")
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
	a.lastTouch.Store("user:"+u.ID, time.Now().Add(-2*time.Minute))
	a.touch(u.ID, "tok-1")
	if v := lastActive(); v < time.Now().Unix()-5 {
		t.Errorf("touch after throttle window = %d, want ~now", v)
	}

	// Session activity uses a separate key so one window cannot mask the other.
	if _, err := st.DB.Exec(`UPDATE users SET last_active_at = 5 WHERE id = ?1`, u.ID); err != nil {
		t.Fatal(err)
	}
	a.lastTouch.Store("sess:tok-1", time.Now().Add(-2*time.Minute))
	a.touch(u.ID, "tok-1")
	if v, ok := a.lastTouch.Load("sess:tok-1"); !ok {
		t.Error("session throttle key missing")
	} else if ts, isTime := v.(time.Time); !isTime || ts.Before(time.Now().Add(-5*time.Second)) {
		t.Errorf("stale session key was not refreshed: %v", v)
	}
	if v := lastActive(); v != 5 {
		t.Errorf("user touch leaked session window: last_active_at = %d, want 5 (user key still fresh)", v)
	}
}

func TestJanitorPassPrunesSessionsAndTouchState(t *testing.T) {
	name := "file:mem_auth_internal_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared"
	st, err := store.Open(name, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := New(&config.Config{
		SessionSecret: hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
		Registration:  "open",
	}, st)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	u := &store.User{Email: "janitor@example.com", Role: store.RoleUser}
	if err := a.Store.Users.Insert(ctx, u); err != nil {
		t.Fatal(err)
	}
	mk := func(token string, expiresAt int64) {
		if err := a.Store.Sessions.Create(ctx, &store.Session{
			Token: token, UserID: u.ID, CreatedAt: 1, ExpiresAt: expiresAt, LastSeenAt: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("expired-tok", time.Now().Add(-time.Hour).Unix())
	mk("live-tok", time.Now().Add(time.Hour).Unix())

	// lastTouch entries: one past limiterIdleFor, one fresh.
	a.lastTouch.Store("sess:gone-1", time.Now().Add(-11*time.Minute))
	a.lastTouch.Store("sess:live-1", time.Now())

	a.janitorPass(ctx)

	for tok, want := range map[string]bool{"expired-tok": false, "live-tok": true} {
		_, err := a.Store.Sessions.Get(ctx, tok, time.Now().Unix())
		if got := err == nil; got != want {
			t.Errorf("session %s present = %v, want %v", tok, got, want)
		}
	}
	if _, ok := a.lastTouch.Load("sess:gone-1"); ok {
		t.Error("stale lastTouch entry survived janitor sweep")
	}
	if _, ok := a.lastTouch.Load("sess:live-1"); !ok {
		t.Error("fresh lastTouch entry evicted by janitor sweep")
	}

	// RunJanitor must return promptly once ctx is cancelled (graceful stop).
	jctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { a.RunJanitor(jctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunJanitor did not stop on context cancel")
	}
}
