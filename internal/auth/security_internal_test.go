package auth

// White-box tests for the OIDC state cookie (HKDF key, tamper/expiry
// rejection), RequestIP forwarding logic, and the race-free first-admin
// bootstrap — all unexported internals, hence an in-package file.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgo/internal/config"
	"chatgo/internal/store"
)

var internalSecret = []byte("0123456789abcdef0123456789abcdef")

func newInternalStore(t *testing.T) *store.Store {
	t.Helper()
	name := "file:mem_auth_wb_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared"
	st, err := store.Open(name, internalSecret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestOIDCStateCookieRoundTrip(t *testing.T) {
	f, err := newOIDCFlow(&config.Config{}, internalSecret)
	if err != nil {
		t.Fatal(err)
	}
	v := f.stateCookieValue("state-1", "nonce-1", "verifier-1", time.Now().Unix())

	state, nonce, pkce, ok := f.parseStateCookie(v)
	if !ok || state != "state-1" || nonce != "nonce-1" || pkce != "verifier-1" {
		t.Fatalf("round-trip = (%q,%q,%q,%v), want the parts back", state, nonce, pkce, ok)
	}
	if parts := strings.Split(v, "."); len(parts) != oidcStateCookieMax {
		t.Errorf("cookie value has %d parts, want %d: %q", len(parts), oidcStateCookieMax, v)
	}
}

func TestOIDCStateCookieRejectsTamperAndExpiry(t *testing.T) {
	f, err := newOIDCFlow(&config.Config{}, internalSecret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()

	// Tampered payload, correctly re-serialised by the attacker → rejected.
	parts := strings.Split(f.stateCookieValue("state-1", "nonce-1", "pkce-1", now), ".")
	parts[0] = "evil"
	if _, _, _, ok := f.parseStateCookie(strings.Join(parts, ".")); ok {
		t.Error("tampered state accepted")
	}

	// Expired (older than stateCookieMaxAge) but correctly signed → rejected
	// server-side; browser Max-Age alone is not enforcement.
	expired := f.stateCookieValue("state-1", "nonce-1", "pkce-1", now-int64(stateCookieMaxAge/time.Second)-60)
	if _, _, _, ok := f.parseStateCookie(expired); ok {
		t.Error("expired state cookie accepted")
	}
	// Just inside the window → accepted.
	fresh := f.stateCookieValue("state-1", "nonce-1", "pkce-1", now-int64(stateCookieMaxAge/time.Second)+60)
	if _, _, _, ok := f.parseStateCookie(fresh); !ok {
		t.Error("in-window state cookie rejected")
	}

	for _, junk := range []string{"", "a.b.c", "a.b.c.d", "a.b.c.d.e.f", "a.b.c.no-int.hmac"} {
		if _, _, _, ok := f.parseStateCookie(junk); ok {
			t.Errorf("junk %q accepted", junk)
		}
	}

	// Future-dated cookies are equally untrustworthy.
	future := f.stateCookieValue("s", "n", "p", now+int64(stateCookieMaxAge/time.Second)+60)
	if _, _, _, ok := f.parseStateCookie(future); ok {
		t.Error("future-dated state cookie accepted")
	}
}

func TestOIDCStateKeyIsHKDFDerived(t *testing.T) {
	f, err := newOIDCFlow(&config.Config{}, internalSecret)
	if err != nil {
		t.Fatal(err)
	}
	const payload = "s.n.p.100"
	got := f.stateHMAC(payload)

	// The old scheme signed with the raw SESSION_SECRET bytes; the derived
	// key must produce a different MAC (domain separation from other
	// SESSION_SECRET users).
	raw := hmac.New(sha256.New, internalSecret)
	raw.Write([]byte(payload))
	if got == hex.EncodeToString(raw.Sum(nil)) {
		t.Error("state HMAC still uses the raw secret (no HKDF derivation)")
	}
	if h := hex.EncodeToString(f.stateKey); len(h) != 64 {
		t.Errorf("derived key length = %d hex chars, want 64", len(h))
	}
}

func TestRequestIPForwarding(t *testing.T) {
	mk := func(remoteAddr, xff, xrp string, trusted []string) string {
		a, err := New(&config.Config{
			SessionSecret:  hex.EncodeToString(internalSecret),
			TrustedProxies: trusted,
		}, newInternalStore(t))
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remoteAddr
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		if xrp != "" {
			r.Header.Set("X-Real-IP", xrp)
		}
		return a.RequestIP(r)
	}

	trusted := []string{"10.0.0.0/8", "203.0.113.7/32"}

	cases := []struct {
		name, remote, xff, xrp, want string
		trusted                      []string
	}{
		{"no headers", "192.0.2.9:4444", "", "", "192.0.2.9", nil},
		// Spoof defence: without TrustedProxies every forwarded header is
		// noise (this is what closed the login-throttle bypass).
		{"xff ignored without trusted proxies", "192.0.2.9:4444", "1.2.3.4, 5.6.7.8", "9.9.9.9", "192.0.2.9", nil},
		{"x-real-ip ignored without trusted proxies", "192.0.2.9:4444", "", "9.9.9.9", "192.0.2.9", nil},
		// With TrustedProxies the walk runs RIGHT to LEFT: a forged leftmost
		// hop loses to the real peer the edge proxy appended.
		{"spoofed left hop loses", "10.0.0.2:1", "1.2.3.4, 203.0.113.9", "", "203.0.113.9", trusted},
		{"trusted hops skipped right-to-left", "10.0.0.2:1", "1.2.3.4, 10.9.9.9, 203.0.113.9", "", "203.0.113.9", trusted},
		{"all hops trusted falls back", "10.0.0.2:1", "10.1.2.3, 10.9.9.9", "", "10.0.0.2", trusted},
		{"x-real-ip honored when untrusted", "10.0.0.2:1", "", "203.0.113.9", "203.0.113.9", trusted},
		{"x-real-ip inside trusted net falls back", "10.0.0.2:1", "", "10.1.2.3", "10.0.0.2", trusted},
		{"garbage hop counts as untrusted", "10.0.0.2:1", "203.0.113.9, not-an-ip", "", "not-an-ip", trusted},
		{"empty hops skipped", "10.0.0.2:1", "1.2.3.4, , 203.0.113.9", "", "203.0.113.9", trusted},
	}
	for _, tc := range cases {
		if got := mk(tc.remote, tc.xff, tc.xrp, tc.trusted); got != tc.want {
			t.Errorf("%s: RequestIP = %q, want %q", tc.name, got, tc.want)
		}
	}

	// Sanity: net.ParseIP agrees with the fixture's IPv6 expectations.
	if ip := net.ParseIP("::1"); ip == nil {
		t.Fatal("fixture broken")
	}
}

// TestFirstAdminBootstrapIsAtomic hammers the empty-database window with two
// concurrent registrations: exactly one account may win the admin role.
func TestFirstAdminBootstrapIsAtomic(t *testing.T) {
	st := newInternalStore(t)
	a, err := New(&config.Config{
		SessionSecret: hex.EncodeToString(internalSecret),
		Registration:  "open",
	}, st)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	roles := make([]string, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			u, err := a.Register(context.Background(), net.IP([]byte{198, 51, 100, byte(i)}).String(),
				"user"+strconv.Itoa(i)+"@example.com", "secretpass-1", "")
			if err != nil {
				t.Errorf("register %d: %v", i, err)
				return
			}
			roles[i] = u.Role
		}(i)
	}
	wg.Wait()

	admins := 0
	for _, r := range roles {
		if r == store.RoleAdmin {
			admins++
		}
	}
	if admins != 1 {
		t.Errorf("admin winners = %d, want exactly 1 (roles %v)", admins, roles)
	}
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("admin rows in db = %d (err %v), want 1", n, err)
	}
	// Roles read back from the DB must match what Register returned.
	for i := 0; i < 4; i++ {
		got, err := st.Users.GetByEmail(context.Background(), "user"+strconv.Itoa(i)+"@example.com")
		if err != nil {
			t.Fatalf("user %d: %v", i, err)
		}
		if got.Role != roles[i] {
			t.Errorf("user %d role = %q, register reported %q", i, got.Role, roles[i])
		}
	}
}
