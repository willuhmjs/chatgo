<script lang="ts">
  // Streaming-safe markdown renderer: marked + DOMPurify + lazy hljs.
  // While `streaming`, re-renders are throttled to one per 80ms window and
  // the unparsed raw tail is shown as plain text (see docs/FE-CHAT-UI.md).
  // Code-block Copy/Wrap buttons + image zoom use event delegation.
  import { renderMarkdown } from '$lib/chat/markdown';
  import { mdStore, openLightbox } from '$lib/chat/state.svelte.js';
  import { copyText } from '$lib/chatview-api';

  let { src = '', streaming = false }: { src?: string; streaming?: boolean } = $props();

  let root = $state<HTMLDivElement | null>(null);
  let html = $state('');
  let tail = $state('');

  let renderedLen = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;

  function render(s: string, live: boolean): void {
    renderedLen = s.length;
    html = renderMarkdown(s, { live, cursor: live });
    tail = '';
  }

  $effect(() => {
    const s = src;
    const live = streaming;
    void mdStore.hljsReady; // re-render with highlighting once hljs lands
    if (!live) {
      if (timer !== undefined) {
        clearTimeout(timer);
        timer = undefined;
      }
      render(s, false);
      return;
    }
    // throttle: at most one parse per 80ms while tokens arrive
    if (timer === undefined) {
      timer = setTimeout(() => {
        timer = undefined;
        render(src, streaming);
      }, 80);
    }
    tail = s.length > renderedLen ? s.slice(renderedLen) : '';
  });

  // clear any pending timer on destroy (runs once; cleanup on teardown)
  $effect(
    () => () => {
      if (timer !== undefined) clearTimeout(timer);
    },
  );

  $effect(() => {
    const el = root;
    if (!el) return;
    const onClick = (e: MouseEvent) => {
      const t = e.target;
      if (!(t instanceof Element)) return;
      const copyBtn = t.closest('.code-copy');
      if (copyBtn instanceof HTMLButtonElement) {
        const code = copyBtn.closest('.codeblock')?.querySelector('pre')?.textContent ?? '';
        void copyText(code).then((ok) => {
          copyBtn.textContent = ok ? 'Copied' : 'Copy failed';
          setTimeout(() => {
            copyBtn.textContent = 'Copy';
          }, 1200);
        });
        return;
      }
      const wrapBtn = t.closest('.code-wrap');
      if (wrapBtn) {
        wrapBtn.closest('.codeblock')?.classList.toggle('wrap');
        return;
      }
      const img = t.closest('img.md-img');
      if (img) {
        const s = img.getAttribute('src');
        if (s) openLightbox(s);
      }
    };
    el.addEventListener('click', onClick);
    return () => el.removeEventListener('click', onClick);
  });
</script>

<div bind:this={root} class="md-body">
  {@html html}<span class="md-tail">{tail}</span>
</div>

<style>
  /* everything below is :global — {@html} children carry no scope attr.
     Type + block styling per docs/DESIGN-POLISH.md §2/§8: 16px/1.7 text,
     code = #000 body (dark) / #f4f4f4 (light) with a 36px header row. */
  :global(.md-body) {
    font-size: 1rem;
    line-height: 1.7;
    word-break: break-word;
    min-width: 0;
  }
  .md-tail {
    white-space: pre-wrap;
    word-break: break-word;
    opacity: 0.75;
  }

  :global(.md-body > :first-child) {
    margin-top: 0;
  }
  :global(.md-body > :last-child) {
    margin-bottom: 0;
  }
  /* prose rhythm per docs/DESIGN-REF.md §4: p/li/list 4–2px margins, 8px headings */
  :global(.md-body p) {
    margin: 4px 0;
  }
  :global(.md-body h1),
  :global(.md-body h2),
  :global(.md-body h3),
  :global(.md-body h4),
  :global(.md-body h5),
  :global(.md-body h6) {
    margin: 8px 0;
    line-height: 1.35;
    font-weight: 500;
  }
  :global(.md-body h1) {
    font-size: 1.5rem; /* 24px — tailwind 2xl */
  }
  :global(.md-body h2) {
    font-size: 1.25rem; /* 20px — xl */
  }
  :global(.md-body h3) {
    font-size: 1.125rem; /* 18px — lg */
  }
  /* preflight leak: `h1..h6 { font-size: inherit }` — without these h4–h6
     rendered at body size with (before h5/h6 were added here) zero margin */
  :global(.md-body h4) {
    font-size: 1rem; /* 16px — explicit, was implicit via inherit */
  }
  :global(.md-body h5) {
    font-size: 1rem;
  }
  :global(.md-body h6) {
    font-size: 0.875rem;
  }
  :global(.md-body ul),
  :global(.md-body ol) {
    margin: 4px 0;
    padding-left: 1.5em;
  }
  /* Tailwind preflight leak (app.css `@import "tailwindcss"`): its
     `ol, ul { list-style: none; margin: 0; padding: 0 }` reset leaks into the
     un-scoped {@html} children and kills every bullet/number. Re-declare at
     .md-body specificity (same cure as lib/extra/markdown.css for .md). */
  :global(.md-body ul) {
    list-style-type: disc;
  }
  :global(.md-body ul ul) {
    list-style-type: circle;
  }
  :global(.md-body ul ul ul) {
    list-style-type: square;
  }
  :global(.md-body ol) {
    list-style-type: decimal; /* numbering itself is the browser's (marked emits plain <ol>, separate lists restart) */
  }
  /* nested lists sit a touch tighter than top-level ones */
  :global(.md-body li > ul),
  :global(.md-body li > ol) {
    padding-left: 1.25em;
  }
  :global(.md-body li) {
    margin: 2px 0;
  }
  :global(.md-body li::marker) {
    color: var(--muted, #a8a8a8);
  }
  /* GFM task lists: markdown.ts tags task items .task-list-item (the <input>
     marked emits dies in the strict sanitize, so draw a quiet disabled-look
     box instead). li loses its bullet only for these; unclassed items keep a
     plain bullet as graceful fallback. */
  :global(.md-body li.task-list-item) {
    list-style-type: none;
  }
  :global(.md-body li.task-list-item::before) {
    content: '';
    display: inline-block;
    width: 0.85em;
    height: 0.85em;
    margin-right: 0.45em;
    vertical-align: -0.05em;
    border: 1px solid var(--muted, #a8a8a8);
    border-radius: 0.2em;
  }
  :global(.md-body li.task-list-checked::before) {
    content: '\2713'; /* ✓ on a quiet fill — reads as checked + disabled */
    text-align: center;
    line-height: 0.8;
    font-size: 0.9em;
    color: var(--text);
    background: var(--muted, #a8a8a8);
  }
  /* defensive: if a real checkbox ever survives sanitization in the future,
     style it disabled-styled and drop the bullet (modern :has, plain-bullet
     fallback elsewhere) */
  :global(.md-body li:has(> input[type='checkbox'])) {
    list-style-type: none;
  }
  :global(.md-body li > input[type='checkbox']) {
    margin: 0 0.45em 0 0;
    vertical-align: -0.05em;
    accent-color: var(--muted, #a8a8a8);
  }
  :global(.md-body a) {
    color: var(--acc);
    text-decoration: underline;
    text-underline-offset: 2px;
    text-decoration-color: color-mix(in oklab, var(--acc) 45%, transparent);
  }
  :global(.md-body a:hover) {
    text-decoration-color: var(--acc);
  }
  /* blockquote (docs/DESIGN-REF.md §4): 2px gray-700 bar, gray-400 text, not-italic */
  :global(.md-body blockquote) {
    margin: 0.75em 0;
    padding: 0.15em 0 0.15em 0.9em;
    border-inline-start: 2px solid var(--border);
    color: var(--muted, #a8a8a8);
    font-style: normal;
  }
  :global(html.dark .md-body blockquote) {
    border-inline-start-color: #4b4b4b; /* gray-700 */
    color: #b3b3b3; /* gray-400 */
  }
  :global(.md-body hr) {
    margin: 16px 0;
    border: 0;
    border-top: 1px solid var(--panel-2, #3a3a3a);
  }

  /* inline code — quiet fill, no border */
  :global(.md-body :not(pre) > code) {
    background: var(--panel-2, #3a3a3a);
    border-radius: 0.375rem;
    padding: 0.1em 0.35em;
    font-family: ui-monospace, 'SF Mono', SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 0.85em;
  }

  /* fenced code blocks (§8): #000 body in dark, #f4f4f4 in light */
  :global(.md-body .codeblock) {
    margin: 0.9em 0;
    border: 0;
    border-radius: 14px;
    overflow: hidden;
    background: #f4f4f4;
    color: var(--text);
  }
  :global(html.dark .md-body .codeblock) {
    background: #000;
  }
  :global(.md-body .codebar) {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    height: 36px;
    padding: 0 0.35rem 0 0.85rem;
    background: var(--panel-2, #3a3a3a);
    border-bottom: 0;
  }
  :global(.md-body .codelang) {
    font-family: ui-monospace, monospace;
    font-size: 0.75rem;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--muted, #a8a8a8);
  }
  :global(.md-body .codeacts) {
    display: flex;
    gap: 0.15rem;
  }
  :global(.md-body .codebtn) {
    border: 0;
    background: transparent;
    color: var(--muted, #a8a8a8);
    border-radius: 0.5rem;
    font-size: 0.75rem;
    font-weight: 500;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding: 0.25rem 0.55rem;
    cursor: pointer;
    transition:
      color 120ms ease,
      background 120ms ease;
  }
  :global(.md-body .codebtn:hover) {
    color: var(--text);
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
  }
  :global(.md-body .codeblock pre) {
    margin: 0;
    padding: 0.75rem 0.9rem;
    overflow-x: auto;
    background: transparent;
  }
  :global(.md-body .codeblock.wrap pre) {
    white-space: pre-wrap;
    word-break: break-word;
  }
  :global(.md-body .codeblock pre code) {
    font-family: ui-monospace, 'SF Mono', SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 0.84375rem; /* 13.5px (§8) */
    line-height: 1.6;
    background: none;
    border: 0;
    padding: 0;
  }

  /* tables — quiet: no outer frame, hairline row separators only (§4: my 4px) */
  :global(.md-body .md-tablewrap) {
    overflow-x: auto;
    margin: 4px 0;
    border: 0;
    border-radius: 0;
  }
  :global(.md-body table) {
    border-collapse: collapse;
    font-size: 0.875rem;
    margin: 0;
    min-width: 100%;
  }
  :global(.md-body th),
  :global(.md-body td) {
    border: 0;
    border-bottom: 1px solid var(--border);
    padding: 0.45em 0.9em 0.45em 0;
    text-align: left;
  }
  :global(.md-body th:first-child),
  :global(.md-body td:first-child) {
    padding-left: 0.25em;
  }
  :global(.md-body tr:last-child td) {
    border-bottom: 0;
  }
  :global(.md-body th) {
    background: transparent;
    color: var(--muted, #a8a8a8);
    font-weight: 600;
  }

  /* images — rounded-lg, fill-contrast only */
  :global(.md-body img.md-img) {
    max-width: 100%;
    max-height: 480px;
    border-radius: 0.5rem;
    border: 0;
    cursor: zoom-in;
    margin: 0.5em 0;
  }

  /* streaming caret: OWUI recipe (docs/DESIGN-REF.md §3) — 2×14px, gray, soft
     1.5s pulse (opacity 0.25 at 50%) instead of the hard 1s steps(1) blink */
  :global(.md-body .stream-cursor) {
    display: inline-block;
    width: 2px;
    height: 14px;
    margin-left: 2px;
    vertical-align: text-bottom;
    border-radius: 1px;
    background: var(--muted, #979797);
    animation: cursor-pulse 1.5s cubic-bezier(0.4, 0, 0.6, 1) infinite;
  }
  @keyframes cursor-pulse {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.25;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    :global(.md-body .stream-cursor) {
      animation: none;
      opacity: 0.9;
    }
  }

  /* hljs token theme (FE-COLOR: colorized both modes — strings green,
     keywords on the accent hue, numbers orange, comments --faint; class
     names per highlight.js v11). Light values first (code bg #f4f4f4),
     pastel overrides under html.dark (code bg #000). --acc/--faint flip
     with the mode automatically. */
  :global(.md-body .hljs-comment),
  :global(.md-body .hljs-quote) {
    color: var(--faint);
    font-style: italic;
  }
  :global(.md-body .hljs-keyword),
  :global(.md-body .hljs-selector-tag),
  :global(.md-body .hljs-literal),
  :global(.md-body .hljs-control) {
    color: var(--acc);
  }
  :global(.md-body .hljs-string),
  :global(.md-body .hljs-addition),
  :global(.md-body .hljs-regexp) {
    color: #0f7b3e;
  }
  :global(.md-body .hljs-number),
  :global(.md-body .hljs-symbol),
  :global(.md-body .hljs-bullet) {
    color: #b26a00;
  }
  :global(.md-body .hljs-title),
  :global(.md-body .hljs-name),
  :global(.md-body .hljs-section) {
    color: #0a5bc4;
  }
  :global(.md-body .hljs-attr),
  :global(.md-body .hljs-attribute),
  :global(.md-body .hljs-variable),
  :global(.md-body .hljs-template-variable) {
    color: #57606a;
  }
  :global(.md-body .hljs-built_in),
  :global(.md-body .hljs-type),
  :global(.md-body .hljs-class .hljs-title) {
    color: #0e7490;
  }
  :global(.md-body .hljs-meta),
  :global(.md-body .hljs-deletion) {
    color: #9a3412;
  }
  :global(.md-body .hljs-selector-class),
  :global(.md-body .hljs-selector-id) {
    color: #946200;
  }
  :global(html.dark .md-body .hljs-string),
  :global(html.dark .md-body .hljs-addition),
  :global(html.dark .md-body .hljs-regexp) {
    color: #7ee787;
  }
  :global(html.dark .md-body .hljs-number),
  :global(html.dark .md-body .hljs-symbol),
  :global(html.dark .md-body .hljs-bullet) {
    color: #f0b464;
  }
  :global(html.dark .md-body .hljs-title),
  :global(html.dark .md-body .hljs-name),
  :global(html.dark .md-body .hljs-section) {
    color: #82aaff;
  }
  :global(html.dark .md-body .hljs-attr),
  :global(html.dark .md-body .hljs-attribute),
  :global(html.dark .md-body .hljs-variable),
  :global(html.dark .md-body .hljs-template-variable) {
    color: #9fb6d8;
  }
  :global(html.dark .md-body .hljs-built_in),
  :global(html.dark .md-body .hljs-type),
  :global(html.dark .md-body .hljs-class .hljs-title) {
    color: #7fdbca;
  }
  :global(html.dark .md-body .hljs-meta),
  :global(html.dark .md-body .hljs-deletion) {
    color: #e2a0a0;
  }
  :global(html.dark .md-body .hljs-selector-class),
  :global(html.dark .md-body .hljs-selector-id) {
    color: #ffcb6b;
  }
  :global(.md-body .hljs-emphasis) {
    font-style: italic;
  }
  :global(.md-body .hljs-strong) {
    font-weight: 700;
  }
</style>
