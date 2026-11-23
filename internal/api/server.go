// Package api assembles the HTTP server: stdlib mux (Go 1.22 patterns),
// auth middleware slots per route group, security headers, /healthz and the
// embedded-SPA fallback (docs/CONTRACTS.md §HTTP surface).
package api

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"chatgo/internal/api/httpx"
	"chatgo/internal/apichat"
	"chatgo/internal/apimgr"
	"chatgo/internal/audit"
	"chatgo/internal/auth"
	"chatgo/internal/chat"
	"chatgo/internal/config"
	"chatgo/internal/files"
	"chatgo/internal/imggen"
	"chatgo/internal/kb"
	"chatgo/internal/library"
	"chatgo/internal/memory"
	"chatgo/internal/providers"
	"chatgo/internal/search"
	"chatgo/internal/store"
	"chatgo/internal/web"
)

// Deps is everything the server wires into the route registrars.
type Deps struct {
	Cfg       *config.Config
	Store     *store.Store
	Auth      *auth.Auth
	Chat      *chat.Service
	Search    *search.Service
	Files     *files.Service
	Providers providers.Client
}

// Server owns the http.Server plus the assembled handler chain.
type Server struct {
	HTTP *http.Server
	mux  *http.ServeMux
}

// New builds the mux, mounts the route groups and wraps the middleware chain:
//
//	recover → requestLog → securityHeaders → capJSONBody → authPolicy → mux
//
// It returns an error when a feature plane's schema init fails (a broken
// boot must not serve half a feature).
func New(d Deps) (*Server, error) {
	mux := http.NewServeMux()

	// Public core routes.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusOK, map[string]string{"status": "ok", "version": d.Cfg.Version})
	})

	// Route groups (each registers parameterized paths on the shared mux).
	apichat.Register(mux, &apichat.Deps{
		Cfg: d.Cfg, Store: d.Store, Auth: d.Auth, Chat: d.Chat,
		Search: d.Search, Files: d.Files, Providers: d.Providers,
	})
	apimgr.Register(mux, &apimgr.Deps{
		Cfg: d.Cfg, Store: d.Store, Auth: d.Auth, Chat: d.Chat,
		Search: d.Search, Files: d.Files, Providers: d.Providers,
	})

	// v2 feature planes (docs/FEATURES-V2.md), mounted after the core groups.
	// Each owns its DDL (coordination law) and self-inits it here.
	if err := library.Register(mux, &library.Deps{Store: d.Store, Auth: d.Auth}); err != nil {
		return nil, err
	}
	// kb.Register runs its DDL and registers the "kb" chat enricher itself.
	kb.Register(mux, &kb.Deps{Cfg: d.Cfg, Store: d.Store, Auth: d.Auth, Chat: d.Chat})
	memory.Register(mux, &memory.Deps{Cfg: d.Cfg, Store: d.Store, Auth: d.Auth, Providers: d.Providers})
	if d.Chat != nil {
		// memory's enricher is a plain function; the integrator owns the
		// binding into chat's Enricher registry (FEATURES-V2 §BE-MEMORY).
		d.Chat.RegisterEnricher("memory", func(ctx context.Context, c *store.Chat, um providers.ChatMessage) ([]chat.SourceItem, string, error) {
			_, extra, err := memory.Enrich(ctx, d.Store, c.UserID, um.Content)
			return nil, extra, err
		})
	}
	d.Auth.Register2FA(mux) // /api/auth/totp + /api/me/2fa/*
	audit.Register(mux, &audit.Deps{DB: d.Store.DB, Auth: d.Auth})
	imggen.Register(mux, &imggen.Deps{Cfg: d.Cfg, Store: d.Store, Auth: d.Auth, Files: d.Files})

	// SPA fallback (must stay last; /api/* mistakes 404 as JSON below).
	mux.HandleFunc("GET /", spaHandler(d.Auth))

	handler := Recover(RequestLog(SecurityHeaders(CapJSONBody(httpx.MaxJSONBody)(AuthPolicy(d.Auth, mux)))))

	addr := d.Cfg.Addr()
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: SSE streams are long-lived; per-handler ctx governs.
	}
	return &Server{HTTP: srv, mux: mux}, nil
}

// ListenAndServe starts the server; returns nil on graceful shutdown.
func (s *Server) ListenAndServe() error {
	err := s.HTTP.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully drains in-flight requests (SSE included, on ctx done).
func (s *Server) Shutdown(ctx context.Context) error {
	return s.HTTP.Shutdown(ctx)
}

// AuthPolicy enforces the contract's route-group rules by path class:
//
//	public:  /healthz, /api/auth/{login,register,config,logout,oidc/*}, /api/share/*, /s/*, /, assets
//	admin:   /api/providers*, /api/users*, /api/settings (NOT /api/me/settings)
//	authed:  every other /api/* and /files/*
//	optional: the rest (SPA) — Attach only
//
// Scaffold stubs: Require answers 401 everywhere until BE-auth implements it,
// so the policy layer is already correct — only the middleware internals are
// pending.
func AuthPolicy(a *auth.Auth, next http.Handler) http.Handler {
	admin := a.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	}))
	authed := a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	}))
	optional := a.Attach(next)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/healthz", strings.HasPrefix(p, "/api/share/"), strings.HasPrefix(p, "/s/"):
			optional.ServeHTTP(w, r) // Attach keeps "me" working on shared pages
		case strings.HasPrefix(p, "/api/auth/"):
			switch {
			case p == "/api/auth/logout": // cookie-carrying, but tolerant of anon
				optional.ServeHTTP(w, r)
			case p == "/api/auth/me":
				authed.ServeHTTP(w, r)
			default: // login/register/config/oidc
				optional.ServeHTTP(w, r)
			}
		case strings.HasPrefix(p, "/api/providers"),
			strings.HasPrefix(p, "/api/users"),
			p == "/api/settings" || strings.HasPrefix(p, "/api/settings/"):
			admin.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/api/"), strings.HasPrefix(p, "/files/"):
			authed.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/v1/"):
			// Token-authed proxy: proxy.go enforces Bearer ck_ itself (and
			// falls back to an attached session); forcing session here would
			// 401 automation clients before the handler ever ran.
			optional.ServeHTTP(w, r)
		default:
			optional.ServeHTTP(w, r)
		}
	})
}

// spaHandler serves embedded assets, falling back to index.html for
// client-side routes (/c/:id, /admin, …).
//
// Auth-gate (docs/AUTHGATE-CONTRACT.md §1): the SHELL is only served with a
// valid session — anonymous requests for app routes 302 to /login. Files that
// exist in dist (assets/, favicon, …) are served before the guard runs, so
// the login page keeps its styling; a nil a (tests only) disables the guard.
func spaHandler(a *auth.Auth) http.HandlerFunc {
	dist := web.Dist()
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		// Compile-time guaranteed present; keep a defensive runtime path.
		slog.Error("web: embedded index.html missing", "err", err)
	}
	fileServer := http.FileServerFS(dist)

	return func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean(r.URL.Path)
		// Unknown API namespaces must 404 as JSON, not serve the SPA shell.
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/files/") {
			ErrorJSON(w, http.StatusNotFound, "not found")
			return
		}
		name := strings.TrimPrefix(p, "/")
		if name != "" && name != "index.html" {
			if f, err := dist.Open(name); err == nil {
				st, serr := f.Stat()
				f.Close()
				// SECREV-V2 L7-L2: never hand http.FileServer a directory —
				// without an index.html it generates a listing, and GET
				// /assets/ leaked every bundle chunk name anonymously.
				// Directories (and un-stattable entries) fall through to the
				// guard below like any non-file path: 302 anonymous, shell
				// to sessions.
				if serr == nil && !st.IsDir() {
					fileServer.ServeHTTP(w, r)
					return
				}
			}
		}
		// About to serve index.html — the app shell. The "GET /" mux pattern
		// admits only GET/HEAD here; anything without a session bounces to
		// the login page unless it is a public shell path.
		if a != nil && !isPublicShellPath(p) && !a.HasValidSession(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
	}
}

// isPublicShellPath lists the SPA routes whose shell must render without a
// session: /login is the guard's own redirect target (gating it would loop),
// /register is the open/approve signup page, /welcome is AG-2's first-run
// wizard (session bootstrap routes anonymous users there while
// /api/auth/status reports first_run) and /s/{token} is the public shared-
// chat page — it reaches this fallback like any client route (no dedicated
// mux entry) and the SPA fetches its token via public /api/share.
func isPublicShellPath(p string) bool {
	switch p {
	case "/login", "/register", "/welcome":
		return true
	}
	return strings.HasPrefix(p, "/s/")
}
