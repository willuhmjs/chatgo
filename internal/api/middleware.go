package api

import (
	"net/http"

	"chatgo/internal/api/httpx"
)

// Middleware re-exports (httpx) + the security-header set required by the
// contract: CSP, nosniff, X-Robots-Tag noindex, Referrer-Policy same-origin.
//
// CSP note: Vite emits hashed asset filenames (assets/index-<hash>.js), so no
// unsafe-inline/eval is needed for scripts — default-src 'self' suffices.
// style-src needs 'unsafe-inline' for Svelte's injected styles.

// Recover converts panics to 500 JSON.
var Recover = httpx.Recover

// RequestLog logs method/path/status/duration only.
var RequestLog = httpx.RequestLog

// CapJSONBody caps non-multipart request bodies (1MB contract default).
var CapJSONBody = httpx.CapJSONBody

// SecurityHeaders sets the CSP + privacy headers on every response.
func SecurityHeaders(next http.Handler) http.Handler {
	return httpx.SecurityHeaders(next)
}
