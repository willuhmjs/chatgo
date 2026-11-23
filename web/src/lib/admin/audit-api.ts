// Audit-log API facade — GET /api/audit?cursor=&limit=50 (admin-only, 403
// otherwise, newest-first). api.ts is scaffold-owned and frozen, so this small
// module carries its own credentials-included fetch for the one route the
// backend agents added outside the original contract surface.
//
// The mapper is deliberately tolerant: the payload is treated as untrusted
// text, so every missing/oddly-typed field degrades to '' (or ts → 0) instead
// of throwing, and unknown action names simply get the neutral chip style.
import { ApiError } from '$lib/api';

export interface AuditEntry {
  ts: number; // unix seconds; 0 when the server omitted/zeroed it
  user_id: string;
  email: string;
  action: string;
  detail: string;
  ip: string;
}

export interface AuditPage {
  items: AuditEntry[];
  next_cursor: string | null;
}

const LIMIT = 50;

/** Chip hue category for an action name (see AuditTab for the tints). */
export type AuditKind = 'auth' | 'security' | 'admin' | 'neutral';

// Order matters: security is tested before auth so "login_failed" reads amber,
// and before admin so "token_rotate" reads amber rather than blue-purple.
const SECURITY_RE = /2fa|totp|mfa|fail|denied|lock|invalid|brute|suspect|revok|rotat/;
const AUTH_RE = /login|log_?in|log_?out|logout|sign_?[-_]?(in|out|up)|register|session|password/;
const ADMIN_RE = /user|provider|setting|token|role|api_?key|model|sync|create|update|delete|invite|approve|promote|demote/;

/** Hue-categorise an action string; unknown/empty actions are neutral. */
export function actionKind(action: string): AuditKind {
  const a = action.toLowerCase();
  if (!a) return 'neutral';
  if (SECURITY_RE.test(a)) return 'security';
  if (AUTH_RE.test(a)) return 'auth';
  if (ADMIN_RE.test(a)) return 'admin';
  return 'neutral';
}

function str(v: unknown): string {
  if (typeof v === 'string') return v;
  if (typeof v === 'number' && Number.isFinite(v)) return String(v);
  return '';
}

function num(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) ? v : 0;
}

/** Missing/odd fields → '' / 0 rather than undefined leaking into the table. */
function mapEntry(raw: unknown): AuditEntry {
  const o = (raw ?? {}) as Record<string, unknown>;
  return {
    ts: num(o.ts),
    user_id: str(o.user_id),
    email: str(o.email),
    action: str(o.action),
    detail: str(o.detail),
    ip: str(o.ip),
  };
}

/** One cursor page. Pass null/'' cursor for the newest page. */
export async function fetchAuditPage(cursor?: string | null): Promise<AuditPage> {
  const params = new URLSearchParams({ limit: String(LIMIT) });
  if (cursor) params.set('cursor', cursor);
  const res = await fetch(`/api/audit?${params.toString()}`, {
    method: 'GET',
    credentials: 'include',
    headers: { Accept: 'application/json' },
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
  let body: unknown = null;
  try {
    body = await res.json();
  } catch {
    body = null; // 200 with a non-JSON/empty body degrades to an empty page
  }
  const o = (body ?? {}) as Record<string, unknown>;
  const rawItems = Array.isArray(o.items) ? o.items : Array.isArray(body) ? (body as unknown[]) : [];
  const items = rawItems.map(mapEntry);
  const next = typeof o.next_cursor === 'string' && o.next_cursor !== '' ? o.next_cursor : null;
  return { items, next_cursor: next };
}
