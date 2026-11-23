package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"chatgo/internal/config"
	"chatgo/internal/store"
)

func TestLoginSuccessWrongPasswordAndGenericErrors(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })
	env.seedUser(t, "alice@example.com", store.RoleUser, "correct-horse-9")

	// Wrong password and unknown email are indistinguishable.
	bad := env.login("alice@example.com", "wrong-password")
	assertJSONErr(t, bad, http.StatusUnauthorized)
	unknown := env.login("nobody@example.com", "wrong-password")
	assertJSONErr(t, unknown, http.StatusUnauthorized)
	if bad.errMsg() != unknown.errMsg() {
		t.Errorf("error messages leak account existence: %q vs %q", bad.errMsg(), unknown.errMsg())
	}

	// Correct credentials: 200 + session cookie + user payload.
	ok := env.login("ALICE@example.com", "correct-horse-9") // case-insensitive lookup
	if ok.Status != http.StatusOK {
		t.Fatalf("login status = %d, body %q", ok.Status, ok.Body)
	}
	var u userViewJSON
	ok.json(t, &u)
	if u.Email != "alice@example.com" || u.Role != store.RoleUser || u.ID == "" {
		t.Errorf("user payload = %+v", u)
	}
	sc := ok.Header.Get("Set-Cookie")
	if !containsCookie(sc, "chatgo_session=") {
		t.Errorf("missing session cookie in %q", sc)
	}

	// The cookie authenticates the mandatory-auth group.
	thing := env.get("/api/thing")
	if thing.Status != http.StatusOK || string(thing.Body) != "thing:alice@example.com" {
		t.Errorf("/api/thing = %d %q", thing.Status, thing.Body)
	}
	me := env.get("/api/auth/me")
	if me.Status != http.StatusOK {
		t.Fatalf("/api/auth/me = %d %q", me.Status, me.Body)
	}
	me.json(t, &u)
	if u.Email != "alice@example.com" {
		t.Errorf("/me email = %q", u.Email)
	}

	// /api/auth/me touched last_active_at (contract §auth).
	got, err := env.Store.Users.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastActiveAt == 0 {
		t.Error("last_active_at not touched after /api/auth/me")
	}
}

// userViewJSON mirrors the auth package response shape.
type userViewJSON struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

func TestLoginRateLimitPerIP(t *testing.T) {
	env := newTestEnv(t, nil)
	// Seed with a cost-4 hash so the failed compares are fast even under
	// -race; otherwise the 12s token refill out-runs the loop.
	h, err := bcrypt.GenerateFromPassword([]byte("correct-horse-9"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	hash := string(h)
	if err := env.Store.Users.Insert(context.Background(), &store.User{
		Email: "rl@example.com", Name: "RL", Role: store.RoleUser, PasswordHash: &hash,
	}); err != nil {
		t.Fatal(err)
	}

	for i := range 5 {
		r := env.login("rl@example.com", "bad-password")
		if r.Status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401 (body %q)", i+1, r.Status, r.Body)
		}
	}
	r := env.login("rl@example.com", "correct-horse-9") // even the right password is rate-limited
	assertJSONErr(t, r, http.StatusTooManyRequests)
}

// TestFirstUserBecomesAdmin pins the bootstrap: the very first account — even
// under the default approve policy — is promoted to admin so the instance is
// never ownerless.
func TestFirstUserBecomesAdmin(t *testing.T) {
	env := newTestEnv(t, nil) // approve policy, empty DB
	env.register("owner@example.com", "secretpass-1", "Owner")
	u, err := env.Store.Users.GetByEmail(context.Background(), "owner@example.com")
	if err != nil {
		t.Fatalf("first user missing: %v", err)
	}
	if u.Role != store.RoleAdmin {
		t.Errorf("first user role = %q, want admin", u.Role)
	}
}

func TestRegisterOpenAutoLogsIn(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })
	env.seedFirstUser(t)

	r := env.register("carl@example.com", "secretpass-1", "Carl")
	if r.Status != http.StatusOK {
		t.Fatalf("register status = %d, body %q", r.Status, r.Body)
	}
	var u userViewJSON
	r.json(t, &u)
	if u.Role != store.RoleUser {
		t.Errorf("role = %q, want user", u.Role)
	}
	if !containsCookie(r.Header.Get("Set-Cookie"), "chatgo_session=") {
		t.Error("open registration should set a session cookie")
	}
	if got := env.get("/api/thing"); got.Status != http.StatusOK {
		t.Errorf("authenticated /api/thing = %d", got.Status)
	}

	// Duplicate / weak / invalid inputs.
	dup := env.register("carl@example.com", "secretpass-1", "Carl")
	assertJSONErr(t, dup, http.StatusConflict)
	weak := env.register("dora@example.com", "short", "Dora")
	assertJSONErr(t, weak, http.StatusBadRequest)
	badEmail := env.register("not-an-email", "secretpass-1", "X")
	assertJSONErr(t, badEmail, http.StatusBadRequest)
}

func TestRegisterApproveGatesPendingUser(t *testing.T) {
	env := newTestEnv(t, nil) // default policy: approve
	env.seedFirstUser(t)

	r := env.register("pen@example.com", "secretpass-1", "Pen")
	if r.Status != http.StatusCreated {
		t.Fatalf("register status = %d, body %q", r.Status, r.Body)
	}
	var u userViewJSON
	r.json(t, &u)
	if u.Role != store.RolePending {
		t.Errorf("role = %q, want pending", u.Role)
	}
	if containsCookie(r.Header.Get("Set-Cookie"), "chatgo_session=") {
		t.Error("pending registration must not receive a session cookie")
	}

	// Pending users can log in and read /api/auth/me, nothing else.
	if ok := env.login("pen@example.com", "secretpass-1"); ok.Status != http.StatusOK {
		t.Fatalf("pending login = %d %q", ok.Status, ok.Body)
	}
	me := env.get("/api/auth/me")
	if me.Status != http.StatusOK {
		t.Errorf("/api/auth/me for pending = %d, want 200", me.Status)
	}
	thing := env.get("/api/thing")
	assertJSONErr(t, thing, http.StatusForbidden)

	// …and logout still works (Attach route).
	if out := env.do("POST", "/api/auth/logout", nil); out.Status != http.StatusOK {
		t.Errorf("pending logout = %d", out.Status)
	}
}

func TestRegisterClosed(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "closed" })
	// AUTHGATE §2 supersedes the old "closed always 403" expectation: an
	// EMPTY instance admits one admin bootstrap (see
	// TestFirstRunBootstrapUnderClosedPolicy). Closed stays closed once any
	// account exists, so seed one to exercise the steady state.
	env.seedFirstUser(t)

	r := env.register("ghost@example.com", "secretpass-1", "Ghost")
	assertJSONErr(t, r, http.StatusForbidden)

	if _, err := env.Store.Users.GetByEmail(context.Background(), "ghost@example.com"); err != store.ErrNotFound {
		t.Errorf("closed registration created a user (err = %v)", err)
	}

	// Login for pre-existing users still works while registration is closed.
	env.seedUser(t, "old@example.com", store.RoleUser, "secretpass-1")
	if ok := env.login("old@example.com", "secretpass-1"); ok.Status != http.StatusOK {
		t.Errorf("existing-user login = %d, want 200", ok.Status)
	}
}

func TestAuthConfig(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) {
		cfg.Registration = "approve"
		cfg.OIDC = config.OIDCConfig{Issuer: "https://sso.example.com", ClientID: "cid", DisplayName: "Corp"}
	})
	r := env.get("/api/auth/config")
	if r.Status != http.StatusOK {
		t.Fatalf("config = %d %q", r.Status, r.Body)
	}
	var v struct {
		OIDCEnabled  bool   `json:"oidc_enabled"`
		OIDCName     string `json:"oidc_name"`
		Registration string `json:"registration"`
	}
	r.json(t, &v)
	if !v.OIDCEnabled || v.OIDCName != "Corp" || v.Registration != "approve" {
		t.Errorf("config = %+v", v)
	}
}

func TestLogoutClearsSession(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })
	env.seedUser(t, "out@example.com", store.RoleUser, "secretpass-1")
	if ok := env.login("out@example.com", "secretpass-1"); ok.Status != http.StatusOK {
		t.Fatalf("login = %d", ok.Status)
	}
	if got := env.get("/api/thing"); got.Status != http.StatusOK {
		t.Fatalf("pre-logout /api/thing = %d", got.Status)
	}
	if out := env.do("POST", "/api/auth/logout", nil); out.Status != http.StatusOK {
		t.Fatalf("logout = %d", out.Status)
	}
	// Session row gone, cookie cleared → back to 401.
	var n int
	if err := env.Store.DB.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("sessions left: %d (err %v)", n, err)
	}
	if got := env.get("/api/thing"); got.Status != http.StatusUnauthorized {
		t.Errorf("post-logout /api/thing = %d, want 401", got.Status)
	}
}

func TestOIDCDisabledLoginRedirects(t *testing.T) {
	env := newTestEnv(t, nil)
	r := env.get("/api/auth/oidc/login")
	if r.Status != http.StatusFound || r.Location != "/login?error=oidc_disabled" {
		t.Errorf("oidc login w/o config = %d %q", r.Status, r.Location)
	}
}

// Registration runs bcrypt cost 12, so it must be throttled like login:
// 5/min/IP bucket admits its burst, then everything gets a 429.
func TestRegisterRateLimit(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })
	env.seedFirstUser(t) // not the first-user-admin bootstrap

	for i := range 5 {
		r := env.register(fmt.Sprintf("spam%d@example.com", i), "secretpass-1", "")
		if r.Status != http.StatusOK {
			t.Fatalf("register %d: status = %d, want 200 (body %q)", i+1, r.Status, r.Body)
		}
	}
	r := env.register("spam99@example.com", "secretpass-1", "")
	assertJSONErr(t, r, http.StatusTooManyRequests)
}

// Login/Register treat the bcrypt limit as 72 BYTES, not characters, and a
// 73+ byte login attempt is a plain 401 (short-circuited, no panic, no 500).
func TestOversizedPasswordRejected(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })
	env.seedUser(t, "bytes@example.com", store.RoleUser, "correct-horse-9")

	long := strings.Repeat("é", 40) // 120 bytes, 40 "characters"
	res := env.login("bytes@example.com", long)
	assertJSONErr(t, res, http.StatusUnauthorized)

	weak := env.register("mb@example.com", long, "MB")
	assertJSONErr(t, weak, http.StatusBadRequest)
	if !strings.Contains(weak.errMsg(), "bytes") {
		t.Errorf("weak-password error should speak in bytes, got %q", weak.errMsg())
	}
	// 64 multibyte chars = 128 bytes: rejected on registration…
	ok80 := env.register("ok80@example.com", strings.Repeat("a", 73), "X")
	assertJSONErr(t, ok80, http.StatusBadRequest)
}

// Promotion is bootstrap-only: ADMIN_EMAIL grants NOTHING (SECREV4 F-4 — before
// this it pinned the owner deterministically, which meant whoever registered
// that address first owned the instance). Under "approve" both accounts come up
// pending; the owner is only ever the first account ever created, promoted
// inside the insert transaction (see TestFirstAccountIsAdmin).
func TestAdminEmailNeverPromotes(t *testing.T) {
	env := newTestEnv(t, nil) // approve policy
	env.seedFirstUser(t)      // existing account is NOT admin
	env.Cfg.AdminEmail = "boss@example.com"

	r := env.register("BOSS@example.com", "secretpass-1", "Boss") // case-folds to AdminEmail
	if r.Status != http.StatusCreated {
		t.Fatalf("register = %d %q, want 201 pending", r.Status, r.Body)
	}
	u, err := env.Store.Users.GetByEmail(context.Background(), "boss@example.com")
	if err != nil {
		t.Fatalf("admin email user missing: %v", err)
	}
	if u.Role != store.RolePending {
		t.Errorf("ADMIN_EMAIL role = %q, want pending (no promotion)", u.Role)
	}

	// Everyone else still follows the policy.
	env.register("pleb2@example.com", "secretpass-1", "Pleb")
	pleb, err := env.Store.Users.GetByEmail(context.Background(), "pleb2@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if pleb.Role != store.RolePending {
		t.Errorf("non-admin-email role = %q, want pending", pleb.Role)
	}
}

// The one admin grant that remains: the FIRST account ever created (id 1) is
// promoted inside the registration transaction, regardless of policy. Every
// later account — including a later one with the same address — is ordinary.
func TestFirstAccountIsAdminBootstrap(t *testing.T) {
	env := newTestEnv(t, nil) // approve policy: everyone else lands pending

	r := env.register("owner@example.com", "secretpass-1", "Owner")
	// Role is admin, so HandleRegister logs the owner straight in (200 +
	// session) instead of the 201 pending answer — first account only.
	if r.Status != http.StatusOK {
		t.Fatalf("first register = %d %q", r.Status, r.Body)
	}
	u, err := env.Store.Users.GetByEmail(context.Background(), "owner@example.com")
	if err != nil {
		t.Fatalf("first account missing: %v", err)
	}
	if u.Role != store.RoleAdmin {
		t.Errorf("first account role = %q, want admin", u.Role)
	}

	// A second account — same instance, later — is never admin.
	env.register("late@example.com", "secretpass-1", "Late")
	later, err := env.Store.Users.GetByEmail(context.Background(), "late@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if later.Role != store.RolePending {
		t.Errorf("second account role = %q, want pending", later.Role)
	}
}

// Secure-flag matrix: SECURE_COOKIE override beats everything, else
// PUBLIC_URL-https, else this request's X-Forwarded-Proto first hop.
func TestSessionCookieSecureFlag(t *testing.T) {
	cases := []struct {
		name         string
		secureCookie string
		publicURL    string // "" keeps the http test-server URL
		forwarded    string // X-Forwarded-Proto value
		wantSecure   bool
	}{
		{"plain http", "", "", "", false},
		{"xff https auto", "", "", "https", true},
		{"xff first hop wins", "", "", "https, http", true},
		{"xff http", "", "", "http", false},
		{"override false beats xff", "false", "", "https", false},
		{"override true beats plain http", "true", "", "", true},
		{"public url https", "", "https://chat.example.com", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, func(cfg *config.Config) {
				cfg.Registration = "open"
				cfg.SecureCookie = tc.secureCookie
			})
			if tc.publicURL != "" {
				env.Cfg.PublicURL = tc.publicURL
			}
			env.seedUser(t, "sec@example.com", store.RoleUser, "secretpass-1")

			body, _ := json.Marshal(map[string]string{"email": "sec@example.com", "password": "secretpass-1"})
			req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "192.0.2.7:5555"
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			rec := httptest.NewRecorder()
			env.Auth.HandleLogin(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("login = %d %q", rec.Code, rec.Body)
			}
			sc := rec.Header().Get("Set-Cookie")
			if !strings.Contains(sc, "chatgo_session=") {
				t.Fatalf("no session cookie in %q", sc)
			}
			if got := strings.Contains(strings.ToLower(sc), "secure"); got != tc.wantSecure {
				t.Errorf("Secure flag = %v, want %v (Set-Cookie %q)", got, tc.wantSecure, sc)
			}
		})
	}
}

// TestLoginAndRegisterRejectOversizedEmail pins the L8-M2 email wall on the
// unauthenticated surfaces: shape is validated (validEmail, ≤254B) BEFORE
// bcrypt / store query / audit insert, so a 4KB email answers a plain 400
// and never reaches audit_log at all — the audit clamp stays as defense in
// depth for other callers. Registration already 400ed via the same gate
// inside Register; this test covers BOTH so the trace is executable:
// login → validEmail in HandleLogin (no audit row); register → validEmail
// in Auth.Register (400 envelope, house-style).
func TestLoginAndRegisterRejectOversizedEmail(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.Registration = "open" })

	big := strings.Repeat("x", 4096) + "@example.com"

	r := env.login(big, "whatever-1234")
	assertJSONErr(t, r, http.StatusBadRequest)
	var n int
	if err := env.Store.DB.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("audit rows after malformed-email login = %d, want 0", n)
	}

	// Registration: same shape gate, same 400 envelope.
	r = env.register(big, "secretpass-1", "Bot")
	assertJSONErr(t, r, http.StatusBadRequest)

	// A legitimate email inside the 254B shape cap (and the 320B audit cap)
	// still registers: open policy → 200 + session.
	r = env.register(strings.Repeat("a", 200)+"@example.com", "secretpass-1", "Long")
	if r.Status != http.StatusOK {
		t.Fatalf("213-byte email register status = %d, want 200 (body %q)", r.Status, r.Body)
	}

	// Well-formed unknown emails keep the OLD behavior: 401 + one audit row
	// carrying the whole (small) email — no regression on the login.fail
	// audit trail.
	r = env.login("nobody@example.com", "whatever-1234")
	assertJSONErr(t, r, http.StatusUnauthorized)
	var email string
	if err := env.Store.DB.QueryRow(
		`SELECT email FROM audit_log WHERE action = 'login.fail' ORDER BY id DESC LIMIT 1`).Scan(&email); err != nil {
		t.Fatal(err)
	}
	if email != "nobody@example.com" {
		t.Errorf("login.fail audit email = %q, want nobody@example.com", email)
	}
}
