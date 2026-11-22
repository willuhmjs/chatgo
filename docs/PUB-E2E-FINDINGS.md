# Public-Server UI E2E Findings — v0.4.3 browser matrix

**Date:** 2026-11-22 · **Tester:** automated UI-E2E (Playwright + system Chrome) against a live
chatgo server (`mock` upstream on :9111, fresh SQLite DATA_DIR).

**Tested build:** `bin/chatgo` v0.4.3 built 16:52 from `39b4cd9`, serving bundle
`assets/index-2lspnrIZ.js` on `http://127.0.0.1:8110`.
**Cross-checked build:** a binary built at 17:33 from HEAD `7b57b76` (bundle
`index-8PxllPwk.js`) run out-of-tree on `:8113` — used to mark which findings are already fixed
upstream. No repo sources were modified by this test pass.

All repro steps below are automated in `/tmp/uitest/matrix.mjs` (scenarios S1–S9; rerun with
`BASE=http://127.0.0.1:8110 node matrix.mjs`). Screenshots and console/network captures:
`/tmp/uitest/shots/`. Machine-readable results: `/tmp/uitest/results-8110-def.json` (tested
build) and the `:8113` rerun (HEAD build).

---

## Result matrix (tested build 39b4cd9 / index-2lspnrIZ.js)

| # | Scenario | Result | Note |
|---|----------|--------|------|
| S1 | Login renders home | PASS | fresh admin is bounced to `/onboarding` once (observation O-1) |
| S2a | Admin → Providers: expand "n/m models" | **FAIL** | **Bug A** (headline) |
| S2b | Providers: sync / edit / enable-toggle / delete-confirm | PASS | |
| S2c | Admin → Users: pending-row checkbox → Approve enabled | **FAIL** | **Bug B** (same class as A) |
| S3a | Model picker: filter + star-default persists | PASS | after S3a passes, star→`default_model` lands in `/api/me/settings` |
| S3b | Model picker: eye hides → Settings lists → unhide | PASS | |
| S3c | Home model dropdown lists synced models | PASS | |
| S4 | Chat: send / stream / share-link / tags | PASS | share copies `/s/<token>` + toast "Link copied"; tags persist |
| S4c | Chat: **Export as Markdown** starts a download | **FAIL** | **Bug E** (export fully broken, all formats) |
| S4b | Chat view: picker trigger shows persisted model on fresh load | **FAIL** | **Bug D** ("no model" label) |
| S5a | Knowledge: collection → upload → Indexed → citation → search | PASS | |
| S5b | Images: no image provider → graceful error banner | PASS | role=alert, no crash |
| S6 | Account menu ~256 px (not full width) | PASS | |
| S7 | Accent color (pink) persists after reload | PASS | `me-settings.accent` |
| S8 | ⌘K palette ("Go to Knowledge") + sidebar collapse/expand | PASS | |
| S9 | Route sweep `/`, `/settings`, `/admin*`, `/gallery`, `/compare`, `/knowledge`, `/images`, `/s/*`, unknown — zero uncaught pageerrors | **FAIL** | **Bug C** — `/gallery` throws on every visit |

`11 PASS / 5 FAIL`. All five failures are product bugs, each root-caused below with a
`file:line`. The HEAD-build rerun (:8113) flips **A, B, C, D to PASS** while **E still FAILs** —
status of each bug against HEAD is marked per report.

---

## FAIL-first bug report

### Bug E — All "Export" menu items do nothing and blank the app (HIGH — still broken at HEAD)

**Repro** (any chat with ≥1 message, signed in):
1. Open a chat → header kebab (**⋯** "Chat actions").
2. Click **Export as Markdown (.md)** (also `.json` and **Export all data (.zip)**).

**Expected:** a file downloads.
**Actual:** nothing downloads, no toast, no console error. The address bar rewrites to
`/api/chats/<id>/export?format=md`, the chat view goes blank (unknown SPA route → only the
sidebar renders), and a manual reload is needed. Silent data-loss-adjacent failure: users cannot
export at all.
**Evidence:** `/tmp/uitest/shots/E-export-hijacked.png`, `probe12` network trace: DOM click
observed, **zero** network requests (server log confirms the request never arrives; a direct
`page.goto` to the same URL *does* download fine, so the endpoint + auth are healthy).
Reproduces on the tested build **and** on the HEAD `7b57b76` build (:8113 rerun).

**Root cause** — interaction of two correct-looking pieces:
- `web/src/lib/chat/ChatActionsMenu.svelte:132-140` — the download helper sets
  `a.download = ''` (empty string). As a DOM *property*, `a.download` is then `''` → **falsy**.
- `web/src/App.svelte:170-180` — the "SPA-ify same-origin anchor clicks" `$effect` guards with
  `... || a.target || a.download || a.hasAttribute('data-no-spa')`. Because `a.download` reads
  `''`/falsy, the guard misses, the handler calls `e.preventDefault()` + `navigate(href)`, and
  the router `pushState`s the API URL instead of letting the browser download it.

**Fix (any one):** use `a.hasAttribute('download')` in the App.svelte guard (recommended —
attribute presence is the correct check); or in the helper set a real filename
(`a.download = 'chat.md'`); or `a.setAttribute('download','')`. Add a `data-no-spa` regression
test that clicks each menu item and asserts a `download` event (S4c in the harness does exactly
this and currently fails).

---

### Bug A (HEADLINE in tested build) — Admin → Providers "n/m models" expand button does nothing

**Repro:** Admin → Providers → click the **"3/3 models"** button on a provider row.
**Actual:** `GET /api/providers/<id>/models` fires and returns **200** — and then *nothing*:
`aria-expanded` stays `"false"`, zero model-editor rows render, **zero console errors**.
**Evidence:** `/tmp/uitest/shots/S2a.png`, `/tmp/uitest/shots/S2a.txt` (network trace + DOM
probe). This is the reported "does nothing" symptom, reproduced deterministically.

**Root cause:** `web/src/lib/admin/tabs/ProvidersTab.svelte:20`
`let expanded = $state(new Set<string>())` mutated in place at `:69` / `:72` / `:148`
(`expanded.add(id)` / `.delete(id)`). Svelte 5.59.1's deep-state `proxy()` returns
Set/Map **unwrapped** (`web/node_modules/svelte/src/internal/client/proxy.js:53-55` — prototypes
other than plain object/array are returned raw; verified at runtime in-page:
`$state`-assigned Set is never proxied), so `.add/.delete` never notify and the `{#if
expanded.has(p.id)}` block (`:237`, `:281`) never re-evaluates.

**Status:** **fixed at HEAD** by `7b57b76` (`$state.raw` + copy-on-write reassignment); the
HEAD-build rerun passes S2a. Any deployment built before 17:16 still ships it.

---

### Bug B — Admin → Users: pending-approval checkbox never enables "Approve selected"

**Repro:** Admin → Users → check the checkbox on a `pending` user.
**Actual:** the checkbox goes visually checked, but "Approve selected (N)" stays **disabled**
with stale count — `selected.size` readers never re-run.
**Evidence:** `/tmp/uitest/shots/S2c.png` / `.txt`.

**Root cause:** same class as A — `web/src/lib/admin/tabs/UsersTab.svelte:18`
`let selected = $state(new Set<string>())` mutated in place at `:53-56`
(`toggleSelect` → `.add/.delete`). Instructive: `selectAllPending()` at `:57-59` *did* use
copy-on-write, so "select all" worked while per-row checkboxes were dead — two idioms in one file.

**Status:** **fixed at HEAD** (`7b57b76`); S2c passes on the HEAD build.

---

### Bug C — `/gallery` crashes with an uncaught TypeError on every visit

**Repro:** navigate to `/gallery` (fresh or in-app) in the tested build.
**Actual:** uncaught
`TypeError: n(...).filter is not a function` (bundle `index-2lspnrIZ.js:1764:38564`); the route
renders nothing. Any visit, even with zero files.
**Evidence:** `/tmp/uitest/shots/S9.png` / `S9.txt` (route-sweep attributes the crash to
`/gallery`).

**Root cause:** contract drift. `GET /api/files` returns the paged envelope
`{items, page, per, total}`, but tested-revision `web/src/routes/Gallery.svelte:39` did
`files = await api.listFiles()` and `:22` derives `outputs = files.filter(...)` →
`.filter` on an object → crash every render.
**Status:** **fixed at HEAD** — `web/src/routes/Gallery.svelte:64` now calls
`listFilesPage(...)` and unwraps `.items`; `/gallery` is clean in the HEAD route sweep.
**Not in any running binary built before ~17:30.**

---

### Bug D — Chat-view model picker shows "no model" on fresh load/reload

**Repro:** pick a model in a chat (server persists `model_id`), then reload the page (or open
the chat by URL / shared link).
**Actual:** the composer trigger reads **"no model"** although the chat keeps using the selected
model (`GET /api/chats/<id>` → `model_id` set); it only corrects itself after opening the picker
once.
**Evidence:** `/tmp/uitest/shots/D1-fresh-no-model.png` (before), `D3-after-open.png` (after
opening popup), `S4b.txt` (server-side `model_id` + DOM label captured together).

**Root cause:** `ModelPicker.svelte` lazily calls `loadModels()` only when the popup opens
(tested build `:63-65`; HEAD `:92-98` `toggleOpen`), and `ChatView.svelte` never primes the
model store on mount → `available` is empty → `current` undefined.
**Status:** **partially fixed at HEAD**: `ModelPicker.svelte:57` falls back to the raw
`chat.model_id`, so the trigger now shows `mock-chat` (raw id) instead of "no model" — still
wrong display-name-wise on cold load. **Proper fix:** call `loadModels()` once on ChatView mount
(or eagerly in the models module), then the display label is correct immediately.

---

## Observations (not filed as bugs)

- **O-1 — Onboarding bounce:** an admin whose provider was created via API is bounced to the
  `/onboarding` wizard on first home visit (the wizard's `setup_done` me-settings key was never
  marked because setup was done out-of-band). "Skip setup" dismisses permanently. Arguably
  correct, but flag it if API-driven provisioning is a supported install path (`App.svelte`
  ~line 121 gate).
- **O-2 — Hidden model rows:** `Hide model` closes the picker popup by design (toast confirms);
  count expectations in automation must account for that (`ModelPicker.svelte` `hideModel`).
- **O-3 — Two elements share `aria-label="Chat actions"`** (sidebar row kebab + header kebab);
  any automation or future a11y fix should disambiguate (e.g. "Chat actions" vs
  "Chat row actions").

## Notes on method

- Chrome via `channel: 'chrome'`, console + `pageerror` + API request/response capture on every
  scenario; every failure screenshots to `/tmp/uitest/shots/<id>.png` with a paired `.txt`
  containing the error, pageerrors, console lines, and non-2xx responses.
- Server-side API health was verified independently with `curl`
  (providers/models PUT, users role, share/export endpoints all healthy) — all five bugs above
  are front-end defects.
