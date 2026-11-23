// Lightweight markdown renderer for FE-extra surfaces (Share view, Gallery
// text preview, Compare panes). Uses the chat pipeline's strict DOMPurify
// config (exported from $lib/chat/markdown) — DOMPurify defaults alone were
// weaker than the chat renderer. marked renderer wiring stays local because
// FE-extra renders plainly (no hljs; that theming is FE-chat's).

import { marked } from 'marked';
import DOMPurify from 'dompurify';
import { PURIFY_CFG } from '$lib/chat/markdown';

marked.use({ gfm: true, breaks: true });

// Dedicated DOMPurify instance — the v3 default export doubles as a factory
// (`DOMPurify(root)` → fresh instance with its OWN hook registry). Attaching
// our hook to the shared default instance would leak it into every other
// DOMPurify user in the bundle, notably the strict chat pipeline in
// $lib/chat/markdown. Off-browser the guard falls through to DOMPurify's own
// no-DOM path (isSupported=false, pass-through), matching this module's
// browser-only behavior.
const purify = DOMPurify(typeof window !== 'undefined' ? window : undefined);

// Open external links safely in a new tab (marked leaves targets alone).
// Runs in afterSanitizeAttributes, so setting `target` here survives even
// though PURIFY_CFG forbids it on *input* markup.
purify.addHook('afterSanitizeAttributes', (node) => {
  const el = /** @type {HTMLElement} */ (node);
  if (el.tagName === 'A' && /^https?:/i.test(el.getAttribute('href') ?? '')) {
    el.setAttribute('target', '_blank');
    el.setAttribute('rel', 'noopener noreferrer');
  }
});

/** Render markdown → sanitized HTML string for `{@html …}`. */
export function renderMarkdown(src: string): string {
  const html = marked.parse(src ?? '', { async: false }) as string;
  return purify.sanitize(html, PURIFY_CFG) as string;
}
