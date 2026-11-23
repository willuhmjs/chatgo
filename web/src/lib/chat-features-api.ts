// FE-CHATTER (wave 2) helper layer over the scaffold-owned $lib/api
// (api.ts is frozen — never edit it; everything wave-2-specific lives here).
// Ratings · fork · KB collections · slash prompts · memory count. Endpoint
// shapes per docs/FEATURES-V2.md (frozen). No runes here — components own
// the reactive state. Callers degrade quietly on 404 (ApiError.status === 404
// → hide the affordance; the endpoint isn't on this server build).

import { ApiError } from './api';
import type { Chat } from './api';

async function errorFrom(res: Response): Promise<ApiError> {
  let msg = res.statusText || `HTTP ${res.status}`;
  try {
    const j = (await res.json()) as { error?: string };
    if (j.error) msg = j.error;
  } catch {
    /* body wasn't JSON */
  }
  return new ApiError(res.status, msg);
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) throw await errorFrom(res);
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

// Some wave-2 endpoints may answer bare arrays or {items:[…]} wrappers —
// normalize both into a plain array without ever throwing on shape drift.
function asRows(raw: unknown, wrapperKeys = ['items', 'data', 'results']): Record<string, unknown>[] {
  if (Array.isArray(raw)) return raw as Record<string, unknown>[];
  if (raw && typeof raw === 'object') {
    const o = raw as Record<string, unknown>;
    for (const k of wrapperKeys) {
      if (Array.isArray(o[k])) return o[k] as Record<string, unknown>[];
    }
  }
  return [];
}

export { ApiError };

// ---------- ratings (docs/FEATURES-V2.md §BE-SOCIAL) ----------

export interface Rating {
  value: 1 | -1;
  comment?: string;
}
/** message_id → rating, exactly what GET /api/chats/{id}/ratings returns. */
export type RatingsMap = Record<string, Rating>;

/** Coerce the ratings payload defensively (drop rows with a bad value). */
export function normalizeRatings(raw: unknown): RatingsMap {
  const out: RatingsMap = {};
  if (!raw || typeof raw !== 'object') return out;
  for (const [mid, v] of Object.entries(raw as Record<string, unknown>)) {
    if (!v || typeof v !== 'object') continue;
    const o = v as Record<string, unknown>;
    const value = Number(o.value);
    if (value !== 1 && value !== -1) continue;
    const r: Rating = { value: value as 1 | -1 };
    if (typeof o.comment === 'string' && o.comment.trim()) r.comment = o.comment;
    out[mid] = r;
  }
  return out;
}

export const listRatings = async (chatId: string): Promise<RatingsMap> =>
  normalizeRatings(await req<unknown>('GET', `/api/chats/${encodeURIComponent(chatId)}/ratings`));

export const setRating = (chatId: string, messageId: string, value: 1 | -1, comment?: string) =>
  req<void>('POST', `/api/chats/${encodeURIComponent(chatId)}/messages/${encodeURIComponent(messageId)}/rating`, {
    value,
    comment: comment || undefined,
  });

export const deleteRating = (chatId: string, messageId: string) =>
  req<void>('DELETE', `/api/chats/${encodeURIComponent(chatId)}/messages/${encodeURIComponent(messageId)}/rating`);

// ---------- fork ----------

/** POST /api/chats/{id}/fork {message_id} → the new chat (title "Fork of …"). */
export const forkChat = (chatId: string, messageId: string) =>
  req<Chat>('POST', `/api/chats/${encodeURIComponent(chatId)}/fork`, { message_id: messageId });

// ---------- KB collections (docs/FEATURES-V2.md §BE-RAG) ----------

export interface KbCollection {
  id: string;
  name: string;
  file_count?: number;
  created_at?: number;
}

export async function listKb(): Promise<KbCollection[]> {
  const raw = await req<unknown>('GET', '/api/kb');
  return asRows(raw, ['items', 'collections', 'data'])
    .filter((r) => typeof r.id === 'string' && r.id)
    .map((r) => ({
      id: r.id as string,
      name: typeof r.name === 'string' ? r.name : '',
      file_count: typeof r.file_count === 'number' ? r.file_count : undefined,
      created_at: typeof r.created_at === 'number' ? r.created_at : undefined,
    }));
}

let kbPromise: Promise<KbCollection[]> | null = null;

/** Cached per page-load (the toggle probe); failures retry next call. */
export function listKbCached(): Promise<KbCollection[]> {
  if (!kbPromise) {
    kbPromise = listKb().catch((err: unknown) => {
      kbPromise = null;
      throw err;
    });
  }
  return kbPromise;
}

/** Chat-settings key (FEATURES-V2: kb enricher keys off chat settings JSON). */
export const KB_IDS_KEY = 'kb_collection_ids';

/** Read the per-chat selection out of chats.params. */
export function kbIdsFromChat(chat: Pick<Chat, 'params'> | null | undefined): string[] {
  const raw = chat?.params?.[KB_IDS_KEY];
  if (!Array.isArray(raw)) return [];
  return raw.filter((s): s is string => typeof s === 'string' && s.length > 0);
}

/** Merge/clear the selection into a PATCH-ready params object (keeps siblings). */
export function withKbIds(
  params: Record<string, unknown> | undefined,
  ids: string[],
): Record<string, unknown> {
  const next: Record<string, unknown> = { ...(params ?? {}) };
  if (ids.length) next[KB_IDS_KEY] = ids;
  else delete next[KB_IDS_KEY];
  return next;
}

// ---------- slash prompts (docs/FEATURES-V2.md §BE-SOCIAL prompts) ----------

export interface PromptEntry {
  id: string;
  title: string;
  content: string;
  created_at?: number;
}

export async function listPrompts(): Promise<PromptEntry[]> {
  const raw = await req<unknown>('GET', '/api/prompts');
  return asRows(raw, ['items', 'prompts', 'data'])
    .filter((r) => typeof r.id === 'string' && r.id)
    .map((r) => ({
      id: r.id as string,
      title: typeof r.title === 'string' && r.title ? r.title : firstLine(String(r.content ?? '')),
      content: typeof r.content === 'string' ? r.content : '',
      created_at: typeof r.created_at === 'number' ? r.created_at : undefined,
    }));
}

let promptsPromise: Promise<PromptEntry[]> | null = null;

/** Cached per page-load; failures retry next call. */
export function listPromptsCached(force = false): Promise<PromptEntry[]> {
  if (!promptsPromise || force) {
    promptsPromise = listPrompts().catch((err: unknown) => {
      promptsPromise = null;
      throw err;
    });
  }
  return promptsPromise;
}

export function firstLine(s: string): string {
  return (s.split('\n', 1)[0] ?? '').trim();
}

/** First line of the prompt content, clipped for the menu row preview. */
export function promptPreview(p: PromptEntry): string {
  const line = firstLine(p.content);
  return line.length > 72 ? `${line.slice(0, 72)}…` : line;
}

/** Case-insensitive substring filter over title + content. */
export function filterPrompts(list: PromptEntry[], query: string): PromptEntry[] {
  const q = query.trim().toLowerCase();
  if (!q) return list;
  return list.filter((p) => `${p.title} ${p.content}`.toLowerCase().includes(q));
}

// ---------- memories (ParamDrawer read-only chip) ----------

export interface MemoryItem {
  id: string;
  content?: string;
  enabled?: boolean;
}

export async function listMemories(): Promise<MemoryItem[]> {
  const raw = await req<unknown>('GET', '/api/me/memories');
  return asRows(raw, ['items', 'memories', 'data'])
    .filter((r) => typeof r.id === 'string' && r.id)
    .map((r) => ({
      id: r.id as string,
      content: typeof r.content === 'string' ? r.content : undefined,
      enabled: typeof r.enabled === 'boolean' ? r.enabled : undefined,
    }));
}

let memPromise: Promise<number> | null = null;

/** Count of enabled memories, cached per page-load; rejects on 404 (→ hide). */
export function memoryCountCached(): Promise<number> {
  if (!memPromise) {
    memPromise = listMemories()
      .then((rows) => rows.filter((m) => m.enabled !== false).length)
      .catch((err: unknown) => {
        memPromise = null;
        throw err;
      });
  }
  return memPromise;
}
