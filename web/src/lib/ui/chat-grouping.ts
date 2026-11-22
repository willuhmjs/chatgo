// Pure helpers for the sidebar chat list (FE-shell).
import type { Chat } from '$lib/api';

export interface ChatGroup {
  label: string;
  chats: Chat[];
}

const DAY = 86400;

/** Compact "x ago" for unix-seconds timestamps. */
export function timeAgo(ts: number): string {
  const s = Math.max(0, Math.floor(Date.now() / 1000 - ts));
  if (s < 60) return 'just now';
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  if (d < 7) return `${d}d ago`;
  if (d < 30) return `${Math.floor(d / 7)}w ago`;
  if (d < 365) return `${Math.floor(d / 30)}mo ago`;
  return `${Math.floor(d / 365)}y ago`;
}

function startOfDay(ts: number): number {
  const d = new Date(ts * 1000);
  d.setHours(0, 0, 0, 0);
  return d.getTime() / 1000;
}

/** Bucket a unix-seconds timestamp into the sidebar day sections (§3). */
export function dayGroup(ts: number): string {
  const now = Date.now() / 1000;
  const today = startOfDay(now);
  const day = startOfDay(ts);
  if (day >= today) return 'Today';
  if (day >= today - DAY) return 'Yesterday';
  if (ts > now - 7 * DAY) return 'Previous 7 Days';
  if (ts > now - 30 * DAY) return 'Previous 30 Days';
  return 'Older';
}

const GROUP_ORDER = ['Today', 'Yesterday', 'Previous 7 Days', 'Previous 30 Days', 'Older'];

/** Group chats by day (input order preserved within each bucket). */
export function groupByDay(chats: Chat[]): ChatGroup[] {
  const map = new Map<string, Chat[]>();
  for (const c of chats) {
    const label = dayGroup(c.updated_at);
    const bucket = map.get(label);
    if (bucket) bucket.push(c);
    else map.set(label, [c]);
  }
  return GROUP_ORDER.filter((l) => map.has(l)).map((label) => ({ label, chats: map.get(label)! }));
}

/** Up-to-two-letter initials for avatar fallbacks. */
export function initials(name: string): string {
  const parts = name.trim().split(/[^\p{L}\p{N}]+/u).filter(Boolean);
  if (parts.length === 0) return '?';
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
}

/** First word of a display name, for the hero greeting. */
export function firstName(name: string): string {
  return name.trim().split(/\s+/)[0] || name;
}
