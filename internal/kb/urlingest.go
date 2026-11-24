package kb

// URL ingest (POST /api/kb/{id}/url): fetch a web page server-side, reduce
// it to readable text and run it through the same chunk→embed pipeline as an
// uploaded text file.
//
// SSRF discipline: user-supplied URLs are the classic SSRF surface, so the
// guard is STRICTER than the shared search.ResolveAndCheck (which honors
// AllowLoopback/TrustedURLNets for operator-configured provider URLs): here
// every address that is not global unicast is refused outright, with no
// config override. The hostname is resolved once via DNS and ALL answers
// must pass (one private answer in a mixed A/AAAA set rejects the host).
// Redirects are never followed automatically: the client stops at the first
// 3xx and the loop re-runs the full guard on every hop (≤3 hops), which also
// closes the DNS-rebind-between-hops window hop-by-hop.
//
// The origin URL is persisted without a schema change: the file name becomes
// "Title (domain)" (or the bare host when the page has no title) plus a
// leading "Source: <final url>" line inside the indexed text, and the row's
// display mime is "text/uri-list" so clients can recognize URL-sourced files.

import (
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	// urlFetchTimeout bounds one full fetch exchange (all hops).
	urlFetchTimeout = 15 * time.Second
	// maxURLBodyBytes caps the fetched payload (io.LimitReader + 1 byte probe).
	maxURLBodyBytes int64 = 2 << 20
	// maxURLRedirects caps manually re-guarded hops after the first request.
	maxURLRedirects = 3
	// maxURLLen bounds the request-body "url" field.
	maxURLLen = 2048
	// maxURLNameLen bounds the caller-supplied "name" field.
	maxURLNameLen = 120
	// urlUserAgent — a normal-ish UA: some sites reject default Go agents.
	urlUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 ChatGoKB/1.0"
	// uriListMime marks URL-sourced rows (display only, never stored payload).
	uriListMime = "text/uri-list"
)

// lookupIPs resolves a hostname to every A/AAAA answer. Package-level so
// tests drive the guard with a fake resolver and never touch the network.
var lookupIPs = func(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

var (
	denyCGNAT  = mustPrefix("100.64.0.0/10")
	deny6to4   = mustPrefix("2002::/16")
	denyTeredo = mustPrefix("2001::/32")
)

func mustPrefix(cidr string) *net.IPNet {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic("kb: bad CIDR " + cidr)
	}
	return n
}

// urlIPBlocked is the no-override deny set: everything but global unicast.
// Beyond the loopback/private/link-local/multicast/unspecified predicates it
// also pins the same transition/CNAT ranges the house guard (search/guard.go)
// denies — 6to4/Teredo literals can embed a private v4 while looking like
// global unicast to Go's predicates — plus CGNAT, and the final
// IsGlobalUnicast check sweeps benchmark/reserved space.
func urlIPBlocked(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4 // unmap IPv6-mapped IPv4 before the checks
	}
	if ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if deny6to4.Contains(ip) || denyTeredo.Contains(ip) || denyCGNAT.Contains(ip) {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return true
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	return !ip.IsGlobalUnicast()
}

// guardURLHost validates one URL for fetching: http(s) only, host required,
// no embedded credentials, and every resolved address outside the deny set.
// Errors are client-safe (the offending IP is never echoed).
func guardURLHost(ctx context.Context, u *url.URL) error {
	bad := func(msg string) error { return httpErr(http.StatusBadRequest, msg) }
	if u.Scheme != "http" && u.Scheme != "https" {
		return bad("url must be an http(s) URL")
	}
	if u.User != nil {
		return bad("url must not embed credentials")
	}
	host := u.Hostname()
	if host == "" {
		return bad("url must include a host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if urlIPBlocked(ip) {
			return bad("url points at a loopback, private or reserved address")
		}
		return nil
	}
	ips, err := lookupIPs(ctx, host)
	if err != nil || len(ips) == 0 {
		return bad("url host could not be resolved")
	}
	for _, ip := range ips {
		if urlIPBlocked(ip) {
			return bad("url resolves to a loopback, private or reserved address")
		}
	}
	return nil
}

// fetchedPage is one successful URL fetch.
type fetchedPage struct {
	Body     []byte // raw page bytes (≤ maxURLBodyBytes)
	Title    string // extracted <title> ("" when absent)
	FinalURL string // URL after redirects
	Host     string // hostname of the final URL
}

// fetchPage is the injectable seam the handler calls (tests stub it so the
// handler flow never performs real network I/O).
var fetchPage = func(ctx context.Context, rawURL string) (*fetchedPage, error) {
	return doFetchPage(ctx, rawURL)
}

// doFetchPage performs the guarded GET with manual redirect following.
func doFetchPage(ctx context.Context, rawURL string) (*fetchedPage, error) {
	client := &http.Client{
		Timeout: urlFetchTimeout,
		// Manual hops only: automatic following would dial un-guarded hosts.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()

	next := rawURL
	for hop := 0; ; hop++ {
		u, err := url.Parse(strings.TrimSpace(next))
		if err != nil {
			return nil, httpErr(http.StatusBadRequest, "url must be an http(s) URL")
		}
		if err := guardURLHost(ctx, u); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, httpErr(http.StatusBadRequest, "url must be an http(s) URL")
		}
		req.Header.Set("User-Agent", urlUserAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5")

		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, httpErr(http.StatusBadGateway, "timed out fetching the URL")
			}
			return nil, httpErr(http.StatusBadGateway, "could not fetch the URL")
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc, lerr := resp.Location()
			drainClose(resp.Body)
			if lerr != nil {
				return nil, httpErr(http.StatusBadGateway, "url redirect carried no location")
			}
			if hop >= maxURLRedirects {
				return nil, httpErr(http.StatusBadGateway, "url redirected too many times (max 3)")
			}
			next = loc.String() // resolved against the current URL by resp.Location
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, httpErr(http.StatusBadGateway, "URL responded "+resp.Status)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxURLBodyBytes+1))
		if err != nil {
			return nil, httpErr(http.StatusBadGateway, "could not read the URL response")
		}
		if int64(len(body)) > maxURLBodyBytes {
			return nil, httpErr(http.StatusRequestEntityTooLarge, "page exceeds the 2 MiB limit")
		}
		if _, err := classifyText(body); err != nil {
			return nil, httpErr(http.StatusUnsupportedMediaType, "URL did not return UTF-8 text content")
		}
		return &fetchedPage{
			Body:     body,
			Title:    extractPageTitle(string(body)),
			FinalURL: u.String(),
			Host:     u.Hostname(),
		}, nil
	}
}

func drainClose(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, 4<<10))
	_ = rc.Close()
}

// --- readable-text extraction (careful stdlib/regexp, no new deps) --------

var (
	htmlCommentRE = regexp.MustCompile(`(?is)<!--.*?-->`)
	// Comment strip, a non-greedy element-pair strip (RE2 has no
	// backreferences, so the close-tag alternation re-lists the open tags)
	// plus an unterminated-open fallback for script-ish payloads whose
	// content must never leak into the index. head is deliberately NOT in the
	// fallback list: HTML5 pages may legally omit </head> and its remaining
	// content (title/meta text) is mostly harmless.
	htmlDropREs  = pairAndOpenRes([]string{"script", "style", "noscript", "template", "svg", "iframe", "object", "form", "select", "head"})
	htmlAnyTagRE = regexp.MustCompile(`(?s)<[a-zA-Z!/][^>]*>`)
	htmlTitleRE  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	hSpaceRE     = regexp.MustCompile(`[ \t\v\f\x{00a0}]+`)
	hSpaceNlRE   = regexp.MustCompile(`[ ]+\n`)
	hNlSpaceRE   = regexp.MustCompile(`\n[ ]+`)
	hBreaksRE    = regexp.MustCompile(`\n{2,}`)
)

func pairAndOpenRes(tags []string) []*regexp.Regexp {
	ors := strings.Join(tags, "|")
	return []*regexp.Regexp{
		htmlCommentRE,
		regexp.MustCompile(`(?is)<(?:` + ors + `)\b[^>]*>.*?</(?:` + ors + `)\s*>`),
		regexp.MustCompile(`(?is)<(?:script|style|noscript|template|svg|iframe|object)\b[^>]*>.*`),
	}
}

// blockTags become newlines when they open or close; every other tag becomes
// a single space so adjacent words never fuse together.
var blockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "br": true,
	"caption": true, "dd": true, "details": true, "div": true, "dl": true, "dt": true,
	"figcaption": true, "figure": true, "footer": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "header": true, "hr": true,
	"li": true, "main": true, "nav": true, "ol": true, "p": true, "pre": true,
	"section": true, "summary": true, "table": true, "tbody": true, "td": true,
	"tfoot": true, "th": true, "thead": true, "tr": true, "ul": true,
}

// tagName extracts the lowercase element name from a raw "<tag ...>" match.
func tagName(tag string) string {
	i := 1
	if len(tag) > 2 && tag[1] == '/' {
		i = 2
	}
	j := i
	for j < len(tag) && ((tag[j] >= 'a' && tag[j] <= 'z') || (tag[j] >= 'A' && tag[j] <= 'Z')) {
		j++
	}
	return strings.ToLower(tag[i:j])
}

// extractReadableText reduces an HTML (or plain-text) page to text:
// comments + script/style/head-ish elements removed, block boundaries →
// newlines, entities unescaped, whitespace collapsed. Plain text passes
// through with only whitespace normalization, so non-HTML text URLs work.
func extractReadableText(src string) string {
	s := src
	for _, re := range htmlDropREs {
		s = re.ReplaceAllString(s, " ")
	}
	s = htmlAnyTagRE.ReplaceAllStringFunc(s, func(tag string) string {
		if blockTags[tagName(tag)] {
			return "\n"
		}
		return " "
	})
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = hSpaceRE.ReplaceAllString(s, " ")
	s = hSpaceNlRE.ReplaceAllString(s, "\n")
	s = hNlSpaceRE.ReplaceAllString(s, "\n")
	s = hBreaksRE.ReplaceAllString(s, "\n") // every block boundary → ONE newline
	return strings.TrimSpace(s)
}

// extractPageTitle returns the page's <title> text ("" when absent).
func extractPageTitle(src string) string {
	m := htmlTitleRE.FindStringSubmatch(src)
	if m == nil {
		return ""
	}
	t := html.UnescapeString(htmlAnyTagRE.ReplaceAllString(m[1], " "))
	t = strings.Join(strings.Fields(t), " ")
	if r := []rune(t); len(r) > 160 {
		t = string(r[:160])
	}
	return t
}

// urlFileName derives the kb_file display name: "Title (domain)" per the
// product spec, or the bare hostname when no title is available.
func urlFileName(title, host string) string {
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return sanitizeName(host)
	}
	return sanitizeName(fmt.Sprintf("%s (%s)", title, host))
}
