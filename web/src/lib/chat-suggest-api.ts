// Suggest-next-message API client (sparkle button) for
// POST /api/chats/{id}/suggest. Lives in its own file because
// lib/chatview-api.ts deliberately contains no raw fetch (everything there
// goes through lib/api.ts wrappers); this mirrors lib/api.ts's req()/
// errorFrom() conventions and reuses its exported ApiError.

import { ApiError } from './api';

/** Server-side max items the suggest endpoint ever returns. */
const MAX_SUGGESTIONS = 5;

/**
 * Ask the instance task model for short questions the user might plausibly
 * ask next in this chat (Open WebUI-style suggestions). Resolves with 2-5
 * non-empty strings. Throws ApiError on failure — 503 means the task model
 * is unavailable/disabled, 429 means a suggest is already running for this
 * user (offer again shortly); `e.status` distinguishes them.
 */
export async function suggestNext(chatId: string): Promise<string[]> {
  const res = await fetch(`/api/chats/${encodeURIComponent(chatId)}/suggest`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: '{}',
  });
  if (!res.ok) {
    let msg = res.statusText || `HTTP ${res.status}`;
    try {
      const j = (await res.json()) as { error?: string };
      if (j.error) msg = j.error;
    } catch {
      /* body wasn't JSON */
    }
    throw new ApiError(res.status, msg);
  }
  const data = (await res.json().catch(() => null)) as { suggestions?: unknown } | null;
  const items = Array.isArray(data?.suggestions)
    ? data.suggestions.filter((s): s is string => typeof s === 'string' && s.trim().length > 0)
    : [];
  return items.slice(0, MAX_SUGGESTIONS);
}
