// FE-chat extensions over lib/api.ts (contract: extend, never edit api.ts).
// Chat-view-only helpers: typed chat params, SSE payload shapes, capability
// probing, clipboard, formatting, localStorage keys. No runes here — reactive
// stores live in $lib/chat/state.svelte.js.

import { getMeSettings, patchChat } from './api';
import type { Chat, SourceRef } from './api';

// ---------- SSE payload shapes (docs/CONTRACTS.md §SSE protocol) ----------

export interface StreamTokenData {
  content?: string;
}
export interface StreamTitleData {
  title?: string;
}
export interface StreamSourcesData {
  items?: SourceRef[];
  /** set when the turn's web search FAILED; items then holds only enricher hits */
  error?: string;
}
export interface StreamErrorData {
  message?: string;
}
export interface StreamUsageData {
  usage?: { in: number; out: number } | null;
}

/**
 * Regenerate semantics (contract is thin here — `Messages.DeleteFrom(chatID,
 * msgID) // for regenerate`): we treat `?from=<id>` as "the server deletes
 * this message and everything after, then re-plays the turn". Therefore:
 *  - Retry an assistant turn  → from = assistant id, empty body.
 *  - Edit a user turn         → from = user id, body carries the new content
 *    so the re-created user message uses the edited text.
 * If the server actually KEEPS `from` instead of deleting it, edits still
 * work (we send the new content); if it ignores body.content we degrade to
 * "retry with original text" rather than corrupting history. Reported as an
 * assumption in docs/FE-CHAT-UI.md.
 */
export interface RegenerateBody {
  content?: string;
  images?: { file_id: string }[];
  search?: boolean;
}

// ---------- chat params (chats.params_json) ----------

export interface ChatParams {
  temperature?: number;
  top_p?: number;
  max_tokens?: number;
  stop?: string[];
}

export interface ParamFieldSpec {
  key: 'temperature' | 'top_p';
  label: string;
  min: number;
  max: number;
  step: number;
  fallback: number;
  hint: string;
}

export const PARAM_SLIDERS: ParamFieldSpec[] = [
  {
    key: 'temperature',
    label: 'Temperature',
    min: 0,
    max: 2,
    step: 0.05,
    fallback: 1,
    hint: 'Higher = more creative, lower = more focused',
  },
  {
    key: 'top_p',
    label: 'Top-p (nucleus)',
    min: 0,
    max: 1,
    step: 0.01,
    fallback: 1,
    hint: 'Samples from the smallest set of tokens above this cumulative probability',
  },
];

export const MAX_TOKENS_BOUNDS = { min: 1, max: 262144 };

const num = (v: unknown): number | undefined =>
  typeof v === 'number' && Number.isFinite(v) ? v : undefined;

/** Coerce arbitrary chats.params JSON into a typed ChatParams. */
export function normalizeParams(raw: unknown): ChatParams {
  const out: ChatParams = {};
  if (!raw || typeof raw !== 'object') return out;
  const o = raw as Record<string, unknown>;
  const t = num(o.temperature);
  if (t !== undefined) out.temperature = Math.min(2, Math.max(0, t));
  const p = num(o.top_p);
  if (p !== undefined) out.top_p = Math.min(1, Math.max(0, p));
  const m = num(o.max_tokens);
  if (m !== undefined)
    out.max_tokens = Math.min(MAX_TOKENS_BOUNDS.max, Math.max(MAX_TOKENS_BOUNDS.min, Math.floor(m)));
  if (Array.isArray(o.stop)) {
    const stop = o.stop.filter((s): s is string => typeof s === 'string' && s.length > 0);
    if (stop.length) out.stop = stop;
  }
  return out;
}

/** Drop keys that equal "provider default" so PATCH stays clean. */
export function compactParams(p: ChatParams): ChatParams {
  const out: ChatParams = {};
  if (p.temperature !== undefined && p.temperature !== 1) out.temperature = p.temperature;
  if (p.top_p !== undefined && p.top_p !== 1) out.top_p = p.top_p;
  if (p.max_tokens !== undefined && p.max_tokens > 0) out.max_tokens = p.max_tokens;
  if (p.stop && p.stop.length) out.stop = p.stop;
  return out;
}

export async function patchChatParams(id: string, params: ChatParams): Promise<Chat> {
  return patchChat(id, { params: compactParams(params) as Record<string, unknown> });
}

/** Stop sequences: newline- or comma-separated text ⇄ string[]. */
export function parseStopList(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean)
    .slice(0, 8);
}
export function formatStopList(stop?: string[]): string {
  return (stop ?? []).join('\n');
}

// ---------- capability probing (web-search toggle visibility) ----------

/**
 * Is the SearXNG-backed search toggle worth showing?
 * /api/me/settings carries the computed `search_enabled` as a real JSON
 * boolean (apimgr handleMeGet derives it from the global `search_url`
 * setting, with SEARXNG_URL config as the other source). Anything else —
 * absent, non-boolean, 403/404/anonymous — ⇒ hidden. Never render values.
 *
 * The former admin fallback (GET /api/settings, reading `searxng_url`) is
 * deleted, not renamed: that route is requireAdmin, so a regular user could
 * never read `search_url` from it (403; and the keys it read,
 * searxng_url/SEARXNG_URL, were never in that map), while admins now get the
 * same `search_enabled` boolean from /api/me/settings as everyone else — no
 * role remained for which the fallback could see a key.
 */
export async function probeSearchEnabled(_userRole?: string): Promise<boolean> {
  try {
    const s = await getMeSettings();
    const v = s['search_enabled'] ?? s['web_search'];
    if (typeof v === 'boolean') return v;
  } catch {
    /* endpoint absent / anonymous — stay hidden */
  }
  return false;
}

// ---------- clipboard (with execCommand fallback) ----------

export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* insecure context / permission denied — fall through */
  }
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.left = '-9999px';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    return ok;
  } catch {
    return false;
  }
}

// ---------- formatting ----------

export function formatContextLen(n?: number): string {
  if (!n || n <= 0) return '';
  if (n >= 1_000_000) return `${Math.round(n / 1_000_000)}M`;
  if (n >= 1000) return `${Math.round(n / 1000)}k`;
  return String(n);
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

export function domainOf(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, '');
  } catch {
    return url.slice(0, 40);
  }
}

export function fmtClock(tsSec: number): string {
  try {
    return new Date(tsSec * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  } catch {
    return '';
  }
}

export function fmtDateTime(tsSec: number): string {
  try {
    return new Date(tsSec * 1000).toLocaleString();
  } catch {
    return '';
  }
}

/** Rough markdown → speakable plain text for speechSynthesis. */
export function stripForTTS(md: string): string {
  return md
    .replace(/```[\s\S]*?```/g, ' code block. ')
    .replace(/`([^`]*)`/g, '$1')
    .replace(/!\[[^\]]*\]\([^)]*\)/g, ' image. ')
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/^\s{0,3}#{1,6}\s+/gm, '')
    .replace(/^\s{0,3}[->*+]\s+/gm, '')
    .replace(/[*_~>#|]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
    .slice(0, 8000);
}

// ---------- localStorage keys (documented in docs/FE-CHAT-UI.md) ----------

export const LS_KEYS = {
  /** '1' | '0' — Enter sends (default) vs Enter = newline, Shift+Enter sends. */
  sendEnter: 'chatgo-send-enter',
  /** cached capability probe so the search pill doesn't flicker on load */
  capSearch: 'chatgo-cap-search',
  /** '1' | '0' — sticky web-search pill state across messages/reloads */
  composerSearch: 'chatgo-composer-search',
} as const;

export function lsGet(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}
export function lsSet(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* private mode */
  }
}

export function prefersEnterSend(): boolean {
  return lsGet(LS_KEYS.sendEnter) !== '0';
}
export function setPrefersEnterSend(v: boolean): void {
  lsSet(LS_KEYS.sendEnter, v ? '1' : '0');
}
