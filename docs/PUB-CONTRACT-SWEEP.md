# PUB-CONTRACT-SWEEP — FE↔BE wire-contract forensic audit

Date: 2026-11-22 · Subagent: CONTRACT-SWEEP · Mode: **read-only** (zero source edits; this file is the only write).

Method: every Go 1.22 `mux.HandleFunc` pattern was inventoried and cross-matched against its
FE caller (`web/src/lib/*.ts`, `web/src/lib/admin/*`, `web/src/lib/chat/*`, raw `fetch` in routes).
Both sides are cited `file:line`. The bug class that produced every CONFIRMED finding is the same
one as the historical `has_key` incident: **a field/envelope/type the two sides each believe they
agreed on, verified only against each side's own tests** — so both test suites pass while the
product misbehaves.

---

## 1. CONFIRMED-BROKEN (ranked by user impact)

| # | Surface | FE site | BE site | Mismatch | User-visible symptom (plain language) | One-line fix |
|---|---------|---------|---------|----------|----------------------------------------|--------------|
| 1 | Web-search toggle in the composer | `web/src/lib/chatview-api.ts:143-161` (`probeSearchEnabled`: path A requires `typeof v === 'boolean'` at :147; path B reads `searxng_url`/`SEARXNG_URL` at :154) | `internal/apimgr/admin.go:351` emits `"search_enabled": strconv.FormatBool(...)` — a **string** `"true"/"false"` in `GET /api/me/settings`; the admin `GET /api/settings` key is `search_url` (`internal/apimgr/admin.go:95`) | **type drift** (string vs boolean) **and key drift** (`search_url` vs `searxng_url`) in both probe paths | The SearXNG web-search globe button never appears in the chat composer, even when the admin has fully configured a search URL — web search is silently unreachable from the UI (`Composer.svelte:521` `{#if capabilities.search}`; fed by `state.svelte.js:165`). | In `probeSearchEnabled` accept the string form: `if (typeof v === 'string') return v === 'true';` (or emit a real JSON bool at admin.go:351). |
| 2 | Image gallery history on `/images` | `web/src/lib/pages-api.ts:294-306` — `listImages` probes envelope keys `'files','items','data'` (:296) but **not `'images'`** | `internal/imggen/save.go:149` — `GET /api/images` returns `{"images": [...]}` | **envelope key drift** (`images` omitted from the FE's `listOf` probe list; `generateImages` at :289 *does* include `'images'`, so only the list call is broken) | The "History" strip on the image-generation page is permanently empty even after generating images this very session — previously generated images look lost. (The generation result itself still renders.) | Add `'images'` to the key list at pages-api.ts:296. |
| 3 | Gallery page (`/gallery`) | `web/src/lib/api.ts:296-297` types `listFiles` as `FileMeta[]`; `web/src/routes/Gallery.svelte:37` assigns it directly and :22 calls `files.filter(...)` | `internal/apichat/files.go:128` — `GET /api/files` returns `{"items":[...],"total":…,"page":…,"per":…}` | **envelope drift + wrong frozen type** — the FE treats a paged object as a bare array | The `/gallery` page throws (`files.filter is not a function`) and shows a broken/empty view instead of the artifacts+images grid; the same helper's `kind=` variant can't save it. | Unwrap in api.ts: `req<{items: FileMeta[]}>(…).then(r => r.items)` (or return `r.items` after a `Array.isArray` guard for forward-compat). |
| 4 | "Extract memories from a chat" count | `web/src/lib/security-api.ts:169-178` — accepts a number only under `count/extracted/added/created/new` (:173-176) or an array under **`memories`** (:177) | `internal/memory/extract.go:62-65,124` — `POST /api/me/memories/extract` returns `{"created": [ …Memory objects… ]}` | **type drift on a shared key name**: FE tries `created` as a *number* only; the array-under-`created` shape hits none of its branches → falls through to `return 0` (:178) | After a ~15 s extraction the toast always says "0 memories extracted", making the feature look broken — yet the memories *are* saved and appear in the list right after (SettingsMe.svelte:312-315). | Before the numeric loop, add `if (Array.isArray(o.created)) return o.created.length;` (security-api.ts:173). |
| 5 | 2FA status probe on Settings → Account | `web/src/lib/security-api.ts:57-79` — bodiless `POST /api/me/2fa/setup {}` probe maps **409⇒'enabled'**, assuming the enrollment check runs *before* the password check (comment at :59-61) | `internal/auth/totp_handlers.go:77-104` — actual order: decode(:77) → **rate bucket**(:81) → SSO-only 400(:85) → **password check → 401**(:89-96) → enrolled → 409(:98-104); the order is deliberate ("409 must not answer before 401", :66-68) | **status-code ordering assumption inverted**: the passwordless probe gets 401 (not 409) for a user who *has* 2FA → FE maps 401⇒'off' (:77) | A user with two-factor enabled sees the badge **"Off — Set up 2FA"** in their own settings; clicking "Set up" then honestly answers "two-factor is already set up" — confusing self-gaslighting about their security state. | Have the probe treat *any* non-404/405/400 as inconclusive-off only for disabled… simpler: add a cheap `GET /api/me` field (`totp_enabled`) and read it; or FE: send `password:''` never — probe via the login path. Minimal: expose `totp_enabled` in `GET /api/me/settings` and drop the status-code guessing. |
| 6 | TOTP sign-in after one typo | `web/src/routes/Login.svelte:134-156` — on 401 keeps `totpToken` (:119,:136) and shows "Incorrect code — try again." (:148, comment assumes "retry allowed within the token TTL") | `internal/auth/totp_handlers.go:288-294` — the challenge nonce is **burned on every attempt, pass or fail** (`totpMarkNonceUsed`), so any resubmit of the same `totp_token` fails `replayed_token` → 401 | **semantic drift on token reuse**: FE contract says "code may be retried within TTL" (that is true for the *setup* flow, `Handle2FAEnable` :126-128 "retry within the TTL", but NOT for the login challenge) | One mistyped 6-digit code locks the user out of that challenge: every retry — even with the now-correct code — fails with "invalid or expired verification token"; the only way in is Cancel + retyping the password to mint a new challenge. | FE: on 401 from `/api/auth/totp`, clear `totpToken`/`totpMode` and bounce back to the password form with the error; or BE: burn the nonce only on success (replay protection still holds via the ±window monotonic step check). |

---

## 2. SUSPECT (works today by luck / by accident; one change away from breaking)

| # | Surface | FE site | BE site | Fragility | Recommendation |
|---|---------|---------|---------|-----------|----------------|
| S1 | `/gallery` freshness cap | `Gallery.svelte:37` calls `listFiles()` with no paging params | `files.go:111-117` defaults `per=30`, max 100 | Even after fixing #3, the gallery silently stops at the 30 newest files; "All (N)" chips under-report | Pass `per=100` and page, or return all outputs from a dedicated `kind=artifact|image` unpaginated route. |
| S2 | 2FA probe burns the setup rate budget | probe fires once per Account-tab mount (`SettingsMe.svelte:366-369`) | `totp_handlers.go:81` — `Allow("2fa:setup:<uid>", …)` runs **before** the password check, so bodyless probes consume the same 3/min bucket as real setups | Rapid tab remounts (or finding #5's mislabeled "off" → users hammering "Set up") can 429 a legitimate setup; FE maps 429⇒'off' (`security-api.ts:65-66`) which hides the real cause | Give probes their own limiter key, or implement the `totp_enabled` field so no POST probe exists. |
| S3 | Memory edit round-trip | `security-api.ts:147-159` — always tries `PATCH`, catches 405, retries `PUT` | `internal/memory/memory.go:154` registers `PUT` only (Go mux answers 405+Allow) | Works, but every enable-toggle/edit pays a dead request; if the BE ever adds a PATCH with different semantics the FE silently takes the new path and stops testing PUT | Register `PATCH` as an alias, or make the FE PUT-first (it already has the fallback). |
| S4 | Strict-400 on me-settings unknown keys | every FE writer uses only allowlisted keys today (`accent` — `accent.svelte.js:106`; `default_model`/`hidden_models` — `model-prefs.svelte.js:18-19`; `name` — `api-shell.ts:54`; `setup_done` — `Onboarding.svelte:195`) | `admin.go:443-446` — `handleMePut` 400s **loudly** on any key outside `meSettingKeys` (`admin.go:262-269`) | FE comments in `accent.svelte.js:103-108` / `model-prefs.svelte.js:68-72` claim unknown keys "degrade gracefully", which was only ever true of the *response*; it holds today because the allowlist matches. Any new FE pref key added without the BE map fails loudly (still 200-shaped UX via `.catch`, so drift would be invisible until a key is needed) | Keep a single source of truth: generate `meSettingKeys` from a shared doc table or assert it in a contract test. |
| S5 | `send_on_enter` copy vs storage | Settings copy says "Preferences — Stored on this device" (`SettingsMe.svelte:413`) but the section sits next to accent/model prefs that *are* server-synced; `setSendOnEnter` is localStorage-only (`api-shell.ts`) | no BE key (allowlist has no `send_on_enter`) | Not broken — just the kind of expectation drift this audit keeps finding; admins will file it as "sync doesn't work" | Either say so in the row's hint text or add the key to both sides. |
| S6 | Images list ignores `created_at` sort/paging | `pages-api.ts:294-306` renders BE order (newest-first, hard-coded 50: `save.go:131-136`, `listRecentLength`) | `save.go:134-136` fixed `page=1, per=listRecentLength` | The History strip is a fixed 50-window with no way to see older images; deleting the newest 50 still hides the rest | Document the cap or expose `?page=`. |

---

## 3. COSMETIC (contract inconsistencies with no user impact today)

| # | Surface | Detail |
|---|---------|--------|
| C1 | `PUT /api/chats/{id}/folder` returns 200 + full chat (`chats.go:247`) while `api.ts:180-181` types `void` — response dropped. Harmless; keep the type or use the echo to refresh the sidebar row. |
| C2 | Tags ride on `chatJSON` for list/get/patch/fork (`chats.go:283` `withTags`) but a freshly created chat (`POST /api/chats`) can't have tags anyway — consistent enough; FE `ChatActionsMenu.svelte:28-30` widens the frozen DTO locally with its own `ChatWithTags`. |
| C3 | `GET /api/settings` mixes *stored* keys with the *computed* string `search_enabled`; FE `SystemTab.svelte` ignores it, so no crash — but the map is typed `Record<string,string>` while its semantics differ per key (stored vs computed). |
| C4 | `GET /api/me/settings` response also carries the computed `search_enabled` string among stored strings (`admin.go:351`), so `meSettings()`'s `map[string]string` conflates both namespaces. |
| C5 | `parseFrame` accepts `event:` with no space (`api.ts:206-222`); Go writes `event: name\n`. Both sides tolerate each other's whitespace — noted only for byte-level completeness. |
| C6 | BE returns `[]exportChat` (`export.go`) / empty arrays generally where FE `listOf` expects arrays — consistent; the earlier-suspected "empty export" trap does not exist (chat export is a cookie GET anchor, `ChatActionsMenu.svelte:142-149`). |

---

## 4. Verified-matching (checked field-by-field, no drift)

- **SSE protocol** (`internal/chat/service.go` ↔ `api.ts:193-276`, `sse.svelte.js`): event names `meta|title|sources|token|done|error`, `\n\n` frame split, `:` heartbeat tolerated, `done.usage.in/out`, terminal `error{message}`.
- **Send/regenerate**: `sendMessageReq{content, images[{file_id}], search}` (`messages.go:106-114`) = `SendMessageBody`; `?from=` cursor; bare-array `GET …/messages` (`messages.go:102`).
- **Chat CRUD**: `chatCreateReq`/`chatPatchReq` (`chats.go`) vs every real caller body (`Sidebar:216,225`, `ParamDrawer:77,112`, `Composer:322`, `ModelPicker:90`, `Compare:105-113`, `api-shell:115-124`) — no caller sends a key outside the strict-decode structs; `chatPage{chats,total,page,per}` = `ChatPage`.
- **Folders/tags/chat-folder/chat-tags**: routes, bodies (`{folder_id}` nullable, `{tag_ids}`), envelopes (bare arrays), 409-on-duplicate-tag.
- **Providers (admin)**: `providerJSON` incl. `has_key`/`model_count`/`models_enabled`, masked-key keep-on-blank (Modal omits `api_key` when blank), `PUT /providers/{id}/models` batch ↔ refresh, sync `{count, added}`.
- **Users/tokens/reset-2fa**: roles enum incl. `pending`; `{was_enabled, sessions_revoked, new_password?}` (`users.go:166-171`) ↔ UsersTab raw fetch (`UsersTab.svelte:107`).
- **Auth**: login `{email,password}` → User **or** `{requires_totp, totp_token}` (`handlers.go:95-96`) ↔ `Login.svelte:117-122`; `POST /api/auth/totp {totp_token,code}`; register first-run bootstrap works under all three policies (first row promoted + session: `local.go:291-315`), `{email,password,name}` strict decode; `config{oidc_enabled,oidc_name,registration(open|approve|closed),first_run}` ↔ `AuthConfig`; status `{authenticated,first_run}`.
- **2FA setup/enable/disable bodies** `{password}`/`{code}`/`{password}` ↔ `{secret,otpauth_uri}`/`{ok}`/`{ok}` (strict decodes all satisfied).
- **me-settings**: all five FE-written keys (`accent`, `name`, `setup_done`, `default_model`, `hidden_models`) ⊂ `meSettingKeys`; all values sent as JSON strings per `handleMePut`'s string-only rule; hidden_models double-decode tolerance matches storage.
- **Admin settings**: FE writes only `title, task_model, kb_embed_model, system_prompt, search_url, registration` (`SystemTab.svelte` load/save) ⊂ `settingValidators` (`admin.go:88-100`); `registration` enum validated both sides.
- **KB**: `GET /api/kb → {items,total,page,per}`, `GET …/files → {…,"files":[…]}`, `POST …/search → {items:[chunkHit]}` with `chunkHit{file_name,content,score}` (`vector.go:54-61`) — all covered by the FE `listOf/asRows` key lists incl. `bytes→size` fallback; `collectionDTO{…,file_count}` ✓.
- **Prompts**: `{prompts}` envelope + `{prompts|items|data}` tolerance; ratings `map[messageID]` ↔ `normalizeRatings`; fork `{message_id}` ↔ `{forkReq}` 201 + chat shape.
- **Files upload**: `POST /api/files` (multipart `file`, `?chat=`) → 201 `fileDTO{id,name,mime,size,kind,url,created_at}` ↔ `FileMeta` (+ extra `created_at` the FE ignores).
- **Memory CRUD**: list `{items,total,limit}` ⊃ FE key list; create 409-on-limit (`api.go:69-70` + `memory.go:47-48,180`); PUT update decodes `map[string]any` (no strict-400 risk for FE patches).
- **Images generate**: `{prompt,size(json.RawMessage — FE's plain number accepted),n,provider_id?}` → `{images:[{id,url,…}]}`, `'images'` ∈ generate's probe list.
- **Export**: per-chat `GET …/export?format=md|json` + zip `GET /api/me/export` as cookie-GET anchors only — no JSON contract.
- **Share**: `POST/DELETE …/share` → `{token}`; public `GET /api/share/{token}` → `{chat,messages}` (`share.go:32-35`) ↔ `SharePayload`.
- **Audit**: `{items, next_cursor(string)}` ↔ `audit-api.ts:70-98`.

---

## 5. Root-cause pattern & guardrail suggestion

Every finding in §1 passed both test suites because each side tested its own side of the
assumption (Go asserts `FormatBool`; TS asserts `typeof === 'boolean'`). A cheap guardrail:
a golden-file contract test that boots the mux and replays **the exact FE request bodies and
asserts the exact envelope keys the FE normalizers probe** (the `listOf`/`asRows` key lists are a
machine-readable inventory of FE expectations — they practically are a contract spec already).
