package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// OriginGuard matrix (SECREV4 H1): mutations under /api/ only; a present
// Origin/Referer must name this Host (scheme-insensitive, default-port
// normalized, case-insensitive); no provenance header = server-to-server
// contract, allowed; everything else fails closed with 403 origin_mismatch.
func TestOriginGuard(t *testing.T) {
	const teapot = http.StatusTeapot // sentinel for "guard allowed it through"
	guard := OriginGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(teapot)
	}))

	post := func(path, origin, referer, host string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if referer != "" {
			r.Header.Set("Referer", referer)
		}
		w := httptest.NewRecorder()
		guard.ServeHTTP(w, r)
		return w
	}

	t.Run("no provenance passes (CLI contract)", func(t *testing.T) {
		if w := post("/api/logout", "", "", "chat.local"); w.Code != teapot {
			t.Fatalf("no-header POST = %d, want pass-through", w.Code)
		}
	})
	t.Run("same-origin Origin passes", func(t *testing.T) {
		if w := post("/api/logout", "http://chat.local", "", "chat.local"); w.Code != teapot {
			t.Fatalf("same-origin POST = %d, want pass-through", w.Code)
		}
	})
	t.Run("cross-site Origin is refused", func(t *testing.T) {
		w := post("/api/logout", "http://evil.com", "", "chat.local")
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "origin_mismatch") {
			t.Fatalf("evil Origin = %d %q, want 403 origin_mismatch", w.Code, w.Body)
		}
	})
	t.Run("Referer is checked too", func(t *testing.T) {
		if w := post("/api/providers/x", "", "http://evil.com/p", "chat.local"); w.Code != http.StatusForbidden {
			t.Fatalf("evil Referer = %d, want 403", w.Code)
		}
		if w := post("/api/providers/x", "", "http://chat.local/admin", "chat.local"); w.Code != teapot {
			t.Fatalf("same-origin Referer = %d, want pass-through", w.Code)
		}
	})
	t.Run("garbage claims fail closed", func(t *testing.T) {
		for _, bad := range []string{"null", "http://", "://x", "evil.com"} {
			if w := post("/api/logout", bad, "", "chat.local"); w.Code != http.StatusForbidden {
				t.Errorf("Origin %q = %d, want 403 (fail closed)", bad, w.Code)
			}
		}
	})
	t.Run("default port and case normalize", func(t *testing.T) {
		if w := post("/api/logout", "http://chat.local:80", "", "chat.local"); w.Code != teapot {
			t.Errorf("explicit :80 = %d, want pass-through", w.Code)
		}
		if w := post("/api/logout", "https://chat.local:443", "", "chat.local"); w.Code != teapot {
			t.Errorf("explicit :443 = %d, want pass-through", w.Code)
		}
		if w := post("/api/logout", "http://CHAT.LOCAL", "", "chat.local"); w.Code != teapot {
			t.Errorf("uppercase host = %d, want pass-through", w.Code)
		}
	})
	t.Run("non-mutating methods never guarded", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/logout", nil)
		r.Host = "chat.local"
		r.Header.Set("Origin", "http://evil.com")
		w := httptest.NewRecorder()
		guard.ServeHTTP(w, r)
		if w.Code != teapot {
			t.Fatalf("GET with evil Origin = %d, want pass-through", w.Code)
		}
	})
	t.Run("non-API paths unguarded", func(t *testing.T) {
		for _, p := range []string{"/healthz", "/v1/chat/completions", "/s/tok", "/"} {
			if w := post(p, "http://evil.com", "", "chat.local"); w.Code != teapot {
				t.Errorf("%s with evil Origin = %d, want pass-through (token auth or static)", p, w.Code)
			}
		}
	})
	t.Run("all mutating methods guarded", func(t *testing.T) {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			r := httptest.NewRequest(m, "/api/chats", nil)
			r.Host = "chat.local"
			r.Header.Set("Origin", "http://evil.com")
			w := httptest.NewRecorder()
			guard.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s = %d, want 403", m, w.Code)
			}
		}
	})
}
