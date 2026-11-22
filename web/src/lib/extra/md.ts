// Lightweight markdown renderer for FE-extra surfaces (Share view, Gallery
// text preview, Compare panes).
//
// DEVIATION NOTE: FE-chat owns src/lib/chat/** and its planned
// marked+DOMPurify+hljs Markdown component, but that module does not exist
// yet (placeholder routes only), so it is not importable. This file is a
// deliberately small duplicate — when FE-chat ships lib/chat/Markdown.svelte
// with a pure `render()` export, swap this out for it.
// No highlight.js here: code blocks render plainly; hljs theming is FE-chat's.

import { marked } from 'marked';
import DOMPurify from 'dompurify';

marked.use({ gfm: true, breaks: true });

// Open external links safely in a new tab (marked leaves targets alone).
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  const el = /** @type {HTMLElement} */ (node);
  if (el.tagName === 'A' && /^https?:/i.test(el.getAttribute('href') ?? '')) {
    el.setAttribute('target', '_blank');
    el.setAttribute('rel', 'noopener noreferrer');
  }
});

/** Render markdown → sanitized HTML string for `{@html …}`. */
export function renderMarkdown(src: string): string {
  const html = marked.parse(src ?? '', { async: false }) as string;
  return DOMPurify.sanitize(html, { ADD_ATTR: ['target', 'rel'] });
}
