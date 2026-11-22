# FE-CHAT UI — ChatView contract (props in, events out, storage keys)

Owner: **FE-chat**. Files owned:

- `web/src/routes/ChatView.svelte`
- `web/src/lib/chat/**` (`MessageList`, `MessageRow`, `Composer`, `Markdown`,
  `ModelPicker`, `ParamDrawer`, `Lightbox`, `state.svelte.js`, `markdown.ts`,
  `group.ts`, `ui/IconButton.svelte`, `ui/Modal.svelte`)
- `web/src/lib/chatview-api.ts` (typed extensions over `lib/api.ts`; `api.ts` untouched)

`lib/ui/*` (FE-shell) did not exist when this shipped, so chat ships its own
primitives under `lib/chat/ui/` — zero cross-agent imports besides scaffold
lib (`api`, `sse.svelte.js`, `session.svelte.js`, `router.svelte.js`,
`shortcuts.svelte.js`, `toast.svelte.js`).

## Props in

`App.svelte` already mounts us correctly:

```svelte
{:else if route.name === 'chat'}
  <ChatView id={route.params.id} />
```

| prop | type | notes |
| --- | --- | --- |
| `id` | `string?` | chat id from `/c/:id`. If empty/undefined ChatView renders a neutral "Start a new chat" stub — the **new/empty state (`/`) belongs to Home (FE-shell)**; never mount ChatView for `/`. |

### What Home should do for "New chat"

1. `api.createChat({ provider_id?, model_id? })` (backend picks defaults when
   omitted), then `navigate('/c/' + chat.id)`.
2. Optionally dispatch nothing — ChatView reloads by id.
3. ChatView does not embed a Home composer; `lib/chat/Composer.svelte` is
   reusable, but its current API expects a persisted chat (`chat` prop) — if
   Home wants it, create the chat first.

If you need the chat list to refresh after our edits, listen to
`chatgo:chat-updated` (below); a `title` change also arrives via `chatgo:title`.

## Window events we emit

| event | detail | when |
| --- | --- | --- |
| `chatgo:title` | `{ chat_id: string, title: string }` | SSE `title` event arrives (generated chat title). Also set `document.title = "<title> — ChatGo"` ourselves. |
| `chatgo:chat-updated` | `{ chat: Chat }` | after PATCH `/api/chats/{id}` returns (model pill switch, param drawer save/reset). Sidebar can refresh title/pinned copy. |

## Window events we listen for

None required. Global shortcuts go through `lib/shortcuts.svelte.js`:
we register `mod+enter` (send) and `slash` (focus composer). **`⌘K` is not ours**
(shell/command-palette). `Escape` is handled locally (edit box, model dropdown,
param drawer, lightbox) via `stopPropagation` in capture phase, so drawers and
popovers close top-down.

## localStorage keys (ours)

| key | values | meaning |
| --- | --- | --- |
| `chatgo-send-enter` | `'1'` (default) / `'0'` | `'1'`: Enter sends, Shift+Enter newline. `'0'`: Enter = newline, Shift+Enter sends. ⌘/Ctrl+Enter always sends. Settings page may expose a toggle (`setPrefersEnterSend` exported from `chatview-api.ts`). |
| `chatgo-cap-search` | `'1'/'0'` | cached web-search capability probe (see below); refreshes each app start. |

(`chatgo-theme` is the scaffold theme store's.)

## Search toggle visibility

`chatview-api.probeSearchEnabled()` order: `/api/me/settings` key
`search_enabled`/`web_search` (bool) → wins; else if admin: `/api/settings`
non-empty `searxng_url`; else hidden (non-admins can't probe env — per task
spec). Never renders setting values.

## Regenerate / edit semantics (assumption — please confirm, BE-apichat)

Contract only pins `Messages.DeleteFrom(chatID, msgID) // for regenerate`, and
`POST …/regenerate?from={msgID}` accepts a body typed `Partial<SendMessageBody>`.
We therefore treat **`from` = first message deleted (inclusive)**:

- **Retry assistant msg** → `?from=<assistantId>`, empty body → server deletes
  that reply (+tail) and regenerates.
- **Edit user msg** → optimistic local replace, `?from=<userId>` **with body
  `{ content }`** so the re-created user turn carries the new text (this is the
  "DELETE tail + resend" path fused into one call; there is no separate
  message-delete HTTP route in the contract to call).
- If the server instead *keeps* `from` (resends it as-is), editing degrades to
  "retry with original text" — nothing corrupts, but the edit would be lost.
  Flagged here + `// CONTRACT` comment in `chatview-api.ts`.

`stop` mid-stream aborts the fetch; the partial assistant message is kept with
a "Generation stopped." note (dropped entirely if nothing arrived).

## Streaming notes (for perf reviewers)

- Tokens are buffered and flushed once per `requestAnimationFrame` (~60fps
  max) — SSE callbacks never touch DOM.
- Markdown re-render is throttled to one parse per 80 ms while streaming; the
  unparsed raw tail is shown as plain text, and a blinking cursor span is
  appended.
- Rows use `content-visibility: auto` (no virtualizer dependency); a short
  rAF "settle" keeps the view pinned while offscreen rows size themselves.
- Auto-scroll sticks to bottom; wheel-up / touch / PageUp releases it and a
  floating **Jump to latest (N)** pill counts missed messages.

## Message rendering

- marked + DOMPurify strict profile (`USE_PROFILES.html`, no data-attrs,
  forbids script/iframe/style/form/svg/math…; classes allowed), then a
  DOMParser pass wraps tables for horizontal scroll and pins
  `target=_blank rel="noopener noreferrer"` on external links.
- highlight.js `lib/common` lazy-loaded on first code block (no CDN); plain
  escaped code until ready, then auto re-render. Auto-language-detect only on
  final render.
- Code blocks: header with language + **Copy** (clipboard w/ execCommand
  fallback) + **Wrap** toggle. Images: click → lightbox. **KaTeX not included
  in v1** (per task).
- Sources (search): pill row → expandable `[n] domain` chips, no external
  favicon fetches.
- Assistant rows: flat, full width, gradient avatar; user rows: right-aligned
  bubble. Hover bar = copy / retry (assistant) / edit (user) / read-aloud
  (`speechSynthesis`, hidden when unsupported). Long messages (>3200 chars)
  collapse with Expand.
- **Deviation:** the `Message` DTO exposes no attached-image metadata
  (`message_images` table exists but nothing in the JSON), so sent images
  render only if the backend embeds markdown image links into `content`;
  pre-send they show as local thumbnail chips. Needs a BE field (e.g.
  `images: [{id,url,name}]`) on `GET …/messages` for full history thumbnails.

## Composer features

auto-grow textarea (max 10 lines) · Enter/⌘Enter rules above · model pill
(grouped by provider, context-len badge, PATCH persists per chat) · search
toggle (capability-gated) · image paste/drag/clip → `/api/files` with 4-image
/20 MB client guards + thumbnail chips · Web Speech dictation (continuous,
interim results, browser lang, auto-resume, hidden when unsupported) · stop
button mid-stream.

## Param drawer

Right slide-over: temperature (0–2), top_p (0–1) slider+number, max_tokens,
stop sequences. Debounced (400 ms) `PATCH /api/chats/{id}` with `params`;
Reset clears to provider defaults. Esc/backdrop close.

## Gate

`npx svelte-check --threshold error` → **0 errors** (0 warnings in FE-chat
files; remaining warnings belong to other agents' files).
