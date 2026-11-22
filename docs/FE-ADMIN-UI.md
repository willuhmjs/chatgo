# FE-Admin UI — integration notes

Owned by **FE-admin**: `web/src/routes/{Login,Register,Admin}.svelte` and everything under
`web/src/lib/admin/`. Built against `docs/CONTRACTS.md`; nothing outside this set was touched
(no App/router/api.ts edits, no git/npm/vite — verified with `npx svelte-check --threshold error`:
**0 errors in every owned file**; see §Deviations for project-wide status).

## File map

```
web/src/lib/admin/
  api-admin.ts            typed admin facade (see deviation #1) + settings allowlist
  format.ts               relTime / ctxLabel / errMsg helpers
  TokensPanel.svelte      embeddable personal API-token manager
  AuthShell.svelte        split hero layout shared by Login + Register
  ui/                     local primitives (Modal, Confirm, Toggle, Badge, Field,
                          EmptyState, Spinner, styles.ts) — see deviation #10
  tabs/ProvidersTab.svelte  providers table, expandable models, sync, CRUD modal
  tabs/ProviderModal.svelte create/edit provider form
  tabs/UsersTab.svelte      pending approval queue + users table + role selects
  tabs/SystemTab.svelte     system settings form + dirty save bar + TokensPanel
web/src/routes/Login.svelte     split hero, email/pw + SSO, ?error= banner
web/src/routes/Register.svelte  strength meter, closed-policy card, approval screen
web/src/routes/Admin.svelte     guard (403 card) + tab subnav
```

## Tab routing

The scaffold router (`$lib/router.svelte.js`, frozen) knows only `/admin` — no `/admin/:tab` —
and its reactive `location` tracks **pathname only** (queries stripped by `navigate()`). So:

- Active tab is internal `$state` inside `Admin.svelte`.
- Mirrored to `?tab=users|system` with `history.replaceState` (providers = clean URL); the
  router never observes queries, so this cannot desync App.svelte.
- Deep links `/admin?tab=users` resolve on mount. Browser back/forward *between tabs of the same
  path* won't flip the tab (only full path changes drive the router) — accepted limitation.

## Admin → System settings

`GET/PUT /api/settings`. The UI reads/writes **only** this allowlist
(defined in `api-admin.ts` as `SETTINGS_KEYS`):

| key              | control                    | notes |
|------------------|----------------------------|-------|
| `task_model`     | text + datalist of synced models | value format `provider:model` — see deviation #7 |
| `system_prompt`  | textarea                   | empty = none |
| `search_url`     | text                       | empty = search disabled |
| `registration`   | radio: `open` / `approve` / `closed` | matches §Config `Registration` |

PUT always sends all four keys (blank string clears a value). Unknown keys coming from the
server are ignored, never echoed back. Dirty-state save bar compares against the last
fetched/saved snapshot.

## TokensPanel — embedding for FE-shell (Settings page)

`TokensPanel.svelte` is prop-less, self-loading, and works for **any** signed-in user (it is not
admin-only). Embed in the regular Settings page with:

```svelte
import TokensPanel from '$lib/admin/TokensPanel.svelte';
<TokensPanel />
```

A `//INTEGRATION:` comment marks the export path at the top of the file. Behaviour: create modal
→ **one-time full-key reveal** with copy button and "shown once" warning → masked list
(`ck_••••…`; the backend stores only a sha256 so there is nothing to mask-list beyond that) →
delete with confirm. Already wired at the bottom of the Admin System tab.

## Login / Register contract touchpoints

- `GET /api/auth/config` (public) drives: SSO button presence + `{oidc_name}` display name,
  and whether the register link shows (`registration !== 'closed'`).
- SSO = full-page `location.assign('/api/auth/oidc/login')`. The OIDC callback is expected to
  report failures as `/login?error=<code|description>`; banner + toast render it (known codes
  are humanized; `error_description` takes precedence) and the query is scrubbed.
- Register: `POST /api/auth/register`; response `role === 'pending'` → "Request submitted,
  awaiting approval" screen (with a logout button that clears any partial cookie). Any other
  role → toast + redirect to `/login`. Closed policy shows an informational card instead of the
  form.

## Guard

`/admin` for non-admins renders a friendly 403 card (link home; unauthenticated visitors get a
sign-in CTA variant). Uses `session.ready` to avoid flashing the card during bootstrap.

## Deviations & assumptions (reported per CONTRACTS.md rule)

1. **`api-admin.ts` is a facade, not a duplicate.** The brief assumed providers/settings/users/
   tokens endpoints were missing from `api.ts` — they all exist there (frozen scaffold). The
   module re-exports them under one admin surface and adds only genuinely new helpers
   (settings allowlist, pending filter, capability flags). `// CONTRACT-DEVIATION` in-file.
2. **Settings key allowlist is derived.** CONTRACTS.md never literally lists it; keys
   `task_model`, `system_prompt`, `search_url`, `registration` mirror §Config fields
   (TaskModel, SearXNGURL, Registration) + the brief. If BE-apimgr picks different keys, change
   `SETTINGS_KEYS` in `api-admin.ts` only.
3. **Model rename / enable are read-only.** No `PATCH/PUT /api/models/...` exists in the frozen
   HTTP surface, so the providers sub-table displays display-name and enabled state as
   read-only text with a caption. `CAN_EDIT_MODELS` flag in `api-admin.ts` marks the hook-up
   point when BE ships it.
4. **`ModelEntry` has no `enabled` field** in the scaffold DTO; the badge shows `enabled`
   unless a future payload adds `enabled: false`.
5. **Pending queue is client-filtered** from `GET /api/users` (no `/api/users/pending` route);
   the client also doesn't paginate the users list because `api.ts:listUsers()` exposes no
   query params. >100 users will need BE to surface pagination.
6. **Tabs use internal state + `?tab=` replaceState mirror** (see Tab routing) — router is
   frozen and lacks `/admin/:tab`.
7. **Task-model datalist emits `provider_name:model_id`**, preferring the human-readable
   `ModelEntry.provider` (falling back to `provider_id`). If the backend resolves only
   `provider_id:model_id`, change `modelOptionValue()` in `SystemTab.svelte` (one line).
8. **Provider api_key keep-unchanged**: edit form omits `api_key` from the PUT body entirely
   when left blank; create with blank key omits it too. Key inputs are write-only (never
   populated). Assumes BE treats a missing `api_key` on PUT as "keep" and an empty string as
   "clear" (UI never sends empty string).
9. **Self-row guard is UI-only**: own role `<select>` disabled ("can't lock yourself out");
   server must still enforce its own rules (last-admin demotion etc.).
10. **Local primitives kept** (`$lib/admin/ui/*`). `$lib/ui` (FE-shell) did not exist when
    building started and still fails svelte-check while they iterate; local kit matches the
    same design tokens and exports swap-friendly names (`Button`-classes, `Modal`, `Toggle`,
    `Badge`, `EmptyState`, …). FE-shell can swap imports per-component once their kit is green.
11. **svelte-check status:** every owned file is error-free (0 errors, 0 warnings). The
    whole-project run currently reports failures **only** in other agents' in-flight files
    (`src/lib/ui/Sidebar.svelte` → FE-shell, `src/lib/chatview-api.ts` → FE-chat), which this
    agent must not edit.
12. `Register` treats any non-`pending` role response as instant success; assumes the backend
    does **not** auto-login pending users (logout is offered regardless).
