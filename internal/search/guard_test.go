package search

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgo/internal/config"
)

func testCfg(allowLoopback bool, trusted ...string) *config.Config {
	return &config.Config{AllowLoopback: allowLoopback, TrustedURLNets: trusted}
}

// stubDNS replaces hostname resolution for the duration of the test.
func stubDNS(t *testing.T, answers map[string][]net.IP) {
	t.Helper()
	prev := lookupIPAddr
	lookupIPAddr = func(_ context.Context, host string) ([]net.IPAddr, error) {
		ips, ok := answers[host]
		if !ok {
			return nil, errors.New("no such host: " + host)
		}
		out := make([]net.IPAddr, 0, len(ips))
		for _, ip := range ips {
			out = append(out, net.IPAddr{IP: ip})
		}
		return out, nil
	}
	t.Cleanup(func() { lookupIPAddr = prev })
}

// stubDial records dial targets and fails every dial (no real network in tests).
func stubDial(t *testing.T) *[]string {
	t.Helper()
	var dialed []string
	prev := dialContext
	dialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
		dialed = append(dialed, network+" "+addr)
		return nil, errors.New("stub dial: refused")
	}
	t.Cleanup(func() { dialContext = prev })
	return &dialed
}

func guardedClient(dial func(context.Context, string, string) (net.Conn, error)) *http.Client {
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DialContext: dial},
	}
}

func TestLoopbackAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	dial, err := ResolveAndCheck(context.Background(), srv.URL, testCfg(true))
	if err != nil {
		t.Fatalf("ResolveAndCheck(loopback allowed): %v", err)
	}
	resp, err := guardedClient(dial).Get(srv.URL)
	if err != nil {
		t.Fatalf("GET via guarded dialer: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("got %d %q, want 200 ok", resp.StatusCode, body)
	}
}

func TestLoopbackBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	if _, err := ResolveAndCheck(context.Background(), srv.URL, testCfg(false)); err == nil {
		t.Fatal("ResolveAndCheck(loopback blocked) succeeded, want error")
	} else if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error %q should mention the blocked address", err)
	}
}

func TestMetadataIPLiteralRejected(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://[::ffff:169.254.169.254]/", // IPv6-mapped bypass form
		"https://169.254.169.254/",
	} {
		if _, err := ResolveAndCheck(ctx, raw, testCfg(true)); err == nil {
			t.Errorf("ResolveAndCheck(%q) succeeded, want metadata rejection even with AllowLoopback", raw)
		}
	}
}

func TestCGNATAndPrivate(t *testing.T) {
	ctx := context.Background()
	// CGNAT: always rejected, even if (absurdly) trusted.
	cfg := testCfg(false, "100.64.0.0/10")
	if _, err := ResolveAndCheck(ctx, "http://100.64.1.2/", cfg); err == nil {
		t.Error("CGNAT 100.64.1.2 allowed, want rejection")
	}
	// RFC1918: rejected unless trusted.
	if _, err := ResolveAndCheck(ctx, "http://10.1.2.3/", testCfg(false)); err == nil {
		t.Error("private 10.1.2.3 allowed without TrustedURLNets")
	}
	if _, err := ResolveAndCheck(ctx, "http://10.1.2.3/", testCfg(false, "10.0.0.0/8")); err != nil {
		t.Errorf("private 10.1.2.3 in TrustedURLNets rejected: %v", err)
	}
}

// TestDNSRebindingStyle: the resolver answers [public, loopback]; with
// loopback blocked only the public IP may ever be dialled — dial never
// re-resolves and never targets the poisoned answer.
func TestDNSRebindingStyle(t *testing.T) {
	stubDNS(t, map[string][]net.IP{
		"rebind.test": {net.ParseIP("93.184.216.34"), net.ParseIP("127.0.0.1")},
	})
	dialed := stubDial(t)

	dial, err := ResolveAndCheck(context.Background(), "http://rebind.test:8080/", testCfg(false))
	if err != nil {
		t.Fatalf("ResolveAndCheck: %v", err)
	}
	if _, err := dial(context.Background(), "tcp", "rebind.test:8080"); err == nil {
		t.Fatal("stub dial unexpectedly succeeded")
	}
	if len(*dialed) != 1 {
		t.Fatalf("dial attempts = %v, want exactly 1 (pinned public IP)", *dialed)
	}
	want := "tcp 93.184.216.34:8080"
	if (*dialed)[0] != want {
		t.Fatalf("dialed %q, want %q (loopback answer must stay pinned out)", (*dialed)[0], want)
	}
}

// TestDialsPinnedIPNotHostname proves the dialer connects to the validated
// IP literal; combined with hostname "evil.test", a live request still lands
// on the 127.0.0.1 test server (pinning works end-to-end).
func TestDialsPinnedIPNotHostname(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "pinned")
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("server port: %v", err)
	}
	stubDNS(t, map[string][]net.IP{"svc.test": {net.ParseIP("127.0.0.1")}})

	dial, err := ResolveAndCheck(context.Background(), "http://svc.test:"+port+"/", testCfg(true))
	if err != nil {
		t.Fatalf("ResolveAndCheck: %v", err)
	}
	resp, err := guardedClient(dial).Get("http://svc.test:" + port + "/")
	if err != nil {
		t.Fatalf("GET via pinned dialer: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "pinned" {
		t.Fatalf("body = %q, want pinned", body)
	}
}

func TestDialRefusesForeignHost(t *testing.T) {
	stubDNS(t, map[string][]net.IP{"good.test": {net.ParseIP("93.184.216.34")}})
	stubDial(t)

	dial, err := ResolveAndCheck(context.Background(), "https://good.test/", testCfg(false))
	if err != nil {
		t.Fatalf("ResolveAndCheck: %v", err)
	}
	if _, err := dial(context.Background(), "tcp", "other.test:443"); err == nil ||
		!strings.Contains(err.Error(), "refused") {
		t.Fatalf("foreign-host dial err = %v, want refusal", err)
	}
}

func TestSchemeAndHostValidation(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{"ftp://example.com/", "file:///etc/passwd", "http:///no-host", "//nowhere"} {
		if _, err := ResolveAndCheck(ctx, raw, testCfg(true)); err == nil {
			t.Errorf("ResolveAndCheck(%q) succeeded, want error", raw)
		}
	}
}

func TestIPv6MappedLoopbackRejected(t *testing.T) {
	stubDNS(t, map[string][]net.IP{
		"v6map.test": {net.ParseIP("::ffff:127.0.0.1")},
	})
	if _, err := ResolveAndCheck(context.Background(), "http://v6map.test/", testCfg(false)); err == nil {
		t.Fatal("IPv6-mapped loopback allowed with AllowLoopback=false")
	}
}

func TestPolicyUnitCases(t *testing.T) {
	p := &ipPolicy{allowLoopback: false, trusted: []*net.IPNet{mustCIDR("fd00:1234::/64")}}
	cases := []struct {
		ip    string
		allow bool
	}{
		{"8.8.8.8", true},
		{"2001:4860:4860::8888", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"10.0.0.1", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"fc00::1", false},
		{"fd00:1234::5", true}, // ULA inside TrustedURLNets
		{"169.254.169.254", false},
		{"169.254.1.1", false},
		{"100.64.0.1", false},
		{"100.127.255.255", false},
		{"0.0.0.0", false},
		{"::", false},
		{"224.0.0.1", false},
	}
	for _, c := range cases {
		err := p.allow(net.ParseIP(c.ip))
		if (err == nil) != c.allow {
			t.Errorf("allow(%s) err=%v, want allow=%v", c.ip, err, c.allow)
		}
	}
}
