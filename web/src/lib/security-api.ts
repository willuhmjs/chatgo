// FE-SEC wave-2 API facade: 2FA (BE-SEC shipped these in internal/auth/
// totp_handlers.go) + the memory manager (endpoints land in parallel — the
// mappers below are deliberately tolerant and callers degrade on 404).
//
// api.ts is scaffold-owned and frozen, so — same pattern as admin/audit-api.ts
// — this module carries its own credentials-included fetch. JSON {error}
// bodies are mapped to ApiError(message); anything else falls back to the
// status text. Responses are treated as untrusted: every field degrades to a
// safe default instead of throwing.
import { ApiError } from '$lib/api';

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: 'include',
    headers:
      body !== undefined
        ? { 'Content-Type': 'application/json', Accept: 'application/json' }
        : { Accept: 'application/json' },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    let msg = res.statusText || `HTTP ${res.status}`;
    try {
      const j = (await res.json()) as { error?: string };
      if (typeof j?.error === 'string' && j.error) msg = j.error;
    } catch {
      /* body wasn't JSON */
    }
    throw new ApiError(res.status, msg);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

// ---------- two-factor authentication ----------

/** POST /api/me/2fa/setup response (QR codes are never rendered — manual key + URI only). */
export interface TwoFactorSetup {
  secret: string;
  otpauth_uri: string;
}

/** Route-level outcome of the 2FA probe; 'unavailable' hides the whole section. */
export type TwoFactorStatus = 'off' | 'enabled' | 'unavailable';

export const setupTwoFactor = (password: string) =>
  call<TwoFactorSetup>('POST', '/api/me/2fa/setup', { password });

export const enableTwoFactor = (code: string) =>
  call<{ ok: boolean }>('POST', '/api/me/2fa/enable', { code });

export const disableTwoFactor = (password: string) =>
  call<{ ok: boolean }>('POST', '/api/me/2fa/disable', { password });

/**
 * Feature probe without a password: POST /api/me/2fa/setup with an empty body.
 * The handler checks (in order): rate limit → body decode → OIDC-only →
 * already-enabled → password match, so a bodiless probe never reaches secret
 * generation. Mapping:
 *  - 404/405  ⇒ route not wired on this server        → 'unavailable'
 *  - 409      ⇒ "already enabled"                     → 'enabled'
 *  - 400      ⇒ SSO-only account (setup refuses them) → 'unavailable'
 *  - anything else (401 bad password, 429 throttled,
 *    even an odd 200) ⇒ route is live and 2FA is off  → 'off'
 */
export async function probeTwoFactor(): Promise<TwoFactorStatus> {
  try {
    await call('POST', '/api/me/2fa/setup', {});
    return 'off';
  } catch (e) {
    if (!(e instanceof ApiError)) return 'unavailable'; // network blew up — stay quiet
    if (e.status === 404 || e.status === 405) return 'unavailable';
    if (e.status === 409) return 'enabled';
    if (e.status === 400) return 'unavailable'; // OIDC-only account
    return 'off';
  }
}

// ---------- memories ----------

export interface Memory {
  id: string;
  content: string;
  /** 'manual' | 'extracted' in practice; unknown sources surface as-is. */
  source: string;
  enabled: boolean;
  created_at: number;
  updated_at: number;
}

export interface MemoryPatch {
  content?: string;
  enabled?: boolean;
}

function str(v: unknown, fallback = ''): string {
  return typeof v === 'string' ? v : fallback;
}

function num(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) ? v : 0;
}

/** SQLite-backed servers may send 0/1 instead of booleans; absent means on. */
function boolish(v: unknown, fallback: boolean): boolean {
  if (typeof v === 'boolean') return v;
  if (typeof v === 'number' && Number.isFinite(v)) return v !== 0;
  if (typeof v === 'string') return v === 'true' || v === '1';
  return fallback;
}

function mapMemory(raw: unknown): Memory {
  const o = (raw ?? {}) as Record<string, unknown>;
  return {
    id: str(o.id),
    content: str(o.content),
    source: str(o.source, 'manual'),
    enabled: boolish(o.enabled, true),
    created_at: num(o.created_at),
    updated_at: num(o.updated_at),
  };
}

/** Accepts a bare array or a {memories|items:[…]} wrapper — whatever BE ships. */
function mapMemoryList(body: unknown): Memory[] {
  if (Array.isArray(body)) return body.map(mapMemory);
  const o = (body ?? {}) as Record<string, unknown>;
  const items = Array.isArray(o.memories) ? o.memories : Array.isArray(o.items) ? (o.items as unknown[]) : [];
  return items.map(mapMemory);
}

/** GET /api/me/memories → caller's memories (404 = feature not wired yet). */
export const listMemories = async (): Promise<Memory[]> => mapMemoryList(await call<unknown>('GET', '/api/me/memories'));

/** POST /api/me/memories {content}; the server 409s past the 200-cap. */
export const addMemory = async (content: string): Promise<Memory | null> => {
  const body = await call<unknown>('POST', '/api/me/memories', { content });
  return body == null ? null : mapMemory(body);
};

/**
 * PATCH /api/me/memories/{id} {content?, enabled?}; falls back to PUT when the
 * server registered the route under a different verb (Go mux answers 405).
 */
export async function updateMemory(id: string, patch: MemoryPatch): Promise<Memory | null> {
  const path = `/api/me/memories/${encodeURIComponent(id)}`;
  try {
    const body = await call<unknown>('PATCH', path, patch);
    return body == null ? null : mapMemory(body);
  } catch (e) {
    if (e instanceof ApiError && e.status === 405) {
      const body = await call<unknown>('PUT', path, patch);
      return body == null ? null : mapMemory(body);
    }
    throw e;
  }
}

export const deleteMemory = (id: string) =>
  call<void>('DELETE', `/api/me/memories/${encodeURIComponent(id)}`);

/**
 * POST /api/me/memories/extract {chat_id} → how many memories the model pulled
 * out (slow: up to ~15s). Tolerates {count} | {extracted} | {memories:[…]} |
 * a bare array as the response shape.
 */
export async function extractMemories(chatId: string): Promise<number> {
  const body = await call<unknown>('POST', '/api/me/memories/extract', { chat_id: chatId });
  if (Array.isArray(body)) return body.length;
  const o = (body ?? {}) as Record<string, unknown>;
  for (const k of ['count', 'extracted', 'added', 'created', 'new']) {
    const v = o[k];
    if (typeof v === 'number' && Number.isFinite(v)) return v;
  }
  if (Array.isArray(o.memories)) return o.memories.length;
  return 0;
}
