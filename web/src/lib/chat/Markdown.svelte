<script lang="ts">
  // Streaming-safe markdown renderer: marked + DOMPurify + lazy hljs + lazy KaTeX.
  // While `streaming`, re-renders are throttled to one per 80ms window and
  // the unparsed raw tail is shown as plain text (see docs/FE-CHAT-UI.md).
  // Code-block Copy/Wrap buttons + image zoom use event delegation.
  // KaTeX is pulled in via dynamic import ONLY when $…$ / $$…$$ math is found
  // (chunk-split out of the main bundle); any failure falls back to plain text.
  import DOMPurify from 'dompurify';
  import { tick } from 'svelte';
  import { renderMarkdown } from '$lib/chat/markdown';
  import { mdStore, openLightbox } from '$lib/chat/state.svelte.js';
  import { copyText } from '$lib/chatview-api';

  let { src = '', streaming = false }: { src?: string; streaming?: boolean } = $props();

  let root = $state<HTMLDivElement | null>(null);
  let html = $state('');
  let tail = $state('');

  let renderedLen = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;

  // ---------- lazy KaTeX ----------

  type KatexModule = typeof import('katex');
  let katex: KatexModule | null = null;
  let katexLoading: Promise<void> | null = null;
  let katexTick = $state(0); // bump re-runs the render effect once KaTeX lands

  function ensureKatex(): void {
    if (katex || katexLoading) return;
    // js + css both arrive in the math chunk, not the main one
    katexLoading = Promise.all([import('katex'), import('katex/dist/katex.min.css')])
      .then(([mod]) => {
        katex = mod;
        katexTick++;
      })
      .catch(() => {
        katexLoading = null; // allow retry on the next render
      });
  }

  interface MathItem {
    tex: string;
    display: boolean;
    raw: string;
  }

  // ---------- lazy Mermaid (diagram fences only; chunk-split from main) ----------

  let mermaidLoading: Promise<unknown> | null = null;
  let mermaidTick = $state(0); // bump re-runs the paint effect once the lib lands
  let mermaidSeq = 0;

  function ensureMermaid(): Promise<unknown> {
    if (!mermaidLoading) {
      mermaidLoading = import('mermaid')
        .then((mod) => {
          const m = ((mod as { default?: unknown }).default ?? mod) as {
            initialize: (cfg: Record<string, unknown>) => void;
          };
          m.initialize({
            startOnLoad: false,
            securityLevel: 'strict',
            theme: document.documentElement.classList.contains('dark') ? 'dark' : 'neutral',
            fontFamily: 'inherit',
          });
          mermaidTick++;
          return m;
        })
        .catch(() => {
          mermaidLoading = null; // retry on the next render
          throw new Error('mermaid unavailable');
        });
    }
    return mermaidLoading;
  }

  /** Replace un-rendered ```mermaid fences with their SVG (final renders only). */
  function paintMermaid(el: HTMLElement): void {
    const blocks = Array.from(
      el.querySelectorAll<HTMLElement>('.codeblock:not(.mmd) code.language-mermaid'),
    );
    if (blocks.length === 0) return;
    void ensureMermaid()
      .then(async (mod) => {
        const m = mod as { render: (id: string, code: string) => Promise<{ svg: string }> };
        for (const code of blocks) {
          const block = code.closest<HTMLElement>('.codeblock');
          if (!block || block.classList.contains('mmd')) continue;
          block.classList.add('mmd'); // claim first: re-entry renders nothing
          try {
            const { svg } = await m.render(`mmd-${++mermaidSeq}`, code.textContent ?? '');
            const holder = document.createElement('div');
            holder.className = 'mermaid-render';
            holder.innerHTML = svg; // mermaid securityLevel:'strict' sanitizes
            block.before(holder);
          } catch {
            block.classList.remove('mmd'); // leave the plain code fence
          }
        }
      })
      .catch(() => {
        /* no mermaid — plain code fences remain */
      });
  }

  // split point: fenced blocks + inline code spans — math inside them is left
  // alone (odd indices of the split() result are the code segments)
  const CODE_SPLIT = /(```[\s\S]*?(?:```|$)|~~~[\s\S]*?(?:~~~|$)|`[^`\n]*`)/;
  const DISPLAY_RE = /(?<!\\)\$\$([\s\S]+?)(?<!\\)\$\$/g;
  // inline: no space after the opening $ / before the closing $, closing $ not
  // followed by a digit ("$5 and $6" stay plain text), no \$ escapes
  const INLINE_RE = /(?<!\\)\$(?![\s$])((?:\\.|[^$\\\n])+?)(?<![\s\\])\$(?!\d)/g;

  /** Tokenize $…$ / $$…$$ into @@katexN@@ placeholders (source-level, before
   *  marked runs, so `_`/`*` inside math survive as literal text). */
  function extractMath(source: string): { masked: string; items: MathItem[] } {
    const items: MathItem[] = [];
    const mask = (text: string): string =>
      text
        .replace(DISPLAY_RE, (raw, tex: string) => {
          if (!tex.trim()) return raw;
          items.push({ tex, display: true, raw });
          return `@@katex${items.length - 1}@@`;
        })
        .replace(INLINE_RE, (raw, tex: string) => {
          if (!tex.trim()) return raw;
          items.push({ tex, display: false, raw });
          return `@@katex${items.length - 1}@@`;
        });
    const parts = source.split(CODE_SPLIT);
    for (let i = 0; i < parts.length; i += 2) parts[i] = mask(parts[i]);
    return { masked: parts.join(''), items };
  }

  const ESC_MAP: Record<string, string> = {
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;',
  };
  const esc = (s: string): string => s.replace(/[&<>"']/g, (c) => ESC_MAP[c] ?? c);

  /** Swap the placeholders in sanitized output for KaTeX HTML (re-sanitized
   *  through DOMPurify). Broken expressions degrade to their raw source. */
  function injectMath(out: string, items: MathItem[]): string {
    const k = katex;
    if (!k || !items.length) return out;
    for (let i = 0; i < items.length; i++) {
      const it = items[i];
      let frag: string;
      try {
        const rendered = k.renderToString(it.tex, {
          displayMode: it.display,
          throwOnError: false,
          errorColor: '#ef4444',
          strict: 'ignore',
          trust: false,
        });
        frag = `<span class="md-math${it.display ? ' md-math-block' : ''}">${DOMPurify.sanitize(rendered)}</span>`;
      } catch {
        frag = esc(it.raw);
      }
      out = out.replaceAll(`@@katex${i}@@`, frag);
    }
    return out;
  }

  function render(s: string, live: boolean): void {
    renderedLen = s.length;
    const { masked, items } = extractMath(s);
    const mathReady = items.length > 0 && katex !== null;
    if (items.length && !katex) ensureKatex();
    // unmasked source path = today's behavior, untouched when no math present
    html = injectMath(renderMarkdown(mathReady ? masked : s, { live, cursor: live }), mathReady ? items : []);
    tail = '';
  }

  $effect(() => {
    const s = src;
    const live = streaming;
    void mdStore.hljsReady; // re-render with highlighting once hljs lands
    void katexTick; // …and with typeset math once KaTeX lands
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
          // OWUI-style "Copied" state — .copied draws the checkmark (CSS ::before)
          copyBtn.textContent = ok ? 'Copied' : 'Copy failed';
          copyBtn.classList.toggle('copied', ok);
          setTimeout(() => {
            copyBtn.textContent = 'Copy';
            copyBtn.classList.remove('copied');
          }, 1200);
        });
        return;
      }
      const wrapBtn = t.closest('.code-wrap');
      if (wrapBtn) {
        wrapBtn.closest('.codeblock')?.classList.toggle('wrap');
        return;
      }
      const srcBtn = t.closest('.code-src');
      if (srcBtn) {
        srcBtn.closest('.codeblock')?.classList.toggle('src');
        return;
      }
      const dlBtn = t.closest('.code-dl');
      if (dlBtn instanceof HTMLButtonElement) {
        const block = dlBtn.closest('.codeblock');
        const code = block?.querySelector('pre')?.textContent ?? '';
        const lang = (block?.querySelector('.codelang')?.textContent ?? 'txt').trim() || 'txt';
        const name = `${lang}-${Date.now().toString(36)}.${lang.replace(/[^a-z0-9]/gi, '') || 'txt'}`;
        const url = URL.createObjectURL(new Blob([code], { type: 'text/plain;charset=utf-8' }));
        const a = document.createElement('a');
        a.href = url;
        a.download = name;
        a.click();
        setTimeout(() => URL.revokeObjectURL(url), 4000);
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

  // Mermaid diagrams land once final HTML is in the DOM (never mid-stream;
  // the raw fence is the streaming state, the SVG replaces it when settled).
  $effect(() => {
    void html;
    void mermaidTick;
    if (streaming) return;
    const el = root;
    if (!el) return;
    void tick().then(() => paintMermaid(el));
  });
</script>

<div bind:this={root} class="md-body">
  {@html html}<span class="md-tail">{tail}</span>
</div>

<style>
  /* everything below is :global — {@html} children carry no scope attr.
     Type + block styling per docs/DESIGN-POLISH.md §2/§8: 16px/1.7 text;
     code blocks follow the OWUI CodeBlock recipe: rounded-xl shell with a
     hairline border, header bar on the body surface (language left, actions
     right), body white in light / #000 in dark (OWUI keeps it theme-aware —
     `bg-white dark:bg-black` — so we do the same rather than always-dark). */
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

  /* fenced code blocks — OWUI: rounded-xl + hairline border, bar shares the
     body surface (white light / #000 dark), small quiet action buttons */
  :global(.md-body .codeblock) {
    margin: 0.9em 0;
    border: 1px solid var(--border);
    border-radius: 0.75rem; /* rounded-xl */
    overflow: hidden;
    background: #ffffff;
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
    padding: 0.3rem 0.45rem 0.3rem 0.9rem;
    background: transparent; /* sits on the code surface, OWUI-style */
    border-bottom: 0;
  }
  :global(.md-body .codelang) {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.75rem;
    color: var(--muted, #a8a8a8);
  }
  :global(.md-body .codeacts) {
    display: flex;
    flex: none;
    gap: 0.15rem;
  }
  :global(.md-body .codebtn) {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    height: 1.625rem;
    border: 0;
    background: transparent;
    color: var(--muted, #a8a8a8);
    border-radius: 0.5rem;
    font-size: 0.75rem;
    font-weight: 400;
    padding: 0 0.5rem;
    cursor: pointer;
    transition:
      color 100ms ease,
      background 100ms ease;
  }
  :global(.md-body .codebtn:hover) {
    color: var(--text);
    background: var(--fill-hover, rgb(255 255 255 / 0.05));
  }
  /* copied state: checkmark rides in front of the label (set by the click
     handler in this component) */
  :global(.md-body .codebtn.copied)::before {
    content: '\2713';
    font-size: 0.7rem;
    line-height: 1;
  }
  :global(.md-body .codeblock pre) {
    margin: 0;
    padding: 0.4rem 0.9rem 0.85rem;
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

  /* KaTeX math (lazy chunk). Block math scrolls sideways instead of
     overflowing; katex-display's own 1em margins are folded into ours. */
  :global(.md-body .md-math) {
    white-space: normal;
  }
  :global(.md-body .md-math-block) {
    display: block;
    margin: 0.55em 0;
    overflow-x: auto;
    overflow-y: hidden;
  }
  :global(.md-body .katex-display) {
    margin: 0;
  }
  :global(.md-body .katex) {
    font-size: 1.02em;
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
     names per highlight.js v11). Light values first (code bg white),
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

  /* ── Mermaid diagrams ───────────────────────────────────────────────────
     Once rendered, the fence folds behind its SVG (the Code button in the
     block's bar folds it back). Unrendered/streaming fences stay plain code. */
  :global(.md-body .mermaid-render) {
    display: flex;
    justify-content: center;
    margin: 10px 0;
    padding: 12px;
    overflow-x: auto;
    border: 1px solid var(--border);
    border-radius: 0.75rem;
    background: var(--bg);
  }
  :global(.md-body .mermaid-render svg) {
    max-width: 100%;
    height: auto;
  }
  :global(.md-body .codeblock.mmd:not(.src) pre) {
    display: none;
  }
  :global(.md-body .codeblock.mmd .code-src) {
    display: inline-flex;
  }
</style>
