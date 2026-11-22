# ChatGo — ChatGPT-fidelity design spec (polish round)

Goal: indistinguishable-feeling ChatGPT dark UI, Open WebUI feature set. Everything
here overrides earlier design notes. When in doubt: **monochrome, quiet, spacious.**

## 1. Palette (dark = the product)
```
--bg        #212121   main pane (ChatGPT's canvas)
--bg-deep   #171717   sidebar, deepest surfaces
--panel     #2f2f2f   composer pill, popovers, cards (was #17171a)
--panel-2   #3a3a3a   hover fills, secondary pills, code header
--fill-hover rgba(255,255,255,.065)
--border    rgba(255,255,255,.10)   hairlines only; prefer NO border + fill contrast
--text      #ececec   primary
--muted     #a8a8a8   secondary/times/labels
--faint     #6e6e6e   disabled, placeholders
--accent    #ffffff   primary buttons are WHITE with near-black text (ChatGPT style)
--accent-soft rgba(99,102,241,.18)  keep indigo ONLY for focus rings, active nav, links
--danger    #ef4444 ; --ok #10a37f (rare)
```
Light mode: #ffffff canvas, #f9f9f9 sidebar, #f4f4f4 composer, text #0d0d0d, same structure.
Radius: pills/full → composer 26px; cards/menus 14-16px; row items 10px.
Shadows: only the composer gets a soft one `0 2px 12px rgba(0,0,0,.25)`; menus `0 8px 28px rgba(0,0,0,.45)`.

## 2. Typography
System stack already fine. Message text 16px/1.7 (assistant) — near-black-white contrast,
NOT pure #fff. Sidebar rows 14px. Section labels ("Today", "Yesterday") 12px, --muted,
medium weight. Titles 15px medium. Composer 16px.

## 3. Sidebar (ChatGPT-exact feel)
- Width 260px, bg --bg-deep, NO right border (fill contrast only), slides 200ms ease.
- Top row: sidebar-toggle icon + new-chat icon button (pencil-square, ghost icon-button,
  hover --fill-hover) + small brand glyph. NO big gradient button.
- "Search chats" ghost row with magnifier.
- Chat rows: h-9, rounded-lg, px-2.5, gap from section label 2px; title truncate; hover
  fill --fill-hover; active chat = --fill-hover (no accent bar); ellipsis menu appears on
  hover/focus (fade 120ms). Time grouping: Today/Yesterday/Previous 7 Days/Previous 30
  Days/Older. Folders as collapsible rows with caret. Tags as quiet chips (#2f2f2f, no border).
- Bottom user block: avatar + name (14px) + role pill; chevron opens menu
  (Settings / Theme / Admin / Log out) as rounded menu, not inline links.
- Thin scrollbars everywhere: 8px, thumb rgba(255,255,255,.12), invisible until scroll/hover.

## 4. New chat / Home (signature ChatGPT moment)
- Greeting vertically centered slightly above middle: "What can I help with?" 30px, weight
  600, tracking-tight (drop the current hero cards gradient treatment).
- Composer sits UNDER the greeting, horizontally centered, same 48rem column, its full
  ChatGPT pill styling (§6). Submitting navigates to /c/:id with the staged prompt (mechanism exists).
- Suggestion prompts: 3 quiet 14px pills under the composer, --panel fill, hover --panel-2.

## 5. Conversation view
- Column 42rem content / composer 48rem max; generous 24-28px gaps between messages.
- USER: right-aligned pill, bg --panel, radius 20px, padding 10px 16px, max-width 85%.
- ASSISTANT: no bubble — plain text on canvas, 16px, with a small "ChatGo" wordmark
  (or sparkle glyph) 6px above at 13px --muted ONLY on first message of a turn.
- Hover action bar: BELOW assistant text, left-aligned row of 15px ghost icons
  (copy · thumbs · regenerate · share) appearing opacity 0→1 120ms; icons --muted → --text.
- Streaming: blinking tail caret after last token; on error → quiet red-tinted line + Retry pill.
- Stop: send button morphs into a stop circle (same slot, white circle + black square).
- Scroll: auto-stick bottom; "↓" circular white button floating above composer when detached
  with unseen-count, not a text pill.

## 6. Composer (THE ChatGPT moment)
- Container: bg --panel, radius 26px, padding 10px 12px 8px, soft shadow, NO border;
  focus-within: slightly brighter #353535 (no ring glow).
- Textarea: transparent, 16px, autosize 1→10 lines, placeholder "Ask anything".
- Bottom row inside pill: left = [+] ghost circle (attach menu), [🌐] toggle (only when
  search enabled; active = white icon + tiny white dot), model picker (text + ⌄, opens menu);
  right = mic circle + send circle 32px (white circle/black ↑; disabled = white at 25% opacity).
- Model menu: rounded-xl --panel, groups by provider, check on right, model description
  line 12px --muted; 48-120ms scale(0.97→1)+fade pop animation.
- Attachment chips: rounded-lg --panel-2 rows above textarea inside the pill (thumb 28px).

## 7. Menus/popovers/toasts
Every floating layer: bg --panel, radius 14-16px, shadow-menu, item h-9 text-sm rounded-lg,
danger row in --danger. Animate 120-160ms fade + 2px rise; respect prefers-reduced-motion.
Toasts: bottom-center pill (not bottom-right stack) like ChatGPT's ephemera.

## 8. Code blocks
Inside assistant text: bg #000 (dark) / #f4f4f4 (light), radius 14px, header row 36px with
--muted lang label left, COPY button right (icon+COPY 12px uppercase), body 13.5px mono.

## 9. Motion rules
150-250ms ease-out fades/rises on: sidebar collapse, menus, message appearance (only for
new messages), jump-to-latest button, model menu. Nothing bouncy. No layout shift ever
during streaming. prefers-reduced-motion → disable.

## 10. Rules for polish agents
- svelte 5 runes; verify `npx svelte-check --threshold error` = 0 errors; NO npm install / vite build.
- Do NOT change component prop signatures, store APIs, endpoints, or DOM event names.
- Only touch files you own (see your dispatch). Use the tokens above (lib agent owns app.css;
  others just consume vars). No new deps, no inline hex where a var exists.
- Keep all a11y (focus-visible, aria, keyboard) — refinement must not remove behavior.
