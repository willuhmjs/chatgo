# PARITY-V3 — Open WebUI parity batch (RAM-neutral)

Date: 2026-11-25. Goal: close OWUI gaps without growing the server footprint
(every item is a small query, a reuse of existing machinery, or pure
frontend). Re-benched with `make bench`: idle 19.1 MiB, steady 21.4 MiB
(macOS host numbers; Linux ~1.5–2× lower) — unchanged vs. the README table.

## 1. OpenAI-compat proxy hardening + GET /v1/models (internal/apimgr/proxy.go)

- **Security fix:** `models.min_role='admin'` gating was enforced on the app
  paths (GET /api/models hides; chat send refuses) but NOT on the /v1 proxy —
  a plain user's `ck_` token could call admin-only models via
  `POST /v1/chat/completions` (both `prov:model` and bare-id forms).
  `resolveModel` now takes the caller's role; gated models answer a plain
  404 `model_not_found` for non-admins (hidden, never "forbidden" — same
  no-existence-leak rule as the catalog).
- **GET /v1/models** (was missing; most OpenAI clients probe it): returns
  `object:"list"` of `{id:"ProviderName:model_id", object:"model", created,
  owned_by}`, plus the bare `model_id` whenever it is unique in the caller's
  visible set. Scoped by role AND enabled flags — the list never advertises
  an id the same caller would 404 on. Auth = same Bearer/session rules as
  completions.
- Bare-id unknown model now answers **404 model_not_found** (was 400) —
  OpenAI parity.

## 2. Account deletion (OWUI admin delete + "delete personal account")

- `DELETE /api/users/{id}` (admin) and `DELETE /api/me` (self) —
  internal/apimgr/users.go. Guards: no self-delete via the admin endpoint
  (400 → use /api/me); the last admin can never be deleted (mirrors the
  role-demotion guard); foreign/unknown → 404; `user.delete` audit line.
- Cleanup layers:
  - `store.UsersRepo.Delete` — tx: user row (FK CASCADEs: sessions, folders,
    tags, chats→messages→chat_tags/ratings/summaries, files, api_tokens,
    prompts, notes) + `me.<id>.*` settings rows (no FK, would orphan).
  - `apimgr.SetUserDeleteHook` — integrator binding (same pattern as the
    memory enricher in server.go) for tables WITHOUT an FK to users:
    `kb.DeleteUserData` (collections → files/chunks cascade),
    `memory.DeleteUserData`, and `files.Service.PurgeUser` (rows + on-disk
    blobs + thumbs; runs BEFORE the row delete so paths still resolve).
- UI: UsersTab row **Delete** (Confirm dialog, hidden for yourself);
  SettingsMe danger-zone **Delete account** next to Sign-out.

## 3. Regenerate chat title

- `chat.Service.RegenerateTitle` (internal/chat/title.go) — task-model
  re-naming overwriting the existing title (auto-title only fills the
  default). Unlike the auto path there is NO truncation fallback: no/unset/
  failing task model ⇒ typed `ErrTitleUnavailable` ⇒ clean 503.
- `POST /api/chats/{id}/title` (internal/apichat/title.go) → `{"title":…}`;
  ownership 404, body-capped, no-existence-leak — same shape as /suggest.
- UI: chat "…" menu → Sparkles "Regenerate title"; sidebar + tab title
  update via the existing `chatgo:title` / `chatgo:chat-updated` events.

## 4. Tokens/sec readout (pure frontend)

- ChatView times each stream (`streamT0` → `done`) and stamps
  `UIMessage.duration_ms`; MessageRow's token chip gains
  ` · N.N tok/s` when a locally-timed duration exists, plus a tooltip with
  prompt/completion counts + seconds (OWUI hover parity). Reloaded rows keep
  the persisted counts; the rate is session-local, nothing new on the wire.

## 5. Inline text-file attachments (pure frontend, zero server cost)

- Composer (web/src/lib/chat/Composer.svelte): paste/drop/pick text & code
  files (extension allowlist, ≤100 KB, max 4) — read with `File.text()`,
  chip with FileText icon, send composes them into the message as fenced
  blocks (4-backtick fence when content contains ```` ``` ````). Content is
  part of the user message: survives reload/branches, FTS-indexed, no new
  endpoints, no uploads. Big documents keep using Knowledge bases.

## Tests

- `internal/apimgr/proxy_test.go` — gated model 404s for user tokens (both
  forms) & admin resolves; /v1/models scoping (anon 401, hidden/disabled/
  ambiguous-bare ids absent, admin sees gated).
- `internal/apimgr/apimgr_test.go` — unknown-model expectation moved 400→404.
- `internal/apimgr/users_delete_test.go` — admin delete happy path (hook +
  me-settings gone), self-delete via admin endpoint 400, last-admin guards,
  self-delete 204, non-admin 403.
- `internal/apichat/title_test.go` — happy path (persists, cleaning rules),
  foreign 404, anon 401, big body 400, task-model failures 503.
- `make lint` / `go test ./...` / `svelte-check` / `make bench` all green.
