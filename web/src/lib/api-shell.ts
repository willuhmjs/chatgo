// FE-shell additions around the scaffold-owned api.ts (contract rule: never
// edit api.ts, wrap it instead). Pref helpers, pending-prompt handoff, and
// stub endpoints that aren't in docs/CONTRACTS.md §HTTP surface yet.
import * as api from './api';
import { ApiError } from './api';
import type { Chat, ModelEntry } from './api';

// ---------- local preferences ----------

const SEND_ENTER_KEY = 'chatgo:send-on-e...';
const HOME_MODEL_KEY = 'chatgo:home-model';

/** Fired on window whenever shell-owned prefs change (Home listens). */
export const PREFS_EVENT = 'chatgo:prefs';

function readLS(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}
function writeLS(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* private mode etc. */
  }
}

/** Enter sends (default true); when false, ⌘/Ctrl+Enter sends. Persisted locally. */
export function getSendOnEnter(): boolean {
  return readLS(SEND_ENTER_KEY) !== '0';
}
export function setSendOnEnter(v: boolean) {
  writeLS(SEND_ENTER_KEY, v ? '1' : '0');
  window.dispatchEvent(new CustomEvent(PREFS_EVENT));
}

/** Home hero model-pill selection ("providerID:modelID"), persisted locally. */
export function getHomeModel(): string {
  return readLS(HOME_MODEL_KEY) ?? '';
}
export function setHomeModel(id: string) {
  writeLS(HOME_MODEL_KEY, id);
}

// ---------- me / account ----------

/**
 * CONTRACT-DEVIATION: contract has no PATCH-users-me endpoint; profile name is
 * persisted via PUT /api/me/settings under key `name` (free-form JSON map).
 */
export const setMeName = (name: string) => api.setMeSettings({ name });

/**
 * POST /api/me/password — local accounts only (OIDC-only users get a server
 * error). Throttled per session (5/min); errors surface as friendly toasts.
 */
export async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  const res = await fetch('/api/me/password', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
  });
  if (res.ok) return;
  let msg = res.statusText || `HTTP ${res.status}`;
  try {
    const j = (await res.json()) as { error?: string };
    if (j.error) msg = j.error;
  } catch {
    /* non-JSON body */
  }
  if (res.status === 404) msg = 'This server does not support password changes yet.';
  throw new ApiError(res.status, msg);
}

/**
 * CONTRACT-DEVIATION: no "revoke all sessions" endpoint in the contract;
 * logout ends the current server-side session, which is the strongest
 * guarantee the current API offers. TODO: swap for DELETE /api/sessions when
 * BE ships it.
 */
export async function logoutEverywhere(): Promise<void> {
  await api.logout();
}

// ---------- Home → ChatView handoff ----------

const pendingKey = (chatId: string) => `chatgo:pending-prompt:${chatId}`;

/** Stash a first message so ChatView can auto-send it when it mounts.
    JSON shape carries the composer toggles (web search) that must survive the
    Home → ChatView hop; takePendingPrompt still accepts legacy plain strings. */
export interface PendingPrompt {
  content: string;
  search?: boolean;
}

export function stashPendingPrompt(chatId: string, prompt: PendingPrompt) {
  writeLS(pendingKey(chatId), JSON.stringify(prompt));
}

/** Read + clear the stashed first message for a chat (ChatView integration point). */
export function takePendingPrompt(chatId: string): PendingPrompt | null {
  const k = pendingKey(chatId);
  const v = readLS(k);
  if (v === null) return null;
  try {
    localStorage.removeItem(k);
  } catch {
    /* ignore */
  }
  try {
    const p = JSON.parse(v) as PendingPrompt;
    if (p && typeof p.content === 'string') return p;
  } catch {
    /* legacy plain-string stash from a pre-search build */
  }
  return { content: v };
}

/**
 * Create a chat with the home-picked model and stash `prompt` for ChatView
 * (see takePendingPrompt). Returns the fresh chat for navigation.
 *
 * Deliberately NO title: the store's "New chat" placeholder lets the server
 * generate a real one on the first exchange (internal/chat/title.go —
 * TaskModel, falling back to first-48-runes). Pre-naming with a prompt slice
 * made startTitle treat the chat as "already titled" and skip generation.
 */
export async function startChat(prompt: string, model?: ModelEntry, search?: boolean): Promise<Chat> {
  const body: Partial<Chat> = {};
  if (model) {
    body.provider_id = model.provider_id;
    body.model_id = model.model_id;
  }
  const chat = await api.createChat(body);
  stashPendingPrompt(chat.id, { content: prompt, search: search || undefined });
  return chat;
}
