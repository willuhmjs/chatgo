# PUB-GAP-AUDIT — completeness sweep before public release

Read-only audit of `/Users/will/git/chatgo` (web/src + internal). Date: 2026-11-22.
Other agents live-editing: `chat/state.svelte.js`, `ModelPicker.svelte`, `Home.svelte`,
`Onboarding.svelte`, `CommandPalette.svelte`, `SettingsMe.svelte`, `App.svelte` boot area,
`api-shell.ts`, `chat/model-prefs.svelte.js` (new), `internal/apimgr/admin.go`. Line numbers
are from HEAD at audit time; expect drift in those files.

Verdict: the v2 backend is **complete** (every v2 endpoint is registered in Go — ratings, forks,
exports, share, memories, KB, images, audit, TOTP, me-settings). The frontend is ~90% wired.
The "incomplete" feeling is dominated by **two dead routes** (P0-1) and **one always-400
setting** (P0-2), plus a handful of server endpoints with no UI (share, exports, tags).

---

## P0 — broken, user-visible

### P0-1 `/knowledge` and `/images` render "Not found" (pages fully built, no App branch)

The router table and imports exist; the render chain doesn't.

- Route names (`web/src/lib/router.svelte.js:4-18`): home, chat, compare, gallery,
  **knowledge (:9)**, **images (:10)**, settings, admin, login, register, welcome, onboarding, share.
- App imports both (`web/src/App.svelte:21-22`) but the `{#if}` chain
  (`App.svelte:215-231`) branches only: home, chat, compare, gallery, settings, admin,
  else→**404 line (:227-230)**. Bare chain (`:167-178`): login, register, welcome(→FirstRun),
  onboarding, share(else). `knowledge`/`images` fall into the 404 `{:else}`.
- Full diff, routes ↔ branches ↔ files: every route has a branch and a file **except**
  `knowledge` and `images` (files `routes/Knowledge.svelte`, `routes/Images.svelte` are
  complete — CRUD/upload/search/generate/skeletons/empty states included).
  `welcome`→FirstRun ✓, `share` ✓, register ✓, onboarding ✓; first-run redirect → `/welcome` ✓
  (`lib/session.svelte.js:42-50`); no folders/tags routes exist (they are sidebar-only features ✓).
- **Dead links pointing into the 404:** Sidebar nav rows `/knowledge` + `/images`
  (`web/src/lib/ui/Sidebar.svelte:482-499`) — both desktop and mobile drawer. No other
  unbranched hrefs found.

**Fix:** add two lines to `App.svelte:226` area:
`{:else if route.name === 'knowledge'}<Knowledge />` and same for `images`. Nothing else needed.

### P0-2 Profile "Save" always fails with 400 (`name` not in meSettingKeys)

- `setMeName` PUTs `{ name }` (`web/src/lib/api-shell.ts:54`).
- `meSettingKeys` = theme, setup_done, accent, default_model, hidden_models
  (`internal/apimgr/admin.go:259-265`) — **no `name`**.
- `handleMePut` hard-400s unknown keys (`admin.go:418-421`, "unknown setting key name").
- Caller: `SettingsMe.svelte:77` (`saveName`, :72-85) → user sees red toast every save. Not silent.

**Fix:** one line in `admin.go`: `"name": true` in `meSettingKeys` (+ optional length validator in
`meSettingValidators`, ≤200 runes). File is being edited — hand to the admin.go owner. No FE change.

---

## P1 — endpoints exist, UI missing (feature unreachable) / UI exists, no endpoint

### P1-1 Share toggle missing — whole share feature unreachable
- `shareChat` / `unshareChat` (`web/src/lib/api.ts:177-178`) — **zero call sites** (only
  non-api.ts mention of share_token is a badge, `Sidebar.svelte:378`).
- Go: `POST/DELETE /api/chats/{id}/share` registered; `/s/:token` page (`routes/Share.svelte`)
  renders + revoked-link UX ✓ — but no link can ever be created from the app.
- ChatView header (`routes/ChatView.svelte:353-365`) has only the params drawer button.

**Fix:** ChatView header menu: Share/Unshare toggle → `api.shareChat(id)` then copy
`location.origin + '/s/' + token` via `copyText` (`lib/chatview-api.ts:165`); show state from
`chat.share_token`; sidebar dot then lights up naturally.

### P1-2 Export buttons missing
- Go: `GET /api/chats/{id}/export`, `GET /api/me/export` (`internal/library/export.go`, mux).
- **Zero FE call sites** (grep `/export` in web/src → none).

**Fix:** "Export .md" item in the same ChatView header menu (link with `data-no-spa` +
`download`, or fetch→blob), same-origin cookie works.

### P1-3 Tags: no create, no assign — tag feature unreachable
- Sidebar renders tag filter chips (`Sidebar.svelte:503-517`) from `api.listTags` (:143), but
  `createTag/updateTag/deleteTag/setChatTags` (`api.ts:182,307-309`) have **zero call sites**.
- Go: full tags CRUD + `PUT /api/chats/{id}/tags` registered.

**Fix (minimal):** "New tag" affordance next to chips + "Tag…" item in the chat-row dropdown
(`Sidebar.svelte:385-406`) writing `setChatTags`. If out of scope for launch, hide the chip row.

### P1-4 Model prefs (default/hidden models) — server + module exist, never loaded
- `web/src/lib/chat/model-prefs.svelte.js` (new): `load()`, `setDefault`, `hide`, `unhide`,
  `unhideAll` — **no external callers**; only `isHidden` is consumed
  (`chat/state.svelte.js:6,110`), and since `load()` never runs, `hiddenModels` stays `[]`.
- Server keys `default_model` / `hidden_models` are allowlisted + validated
  (`admin.go:263-276`). `Home.svelte` still uses localStorage `getHomeModel`
  (`api-shell.ts:41-46`), not the server default.

**Fix (leave to the agent currently editing state/ModelPicker/Home):** call `load()` from
`loadModels()`, add hide/default controls in `ModelPicker.svelte`, seed Home from
`modelPrefs.defaultModel`.

### P1-5 Admin System tab misses two server settings
- `kb_embed_model` (`admin.go:91`) — the RAG embedding escape hatch — is not in the SystemTab
  form (fields are task_model/system_prompt/search_url/registration,
  `lib/admin/tabs/SystemTab.svelte:21-24`).
- `title` (instance display name, `admin.go:87`) — never read/written by any FE code.

**Fix:** add two Fields to SystemTab mirroring task_model's "provider:model" picker; either
surface `title` (e.g., login/sidebar brand) or drop the key.

---

## P2 — polish gaps

- **P2-1 Accent never syncs.** `setAccent` is localStorage-only (`lib/accent.svelte.js:41-50`);
  `syncAccentFromServer` (:54-64) has **zero callers**; server allowlists `accent`
  (`admin.go:262`). Fix: in `setAccent`, fire `setMeSettings({accent})`; on boot (after
  bootstrap's me-settings fetch) call `syncAccentFromServer(s.accent)`.
- **P2-2 Theme never syncs** despite `theme` being an allowlisted me-key
  (`admin.go:260`); `theme.svelte.js` is localStorage-only. UI copy says "on this device", so
  either wire it or remove the key from the contract.
- **P2-3 CommandPalette lacks Knowledge/Images actions** — actions array has New chat/theme/
  Compare/Gallery/Settings/Admin/logout only (`lib/extra/CommandPalette.svelte:96-103`).
  Add two rows (also fixes discoverability when the rail is collapsed).
- **P2-4 Sidebar rail mode has no Knowledge/Images icons** — nav rows render only `!rail`
  (`Sidebar.svelte:467,482`); rail block (:422-439) has Search but no BookOpen/Image icons.
- **P2-5 404 fallback is a bare one-liner** ("Not found — go home", `App.svelte:227-230`);
  anonymous visitors to app pages get chrome with no sign-in CTA, and once P0-1 lands,
  `/knowledge` anonymous → 401 toast (Knowledge.svelte:62-64 handles 404 quietly, not 401).
  Fix: give the else-branch a small card (Home/Sign-in); optionally gate Knowledge/Images on
  `session.user`.
- **P2-6 Images page offers Generate even with no image models** — `hasImageModels`
  (`lib/pages-api.ts:307-309`) has zero callers; `Images.svelte:34` builds provider options
  unconditionally. Fix: banner/disabled Generate when `!hasImageModels(models)`.
- **P2-7 Stale comments** (confuse auditors, not users): `api-shell.ts:57-59` claims
  `/api/me/password` 404s — it exists now (`admin.go:153`) and works; :80-84
  `logoutEverywhere` deviation (works, by design); `extra-api.ts:78-86` thumbURL //INTEGRATION —
  Gallery loads full-size files because no thumb endpoint is exposed (perf nit).
- **P2-8 Dead helpers (harmless, prune or ignore):** `completeProxy` (`api.ts:336`),
  `healthz` (`api.ts:351`), `fileURL` (`api.ts:296`), `firstLine` (`pages-api.ts:181`),
  `normalizeRatings` is internal-use only (fine).

---

## Dimensions swept (results)

**1. Routes.** Router(13) ↔ App branches ↔ 15 route files: only `knowledge`+`images` missing
branches (P0-1). All hrefs (`/`, `/c/:id`, `/login`, `/settings`, `/admin`, `/welcome`, Sidebar's
`/knowledge`,`/images`) resolve except the P0-1 pair. 404 UX = P2-5.

**2. API↔UI.** All Go mux routes enumerated. FE-dead endpoints: share POST/DELETE (P1-1),
both exports (P1-2), tags CRUD + chat-tags (P1-3), settings `title`/`kb_embed_model` (P1-5).
Wired-and-verified: ratings thumbs + comment (`ChatView.svelte:290-325`, `MessageRow.svelte:21-22`
ThumbsUp/Down; 404-degrades via `ratingsOk`), forks (:328-341, hides on 404), KB (Knowledge page
full CRUD/search; Composer selector `Composer.svelte:304-322`; `withKbIds`), memories (SettingsMe
list/add/edit/delete/toggle/source badge :738/extract picker :787-814 — all endpoints exist),
2FA setup/enable/disable + login TOTP step (`Login.svelte:23-43`) + admin reset-2fa
(`UsersTab.svelte:103-107`), images generate/history, audit tab (`AuditTab` w/ cursor paging),
prompt library CRUD (`SettingsPrompts.svelte`) + slash autocomplete (`SlashPrompts` in Home +
Composer), provider sync buttons (`ProvidersTab.svelte:80,98`, `Onboarding.svelte:138`).
Compare/Gallery work end-to-end (Compare: ephemeral chats created/deleted
`Compare.svelte:80-83,204-208`; Gallery: listFiles + same-origin /files). No UI action hits a
missing Go endpoint — the reverse direction is where the gaps are.

**3. Placeholders.** No `console.log`, no "coming soon". Remaining `TODO`/`//INTEGRATION` markers
are P2-7's stale comments + intentional hooks (accent, thumbURL, pick-model event — ChatView does
listen). No stub pages.

**4. Empty states.** ✓ Knowledge, Images, Gallery (:124), Audit, Memories (SettingsMe:708-711),
Prompts, Sidebar, ChatView. Gaps: 404 branch (P2-5); anonymous/401 paths (P2-5).

**5. Loading.** ✓ skeletons/spinners: Knowledge (list + per-collection), Images (history grid),
Home (models), ChatView (pulse), Sidebar, Audit, Memories, Prompts, Compare (model list),
Admin session gate. Bare: none material.

**6. Keyboard/palette.** ⌘K works; missing Knowledge/Images actions (P2-3); Compare/Gallery
present; ⌘B sidebar toggle ✓; arrow-nav in sidebar ✓.

**7. Mobile.** No desktop-only v2 layouts found (Compare `md:grid-cols` :266; Images/Knowledge
use `auto-fill/minmax`; audit table has `overflow-x-auto`; drawer + safe-area insets wired).

---

## Suggested fixer split (file-disjoint, same-day close)

**Cluster A — "Make the app reachable" (P0-1, P2-3, P2-4, P2-5) — DO FIRST, ~30 min**
Files: `web/src/App.svelte` (render block :215-231 only — do NOT touch the boot $effects
another agent owns), `web/src/lib/extra/CommandPalette.svelte` (actions array — coordinate,
owner is live-editing this file), `web/src/lib/ui/Sidebar.svelte` (rail icons).
Unlocks two fully-built pages + their sidebar links. Highest impact/effort ratio in the repo.

**Cluster B — "Me-settings correctness" (P0-2, P1-4, P2-1/P2-2) — assign to the admin.go /
model-prefs owners** (their live files overlap this cluster exactly): add `"name"` to
`meSettingKeys` (+ validator/test next to `admin_memodels_test.go`); call `modelPrefs.load()`
from `state.svelte.js#loadModels` + ModelPicker hide/default controls; accent PUT+boot-sync
(`accent.svelte.js` + the App boot area they already own). No new files needed.

**Cluster C — "Chat header actions" (P1-1, P1-2, P1-3, optional P2-6)**
Files: `web/src/routes/ChatView.svelte` (header menu: share toggle + copy link + export .md),
`web/src/lib/ui/Sidebar.svelte` **or** ChatView menu for tag assign/create (if A touched
Sidebar, put tags in the ChatView menu instead to stay disjoint), `routes/Images.svelte`
(P2-6 gate). `ChatView.svelte` is **not** on any live-editor list — safe today. Biggest P1
value (share + export + tags = three invisible v2 features shipped).

Everything else is P2 backlog; nothing else blocks public release.
