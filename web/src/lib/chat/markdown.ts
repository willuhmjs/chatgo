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
      // Copy/Wrap buttons are intentionally NOT emitted here: everything this
      // renderer returns goes through the strict pass-1 sanitize, whose
      // FORBID_TAGS forbids <button> (attacker markup must die there). The
      // buttons our DOM-created UI needs are added by enhance() AFTER that
      // pass, and survive the post-enhance re-sanitize via ENHANCE_CFG.
      return (
        `<div class="codeblock"><div class="codebar">` +
        `<span class="codelang">${esc(label)}</span>` +
        `</div><pre><code${langAttr}>${inner}</code></pre></div>`
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
    // GFM task lists: marked's default listitem emits <input type=checkbox>,
    // which the strict pass-1 sanitize below forbids (PURIFY_CFG FORBID_TAGS) —
    // so the checkbox would silently vanish. Instead we mark the <li> with
    // task-list-item / task-list-checked classes (class survives both purify
    // passes) and Markdown.svelte draws a disabled-look checkbox via ::before.
    // Non-task items (incl. nested lists + loose lists) reproduce the default
    // renderer exactly: `<li>` + parser.parse(tokens, loose).
    listitem(item: Tokens.ListItem): string {
      const body = this.parser.parse(item.tokens, !!item.loose);
      if (!item.task) return `<li>${body}</li>\n`;
      const cls = item.checked ? 'task-list-item task-list-checked' : 'task-list-item';
      return `<li class="${cls}">${body}</li>\n`;
    },
  },
});

// ---------- DOMPurify (strict) ----------

/**
 * Strict DOMPurify config — exported so FE-extra surfaces (lib/extra/md.ts)
 * sanitize with the same policy as the chat renderer instead of DOMPurify
 * defaults. FORBID_TAGS already covers svg/math/template/form/style plus the
 * usual vectors.
 */
export const PURIFY_CFG: NonNullable<Parameters<typeof DOMPurify.sanitize>[1]> = {
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
  // Keep an attribute mirror (data-named-* for id/name) so re-sanitising an
  // already-sanitised string is lossless. Without it DOMPurify's first pass
  // moves id → data-ps-md-0 and deletes the id attribute, so a second pass
  // (share/compare re-renders chat markdown) can no longer see them. This does
  // NOT weaken the profile: FORBID_TAGS still drops svg/script/etc outright,
  // and the data-named-* mirrors are inert attributes.
  SANITIZE_NAMED_PROPS: true,
};

// ---------- post-processing (tables scroll-x, safe external links) ----------

// Dedicated DOMPurify instance (v3 default export doubles as a factory).
// Isolates the chat pipeline from hooks any module might attach to the
// shared default instance. Off-browser this falls through to DOMPurify's
// no-DOM path (isSupported=false → pass-through), consistent with the
// `typeof DOMParser` guard in enhance().
const purify = DOMPurify(typeof window !== 'undefined' ? window : undefined);

// Config for the post-enhance re-sanitize: identical strict policy except
// (a) `target` lifted from FORBID_ATTR + ADD_ATTR'd, and (b) `button` lifted
// from FORBID_TAGS.
// (a) Safe because the primary pass already stripped attacker-supplied
//     `target` (FORBID_ATTR there), so the only `target` still in the HTML is
//     the `_blank` enhance() itself sets on https/mailto links. (ADD_ATTR is
//     required too: the `html` USE_PROFILE does not allow `target` on its own.)
// (b) Safe because every `<button>` that could carry attacker attributes was
//     removed by pass 1 (FORBID_TAGS there still forbids `button`); the only
//     buttons reaching this final pass are the inert ones enhance() creates
//     via DOM APIs (createElement — never attacker-parsed markup). FORBID_ATTR
//     stays as strict as pass 1's (form/formaction/autofocus/style/…) and the
//     html profile still drops every event handler, so even a hypothetical
//     mXSS-regenerated button would arrive fully de-armed. Harness-verified
//     against the mXSS corpus + 21 button-smuggle payloads.
export const ENHANCE_CFG: typeof PURIFY_CFG = {
  ...PURIFY_CFG,
  FORBID_TAGS: (PURIFY_CFG.FORBID_TAGS ?? []).filter((tag) => tag !== 'button'),
  ADD_ATTR: ['target'],
  FORBID_ATTR: (PURIFY_CFG.FORBID_ATTR ?? []).filter((attr) => attr !== 'target'),
};

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
  // Code-block action buttons (delegated handlers live in Markdown.svelte,
  // selectors .code-copy/.code-wrap). Created with DOM APIs here — after the
  // strict pass-1 sanitize, before the post-enhance re-sanitize — so
  // attacker-supplied <button> markup (which pass 1 forbids) can never be
  // what reaches the page.
  for (const bar of Array.from(doc.querySelectorAll('.codebar'))) {
    if (bar.querySelector('.codeacts')) continue;
    const acts = doc.createElement('span');
    acts.className = 'codeacts';
    const mkBtn = (cls: string, title: string, text: string): void => {
      const btn = doc.createElement('button');
      btn.setAttribute('type', 'button');
      btn.className = `codebtn ${cls}`;
      btn.title = title;
      btn.textContent = text;
      acts.appendChild(btn);
    };
    mkBtn('code-wrap', 'Toggle soft wrap', 'Wrap');
    mkBtn('code-copy', 'Copy code', 'Copy');
    bar.appendChild(acts);
  }
  // mXSS hardening: mutations + DOMParser re-parse + innerHTML re-serialize
  // is exactly the mutate→re-parse pattern mXSS payloads ride on, so run one
  // final sanitize before the string reaches `{@html}`.
  return purify.sanitize(doc.body.innerHTML, ENHANCE_CFG) as string;
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
    out = purify.sanitize(html, PURIFY_CFG) as string;
  } catch {
    out = `<p>${esc(src)}</p>`;
  }
  out = enhance(out);
  if (opts.cursor) out += '<span class="stream-cursor"></span>';
  return out;
}
