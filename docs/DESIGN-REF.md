# ChatGo — Design Reference (extracted from Open WebUI source)

Ground-truth values scraped from `open-webui@0666e59` (`src/tailwind.css` `@theme`,
`src/app.css`, `chat/{Chat,MessageInput,Placeholder}.svelte`, `chat/Messages/*`,
`layout/Sidebar/ChatItem.svelte`). **Reference only — design values and layout
structure, not copied code** (their license carries brand-preservation terms; we
re-implement in our own components under Apache-2.0).

Use this table during the post-polish integration pass to settle any argument about
"what does ChatGPT actually look like" with measured values.

## 1. Their palette — pure-neutral OKLCH grays (no blue tint)

| token      | oklch(L 0 0) | ≈ sRGB    | their usage                                        | ours (DESIGN-POLISH)         |
|------------|-------------|-----------|----------------------------------------------------|------------------------------|
| gray-50    | 0.98        | `#fafafa` | light canvas                                       | `--bg` #ffffff / light canvas|
| gray-100   | 0.94        | `#ededed` | user bubble (light), dividers                      | `--panel` light ≈ #f4f4f4    |
| gray-200   | 0.92        | `#e6e6e6` | borders (light), slider tracks                     | `--panel-2`                  |
| gray-300   | 0.85        | `#cfcfcf` | strong borders, light scrollbar                    |                              |
| gray-400   | 0.77        | `#b3b3b3` | muted icons (light), blockquote text               | `--muted` light              |
| gray-500   | 0.69        | `#979797` | secondary text both modes, caret                   | `--muted`                    |
| gray-600   | 0.51        | `#656565` | tertiary text (light)                              |                              |
| gray-700   | 0.42        | `#4b4b4b` | blockquote bar (dark), prose headings light        |                              |
| gray-800   | 0.32        | `#333333` | **assistant/user bubble dark**, hover fills        | `--panel` dark #2f2f2f ≈ ok  |
| gray-850   | 0.27        | `#262626` | sidebar rows, hover chips, raised surfaces         | `--panel-2` dark #2a2a2a     |
| gray-900   | 0.20        | `#171717` | **their main canvas**, dark inverted-pill bg       | our sidebar #171717          |
| gray-950   | 0.16        | `#0d0d0d` | deepest wells, sidebar (they invert vs us)         | `--bg-deep` #0d0d0d          |

Their dark canvas = `#171717`; ours = `#212121` canvas + `#171717` sidebar
(real-ChatGPT split). Keep ours; the delta is a few L-points and our hierarchy
(canvas > panel > sidebar) matches openai.com, theirs (canvas < panel) is fine too.
**Key takeaway: everything is chroma-0 neutral grays — never blue-tinted darks.**

## 2. Composer (their MessageInput.svelte, the single most ChatGPT-critical element)

```
container: rounded-3xl(24px) · bg white/80 ↔ oklch(0.24 0 0)/90 (≈#2e2e2e/90)
           backdrop-blur-md · border gray-200/60 ↔ gray-800/40 (near-invisible)
           shadow-[0_6px_24px_-4px_rgba(0,0,0,0.1)] · px-0.5 · transition
stop btn:  rounded-full bg-white/20 p-1.5 size-6-ish, white icon
inner pills: rounded-2xl bg-white dark:bg-gray-900/60 border-gray-800/50
placeholder: "Send a Message"
```

Integration check vs POLISH-2's result (r26 `--panel` pill, `--shadow-composer`,
focus brighten via color-mix): adopt **backdrop-blur on the composer** and the
**hairline border at ~14% of a #333** in dark; borderless is ChatGPT-2025, the
blur+hairline is what OWUI shipped — either passes, blur reads richer over our
`#212121`. Their shadow is exactly our `--shadow-composer` spec.

## 3. Messages

- User bubble: rounded **24px** (rounded-3xl), `bg-gray-50 ↔ gray-800 #333`, px-4 py-3,
  max-w ~70–90%. Our spec said r20/#2f2f2f — POLISH-2 shipped r20/#2f2f2f. **Adopt r24 +
  #2f2f2f** (halfway; ChatGPT proper uses ≈r24 bubble on canvas, no border).
- Assistant: no avatar, no bubble, plain canvas text — both agree. ✓ (POLISH-2 done)
- Wordmark above first message of assistant groups: OWUI does exactly this. ✓ done.
- Streaming caret: `w-[0.125rem] h-3.5` (2×14px) `bg-gray-400 dark:bg-gray-500`,
  `cursor-pulse 1.5s cubic-bezier(0.4,0,0.6,1) infinite` (opacity 0.25 at 50%).
  POLISH-2 shipped 2px steps(1) 1s blink — **swap to their pulse** (softer, ChatGPT-y).

## 4. Prose rhythm (their app.css @apply chains — the "feel" most people can't name)

- Body `0.90625rem` (14.5px) vs our 16px — **keep 16px** (ChatGPT uses 16), but copy the
  margins: `p:my-1` (4px!), `headings:my-2` (8px), `ul/ol:my-1`, `li:my-0.5`,
  `pre:my-0`, `table:my-1`, `hr:my-4`, `whitespace-pre-line`. Headings:
  h1 2xl / h2 xl / h3 lg, `font-medium` (500) **not bold**.
- Blockquote: `border-s-2 gray-700 #4b4b4b`, `text-gray-400 #b3b3b3`, **not-italic**.
- Code: `pre:my-0` inside bubbles; hljs body has no before/after decorations.
- Font stack: `-apple-system, BlinkMacSystemFont, 'Inter', ui-sans-serif, system-ui…`
  → system-first, Inter only if locally installed. Never load Inter over the wire.

## 5. Ghost/pill button recipes

| pattern            | their classes (light ↔ dark)                                             |
|--------------------|--------------------------------------------------------------------------|
| icon ghost         | `size-6 rounded-lg text-gray-500 dark:text-gray-400 hover:bg-gray-50/40 dark:hover:bg-gray-800/40 hover:text-gray-700 dark:hover:text-gray-200 transition` |
| primary pill       | `h-7 rounded-lg bg-gray-900 text-white hover:bg-black ↔ bg-gray-100 text-gray-900 hover:bg-white` (inverted in dark = our white-circle logic) |
| hover preview chip | `rounded-lg bg-gray-850/95 text-gray-300 text-xs shadow-sm ring-1 ring-black/5 dark:ring-white/10` |

## 6. Sidebar rows (their ChatItem.svelte)

Compact rows, single-line `text-sm line-clamp-1`, hover `bg-gray-50/40 ↔ gray-800/40`,
rounded-lg, icon buttons appear on row-hover only (group-hover), same ghost recipe §5.
Active chat gets fill, not accent — **never paint active items blue**. Our h-9 rows stay.

## 7. Scrollbars

Hidden until hovered/active (`visibility:hidden` on `::-webkit-scrollbar-thumb`, revealed
on hover, `scrollbar-gutter` reserved). Adopt for sidebar + chat scroller: visible only
while pointer is inside the scroller. **Ruling: we keep POLISH-1's 8px thumb /
`rgba(255,255,255,.12)` via `scrollbar-color` (opacity equivalent, standards-based) —
supersedes this doc's 4px suggestion; DESIGN-POLISH §3 wins.**

## 8. Motion

- Everything `transition` default 150ms ease; menus `scale(0.97→1)+fade ~90ms` ✓;
- message entrance fade+rise 200ms append-only ✓ (POLISH-2);
- cursor-pulse 1.5s (§3); reduced-motion kills all of it ✓;
- greeting: `text-3xl` (30px) — OWUI left-aligns "How can I help you today?";
  ChatGPT centers "What can I help with?" — **we center, 26–30px, weight 600** (POLISH-2
  shipped 26px; bump to 28px if it looks small next to 16px body).

## Integration delta checklist (apply after all agents land, in owned-file order)

1. Markdown.svelte: caret → pulse recipe; prose margins → §4 values; blockquote recipe.
2. Composer.svelte: add `backdrop-blur-md` + `border: 1px solid rgb(255 255 255/0.06)`;
   keep r26.
3. MessageRow.svelte: bubble radius 20→24.
4. app.css: scrollbar §7 rule for `.scroll-thin`; confirm font stack §4.
5. Greeting size 26→28px if vertical rhythm feels small.
6. Only if time: hover-preview chips §5 on sidebar folder/branch affordances.
