package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// OriginGuard is the browser-side cross-site-request gate (SECREV4 H1) for
// cookie-authenticated state changes. Auth is session-cookie based, so a
// malicious page could otherwise force a logged-in visitor's browser into
// POST/PUT/PATCH/DELETE calls on /api (logout-CSRF, share creation, provider
// edits…). Cookie SameSite already blocks *cross-site* sends; this closes the
// same-site-subdomain hole (evil.corp.example.com vs app.corp.example.com are
// same-site to the browser but not to us) and makes the defense independent
// of the cookie flag.
//
// Policy is deliberately narrow:
//
//   - Non-mutating methods (GET/HEAD/OPTIONS) are never touched — safe by
//     spec, and OIDC's browser GET callbacks must keep working.
//   - A request carrying neither Origin nor Referer passes. Server-to-server
//     callers (curl, CLI, ck_ tokens on /v1) send neither; browsers attach
//     Origin to every CORS-mode mutation and Referer to same-origin form
//     posts, so a mutating browser request without any provenance signal
//     cannot be produced here.
//   - If either header IS present it must name exactly this Host (scheme
//     ignored, default port normalized, case-insensitive). Garbage, "null",
//     or a foreign host answers 403 origin_mismatch — fail closed.
//
// It must run AFTER CapJSONBody (reject oversized bodies without parsing
// them) and BEFORE AuthPolicy (a cross-site request is refused even before
// its credentials are examined).
func OriginGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mutatingMethod(r.Method) || !guardedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		claim, present := originClaim(r)
		if !present {
			next.ServeHTTP(w, r) // CLI / server-to-server contract
			return
		}
		if strings.EqualFold(hostOnly(claim), hostOnly(r.Host)) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, `{"error":"origin_mismatch"}`, http.StatusForbidden)
	})
}

func mutatingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// guardedPath covers only the cookie-authenticated surface (/api). /v1 is
// deliberately NOT guarded: its contract is Authorization: Bearer ck_… —
// browsers never send the credential implicitly, so cross-site requests
// cannot authenticate there and the guard would buy nothing while risking
// breakage for non-browser clients that do send an Origin.
func guardedPath(p string) bool {
	return strings.HasPrefix(p, "/api/") || p == "/api"
}

// originClaim returns the provenance claim (Origin wins over Referer) and
// whether either header was present at all.
func originClaim(r *http.Request) (string, bool) {
	if o := r.Header.Get("Origin"); o != "" {
		return o, true
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		if err != nil || u.Host == "" {
			return ref, true // unparseable → claim fails hostOnly below → 403
		}
		return u.Scheme + "://" + u.Host, true
	}
	return "", false
}

// hostOnly lowercases a host and strips the default port (":80" http,
// ":443" https) so http://host:80 == http://host and the Host header (which
// omits the default port) compares equal.
func hostOnly(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimSuffix(host, "/")
	if h, port, err := net.SplitHostPort(host); err == nil {
		if port == "80" || port == "443" {
			return h
		}
	}
	return host
}
