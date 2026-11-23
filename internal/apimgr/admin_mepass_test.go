package apimgr

// Tests for the /api/me/password hardening (per-session 5/min throttle,
// shared bcrypt semaphore, byte-based length wording). Lives in its own file
// to keep ownership boundaries from colliding with apimgr_test.go.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"chatgo/internal/store"
)

type mePassEnvT struct {
	D       *Deps
	handler http.Handler
	cookie  string
}

// newMePassEnv wires POST /api/me/password behind the real Require
// middleware with a live session cookie for a password account.
func newMePassEnv(t *testing.T) *mePassEnvT {
	t.Helper()
	d, _ := testDeps(t)

	// Cheap hash so the repeated-attempt tests do not burn cost-12 bcrypts.
	h, err := bcrypt.GenerateFromPassword([]byte("current-pass-1"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	hash := string(h)
	u := &store.User{Email: "rotate@example.com", Name: "R", Role: store.RoleUser, PasswordHash: &hash}
	if err := d.Store.Users.Insert(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	sess, err := d.Auth.CreateSession(context.Background(), u.ID, "test", "192.0.2.5")
	if err != nil {
		t.Fatal(err)
	}

	hnd := &adminH{d: d}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/me/password", hnd.handleMePassword)
	return &mePassEnvT{D: d, handler: d.Auth.Require(mux), cookie: sess.Token}
}

func (e *mePassEnvT) post(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/me/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "chatgo_session", Value: e.cookie})
	req.RemoteAddr = "192.0.2.5:4321"
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// TestMePasswordRateLimitPerSession: 5 attempts admitted, 6th (and even a
// correct-password attempt) gets 429 — current-password guessing is bounded.
func TestMePasswordRateLimitPerSession(t *testing.T) {
	env := newMePassEnv(t)

	body := `{"current_password":"wrong","new_password":"brand-new-pass"}`
	for i := range 5 {
		rec := env.post(t, body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401 (body %q)", i+1, rec.Code, rec.Body)
		}
	}
	rec := env.post(t, body)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: status = %d, want 429 (body %q)", rec.Code, rec.Body)
	}

	// The CORRECT current password is throttled while the bucket is dry.
	ok := env.post(t, `{"current_password":"current-pass-1","new_password":"brand-new-pass"}`)
	if ok.Code != http.StatusTooManyRequests {
		t.Fatalf("throttled-but-correct attempt: status = %d, want 429", ok.Code)
	}
}

// Length errors speak in BYTES (bcrypt truncation boundary), and cheap length
// checks run before any bcrypt work.
func TestMePasswordByteWording(t *testing.T) {
	env := newMePassEnv(t)

	rec := env.post(t, `{"current_password":"current-pass-1","new_password":"ééé"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bytes") {
		t.Fatalf("short unicode password: %d %q, want 400 mentioning bytes", rec.Code, rec.Body)
	}
	rec = env.post(t, `{"current_password":"current-pass-1","new_password":"`+strings.Repeat("a", 73)+`"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bytes") {
		t.Fatalf("oversized password: %d %q, want 400 mentioning bytes", rec.Code, rec.Body)
	}
}

// Happy path: correct current password rotates the hash, revokes old
// sessions and issues a fresh one.
func TestMePasswordRotateAndRevoke(t *testing.T) {
	env := newMePassEnv(t)

	rec := env.post(t, `{"current_password":"current-pass-1","new_password":"brand-new-pass"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d %q", rec.Code, rec.Body)
	}
	u, err := env.D.Store.Users.GetByEmail(context.Background(), "rotate@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if u.PasswordHash == nil || bcrypt.CompareHashAndPassword([]byte(*u.PasswordHash), []byte("brand-new-pass")) != nil {
		t.Error("password hash not rotated")
	}
	// Old cookie's session row is gone; the response set a replacement.
	if _, err := env.D.Store.Sessions.Get(context.Background(), env.cookie, 9_999_999_999); err == nil {
		t.Error("old session survived rotation")
	}
	if sc := rec.Header().Get("Set-Cookie"); !strings.Contains(sc, "chatgo_session=") {
		t.Errorf("no replacement session cookie in %q", sc)
	}
}
