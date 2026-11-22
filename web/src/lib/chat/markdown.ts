// Markdown pipeline for FE-chat: marked → custom renderers → DOMPurify
// (strict) → DOMParser post-processing. Streaming-safe: `live: true` keeps
// highlighting cheap (only registered langs; no auto-detect) and lets the
// caller debounce re-renders. hljs `common` bundle loads lazily; when it
// arrives, registered onHljsReady callbacks fire so components re-render
// highlighted. KaTeX intentionally NOT included in v1.
//
// Reactive flag lives in $lib/chat/state.svelte.js (mdStore.hljsReady) — this
// file stays runes-free so it can be plain .ts.

import DOMPurify from 'dompurify';
import { Marked } from 'marked';
import type { Tokens } from 'marked';

// ---------- highlight.js (lazy `common` subset, no CDN) ----------

interface HljsLike {
  highlight(code: string, opts: { language: string; ignoreIllegals?: boolean }): { value: string };
  highlightAuto(code: string, subset?: string[]): { value: string; language?: string };
  getLanguage(name: string): unknown;
}

let hljs: HljsLike | null = null;
let hljsLoading: Promise<void> | null = null;
const readyCbs: Array<() => void> = [];

export function isHljsReady(): boolean {
  return hljs !== null;
}

/** Register a callback fired once the hljs common bundle has loaded. */
export function onHljsReady(cb: () => void): void {
  if (hljs) cb();
  else readyCbs.push(cb);
}

function ensureHljs(): void {
  if (hljs || hljsLoading) return;
  hljsLoading = import('highlight.js/lib/common')
    .then((mod) => {
      hljs = (mod as unknown as { default: HljsLike }).default;
      for (const cb of readyCbs.splice(0)) cb();
    })
    .catch(() => {
      hljsLoading = null; // allow retry on next message
    });
}

// ---------- helpers ----------

const ESC_MAP: Record<string, string> = {
  '&': '&amp;',
  '<': '&lt;',
  '>': '&gt;',
  '"': '&quot;',
  "'": '&#39;',
};
const esc = (s: string): string => s.replace(/[&<>"']/g, (c) => ESC_MAP[c] ?? c);

// Render-time context (single-threaded, set inside renderMarkdown before parse).
let live = false;
let sawCode = false;

// ---------- marked config ----------

const md = new Marked({ gfm: true, breaks: false });

md.use({
  renderer: {
    code({ text, lang }: Tokens.Code): string {
      sawCode = true;
      const raw = (lang ?? '').trim().split(/\s+/)[0] ?? '';
      const label = (raw || 'text').toLowerCase();
      let inner = esc(text);
      if (hljs) {
        try {
          if (raw && hljs.getLanguage(raw)) {
            inner = hljs.highlight(text, { language: raw, ignoreIllegals: true }).value;
          } else if (!live && text.length <= 20000) {
            // auto-detect only in the final render — too pricey per-frame
            inner = hljs.highlightAuto(text).value;
          }
        } catch {
          /* keep escaped fallback */
        }
      }
      const langAttr = raw ? ` class="language-${esc(label)}"` : '';
      return (
        `<div class="codeblock"><div class="codebar">` +
        `<span class="codelang">${esc(label)}</span>` +
        `<span class="codeacts">` +
        `<button type="button" class="codebtn code-wrap" title="Toggle soft wrap">Wrap</button>` +
        `<button type="button" class="codebtn code-copy" title="Copy code">Copy</button>` +
        `</span></div><pre><code${langAttr}>${inner}</code></pre></div>`
      );
    },
    link({ href, title, tokens }) {
      const text = this.parser.parseInline(tokens);
      const t = title ? ` title="${esc(title)}"` : '';
      return `<a href="${esc(href ?? '#')}"${t}>${text}</a>`;
    },
    image({ href, title, text }) {
      const t = title ? ` title="${esc(title)}"` : '';
      return `<img src="${esc(href ?? '')}" alt="${esc(text ?? '')}"${t} loading="lazy" class="md-img" />`;
    },
  },
});

// ---------- DOMPurify (strict) ----------

const PURIFY_CFG: Parameters<typeof DOMPurify.sanitize>[1] = {
  USE_PROFILES: { html: true },
  ALLOW_DATA_ATTR: false,
  // class stays allowed (contract: "allow classes"); strip vectors + layout hijacks
  FORBID_TAGS: [
    'style',
    'script',
    'iframe',
    'form',
    'input',
    'button',
    'textarea',
    'select',
    'object',
    'embed',
    'base',
    'link',
    'meta',
    'noscript',
    'template',
    'slot',
    'portal',
    'math',
    'svg',
    'audio',
    'video',
    'canvas',
    'marquee',
  ],
  FORBID_ATTR: [
    'style',
    'target',
    'srcset',
    'form',
    'formaction',
    'action',
    'autofocus',
    'background',
    'dynsrc',
    'lowsrc',
  ],
};

// ---------- post-processing (tables scroll-x, safe external links) ----------

function enhance(html: string): string {
  if (!html || typeof DOMParser === 'undefined') return html;
  const doc = new DOMParser().parseFromString(`<!doctype html><html><body>${html}</body></html>`, 'text/html');
  for (const table of Array.from(doc.querySelectorAll('table'))) {
    const wrap = doc.createElement('div');
    wrap.className = 'md-tablewrap';
    table.parentNode?.insertBefore(wrap, table);
    wrap.appendChild(table);
  }
  for (const a of Array.from(doc.querySelectorAll('a[href]'))) {
    const href = a.getAttribute('href') ?? '';
    if (/^(https?:|mailto:)/i.test(href)) {
      a.setAttribute('target', '_blank');
      a.setAttribute('rel', 'noopener noreferrer');
    }
  }
  return doc.body.innerHTML;
}

// ---------- public API ----------

export interface RenderOpts {
  /** mid-stream render: skip costly highlight auto-detect */
  live?: boolean;
  /** append blinking cursor span (used for the streaming message) */
  cursor?: boolean;
}

export function renderMarkdown(src: string, opts: RenderOpts = {}): string {
  if (!src) return opts.cursor ? '<span class="stream-cursor"></span>' : '';
  live = !!opts.live;
  sawCode = false;
  let html: string;
  try {
    html = md.parse(src) as string; // sync — no async extensions registered
  } catch {
    return `<p>${esc(src)}</p>`;
  }
  if (sawCode) ensureHljs();
  let out: string;
  try {
    out = DOMPurify.sanitize(html, PURIFY_CFG) as string;
  } catch {
    out = `<p>${esc(src)}</p>`;
  }
  out = enhance(out);
  if (opts.cursor) out += '<span class="stream-cursor"></span>';
  return out;
}
