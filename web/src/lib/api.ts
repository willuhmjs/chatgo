// Typed fetch wrapper for EVERY route in docs/CONTRACTS.md §HTTP surface,
// plus the SSE consumer for message streams. Scaffold-owned; all agents call
// into this — do not hand-roll fetch in components.

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.name = 'ApiError';
  }
}

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

// ---------- DTOs (snake_case mirrors the Go JSON) ----------

export interface User {
  id: string;
  email: string;
  name: string;
  role: 'admin' | 'user' | 'pending';
  avatar_url?: string;
  created_at: number;
  last_active_at: number;
}
export interface AuthConfig {
  oidc_enabled: boolean;
  oidc_name: string;
  registration: 'open' | 'approve' | 'closed';
}
export interface Provider {
  id: string;
  name: string;
  base_url: string;
  enabled: boolean;
  is_default: boolean;
  sort: number;
  has_key?: boolean;
}
export interface ModelEntry {
  provider_id: string;
  provider?: string;
  model_id: string;
  display: string;
  context_len: number;
}
export interface Chat {
  id: string;
  title: string;
  provider_id: string;
  model_id: string;
  params?: Record<string, unknown>;
  pinned: boolean;
  folder_id?: string | null;
  share_token?: string | null;
  created_at: number;
  updated_at: number;
}
export interface ChatPage {
  chats: Chat[];
  total: number;
  page: number;
  per: number;
}
export interface SourceRef {
  title: string;
  url: string;
}
export interface Message {
  id: string;
  chat_id: string;
  role: 'user' | 'assistant' | 'system';
  /** branch tree parent ('' = root); sibling ids share a parent = versions */
  parent_id?: string;
  /** true on the leaf of the chat's active branch (set by the server) */
  is_current?: boolean;
  content: string;
  sources?: SourceRef[];
  error?: string | null;
  tokens_in?: number;
  tokens_out?: number;
  created_at: number;
}
export interface Folder {
  id: string;
  name: string;
  parent_id?: string | null;
  sort: number;
}
export interface Tag {
  id: string;
  name: string;
  color?: string;
}
export interface FileMeta {
  id: string;
  name: string;
  mime: string;
  size: number;
  kind: 'upload' | 'artifact' | 'image';
  url: string;
}
export interface ApiToken {
  id: string;
  name: string;
  created_at: number;
  last_used_at?: number | null;
  key?: string; // returned once on create
}
export interface SharePayload {
  chat: Chat;
  messages: Message[];
}
export interface ChatListQuery {
  q?: string;
  folder?: string;
  tag?: string;
  page?: number;
  per?: number;
}

// ---------- auth ----------

export const login = (email: string, password: string) =>
  req<User>('POST', '/api/auth/login', { email, password });
export const register = (email: string, password: string, name: string) =>
  req<User>('POST', '/api/auth/register', { email, password, name });
export const logout = () => req<void>('POST', '/api/auth/logout');
export const me = () => req<User>('GET', '/api/auth/me');
export const authConfig = () => req<AuthConfig>('GET', '/api/auth/config');
export const oidcLoginURL = '/api/auth/oidc/login'; // full-page redirect

// ---------- providers & models (admin) ----------

export const listProviders = () => req<Provider[]>('GET', '/api/providers');
export const createProvider = (p: Partial<Provider> & { api_key?: string }) =>
  req<Provider>('POST', '/api/providers', p);
export const updateProvider = (id: string, p: Partial<Provider> & { api_key?: string }) =>
  req<Provider>('PUT', `/api/providers/${id}`, p);
export const deleteProvider = (id: string) => req<void>('DELETE', `/api/providers/${id}`);
export const syncProvider = (id: string) => req<{ count: number }>('POST', `/api/providers/${id}/sync`);
export const listModels = () => req<ModelEntry[]>('GET', '/api/models');

// ---------- chats ----------

export const listChats = (q: ChatListQuery = {}) => {
  const qs = new URLSearchParams();
  if (q.q) qs.set('q', q.q);
  if (q.folder) qs.set('folder', q.folder);
  if (q.tag) qs.set('tag', q.tag);
  qs.set('page', String(q.page ?? 1));
  qs.set('per', String(q.per ?? 30));
  return req<ChatPage>('GET', `/api/chats?${qs}`);
};
export const createChat = (body: Partial<Chat> = {}) => req<Chat>('POST', '/api/chats', body);
export const getChat = (id: string) => req<Chat>('GET', `/api/chats/${id}`);
export const patchChat = (id: string, patch: Partial<Chat>) => req<Chat>('PATCH', `/api/chats/${id}`, patch);
export const deleteChat = (id: string) => req<void>('DELETE', `/api/chats/${id}`);
export const shareChat = (id: string) => req<{ token: string }>('POST', `/api/chats/${id}/share`);
export const unshareChat = (id: string) => req<void>('DELETE', `/api/chats/${id}/share`);
export const getShare = (token: string) => req<SharePayload>('GET', `/api/share/${token}`);
export const setChatFolder = (id: string, folderId: string | null) =>
  req<void>('PUT', `/api/chats/${id}/folder`, { folder_id: folderId });
export const setChatTags = (id: string, tagIds: string[]) =>
  req<void>('PUT', `/api/chats/${id}/tags`, { tag_ids: tagIds });

// ---------- messages ----------

export interface SendMessageBody {
  content: string;
  images?: { file_id: string }[];
  search?: boolean;
  /** regenerate only: insert the new version beside the anchor instead of
   *  deleting it and the tail (Open WebUI-style branch history) */
  branch?: boolean;
}

export type SSEEventName = 'meta' | 'title' | 'sources' | 'token' | 'done' | 'error';
export interface SSEEvent {
  event: SSEEventName | string;
  data: unknown;
}
export interface StreamMeta {
  message_id: string;
  chat_id: string;
  /** user row persisted for this turn (send / branch-edit); "" on regenerate */
  user_message_id?: string;
}
export interface StreamDone {
  usage: { in: number; out: number } | null;
}

function parseFrame(frame: string): SSEEvent | null {
  let event = 'message';
  const dataLines: string[] = [];
  for (const line of frame.split('\n')) {
    if (line.startsWith(':')) continue; // heartbeat ": ping"
    if (line.startsWith('event:')) event = line.slice(6).trim();
    else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim());
  }
  if (dataLines.length === 0) return null;
  let data: unknown = dataLines.join('\n');
  try {
    data = JSON.parse(data as string);
  } catch {
    /* keep raw string */
  }
  return { event, data };
}

/**
 * POST an SSE stream endpoint and pump frames into onEvent until the
 * terminal `done`/`error` (or abort). Used by both messages + regenerate.
 */
async function streamPost(
  url: string,
  body: unknown,
  onEvent: (ev: SSEEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal,
  });
  if (!res.ok || !res.body) throw await errorFrom(res);
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let idx: number;
    while ((idx = buf.indexOf('\n\n')) >= 0) {
      const frame = buf.slice(0, idx);
      buf = buf.slice(idx + 2);
      const ev = parseFrame(frame);
      if (ev) onEvent(ev);
    }
  }
}

/** POST /api/chats/{id}/messages → SSE (protocol in contract). */
export const streamMessage = (
  chatID: string,
  body: SendMessageBody,
  onEvent: (ev: SSEEvent) => void,
  signal?: AbortSignal,
) => streamPost(`/api/chats/${encodeURIComponent(chatID)}/messages`, body, onEvent, signal);

/** POST /api/chats/{id}/regenerate?from={msgID} → SSE. */
export const streamRegenerate = (
  chatID: string,
  fromMsgID: string | undefined,
  body: Partial<SendMessageBody>,
  onEvent: (ev: SSEEvent) => void,
  signal?: AbortSignal,
) => {
  const q = fromMsgID ? `?from=${encodeURIComponent(fromMsgID)}` : '';
  return streamPost(`/api/chats/${encodeURIComponent(chatID)}/regenerate${q}`, body, onEvent, signal);
};

/**
 * POST /api/chats/{id}/messages/{msgID}/retract — delete a cancelled exchange
 * (the assistant message, the user turn it answered, and the tail) without
 * re-sending. `msgID` is the ASSISTANT message id from the SSE `meta` event —
 * the server walks back over the user prompt itself. 409 while the chat's
 * stream is still settling; 404 when unknown/foreign (never leaks existence).
 */
export const retractMessage = (chatID: string, msgID: string) =>
  req<{ deleted: number }>(
    'POST',
    `/api/chats/${encodeURIComponent(chatID)}/messages/${encodeURIComponent(msgID)}/retract`,
  );

export const listMessages = (chatID: string, before?: string, limit = 200) => {
  const qs = new URLSearchParams({ limit: String(limit) });
  if (before) qs.set('before', before);
  return req<Message[]>('GET', `/api/chats/${chatID}/messages?${qs}`);
};

/**
 * POST /api/chats/{id}/branch {message_id} → {leaf_id} — move the chat's active
 * branch to the version named by message_id, landing on that version's newest
 * continuation. Returns the new leaf so the caller recomputes the visible path
 * from the message tree it already holds (no refetch).
 */
export const switchBranch = (chatID: string, messageID: string) =>
  req<{ leaf_id: string }>(
    'POST',
    `/api/chats/${encodeURIComponent(chatID)}/branch`,
    { message_id: messageID },
  );

// ---------- files ----------

export async function uploadFile(file: File, chatId?: string): Promise<FileMeta> {
  const fd = new FormData();
  fd.append('file', file);
  const url = chatId ? `/api/files?chat=${encodeURIComponent(chatId)}` : '/api/files';
  const res = await fetch(url, { method: 'POST', body: fd });
  if (!res.ok) throw await errorFrom(res);
  return (await res.json()) as FileMeta;
}
export const listFiles = (kind?: string) =>
  req<FileMeta[]>('GET', kind ? `/api/files?kind=${kind}` : '/api/files');

// ---------- folders & tags ----------

export const listFolders = () => req<Folder[]>('GET', '/api/folders');
export const createFolder = (name: string, parentId?: string | null) =>
  req<Folder>('POST', '/api/folders', { name, parent_id: parentId ?? null });
export const updateFolder = (id: string, patch: Partial<Folder>) => req<Folder>('PUT', `/api/folders/${id}`, patch);
export const deleteFolder = (id: string) => req<void>('DELETE', `/api/folders/${id}`);

export const listTags = () => req<Tag[]>('GET', '/api/tags');
export const createTag = (name: string, color?: string) => req<Tag>('POST', '/api/tags', { name, color });
export const updateTag = (id: string, patch: Partial<Tag>) => req<Tag>('PUT', `/api/tags/${id}`, patch);
export const deleteTag = (id: string) => req<void>('DELETE', `/api/tags/${id}`);

// ---------- settings ----------

export const getMeSettings = () => req<Record<string, unknown>>('GET', '/api/me/settings');
export const setMeSettings = (s: Record<string, unknown>) => req<Record<string, unknown>>('PUT', '/api/me/settings', s);
export const getSettings = () => req<Record<string, string>>('GET', '/api/settings');
export const setSettings = (s: Record<string, string>) => req<Record<string, string>>('PUT', '/api/settings', s);

// ---------- users (admin) ----------

export const listUsers = () => req<User[]>('GET', '/api/users');
export const approveUser = (id: string) => req<User>('POST', `/api/users/${id}/approve`);
export const setUserRole = (id: string, role: string) => req<User>('PUT', `/api/users/${id}/role`, { role });

// ---------- api tokens ----------

export const listTokens = () => req<ApiToken[]>('GET', '/api/tokens');
export const createToken = (name: string) => req<ApiToken>('POST', '/api/tokens', { name });
export const deleteToken = (id: string) => req<void>('DELETE', `/api/tokens/${id}`);
