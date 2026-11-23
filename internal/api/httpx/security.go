package httpx

import "net/http"

// SecurityHeaders sets the frozen security headers (contract):
//
//	Content-Security-Policy: default-src 'self'; img-src 'self' data: blob:;
//	                         style-src 'self' 'unsafe-inline'; connect-src 'self';
//	                         frame-ancestors 'none'
//	X-Content-Type-Options:  nosniff
//	X-Robots-Tag:            noindex
//	Referrer-Policy:         same-origin
//
// Vite's hashed asset names (assets/*-<hash>.js) are same-origin, so
// script-src falls back to default-src 'self' with no exceptions needed.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Robots-Tag", "noindex")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
