// FE-extra helper layer over the scaffold-owned $lib/api (api.ts is frozen —
// never edit it; anything FE-extra-specific lives here).

import * as api from './api';

// ---------- model identity ----------

/** Canonical `"providerID:modelID"` key (matches Config.TaskModel format). */
export const modelKey = (m: Pick<api.ModelEntry, 'provider_id' | 'model_id'>) =>
  `${m.provider_id}:${m.model_id}`;

export function findModel(models: api.ModelEntry[], key: string): api.ModelEntry | null {
  return models.find((m) => modelKey(m) === key) ?? null;
}

export const modelLabel = (m: Pick<api.ModelEntry, 'display' | 'model_id'>) =>
  m.display || m.model_id;

// ---------- last-model preference ----------

const LAST_MODEL_KEY = 'chatgo:las…odel';

export function saveLastModel(m: api.ModelEntry): void {
  try {
    localStorage.setItem(LAST_MODEL_KEY, modelKey(m));
  } catch {
    /* private mode etc. */
  }
}

export function loadLastModel(): string | null {
  try {
    return localStorage.getItem(LAST_MODEL_KEY);
  } catch {
    return null;
  }
}

/**
 * Broadcast a model switch so the active chat picks it up.
 * //INTEGRATION: FE-chat's ChatView/Composer should listen for
 * //`window.addEventListener('chatgo:pick-model', e => …)` with
 * //`e.detail = { provider_id, model_id }` and persist the selection on the
 * //chat (PATCH /api/chats/{id}) exactly like its own ModelPicker does. The
 * //palette also PATCHes the chat itself when one is open, so chat views
 * //that don't listen still stay correct after reload via localStorage.
 */
export function dispatchPickModel(m: api.ModelEntry): void {
  window.dispatchEvent(
    new CustomEvent('chatgo:pick-model', {
      detail: { provider_id: m.provider_id, model_id: m.model_id },
    }),
  );
}

// ---------- files helpers (Gallery) ----------

export const isImageFile = (f: api.FileMeta) => f.kind === 'image' || f.mime.startsWith('image/');

const TEXTISH = /(^text\/|json|csv|xml|yaml|typescript|javascript|x-sh|markdown)/i;
const TEXT_EXT = /\.(txt|md|markdown|csv|tsv|json|jsonl|ya?ml|log|xml|html?|c|cc?|cpp|go|rs|py|ts|tsx|js|jsx|sh|sql)$/i;

export const isTextFile = (f: api.FileMeta) =>
  TEXTISH.test(f.mime) || TEXT_EXT.test(f.name) || (!isImageFile(f) && !f.mime.startsWith('application/pdf'));

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  let v = n;
  for (const unit of ['KB', 'MB', 'GB']) {
    v /= 1024;
    if (v < 1024) return `${v >= 10 ? Math.round(v) : v.toFixed(1)} ${unit}`;
  }
  return `${v.toFixed(1)} TB`;
}

/**
 * Image thumbnail URL.
 * //INTEGRATION: the backend stores a 512px `<uuid>.thumb` jpeg per image
 * //(internal/files/images.go) but CONTRACTS.md exposes no thumb URL — only
 * //`GET /files/{id}/{name}` for originals. We use the original; swap to a
 * //`/files/{id}/thumb` variant here if the backend ever exposes one.
 */
export const thumbURL = (f: api.FileMeta) => f.url;

/** Fetch a capped text preview for a /files URL (maxBytes read, then cancel). */
export async function fetchTextPreview(url: string, maxBytes = 200_000): Promise<string> {
  const res = await fetch(url);
  if (!res.ok || !res.body) throw new Error(`HTTP ${res.status}`);
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let out = '';
  let seen = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    seen += value?.byteLength ?? 0;
    out += decoder.decode(value, { stream: seen < maxBytes });
    if (seen >= maxBytes) {
      void reader.cancel();
      out += '\n\n… truncated …';
      break;
    }
  }
  return out;
}
