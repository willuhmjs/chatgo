// Message model for the view layer: api.Message + transient client flags,
// plus consecutive-role grouping for the list renderer.

import type { Message } from '../api';

export interface UIMessage extends Message {
  /** assistant placeholder currently receiving SSE tokens */
  streaming?: boolean;
  /** stream was stopped by the user mid-generation */
  stopped?: boolean;
  /** user message not yet acknowledged by the `meta` SSE event */
  pending?: boolean;
  /** why THIS turn's web search failed (live stream only — see `sources`) */
  search_error?: string;
  /** stream wall-clock (ms) from placeholder to the `done` event; local to the
   *  session that streamed it — powers the tok/s readout in MessageRow */
  duration_ms?: number;
}

export interface MsgGroup {
  key: string;
  role: Message['role'];
  items: UIMessage[];
}

let seq = 0;

/** Client-side temp id; server ids never start with "local-". */
export function uid(): string {
  seq += 1;
  return `local-${Date.now().toString(36)}-${seq}-${Math.random().toString(36).slice(2, 8)}`;
}

export function isLocalId(id: string): boolean {
  return !id || id.startsWith('local-');
}

/** Merge consecutive same-role messages into visual groups. */
export function groupMessages(list: UIMessage[]): MsgGroup[] {
  const groups: MsgGroup[] = [];
  for (const m of list) {
    const last = groups[groups.length - 1];
    if (last && last.role === m.role) last.items.push(m);
    else groups.push({ key: m.id, role: m.role, items: [m] });
  }
  return groups;
}

/** Collapse threshold: messages longer than this get an expand control. */
export const COLLAPSE_CHARS = 3200;
export const COLLAPSE_MAX_PX = 448; // 28rem
