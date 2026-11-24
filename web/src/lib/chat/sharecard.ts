// Chat → shareable PNG card (pure client-side, zero deps, zero server cost).
//
// Technique: render a self-styled card DOM off-screen (its own <style>, no
// app CSS, literal colors read from the live theme tokens), serialize with
// XMLSerializer (guarantees well-formed XHTML), wrap it in an SVG
// <foreignObject>, rasterize through an <img> onto a 2× canvas and download.
// No external fonts/images are referenced, so the canvas is never tainted.

import DOMPurify from 'dompurify';
import { Marked } from 'marked';

export interface CardMessage {
  role: string; // 'user' | 'assistant' | ...
  content: string;
}

export interface CardOptions {
  title: string;
  modelLabel: string;
  brand: string;
  messages: CardMessage[];
}

const WIDTH = 860;
const MAX_MESSAGES = 40;
const MAX_CHARS = 14000;

// A private marked instance: default renderers only (no hljs/mermaid/KaTeX —
// a share card wants portable text, and those would degrade inside SVG anyway).
const md = new Marked({ gfm: true, breaks: true });

function esc(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function renderMarkdown(src: string): string {
  const raw = md.parse(src, { async: false }) as string;
  // Cards carry no images/interactive bits; strip them for SVG safety.
  return DOMPurify.sanitize(raw, {
    USE_PROFILES: { html: true },
    FORBID_TAGS: ['img', 'iframe', 'form', 'input', 'button', 'details', 'summary', 'video', 'audio', 'svg'],
    FORBID_ATTR: ['style', 'srcset', 'target'],
  });
}

function themeColors(): Record<string, string> {
  const cs = getComputedStyle(document.documentElement);
  const get = (name: string, fallback: string) => (cs.getPropertyValue(name).trim() || fallback);
  return {
    bg: get('--bg-deep', '#0d0d0d'),
    panel: get('--panel', '#1f1f1f'),
    text: get('--text', '#ececec'),
    muted: get('--muted', '#a8a8a8'),
    border: get('--border', '#2e2e2e'),
    acc: get('--acc', '#6366f1'),
  };
}

/** Render the conversation card and trigger a PNG download. */
export async function chatToPng(opts: CardOptions): Promise<void> {
  const msgs = opts.messages.filter((m) => (m.role === 'user' || m.role === 'assistant') && m.content.trim());
  if (!msgs.length) throw new Error('Nothing to export yet');

  // Newest-last window: drop the oldest messages until under both caps.
  let kept = msgs.slice(-MAX_MESSAGES);
  let total = kept.reduce((n, m) => n + m.content.length, 0);
  while (kept.length > 2 && total > MAX_CHARS) {
    total -= kept[0].content.length;
    kept = kept.slice(1);
  }
  const omitted = msgs.length - kept.length;

  const c = themeColors();
  const host = document.createElement('div');
  host.style.position = 'fixed';
  host.style.left = '-10000px';
  host.style.top = '0';
  host.style.width = `${WIDTH}px`;
  host.style.pointerEvents = 'none';

  const style = document.createElement('style');
  style.textContent = `
    .card { box-sizing: border-box; width: ${WIDTH}px; padding: 36px 34px 22px;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
      background: linear-gradient(160deg, ${c.bg} 0%, ${c.panel} 140%); color: ${c.text}; }
    .card * { box-sizing: border-box; margin: 0; }
    .brandrow { display: flex; align-items: center; gap: 10px; margin-bottom: 6px; }
    .glyph { width: 30px; height: 30px; border-radius: 9px; display: inline-block;
      background: ${c.acc}; color: #fff; font-weight: 700; font-size: 15px; line-height: 30px; text-align: center; }
    .brandname { font-size: 14px; font-weight: 600; color: ${c.muted}; }
    .badge { margin-left: auto; font-size: 12px; color: ${c.text}; background: ${c.bg};
      border: 1px solid ${c.border}; border-radius: 999px; padding: 3px 10px; }
    h1.title { font-size: 21px; font-weight: 650; letter-spacing: -0.01em; margin: 10px 0 18px; }
    .msg { border-radius: 14px; padding: 12px 16px; margin-bottom: 12px; font-size: 14.5px; line-height: 1.55; overflow-wrap: break-word; }
    .user { background: rgba(255,255,255,0.06); border: 1px solid ${c.border}; margin-left: 64px; }
    .asst { background: ${c.bg}; border: 1px solid ${c.border}; margin-right: 40px; }
    .who { font-size: 11px; text-transform: uppercase; letter-spacing: 0.08em; color: ${c.muted}; margin-bottom: 5px; }
    .card p { margin: 0 0 8px; } .card p:last-child { margin-bottom: 0; }
    .card h1, .card h2, .card h3, .card h4 { font-size: 15.5px; margin: 10px 0 6px; }
    .card pre { background: #101014; border: 1px solid ${c.border}; border-radius: 9px; padding: 10px 12px;
      margin: 8px 0; font-size: 12.5px; line-height: 1.5; overflow: hidden; white-space: pre-wrap;
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
    .card code { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 0.92em; }
    .card :not(pre) > code { background: rgba(255,255,255,0.08); border-radius: 5px; padding: 1px 5px; }
    .card ul, .card ol { padding-left: 22px; margin: 6px 0; }
    .card blockquote { border-left: 3px solid ${c.acc}; margin: 8px 0; padding: 2px 0 2px 12px; color: ${c.muted}; }
    .card table { border-collapse: collapse; margin: 8px 0; font-size: 13px; }
    .card th, .card td { border: 1px solid ${c.border}; padding: 4px 8px; }
    .more { font-size: 12.5px; color: ${c.muted}; margin: 2px 0 12px; }
    .foot { display: flex; align-items: center; margin-top: 14px; font-size: 11.5px; color: ${c.muted}; }
    .foot .line { flex: 1; height: 1px; background: ${c.border}; margin: 0 10px; }
  `;

  const card = document.createElement('div');
  card.className = 'card';
  const date = new Date().toLocaleString();
  card.innerHTML = `
    <div class="brandrow">
      <span class="glyph">${esc(opts.brand.charAt(0) || 'C')}</span>
      <span class="brandname">${esc(opts.brand)}</span>
      <span class="badge">${esc(opts.modelLabel)}</span>
    </div>
    <h1 class="title">${esc(opts.title || 'Untitled chat')}</h1>
    ${omitted > 0 ? `<p class="more">… ${omitted} earlier ${omitted === 1 ? 'message' : 'messages'} omitted</p>` : ''}
    ${kept
      .map(
        (m) =>
          `<div class="msg ${m.role === 'user' ? 'user' : 'asst'}">
             <div class="who">${m.role === 'user' ? 'You' : esc(opts.modelLabel)}</div>
             ${renderMarkdown(m.content)}
           </div>`,
      )
      .join('')}
    <div class="foot"><span>${esc(date)}</span><span class="line"></span><span>shared from ${esc(opts.brand)}</span></div>
  `;

  host.appendChild(style);
  host.appendChild(card);
  document.body.appendChild(host);
  try {
    const height = Math.min(Math.ceil(card.getBoundingClientRect().height) + 4, 10000);
    const xhtml = new XMLSerializer().serializeToString(card);
    const svg =
      `<svg xmlns="http://www.w3.org/2000/svg" width="${WIDTH}" height="${height}">` +
      `<foreignObject width="100%" height="100%">${xhtml}</foreignObject></svg>`;

    const img = new Image();
    img.decoding = 'sync';
    await new Promise<void>((resolve, reject) => {
      img.onload = () => resolve();
      img.onerror = () => reject(new Error('Could not rasterize the card'));
      img.src = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg);
    });

    const scale = 2;
    const canvas = document.createElement('canvas');
    canvas.width = WIDTH * scale;
    canvas.height = height * scale;
    const ctx = canvas.getContext('2d');
    if (!ctx) throw new Error('Canvas unavailable');
    ctx.fillStyle = c.bg;
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(img, 0, 0, canvas.width, canvas.height);

    const blob = await new Promise<Blob | null>((res) => canvas.toBlob(res, 'image/png'));
    if (!blob) throw new Error('PNG encoding failed');
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${(opts.title || 'chat').replace(/[^\w\- ]+/g, '').trim().replace(/\s+/g, '-').slice(0, 60) || 'chat'}.png`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 30_000);
  } finally {
    host.remove();
  }
}
