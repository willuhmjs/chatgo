# FE-Extra UI — CommandPalette, Toasts, Compare, Gallery, Share

Owned files (FE-extra):

| File | Purpose |
| --- | --- |
| `web/src/lib/extra/CommandPalette.svelte` | ⌘K palette (chats / actions / models) |
| `web/src/lib/extra/ToastHost.svelte` | polished toast renderer over the scaffold store |
| `web/src/lib/extra/toasts.ts` | `ok()` / `fail()` / `sticky()` shorthands over `push()` |
| `web/src/lib/extra/fuzzy.ts` | 30-line fuzzy scorer used by the palette |
| `web/src/lib/extra/md.ts` + `markdown.css` | marked+DOMPurify renderer for FE-extra surfaces |
| `web/src/lib/extra-api.ts` | helper layer over `$lib/api` (api.ts untouched) |
| `web/src/routes/Compare.svelte` | `/compare` split-pane model shootout |
| `web/src/routes/Gallery.svelte` | `/gallery` artifact/image grid |
| `web/src/routes/Share.svelte` | `/s/:token` public read-only view |

## MOUNTS

FE-extra does **not** own `App.svelte`, so the shell agent must mount two
components there. Nothing in `App.svelte` has a palette slot today, hence
this section (CONTRACTS: shell owns App.svelte).

```svelte
<!-- App.svelte -->
<script lang="ts">
  import CommandPalette from '$lib/extra/CommandPalette.svelte';
  import Toasts from '$lib/extra/ToastHost.svelte';
</script>

<!-- once, anywhere in the tree: -->
<CommandPalette />
<Toasts />
```

1. **`<CommandPalette />`** — self-registering: mounting it binds `mod+k`
   through `$lib/shortcuts.svelte.js` (`onShortcut`). It renders nothing
   until invoked. Unmounting unregisters the shortcut.
2. **`<Toasts />`** — replaces the inline baseline toast stack currently in
   `App.svelte` (the `<!-- toast stack (FE-extra will theme …) -->` block).
   It renders the same `$lib/toast.svelte.js` store — which keeps its exact
   scaffold API (`push/dismiss`, stacking array, `ms` timeout, `kind`) with
   zero edits — with kind icons, colored edges, slide-in stacking and a
   dismiss X. Until FE-shell swaps it, the baseline stack still works.

## INTEGRATION notes

- **`chatgo:pick-model`** — when a palette model item runs, it (a) persists
  `providerID:modelID` to `localStorage['chatgo:last-model']`, (b) dispatches
  `window` CustomEvent `chatgo:pick-model` with
  `detail: { provider_id, model_id }`, and (c) if the current route is a
  chat, `PATCH /api/chats/{id}` immediately. **FE-chat**: have Composer/
  ModelPicker listen for the event and treat it like a local ModelPicker pick
  (persist like ChatView does). Marked `//INTEGRATION` in `extra-api.ts`.
- **Share bootstrap** — *verified, no change needed*: `bootstrap()` in
  `session.svelte.js` swallows the 401 and never redirects, and `App.svelte`
  renders routes ungated, so `/s/:token` is genuinely public. If FE-shell
  later adds an auth route guard, it must exclude `/s/*`.
- **SSE** — `$lib/sse.svelte.js` keeps one global `AbortController`, so
  Compare bypasses it and calls `api.streamMessage()` per pane with its own
  controller for concurrent streams. `md.ts` is a deliberate small duplicate
  of FE-chat's planned Markdown component (it did not exist yet / was not
  importable); swap to `lib/chat/Markdown.svelte` if FE-chat exports a pure
  render function.
- **Compare temp chats** — each pane lazily `POST /api/chats` titled
  `compare: side-by-side (temp)` and `DELETE`s them on route leave and on
  "Clear panes". A hard refresh can orphan one (browsers don't run unload
  fetches reliably) — harmless, visible in the sidebar with that title.
- **Gallery thumbnails** — `<img src={file.url}>` against same-origin
  `GET /files/{id}/{name}` sends the session cookie automatically, so
  thumbnails/preview work with no extra plumbing. The backend computes a
  512px `.thumb` jpeg but CONTRACTS.md exposes **no thumb URL**, so originals
  are used (`thumbURL()` in `extra-api.ts` is the single swap point).
  Gallery lists `listFiles()` filtered client-side to `kind !== 'upload'`
  (contract's `?kind=artifact` alone would hide generated `image` files).
- **Palette empty state** — with an empty query the panel shows the literal
  hint **“Start typing…”** *plus* the default sections (recent chats /
  actions / models), so recents are one keystroke or ↓ away; a non-matching
  query shows “No matches for …”.

## Deviations from the brief

1. `CommandPalette`/`Toasts` are **not mounted** (App.svelte is FE-shell's);
   mount instructions above. The palette therefore only activates once the
   shell mounts it.
2. Markdown is a **duplicate** `md.ts` (marked + DOMPurify, no highlight.js)
   because `lib/chat/**` did not exist at build time; see INTEGRATION.
3. No "copy link" button in the Gallery modal (brief: "share of nothing —
   download only"), only `a[download]` on the same-origin `/files` URL.
4. Toast wrapper only: `toast.svelte.js` kept byte-identical (it already had
   stacking + timeout; the wrapper adds visual stacking/kinds).
5. Whole-project `svelte-check --threshold error` had **0 errors in all
   FE-extra files** at handoff; at that moment unrelated in-flight files
   owned by FE-shell (`lib/ui/**`) and FE-admin (`lib/admin/**`) carried
   errors of their own (not touched per ownership rules).
