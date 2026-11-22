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
  /* everything below is :global — {@html} children carry no scope attr */
  :global(.md-body) {
    font-size: 0.9375rem;
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
  :global(.md-body p) {
    margin: 0.65em 0;
  }
  :global(.md-body h1),
  :global(.md-body h2),
  :global(.md-body h3),
  :global(.md-body h4) {
    margin: 1.25em 0 0.5em;
    line-height: 1.35;
    font-weight: 650;
  }
  :global(.md-body h1) {
    font-size: 1.35em;
  }
  :global(.md-body h2) {
    font-size: 1.2em;
  }
  :global(.md-body h3) {
    font-size: 1.08em;
  }
  :global(.md-body ul),
  :global(.md-body ol) {
    margin: 0.65em 0;
    padding-left: 1.5em;
  }
  :global(.md-body li) {
    margin: 0.25em 0;
  }
  :global(.md-body li::marker) {
    color: var(--muted);
  }
  :global(.md-body a) {
    color: var(--accent);
    text-decoration: underline;
    text-underline-offset: 2px;
    text-decoration-color: color-mix(in oklab, var(--accent) 45%, transparent);
  }
  :global(.md-body a:hover) {
    text-decoration-color: var(--accent);
  }
  :global(.md-body blockquote) {
    margin: 0.75em 0;
    padding: 0.15em 0 0.15em 0.9em;
    border-left: 3px solid color-mix(in oklab, var(--accent) 55%, transparent);
    color: var(--muted);
  }
  :global(.md-body hr) {
    margin: 1.25em 0;
    border: 0;
    border-top: 1px solid var(--border);
  }

  /* inline code */
  :global(.md-body :not(pre) > code) {
    background: var(--panel-2);
    border: 1px solid var(--border);
    border-radius: 0.375rem;
    padding: 0.1em 0.35em;
    font-family: ui-monospace, 'SF Mono', SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 0.85em;
  }

  /* fenced code blocks */
  :global(.md-body .codeblock) {
    margin: 0.85em 0;
    border: 1px solid var(--border);
    border-radius: 0.625rem;
    overflow: hidden;
    background: var(--panel);
  }
  :global(.md-body .codebar) {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    padding: 0.3rem 0.4rem 0.3rem 0.75rem;
    background: var(--panel-2);
    border-bottom: 1px solid var(--border);
  }
  :global(.md-body .codelang) {
    font-family: ui-monospace, monospace;
    font-size: 0.7rem;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--muted);
  }
  :global(.md-body .codeacts) {
    display: flex;
    gap: 0.25rem;
  }
  :global(.md-body .codebtn) {
    border: 1px solid transparent;
    background: transparent;
    color: var(--muted);
    border-radius: 0.375rem;
    font-size: 0.72rem;
    padding: 0.15rem 0.5rem;
    cursor: pointer;
  }
  :global(.md-body .codebtn:hover) {
    color: var(--text);
    background: color-mix(in oklab, var(--text) 7%, transparent);
    border-color: var(--border);
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
    font-size: 0.8125rem;
    line-height: 1.6;
    background: none;
    border: 0;
    padding: 0;
  }

  /* tables scroll horizontally */
  :global(.md-body .md-tablewrap) {
    overflow-x: auto;
    margin: 0.85em 0;
    border: 1px solid var(--border);
    border-radius: 0.5rem;
  }
  :global(.md-body table) {
    border-collapse: collapse;
    font-size: 0.875rem;
    margin: 0;
    min-width: 100%;
  }
  :global(.md-body th),
  :global(.md-body td) {
    border-bottom: 1px solid var(--border);
    border-right: 1px solid var(--border);
    padding: 0.4em 0.75em;
    text-align: left;
  }
  :global(.md-body th:last-child),
  :global(.md-body td:last-child) {
    border-right: 0;
  }
  :global(.md-body tr:last-child td) {
    border-bottom: 0;
  }
  :global(.md-body th) {
    background: var(--panel-2);
    font-weight: 600;
  }

  /* images */
  :global(.md-body img.md-img) {
    max-width: 100%;
    max-height: 480px;
    border-radius: 0.625rem;
    border: 1px solid var(--border);
    cursor: zoom-in;
    margin: 0.5em 0;
  }

  /* streaming cursor */
  :global(.md-body .stream-cursor) {
    display: inline-block;
    width: 0.5ch;
    height: 1.1em;
    margin-left: 2px;
    vertical-align: text-bottom;
    border-radius: 1px;
    background: var(--accent);
    animation: md-cursor-blink 1s steps(2, start) infinite;
  }
  @keyframes md-cursor-blink {
    0%,
    55% {
      opacity: 1;
    }
    56%,
    100% {
      opacity: 0;
    }
  }

  /* hljs token theme (dark-first; class names per highlight.js v11) */
  :global(.md-body .hljs-comment),
  :global(.md-body .hljs-quote) {
    color: #7d8590;
    font-style: italic;
  }
  :global(.md-body .hljs-keyword),
  :global(.md-body .hljs-selector-tag),
  :global(.md-body .hljs-literal),
  :global(.md-body .hljs-control) {
    color: #c792ea;
  }
  :global(.md-body .hljs-string),
  :global(.md-body .hljs-addition),
  :global(.md-body .hljs-regexp) {
    color: #a5d6a7;
  }
  :global(.md-body .hljs-number),
  :global(.md-body .hljs-symbol),
  :global(.md-body .hljs-bullet) {
    color: #f0b464;
  }
  :global(.md-body .hljs-title),
  :global(.md-body .hljs-name),
  :global(.md-body .hljs-section) {
    color: #82aaff;
  }
  :global(.md-body .hljs-attr),
  :global(.md-body .hljs-attribute),
  :global(.md-body .hljs-variable),
  :global(.md-body .hljs-template-variable) {
    color: #9fb6d8;
  }
  :global(.md-body .hljs-built_in),
  :global(.md-body .hljs-type),
  :global(.md-body .hljs-class .hljs-title) {
    color: #7fdbca;
  }
  :global(.md-body .hljs-meta),
  :global(.md-body .hljs-deletion) {
    color: #e2a0a0;
  }
  :global(.md-body .hljs-selector-class),
  :global(.md-body .hljs-selector-id) {
    color: #ffcb6b;
  }
  :global(.md-body .hljs-emphasis) {
    font-style: italic;
  }
  :global(.md-body .hljs-strong) {
    font-weight: 700;
  }
</style>
