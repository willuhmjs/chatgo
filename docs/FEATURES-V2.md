# ChatGo v2 — Colorful-Accent UI + Open WebUI feature parity (LAW for wave 1)

User mandate: look like ChatGPT but **more colorful**; add RAG, prompt library,
ratings, forking, export, image gen, 2FA+audit (memory = wave 2). **Hard constraint:
no RAM growth** — instance stays well under 128Mi RSS (idle currently ~20Mi).

## RAM discipline (all backend agents — non-negotiable)
- NO new Go dependencies. go.mod is frozen. TOTP = roll our own (RFC 6238, HMAC-SHA1,
  base32 = stdlib). Vectors = float32 little-endian BLOB + brute-scan cosine (personal
  scale). QR codes: NOT rendered — show manual key + otpauth:// URI only.
- Streaming everywhere: multipart ingest via io.LimitReader (5 MiB/file cap), image
  proxy with 120s context, export zips streamed to ResponseWriter.
- Embeddings in batches of ≤64 texts, request-scoped, zero cross-request caching.
  Search = load only the selected collections' chunks, scan, release.
- New goroutines at startup: ZERO. Work happens on request paths only.
- audit_log self-trims: after insert, DELETE everything below (max_id - 10_000).
- No package may hold unbounded maps/slices; anything cache-like is capped + documented.

## Coordination law
- Routes: each feature exposes `Register(mux *http.ServeMux, d *Deps)` in its OWN
  package (Deps = small struct the feature defines). **Integrator wires apimgr** —
  do NOT edit internal/apimgr/**, internal/api/server.go, internal/config/**.
- DB: NO edits to store/migrate.go or any store-owned file. Each feature owns its DDL
  (CREATE TABLE IF EXISTS …) executed in its own Init/constructor via store.DB.
- Auth context: use the auth package's exported session-user resolver (auth.Attach has
  already run for /api/*; admin policy = Require). Admin-only endpoints: read user from
  ctx, 403 unless role==admin. BE-SEC may add ONE exported helper to internal/auth if
  none exists.
- SSE: chat citation events reuse the existing web-search "sources" event shape.
- Gates per agent: gofmt clean, go build ./..., go vet ./<yours>/..., go test ./<yours>/...
  (write table tests for happy + limit cases). No git. No web/ edits (FE wave 2 later).

## Feature specs (endpoints are minimums)

### BE-RAG → package internal/kb
Tables: kb_collections(id,user_id,name), kb_files(id,collection_id,name,mime,bytes),
kb_chunks(id,file_id,ordinal,content,embedding BLOB,dim INT).
Endpoints (owner=caller; admin sees all? v1: private per user like chats):
POST/GET/DELETE /api/kb[/{id}], POST /api/kb/{id}/files (multipart, text-ish:
txt/md/csv/json/code; reject binary w/ 415), GET /api/kb/{id}/files, DELETE files,
POST /api/kb/{id}/search {q,top_k} → scored chunks (also used by composer picker).
Pipeline: find where web-search enriches chat completions; add registry
`type Enricher func(ctx, chat, userMsg) ([]Source, extraSystem string, err)` — kb
enricher triggers when chat settings JSON has kb_collection_ids; top-k=6 chunks
prepended as fenced context with [n] markers; sources surfaced in the existing
sources SSE event (url "kb://{file_id}", title=filename). Embed provider = task_model's
provider if it supports /embeddings else first provider that syncs them; clear error
toast-worthy message when none.

### BE-SOCIAL → package internal/library
prompts(id,user_id NULL=admin-curated,title,content,created/updated); ratings(id,
message_id,chat_id,user_id,value -1|1,comment,created); audit not here.
Endpoints: CRUD /api/prompts (own + global readable; global writable admin only),
POST /api/chats/{id}/messages/{mid}/rating {value,comment?} (upsert, own msg's chat),
DELETE rating; POST /api/chats/{id}/fork {message_id} → new chat copying msgs ≤ that
one, title "Fork of <title>", returns chat; GET /api/chats/{id}/export?format=md|json,
GET /api/me/export → streamed zip (chats json + me settings redacted). Ratings appear
in admin stats later; v1 just persist + fetch per chat.

### BE-SEC → package internal/totp + internal/audit + internal/imggen
totp: secret per user (users.totp_secret TEXT — encrypted-at-rest reusing the HKDF
pattern provider keys use), endpoints POST /api/me/2fa/setup → {secret, otpauth_uri}
(only when not enabled; requires current password), POST /api/me/2fa/enable {code},
POST /api/me/2fa/disable {code|password}, replay protection: accept ±1 step, remember
last-used step per user in-memory (1-entry/user map — capped by user count).
Login: when totp set, password success returns {requires_totp:true,
totp_token:<short-lived HMAC'd token>} (NO session cookie yet); POST
/api/auth/totp {totp_token, code} issues the session via the SAME cookie path as
normal login. OIDC unaffected.
audit: table audit_log(id INTEGER PRIMARY KEY AUTOINCREMENT, ts, user_id, email,
action, detail, ip). Expose audit.Record(ctx, deps, action, detail) + wire calls into:
login success/fail, logout, 2fa events, user create/approve/delete/role, provider
create/update/delete, settings PUT, token create/delete, password change (auth package
call-sites BE-SEC owns). GET /api/audit?cursor=&limit (admin) newest-first.
imggen: POST /api/images/generate {provider_id?, prompt, size?, n?} → proxy
/v1/images/generations (60–120s), decode b64 → files.Service rows (kind image, owner
caller); GET /api/images (list own, newest 50); reuse existing file-serving URLs.

## Frontend wave-1 (FE-COLOR ONLY this wave)
Quiet-color accent system: ChatGPT structure untouched; color = signal. app.css defines
--accent-chroma set via html[data-accent="violet|blue|teal|green|orange|pink"] (default
violet): --acc (#a78bfa class), --acc-strong, --acc-tint (bubble bg dark ~18%), 
--acc-gradient (135deg two-stop for send circle + brand glyph). Existing white primary
STAYS white (send circle = accent gradient, primary buttons white). Tinted: user
bubbles, active sidebar row (tint fill, acc text), selection, links/focus already acc,
provider dots in model pickers (deterministic hue per provider name), tag chips
hue-rotate cycle, syntax theme colorized, /welcome glyph gradient, toggle on-state.
Accent choice persisted via existing me-settings theme mechanism (extend value to
JSON or add second key — settings PUT me-settings is being fixed in parallel; store
under key "theme" as "dark|violet" format IF api is being fixed… coordinate: the
onboarding-fix agent owns me-settings allowlist — FE-COLOR must NOT touch
apimgr/admin.go; write picker so it PUTs {setup_done-safe key "accent"}? NO —
keep wave-1 picker LOCAL STORAGE only (localStorage "chatgo.accent"), integrator
promotes to server later. Keep it shippable.

FE-COLOR ownership: ALL of web/src EXCEPT routes/Onboarding.svelte,
lib/session.svelte.js, App.svelte (owned by the onboarding-fix agent), and
routes/Admin* / lib/admin/** (audit tab wave 2). Gate: svelte-check 0 errors.
