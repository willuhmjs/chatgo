# FE-Shell UI — components & integration points

Owner: **FE-shell**. Everything here lives in `web/src/App.svelte`,
`web/src/routes/{Home,Settings,SettingsMe}.svelte`, `web/src/lib/ui/**` and
`web/src/lib/api-shell.ts`. Scaffold-owned files (`api.ts`, `router.svelte.js`,
`session.svelte.js`, `theme.svelte.js`, `toast.svelte.js`, `shortcuts.svelte.js`,
`app.css`) were left untouched; `api.ts` additions are wrapped in `api-shell.ts`.

## App shell (`App.svelte`)

- Layout: fixed sidebar + `flex` main column; main area is the scroll
  container (`min-h-0 flex-1 overflow-y-auto`). Route components fill `h-full`.
- **Bare routes** `login` / `register` / `share` render full-page without
  sidebar/top bar (their components are imported unchanged).
- Sidebar persistence: `localStorage['chatgo:sidebar-collapsed']`
  (`'1'` = 72px icon rail). Toggle: collapse button, or **⌘/Ctrl+B**
  (on mobile ⌘B toggles the drawer instead).
- Mobile (`<768px`, via `matchMedia`): sidebar becomes an off-canvas drawer
  (`transform` + scoped CSS, not inline style) with a click-away backdrop;
  Esc closes; any navigation closes; top bar shows hamburger, chat title, and
  new-chat button.
- Chat title: on `/c/:id` the shell fetches `api.getChat(id)` for the mobile
  top-bar title and `document.title`.
- **SPA anchor interception**: one window-level click handler routes any
  `<a href="/…">` through `navigate()` (skips `target`, `download`,
  modifier-clicks, and `data-no-spa`). Route components get SPA links for free.
- Toast stack (scaffold baseline) + `<ConfirmDialog/>` mounted once here.
- ChatView is rendered as `<ChatView id={route.params.id} />` — matches its
  existing `$props()` (`id: string`). **Do not rename this prop.**

## Component kit (`lib/ui/`)

| Component | Notes |
| --- | --- |
| `Button` | variants `primary · gradient · secondary · ghost · outline · danger`, sizes sm/md/lg, `full` |
| `IconButton` | **required** `label` → `aria-label`; 44px on coarse pointers; `active` state |
| `Input` / `Select` / `Toggle` | token-styled form fields, focus-visible rings, `bind:value` / `bind:checked` |
| `Modal` | `bind:open`, esc/backdrop/× close, `onclose` callback, body scroll-lock, focus restore |
| `Dropdown` + `MenuItem` | trigger = default snippet; menu = `items` snippet receiving `{ close }`; menu is `position:fixed` (escapes scroll clipping), flips near viewport bottom |
| `Tooltip` | CSS hover/focus tooltip, `side: 'top' | 'right'` (rail uses right) |
| `Avatar` | `avatar_url` image or gradient initials |
| `Skeleton`, `EmptyState` | loading + zero-state |
| `confirm.svelte.js` + `ConfirmDialog` | `await confirmDialog({ title, message, confirmLabel, danger })` → `Promise<boolean>`; mount once (App does) |
| `chat-grouping.ts` | `timeAgo`, `dayGroup`, `groupByDay`, `initials`, `firstName` |
| `Sidebar` | see below |

## Sidebar (`lib/ui/Sidebar.svelte`)

- Loads `api.listChats({ q, tag, page, per: 50 })`, `listFolders`, `listTags`
  whenever an active (non-`pending`) user exists; reloads on debounced search
  (**300ms**, server-side `q=`), tag filter, and any navigation (picks up new
  chats/titles). Pagination: “Show more (n)” button.
- Sections: **Pinned** → **Folders** (collapsible, persisted in
  `localStorage['chatgo:folders-collapsed']`; chats render nested inside) →
  **Today / Yesterday / Last 7 days / Older** (by `updated_at`). While a
  search/tag filter is active, results render flat.
- Row hover menu: inline **Rename** (Enter/blur commit, Esc cancel), **Pin /
  Unpin**, **Move to folder…** (radio-list modal incl. “No folder”), **Delete**
  (confirm dialog). Shared `chatRow` snippet used in all sections.
- Folder menu: rename + delete (delete keeps its chats, moves them to the
  main list). “New folder” button in the Folders header.
- Keyboard: ↑/↓ roving focus between chat rows and folder headers inside the
  list; Enter activates the focused row (`<a>` with SPA interception).
- Pending users: “Awaiting approval” banner, **no chats/folders/tags fetch**.
- Collapsed rail: 72px, icon-only recent chats with tooltips, icon user block.
- Bottom user block: initials avatar, name + role badge (admin), email, theme
  toggle, settings link, sign-out. `padding-bottom: env(safe-area-inset-bottom)`.

## Home (`routes/Home.svelte`) — route `/`

- Hero greeting (time-of-day + first name), **model pill picker**
  (`GET /api/models`; selection persisted in `localStorage['chatgo:home-model']`
  as `providerID:modelID`), 3 suggestion cards (click prefills the composer),
  and a quick composer.
- **Send = create + hand-off**: `api-shell.startChat(prompt, model)` →
  `POST /api/chats` with `{ title: prompt.slice(0,60), provider_id, model_id }`,
  stashes the prompt at `localStorage['chatgo:pending-prompt:<chatId>']`,
  then `navigate('/c/<id>')`.
- **ChatView integration point (FE-chat):** on mount with a chat id, call
  `takePendingPrompt(id)` from `$lib/api-shell`; if it returns a string, seed
  the composer / auto-send it as the first message. `Enter`/`⌘Enter` behavior
  follows `getSendOnEnter()` from the same module.
- `pending` users get an approval EmptyState instead of the hero (no model fetch).

## Settings (`routes/Settings.svelte` + `SettingsMe.svelte`)

- Tab shell (`role=tablist`): **Profile / Preferences / Account**. Admins also
  see an “Open admin console →” link to `/admin` (FE-admin route, untouched).
- Profile: display-name form. Persisted via `PUT /api/me/settings` under key
  `name` (see deviations) and reflected live in `session.user.name`.
- Preferences: theme dark/light (uses `theme.svelte.js`, persisted), **Send on
  Enter** toggle (`localStorage['chatgo:send-on-enter']`, default on; a
  `chatgo:prefs` window event notifies open composers).
- Account: email/role, password-change stub form, **Sign out** and
  **Sign out everywhere** (confirm dialog first).

## api-shell.ts additions (api.ts is frozen)

`getSendOnEnter/setSendOnEnter`, `getHomeModel/setHomeModel`,
`setMeName`, `changePassword`, `logoutEverywhere`, `stashPendingPrompt /
takePendingPrompt`, `startChat`, `PREFS_EVENT`.

## Contract deviations

1. **Password change** — `POST /api/me/password {current_password,
   new_password}` is *not* in `docs/CONTRACTS.md` §HTTP surface. Form is
   wired to it and shows “This server does not support password changes yet.”
   on 404. TODO for BE: ship endpoint; it's marked `CONTRACT-DEVIATION` in
   `api-shell.ts`. It is also shown for SSO users (User DTO exposes no
   password-hash flag to hide it).
2. **Logout everywhere** — no session-revoke endpoint exists; calls
   `POST /api/auth/logout` (ends the current server-side session). Marked
   `CONTRACT-DEVIATION`; TODO swap for a real revoke-all endpoint.
3. **Profile name** — no dedicated PATCH endpoint; stored as `name` key in the
   free-form `PUT /api/me/settings` map. Marked `CONTRACT-DEVIATION`.
4. `app.css` was already seeded with tokens and is not in my ownership set —
   no changes made there; all new styling uses component-scoped CSS + the
   existing tokens.

## Verification

`npx svelte-check --threshold error` → **0 errors in all FE-shell-owned
files** (remaining project errors at time of writing are in FE-chat-owned
`src/lib/chatview-api.ts` / `src/lib/chat/MessageRow.svelte`).
