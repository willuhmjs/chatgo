// URL ingest tests: readable-text extraction from canned HTML, the SSRF IP
// guard behind a fake resolver (zero real DNS/HTTP anywhere), title/name
// derivation and the endpoint flow with the fetch seam stubbed.
package kb

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// ---------- extraction ----------

func TestExtractReadableText(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "full page: head/script/style gone, blocks become lines",
			src: `<html><head><title>T</title><meta charset="utf-8">` +
				`<style>body{color:red}</style><script>var secret = "x";</script></head>` +
				`<body><h1>Title</h1><p>First para</p>` +
				`<ul><li>one</li><li>two</li></ul></body></html>`,
			want: "Title\nFirst para\none\ntwo",
		},
		{
			name: "plain text passes through",
			src:  "just two\nlines of text",
			want: "just two\nlines of text",
		},
		{
			name: "inline tags join with spaces",
			src:  `some<b>bold</b>text and <a href="x">docs</a> now`,
			want: "some bold text and docs now",
		},
		{
			name: "entities decode, nbsp normalizes",
			src:  `&amp;&nbsp;&copy; &#8212; caf&eacute;`,
			want: "& © — café",
		},
		{
			name: "script body with embedded tag text never leaks",
			src:  `<p>keep</p><script>var x = "</p> hide <div>me";</script><p>after</p>`,
			want: "keep\nafter",
		},
		{
			name: "unterminated script swallows its tail",
			src:  `<p>keep</p><script>var x = 1; if (a<b) {}`,
			want: "keep",
		},
		{
			name: "comments and noscript drop",
			src:  `<p>a</p><!-- secret comment --><noscript><p>ns</p></noscript><p>b</p>`,
			want: "a\nb",
		},
		{
			name: "escaped angle brackets stay text",
			src:  `use &lt;script&gt; carefully`,
			want: "use <script> carefully",
		},
		{
			name: "whitespace runs collapse",
			src:  "<p>lots   of\t spaces\u00a0here</p>",
			want: "lots of spaces here",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractReadableText(tc.src); got != tc.want {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestExtractPageTitle(t *testing.T) {
	cases := []struct{ src, want string }{
		{`<head><title>Hello &amp; World</title></head>`, "Hello & World"},
		{`<title lang="en">Attr</title>`, "Attr"},
		{"<title>\n  Multi\n line\n</title>", "Multi line"},
		{`<title>A &lt;b&gt; C</title>`, "A <b> C"},
		{`<head></head>`, ""},
		{``, ""},
	}
	for _, tc := range cases {
		if got := extractPageTitle(tc.src); got != tc.want {
			t.Fatalf("title(%q) = %q, want %q", tc.src, got, tc.want)
		}
	}
}

func TestURLFileName(t *testing.T) {
	if got := urlFileName("My Title", "example.com"); got != "My Title (example.com)" {
		t.Fatalf("titled = %q", got)
	}
	if got := urlFileName("  Multi\n Line  ", "example.com"); got != "Multi Line (example.com)" {
		t.Fatalf("spaced = %q", got)
	}
	if got := urlFileName("", "example.org"); got != "example.org" {
		t.Fatalf("untitled = %q", got)
	}
	if got := urlFileName(strings.Repeat("é", 300), "example.com"); len([]rune(got)) != 200 {
		t.Fatalf("runecap len = %d, want 200", len([]rune(got)))
	}
}

// ---------- SSRF guard (fake resolver, no network) ----------

// withResolver swaps the package lookup for the duration of one test.
func withResolver(t *testing.T, fn func(ctx context.Context, host string) ([]net.IP, error)) {
	t.Helper()
	orig := lookupIPs
	lookupIPs = fn
	t.Cleanup(func() { lookupIPs = orig })
}

func TestGuardURLHost(t *testing.T) {
	const (
		wantNoErr = 0
		wantBad   = http.StatusBadRequest
	)
	cases := []struct {
		name string
		url  string
		ips  []net.IP // resolver answers ("" resolver host never consulted for literals)
		lerr error    // resolver error
		want int
	}{
		{name: "public literal", url: "http://93.184.216.34/x", want: wantNoErr},
		{name: "public v6 literal", url: "https://[2606:2800:220:1:248:1893:25c8:1946]/", want: wantNoErr},
		{name: "loopback v4 literal", url: "http://127.0.0.1/x", want: wantBad},
		{name: "loopback v4 non-zero", url: "http://127.1.2.3/x", want: wantBad},
		{name: "loopback v6 literal", url: "http://[::1]/", want: wantBad},
		{name: "mapped loopback", url: "http://[::ffff:127.0.0.1]/", want: wantBad},
		{name: "rfc1918", url: "http://10.1.2.3/", want: wantBad},
		{name: "rfc1918 192.168", url: "http://192.168.0.1/", want: wantBad},
		{name: "rfc1918 172.16", url: "http://172.16.5.5/", want: wantBad},
		{name: "ula v6", url: "http://[fd12:3456::1]/", want: wantBad},
		{name: "metadata", url: "http://169.254.169.254/latest/meta-data/", want: wantBad},
		{name: "link-local v6", url: "http://[fe80::1]/", want: wantBad},
		{name: "multicast v4", url: "http://224.0.0.1/", want: wantBad},
		{name: "multicast v6", url: "http://[ff02::1]/", want: wantBad},
		{name: "unspecified v4", url: "http://0.0.0.0/", want: wantBad},
		{name: "unspecified v6", url: "http://[::]/", want: wantBad},
		{name: "cgnat", url: "http://100.64.0.1/", want: wantBad},
		{name: "6to4 smuggling 127.0.0.1", url: "http://[2002:7f00:1::]/", want: wantBad},
		{name: "teredo range", url: "http://[2001:0000:4136:e378::1]/", want: wantBad},
		{name: "ftp scheme", url: "ftp://example.com/", want: wantBad},
		{name: "file scheme", url: "file:///etc/passwd", want: wantBad},
		{name: "no host", url: "http:///x", want: wantBad},
		{name: "credentials", url: "http://u:p@93.184.216.34/", want: wantBad},
		{name: "not a url", url: "not a url", want: wantBad},
		{name: "dns public", url: "http://example.com/", ips: []net.IP{net.ParseIP("93.184.216.34")}, want: wantNoErr},
		{name: "dns public v6", url: "https://example.com/", ips: []net.IP{net.ParseIP("2606:2800:220:1:248:1893:25c8:1946")}, want: wantNoErr},
		// THE mixed-answer case: one private answer in the set rejects ALL.
		{name: "dns mixed public+private", url: "http://evil.example/", ips: []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.1")}, want: wantBad},
		{name: "dns loopback answer", url: "http://evil.example/", ips: []net.IP{net.ParseIP("127.0.0.1")}, want: wantBad},
		{name: "dns empty answer", url: "http://example.com/", ips: []net.IP{}, want: wantBad},
		{name: "dns failure", url: "http://example.com/", lerr: errors.New("no such host"), want: wantBad},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withResolver(t, func(context.Context, string) ([]net.IP, error) {
				return tc.ips, tc.lerr
			})
			u, err := url.Parse(tc.url)
			if err != nil {
				// url.Parse failures are the handler's job to report; here
				// every malformed case is also 400-shaped.
				if tc.want == wantBad {
					return
				}
				t.Fatalf("parse: %v", err)
			}
			err = guardURLHost(context.Background(), u)
			if tc.want == wantNoErr {
				if err != nil {
					t.Fatalf("guard(%s) = %v, want nil", tc.url, err)
				}
				return
			}
			var he httpError
			if !errors.As(err, &he) || he.status != tc.want {
				t.Fatalf("guard(%s) = %v, want status %d", tc.url, err, tc.want)
			}
		})
	}
}

func TestGuardURLHostLiteralSkipsDNS(t *testing.T) {
	called := false
	withResolver(t, func(context.Context, string) ([]net.IP, error) {
		called = true
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	})
	if err := guardURLHost(context.Background(), mustParseURL(t, "http://127.0.0.1/")); err == nil {
		t.Fatal("loopback literal allowed")
	}
	if err := guardURLHost(context.Background(), mustParseURL(t, "https://93.184.216.34/")); err != nil {
		t.Fatalf("public literal blocked: %v", err)
	}
	if called {
		t.Fatal("DNS consulted for IP literals")
	}
}

func mustParseURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// ---------- endpoint (fetch seam stubbed: no sockets) ----------

func stubFetch(t *testing.T, page *fetchedPage, ferr error) *string {
	t.Helper()
	orig := fetchPage
	var gotURL string
	fetchPage = func(_ context.Context, rawURL string) (*fetchedPage, error) {
		gotURL = rawURL
		return page, ferr
	}
	t.Cleanup(func() { fetchPage = orig })
	return &gotURL
}

func TestURLIngestEndpoint(t *testing.T) {
	e := newTestEnv(t)
	id := e.createCollection(e.tokA, "Web")

	page := &fetchedPage{
		Body:     []byte(`<html><head><title>Golang Guide</title><script>evil()</script></head><body><p>golang modules &amp; tooling</p></body></html>`),
		Title:    "Golang Guide",
		FinalURL: "https://example.com/guide",
		Host:     "example.com",
	}
	gotURL := stubFetch(t, page, nil)

	st, m, raw := e.json("POST", "/api/kb/"+id+"/url", e.tokA, map[string]any{"url": "https://example.com/guide"})
	if st != http.StatusCreated {
		t.Fatalf("url ingest: %d %q", st, raw)
	}
	if *gotURL != "https://example.com/guide" {
		t.Fatalf("fetch got %q", *gotURL)
	}
	if m["name"] != "Golang Guide (example.com)" {
		t.Fatalf("name = %v", m["name"])
	}
	if m["mime"] != "text/uri-list" {
		t.Fatalf("mime = %v", m["mime"])
	}
	if m["id"] == nil {
		t.Fatalf("missing id: %v", m)
	}
	if s, _ := m["id"].(string); s == "" {
		t.Fatalf("empty id: %v", m)
	}

	// The file lists normally and its text (entity-decoded, script-dropped,
	// Source-persisted) is searchable.
	st, m, _ = e.json("GET", "/api/kb/"+id+"/files", e.tokA, nil)
	if st != 200 || len(m["items"].([]any)) != 1 {
		t.Fatalf("file list: %d %v", st, m)
	}
	st, m, raw = e.json("POST", "/api/kb/"+id+"/search", e.tokA, map[string]any{"q": "golang"})
	if st != 200 {
		t.Fatalf("search: %d %q", st, raw)
	}
	hits := m["items"].([]any)
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1: %v", len(hits), hits)
	}
	content, _ := hits[0].(map[string]any)["content"].(string)
	if !strings.Contains(content, "golang modules & tooling") || strings.Contains(content, "evil") {
		t.Fatalf("content = %q", content)
	}
	if !strings.HasPrefix(content, "Source: https://example.com/guide") {
		t.Fatalf("origin url not persisted in content: %q", content)
	}

	// Caller-supplied name wins over the page title.
	st, m, raw = e.json("POST", "/api/kb/"+id+"/url", e.tokA,
		map[string]any{"url": "https://example.com/guide", "name": "My Doc"})
	if st != http.StatusCreated {
		t.Fatalf("named url ingest: %d %q", st, raw)
	}
	if m["name"] != "My Doc (example.com)" {
		t.Fatalf("named = %v", m["name"])
	}

	// Titleless page → bare hostname.
	stubFetch(t, &fetchedPage{Body: []byte("plain text body"), FinalURL: "https://plain.test/x", Host: "plain.test"}, nil)
	st, m, raw = e.json("POST", "/api/kb/"+id+"/url", e.tokA, map[string]any{"url": "https://plain.test/x"})
	if st != http.StatusCreated || m["name"] != "plain.test" {
		t.Fatalf("hostnamed = %d %v", st, m)
	}
}

// The error-propagation checks run as user B: every /url attempt consumes a
// token from the shared per-user 5/min upload bucket, and A's budget is
// already spent by the validation probes above.
func TestURLIngestErrorsAndOwnership(t *testing.T) {
	e := newTestEnv(t)
	idA := e.createCollection(e.tokA, "Web A")
	idB := e.createCollection(e.tokB, "Web B")

	// Validation happens before any fetch (A: 4 spend tokens, ≤ 5/min).
	if st, _, _ := e.json("POST", "/api/kb/"+idA+"/url", e.tokA, map[string]any{"url": "  "}); st != 400 {
		t.Fatalf("blank url: %d, want 400", st)
	}
	if st, _, _ := e.json("POST", "/api/kb/"+idA+"/url", e.tokA, map[string]any{"url": "x"}); st == 201 {
		t.Fatal("schemeless url accepted")
	}
	if st, _, _ := e.json("POST", "/api/kb/"+idA+"/url", e.tokA, map[string]any{
		"url": "https://example.com", "name": strings.Repeat("n", 121)}); st != 400 {
		t.Fatalf("long name: %d, want 400", st)
	}
	if st, _, _ := e.json("POST", "/api/kb/"+idA+"/url", e.tokA, nil); st != 400 {
		t.Fatalf("no body: %d, want 400", st)
	}
	// Ownership + auth boundaries match the files routes (B: token 1).
	stubFetch(t, &fetchedPage{Body: []byte("<p>golang</p>"), FinalURL: "https://x.test/", Host: "x.test"}, nil)
	if st, _, _ := e.json("POST", "/api/kb/"+idA+"/url", e.tokB, map[string]any{"url": "https://x.test/"}); st != 404 {
		t.Fatalf("B into A's collection: %d, want 404", st)
	}
	if st, _, _ := e.json("POST", "/api/kb/"+idA+"/url", "", map[string]any{"url": "https://x.test/"}); st != 401 {
		t.Fatalf("anonymous: %d, want 401", st)
	}
	// Fetch-stage errors carry their status through fail() (B: tokens 2–3).
	stubFetch(t, nil, httpErr(http.StatusUnsupportedMediaType, "URL did not return UTF-8 text content"))
	if st, m, _ := e.json("POST", "/api/kb/"+idB+"/url", e.tokB, map[string]any{"url": "https://x.test/bin"}); st != 415 {
		t.Fatalf("binary page: %d %v, want 415", st, m)
	}
	stubFetch(t, nil, httpErr(http.StatusBadGateway, "could not fetch the URL"))
	st, m, _ := e.json("POST", "/api/kb/"+idB+"/url", e.tokB, map[string]any{"url": "https://x.test/"})
	if st != 502 {
		t.Fatalf("fetch failure: %d %v, want 502", st, m)
	}
	if msg, _ := m["error"].(string); msg != "could not fetch the URL" {
		t.Fatalf("error envelope = %q", msg)
	}
	// Nothing persisted for the failed attempts.
	if _, m, _ := e.json("GET", "/api/kb/"+idB+"/files", e.tokB, nil); len(m["items"].([]any)) != 0 {
		t.Fatalf("failed ingests left rows: %v", m)
	}
}
