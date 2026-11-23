# ChatGo — Build Contracts

**This document is law.** Every agent builds against these interfaces exactly.
If you must deviate, leave a `// CONTRACT-DEVIATION:` comment explaining why and report it in your final message.

Project: single-binary OpenAI-compatible chat webapp in Go + embedded Svelte 5 SPA.
Goals: looks like ChatGPT/Open WebUI, multi-user, OIDC+local auth, < 128Mi RSS.
Module path: `chatgo`. Go 1.24+. License Apache-2.0.

## Ground rules for every agent
1. Only create/edit files inside YOUR ownership set (see Agent Ownership).
2. NEVER edit `go.mod`, `go.sum`, `web/package.json`, `web/package-lock.json`, `docs/CONTRACTS.md`.
3. Backend: your package must pass `go build ./...` and `go vet ./...` and `go test ./internal/<yours>/...` before you finish. gofmt everything.
4. Frontend: verify with `npx svelte-check --threshold error` (do NOT run `vite build`, do NOT run `npm install`).
5. All SQL parameterized. No `fmt.Sprintf` into SQL, ever.
6. Never log secrets, tokens, api keys, or message content.
7. Stubs already compile — replace them, don't move them. Exported names/paths below are frozen.

## Stack (pinned by scaffold, everyone else just imports)
- `net/http` stdlib routing (Go 1.22 patterns: `mux.HandleFunc("GET /api/chats/{id}", …)`)
- `modernc.org/sqlite` (pure Go, WAL, FTS5 available)
- `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2`
- `golang.org/x/crypto/bcrypt`, `golang.org/x/time/rate`
- `github.com/google/uuid`
- Frontend: Svelte 5 (runes), Vite, Tailwind v4, marked, dompurify, highlight.js, lucide-svelte
- Sessions: opaque random tokens (32B url-safe), stored in DB, cookie `chatgo_session`, HttpOnly+SameSite=Lax+Secure(if PUBLIC_URL https).

## Directory layout
```
cmd/chatgo/main.go            wires config→store→services→api→web
internal/config/              env config
internal/store/               db.go, migrate.go, models.go, repos…
internal/auth/                oidc.go local.go sessions.go middleware.go
internal/providers/           client.go sse.go models.go
internal/chat/                service.go prompt.go title.go summarize.go
internal/search/              searxng.go guard.go
internal/files/               store.go images.go
internal/api/                 server.go middleware.go sse.go render.go
internal/apichat/             register.go chats.go messages.go share.go folders.go tags.go
internal/apimgr/              register.go admin.go providers.go users.go tokens.go proxy.go auth.go
internal/web/                 web.go (go:embed all:dist)
web/                          Svelte 5 SPA source; builds to ../internal/web/dist via `npm run build`
```

## Config (internal/config)
```go
type Config struct {
    Port, Host, PublicURL, DataDir string
    DatabasePath  string            // default DataDir/chatgo.db
    SessionSecret string            // hex; required
    OIDC          OIDCConfig        // Issuer, ClientID, ClientSecret, DisplayName (default "SSO")
    Registration  string            // "open" | "approve" | "closed" (default "approve")
    TaskModel     string            // "providerID:modelID" for titles/summaries; optional
    SearXNGURL    string            // optional; enables web-search toggle
    TrustedURLNets []string         // CIDRs allowed for provider/SearXNG URLs beyond public internet
    AllowLoopback bool              // for in-cluster SearXNG default true
    Version       string
}
func Load() (*Config, error) // env-driven, see README table scaffold writes
```

## DB schema (internal/store/migrate.go owns the exact SQL; scaffold writes it)
Tables: `users`, `sessions`, `settings(key,value)`, `providers`, `models`, `chats`,
`messages`, `message_images`, `folders`, `tags`, `chat_tags`, `files`, `api_tokens`,
`summaries`, `messages_fts` (fts5, external content over messages.content, kept in sync by triggers).
All ids TEXT (uuid v4). All timestamps unix seconds INTEGER.
- users: email UNIQUE, name, role(`admin|user|pending`), password_hash NULL for OIDC-only, avatar_url, created_at, last_active_at
- providers: name, base_url, api_key_enc (AES-GCM w/ SessionSecret-derived key), enabled, is_default, sort
- models: provider_id, model_id, display, context_len, enabled — refreshed cache of provider /v1/models
- chats: user_id, folder_id NULL, title, provider_id, model_id, params_json, pinned, share_token NULL UNIQUE, deleted_at NULL, created_at, updated_at
- messages: chat_id, role(`user|assistant|system`), content, sources_json, error NULL, tokens_in, tokens_out, created_at
- files: user_id, chat_id NULL, name, mime, size, path, kind(`upload|artifact|image`)
- api_tokens: user_id, name, key_hash (sha256 of token; shown once), last_used_at
- summaries: chat_id, up_to_message_id, text, created_at

## Store interfaces (scaffold defines in internal/store/repos.go; core repos IMPLEMENTED by scaffold, ★ repos owned by BE4/BE5/BE6 as noted)
```go
type Store struct{ DB *sql.DB /* … */ }
func Open(path string, secret []byte) (*Store, error) // opens WAL, busy_timeout=5000, runs migrations

// scaffold implements:
Users    : Insert/GetByID/GetByEmail/UpdateRole/List/ListPending/Touch/SetPassword
Sessions : Create/Get/Touch/Delete/DeleteExpired
Settings : Get(key)(*string) / Set(key,value) / All()(map[string]string)
Providers: Insert/Get/List/Delete/Update (api_key_enc round-trips)
Models   : ReplaceForProvider/ForProvider/EnabledModels
Chats    : Create/Get/GetByShare/List(userID, opts)/Update/Delete(soft)/SetShare/SetFolder
Messages : Append/List(chatID, limit, beforeID)/Count/DeleteFrom(chatID, msgID) // for regenerate

// BE4 implements (same package, own files):
Folders  : CRUD (per user)          Tags: CRUD + SetChatTags/TagsForChats
SearchMsgs: FTS5 search messages for a user (chats containing matches)
// BE5 implements:
Tokens   : Create/List/HashLookup(name→user, updates last_used)
// BE6 implements:
Files    : Insert/Get/ListByUser(kind filter)/DeleteByUser paths
```

## HTTP surface (exact paths; `internal/api/server.go` builds mux and calls
`apichat.Register(mux, deps)` and `apimgr.Register(mux, deps)`; owns SPA fallback + embed)
```
GET  /healthz
POST /api/auth/login {email,password} | GET /api/auth/config {oidc_enabled,oidc_name,registration}
GET  /api/auth/oidc/login →302 | GET /api/auth/oidc/callback
POST /api/auth/register {email,password,name} (per Registration policy)
POST /api/auth/logout | GET /api/auth/me → User{role,…}
GET/POST /api/providers  PUT/DELETE /api/providers/{id} (admin)
POST /api/providers/{id}/sync → refreshes models cache
GET  /api/models → [{provider_id,model_id,display,context_len,provider}]
CRUD /api/chats  (list supports ?q=&folder=&tag=&page=&per= default 30)
GET/DELETE/PATCH /api/chats/{id}          POST /api/chats/{id}/share → {token} ; DELETE …/share
POST /api/chats/{id}/messages {content, images:[{file_id}], search:bool} → SSE (protocol below)
POST /api/chats/{id}/regenerate?from={msgID} → SSE
POST /api/files (multipart, field `file`; ≤20MB; png/jpg/webp/gif/pdf/txt/md/csv/json) → FileMeta
GET  /files/{id}/{name} (authed, serves original name, content-sniffed type)
GET  /api/files?kind=artifact  → gallery list
Folders/Tags: CRUD /api/folders, /api/tags ; PUT /api/chats/{id}/folder ; PUT /api/chats/{id}/tags
GET/PUT /api/me/settings {theme,…}   | admin: GET/PUT /api/settings
POST /api/settings/search/test (admin) → {ok,hits,error?,hint?}  live SearXNG probe
GET /api/users (admin) | POST /api/users/{id}/approve | PUT /api/users/{id}/role
CRUD /api/tokens → create returns full token once
POST /v1/chat/completions (Bearer ck_… api token, OpenAI-compatible passthrough w/ user_id binding)
GET  /api/share/{token} → public read-only chat payload (also /s/{token} SPA route)
```

## SSE protocol (POST messages; headers: text/event-stream, no-cache, X-Accel-Buffering: no)
Each line: `event: <name>\ndata: <json>\n\n`. Events, in order:
1. `meta`  `{message_id, chat_id}`
2. `title` `{title}`  (only if new-chat title was generated; fire early)
3. `sources` `{items:[{title,url}], error?}` (only when search ran; `error` = why it failed, live-stream only, never persisted)
4. `token` `{content}` many
5. `done`  `{usage:{in,out}|null}` | `error` `{message}`
Server writes heartbeats `: ping\n\n` every 15s while waiting upstream.

## Providers client (internal/providers) — scaffold defines interface + compiles stub
```go
type ChatMessage struct{ Role, Content string; Images []string /*data URLs*/ }
type StreamOpts struct{ ProviderID, ModelID string; Messages []ChatMessage;
    Params map[string]any; SearchCtx string /*extra system text; "" = none*/ }
type Chunk struct{ Content, FinishReason string; Usage *Usage }
type Usage struct{ In, Out int }
type Client interface {
    ListModels(ctx, baseURL, apiKey string) ([]Model, error)
    StreamChat(ctx, p Provider /*decrypted*/, opts StreamOpts) (<-chan Chunk, error)
    Complete(ctx, p Provider, model string, msgs []ChatMessage) (string, error) // non-stream, for task model
}
```
SSRF guard (used by providers + search, implemented in `internal/search/guard.go`):
`ResolveAndCheck(ctx, rawURL, cfg) (net.Conn dialer-func, error)` — http(s) only, resolves DNS
then dials ONLY resolved IPs in TrustedURLNets ∪ public ranges (plus loopback if AllowLoopback);
rejects redirects blindly (client CheckRedirect ≤3, re-validated).

## Chat service (internal/chat)
`Service.Send(ctx, userID, chatID, userMsg, opts) (<-chan api-side events)` orchestrates:
load history (compacted: if est. tokens > 70% of model context, use latest summary + tail),
persist user msg, call providers.Client (with SearchCtx when opts.Search → search.SearXNG),
stream chunks→events, persist assistant msg (content+sources+usage, or error),
fire-and-forget title.go for first exchange (TaskModel), summarize.go when chat > 40 msgs
(keep last summary + last 30 msgs unsummarized).
Token estimate: len(content)/4 chars heuristic — good enough, no tiktoken.

## search (internal/search)
`SearXNG{BaseURL, HC}.Search(ctx, q, n=5) ([]Result{Title,URL,Content})` → calls `GET {base}/search?q=&format=json`.
`ComposeContext(results) string` = fenced block with title/url/1200-char snippets.
`guard.go` as above.

## files (internal/files)
Files land at `DataDir/files/<uuid>.<ext>`; images get a 512px `.thumb` jpeg (golang.org/x/image
decode png/jpeg only, others skip thumb). Vision payloads build `data:<mime>;base64,…` from original.
Sanitize display names (filepath.Base + length cap). PDFs: extract text NOT done v1 — store as
file, attach as context text only for text-ish mimes.

## auth (internal/auth)
Middleware: `Require(session)`→context userID+role; pending/role rules per route group in api/server.go.
OIDC: standard code flow + state+nonce cookie, email verified→auto-create (role per Registration),
existing-by-email auto-link. Local: bcrypt cost 12; login rate-limited 5/min/IP (x/time/rate map).
`/api/auth/me` also updates last_active_at throttled ≥60s.

## Frontend (web/) — Svelte 5 runes only ($state/$derived/$props/$effect), no legacy stores API in components
Routes (hashless, pushState router `src/lib/router.svelte.js` from scaffold):
`/` (new chat) `/c/{id}` `/compare` `/gallery` `/settings` `/admin` (+tabs providers|users|system)
`/login` `/register` `/s/{token}` (public, read-only)
Lib (scaffold-owned): `api.ts` (typed fetch wrapper for every route above, SSE reader for messages),
`sse.svelte.js`, `session.svelte.js` (auth store), `router.svelte.js`, `theme.svelte.js` (dark first,
toggle persists), `shortcuts.svelte.js`.
Design brief: dark-first (#0d0d0f bg, #17171a panels, #2a2a2e borders, #10a37f→but use indigo #6366f1 accent,
Inter/system-sans), left sidebar 260px (new-chat btn, search box, folders w/ collapsible chats,
tags chips, pinned on top), main pane centered 48rem column, composer sticky bottom w/ model pill
+ search toggle + mic + image clip, message rows flat with avatar squares, hover action bars
(copy/retry/edit), code blocks w/ lang label + copy, right-slide param drawer, mobile: sidebar off-canvas.
Keyboard: ⌘K palette (switch chats/models/actions), ⌘N new chat, Esc closes drawers, ⌘Enter send,
/ to focus composer.

## Build
`make build` → `cd web && npm ci && npm run build` (vite outDir: `../internal/web/dist`) → `go build -trimpath -ldflags="-s -w" -o bin/chatgo ./cmd/chatgo`
`make dev` runs vite dev (proxy /api→:8080) + `go run ./cmd/chatgo` with DATA_DIR=./data.

## Agent ownership (v1 swarm)
- **BE-auth** → internal/auth/*.go (all) + tests
- **BE-providers** → internal/providers/*.go (all) + tests
- **BE-chat** → internal/chat/*.go + tests (uses interfaces only)
- **BE-apichat** → internal/apichat/{chats,messages,share,folders,tags}.go + internal/store/{folders,tags,ftssearch}.go + tests
- **BE-apimgr** → internal/apimgr/{admin,providers,users,tokens,proxy}.go + internal/store/tokens.go + tests
- **BE-data** → internal/search/*.go + internal/files/*.go + internal/store/files.go + apimgr auth.go endpoints live with apimgr? NO — auth endpoints: internal/apimgr/auth.go owned by BE-apichat (it has the session ctx)… FINAL: **BE-apichat owns internal/apimgr/auth.go** (auth API endpoints), BE-auth owns the middleware/oidc/local internals.
- **FE-shell** → web/src/{App.svelte,lib/router,lib/session,lib/theme,lib/shortcuts} + routes: Home shell layout, Sidebar, Folders/Tags UI in src/lib/ui/*, Settings shell + me/settings page
- **FE-chat** → src/routes/ChatView.svelte + MessageList, MessageRow, Composer, Markdown (marked+DOMPurify+hljs), image input, Web Speech mic/read-aloud, ParamDrawer, ModelPicker
- **FE-admin** → src/routes/{Login,Register,Admin*}.svelte (providers CRUD, users approve/roles, system settings) + api usage only
- **FE-extra** → src/routes/{Compare,Gallery,Share}.svelte + CommandPalette + Toasts system (src/lib/toast.svelte.js is FE-extra-owned)
- **OPS** → Makefile, Dockerfile (multi-stage node+go→distroless/static, nonroot), LICENSE(Apache-2.0 full text), README.md, SECURITY.md, .github/workflows/build.yml, homelab k8s manifests draft at deploy/ (ns chatgo, deploy w/ requests 64Mi limits 128Mi, PVC, ingress chat.example.com-style), bench/measure.sh

## Non-negotiables
- 401 vs 403 vs 404: a resource not owned by caller returns 404, not 403 (no existence leak). Admin routes 403 for non-admin.
- Every list endpoint paginated (`?page=&per=` capped at 100).
- Every request body capped (json 1MB, multipart 20MB).
- SSE responses flush immediately; upstream cancel propagates on client disconnect.
