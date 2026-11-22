// FE-PAGES helper layer — wave-2 endpoints for /knowledge (BE-RAG),
// /images (BE-SEC imggen) and Settings → Library (BE-SOCIAL prompts).
// api.ts is scaffold-owned and frozen; these routes do NOT belong there and
// components must not hand-roll fetch either (same law), so every call
// FE-PAGES makes lives here. The Go side lands in parallel with this file, so
// payloads are normalized defensively and 404 is treated as "endpoint not
// served yet" — pages swallow those quietly via isMissing() and show empty
// states. Each section lists its response-shape assumptions for the
// integrator to verify against the shipped handlers.

import { ApiError } from './api';
import type { FileMeta, ModelEntry } from './api';

export type { FileMeta, ModelEntry };

async function req<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal,
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
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

/** 404 = endpoint/resource not served yet (parallel build) — pages degrade quietly. */
export function isMissing(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404;
}

/** Toast-ready message for an unknown error. */
export function errMessage(err: unknown, fallback = 'Request failed'): string {
  return err instanceof ApiError && err.message ? err.message : fallback;
}

/** Pick an array out of a payload that may be a bare array or `{ <first present key>: [] }`. */
function listOf<T>(payload: unknown, ...keys: string[]): T[] {
  if (Array.isArray(payload)) return payload as T[];
  if (payload && typeof payload === 'object') {
    for (const k of keys) {
      const v = (payload as Record<string, unknown>)[k];
      if (Array.isArray(v)) return v as T[];
    }
  }
  return [];
}

function firstNum(...vals: unknown[]): number {
  for (const v of vals) if (typeof v === 'number' && Number.isFinite(v)) return v;
  return 0;
}

function firstStr(...vals: unknown[]): string {
  for (const v of vals) if (typeof v === 'string' && v) return v;
  return '';
}

// ---------- knowledge collections (BE-RAG · internal/kb) ----------
// Shape assumptions:
//   GET    /api/kb            → [{id,name,created_at(unix-sec)}] (bare array;
//                               also accepts {collections|items|data: []}).
//                               Optional per-collection file_count|num_files.
//   POST   /api/kb {name}     → the created collection object.
//   DELETE /api/kb/{id}       → 204.
//   GET    /api/kb/{id}/files → [{id,name,mime,size|bytes,created_at|created}]
//   DELETE /api/kb/{id}/files/{fileId} → 204  (assumed sub-path form)
//   POST   /api/kb/{id}/files multipart, field name "file" (mirrors api.ts uploadFile)
//   POST   /api/kb/{id}/search {q,top_k} → [{content|chunk|text, score, file_name|filename|file|name}]
//                               (also accepts {results|hits|chunks|data: []})

export const MAX_UPLOAD_BYTES = 5 * 1024 * 1024; // backend io.LimitReader cap (docs/FEATURES-V2.md)

export interface KbCollection {
  id: string;
  name: string;
  created_at: number;
  /** null = the endpoint doesn't report it (we fall back to the loaded list). */
  file_count: number | null;
}

export interface KbFile {
  id: string;
  name: string;
  mime: string;
  size: number;
  created_at: number;
}

export interface KbSearchHit {
  file_name: string;
  content: string;
  score: number;
}

type Raw = Record<string, unknown>;

function normCollection(c: Raw): KbCollection {
  const fc = c.file_count ?? c.num_files;
  return {
    id: String(c.id ?? ''),
    name: firstStr(c.name) || 'Untitled',
    created_at: firstNum(c.created_at, c.created),
    file_count: typeof fc === 'number' ? fc : null,
  };
}

function normFile(f: Raw): KbFile {
  return {
    id: String(f.id ?? ''),
    name: firstStr(f.name) || 'file',
    mime: firstStr(f.mime),
    size: firstNum(f.size, f.bytes),
    created_at: firstNum(f.created_at, f.created, f.uploaded_at),
  };
}

export async function listKb(): Promise<KbCollection[]> {
  const data = await req<unknown>('GET', '/api/kb');
  return listOf<Raw>(data, 'collections', 'items', 'data').map(normCollection);
}

export async function createKb(name: string): Promise<KbCollection> {
  const raw = await req<Raw>('POST', '/api/kb', { name });
  return normCollection(raw ?? {});
}

export const deleteKb = (id: string) => req<void>('DELETE', `/api/kb/${encodeURIComponent(id)}`);

export async function listKbFiles(collectionId: string): Promise<KbFile[]> {
  const data = await req<unknown>('GET', `/api/kb/${encodeURIComponent(collectionId)}/files`);
  return listOf<Raw>(data, 'files', 'items', 'data').map(normFile);
}

export const deleteKbFile = (collectionId: string, fileId: string) =>
  req<void>('DELETE', `/api/kb/${encodeURIComponent(collectionId)}/files/${encodeURIComponent(fileId)}`);

export async function searchKb(collectionId: string, q: string, topK = 5): Promise<KbSearchHit[]> {
  const data = await req<unknown>(
    'POST',
    `/api/kb/${encodeURIComponent(collectionId)}/search`,
    { q, top_k: topK },
  );
  return listOf<Raw>(data, 'results', 'hits', 'chunks', 'items', 'data').map((h) => ({
    file_name: firstStr(h.file_name, h.filename, h.file, h.name),
    content: firstStr(h.content, h.chunk, h.text),
    score: firstNum(h.score, h.similarity, h._score),
  }));
}

/**
 * Upload one file into a collection via XHR (fetch can't report upload
 * progress). Resolves on 2xx regardless of body — callers refresh the file
 * list. Rejects with ApiError carrying the server's error message.
 */
export function uploadKbFile(
  collectionId: string,
  file: File,
  onProgress?: (pct: number) => void,
): Promise<void> {
  return new Promise((resolve, reject) => {
    const fd = new FormData();
    fd.append('file', file);
    const xhr = new XMLHttpRequest();
    xhr.open('POST', `/api/kb/${encodeURIComponent(collectionId)}/files`);
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && onProgress) onProgress(Math.round((e.loaded / e.total) * 100));
    };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve();
        return;
      }
      let msg = xhr.statusText || `HTTP ${xhr.status}`;
      try {
        const j = JSON.parse(xhr.responseText) as { error?: string };
        if (j.error) msg = j.error;
      } catch {
        /* non-JSON body */
      }
      reject(new ApiError(xhr.status, msg));
    };
    xhr.onerror = () => reject(new ApiError(0, 'Network error during upload'));
    xhr.send(fd);
  });
}

// ---------- prompt library (BE-SOCIAL · internal/library) ----------
// Shape assumptions:
//   GET    /api/prompts → [{id,user_id,title,content,created_at,updated_at}]
//                         (bare array; also accepts {prompts|items|data: []}).
//                         user_id null/absent/"" ⇒ admin-curated ("global").
//   POST   /api/prompts {title,content} → created prompt.
//   PUT    /api/prompts/{id} {title,content} → updated prompt.
//   DELETE /api/prompts/{id} → 204.

export const PROMPT_MAX_LEN = 4096;

export interface Prompt {
  id: string;
  /** null ⇒ global (admin-curated). */
  user_id: string | null;
  title: string;
  content: string;
  created_at: number;
  updated_at: number;
}

function normPrompt(p: Raw): Prompt {
  const uid = p.user_id;
  return {
    id: String(p.id ?? ''),
    user_id: uid === undefined || uid === null || uid === '' ? null : String(uid),
    title: firstStr(p.title) || 'Untitled',
    content: firstStr(p.content),
    created_at: firstNum(p.created_at, p.created),
    updated_at: firstNum(p.updated_at, p.updated, p.created_at),
  };
}

export async function listPrompts(): Promise<Prompt[]> {
  const data = await req<unknown>('GET', '/api/prompts');
  return listOf<Raw>(data, 'prompts', 'items', 'data').map(normPrompt);
}

export async function createPrompt(title: string, content: string): Promise<Prompt> {
  const raw = await req<Raw>('POST', '/api/prompts', { title, content });
  return normPrompt(raw ?? {});
}

export async function updatePrompt(id: string, title: string, content: string): Promise<Prompt> {
  const raw = await req<Raw>('PUT', `/api/prompts/${encodeURIComponent(id)}`, { title, content });
  return normPrompt(raw ?? {});
}

export const deletePrompt = (id: string) => req<void>('DELETE', `/api/prompts/${encodeURIComponent(id)}`);

// ---------- image generation (BE-SEC · internal/imggen) ----------
// Shape assumptions:
//   POST /api/images/generate {prompt, size(number), n(number), provider_id?} →
//     {files:[{id,url,name?}]}; ALSO accepted: {images:[<url-string> | {id,url,name?}]}
//     and bare arrays. Items without a url are dropped (no b64 support assumed).
//   GET /api/images → newest-50 own images as FileMeta-shaped rows
//     [{id,name,mime,size|bytes,kind?,url,created_at(unix-sec)}];
//     {images: []} (shipped envelope, internal/imggen/save.go handleList),
//     bare array, or {files|items|data: []}.
//   DELETE /api/images/{id} → 204 (own images only; foreign/unknown → 404).
//   size is sent as a plain number ("512") — if the Go handler wants OpenAI's
//   "512x512" string instead, change the one `size:` line in generateImages.

export interface GeneratedImage {
  id: string;
  url: string;
  name: string;
}

/** Gallery row: FileMeta plus the server timestamp (unix seconds). */
export interface ImageFile extends FileMeta {
  /** 0 when the server did not report it (older build) — callers fall back. */
  created_at: number;
}

function normImage(item: unknown): GeneratedImage | null {
  if (typeof item === 'string') return item ? { id: '', url: item, name: 'image' } : null;
  if (!item || typeof item !== 'object') return null;
  const r = item as Raw;
  const url = firstStr(r.url, r.file_url, r.href);
  if (!url) return null;
  return { id: firstStr(r.id), url, name: firstStr(r.name) || 'image' };
}

export async function generateImages(
  body: { prompt: string; size: number; n: number; providerId?: string },
  signal?: AbortSignal,
): Promise<GeneratedImage[]> {
  const payload: Raw = { prompt: body.prompt, size: body.size, n: body.n };
  if (body.providerId) payload.provider_id = body.providerId;
  const data = await req<unknown>('POST', '/api/images/generate', payload, signal);
  return listOf<unknown>(data, 'files', 'images', 'items', 'data')
    .map(normImage)
    .filter((i): i is GeneratedImage => i !== null);
}

export async function listImages(): Promise<ImageFile[]> {
  const data = await req<unknown>('GET', '/api/images');
  // 'images' is what the backend actually sends (save.go handleList);
  // PUB-CONTRACT-SWEEP #2 — the probe once omitted it and the strip sat empty.
  return listOf<Raw>(data, 'images', 'files', 'items', 'data')
    .map((f) => ({
      id: firstStr(f.id),
      name: firstStr(f.name) || 'image',
      mime: firstStr(f.mime) || 'image/png',
      size: firstNum(f.size, f.bytes),
      kind: 'image' as const,
      url: firstStr(f.url),
      created_at: firstNum(f.created_at),
    }))
    .filter((f) => f.url !== '');
}

export const deleteImage = (id: string) => req<void>('DELETE', `/api/images/${encodeURIComponent(id)}`);

// ---------- files list, paged (gallery · internal/apichat/files.go) ----------
// Shape assumptions (verified against handleFileList, files.go:100-129):
//   GET /api/files?kind=&page=&per= →
//     {items:[{id,name,mime,size,kind,url,created_at(unix-sec)}],total,page,per}
//     (per defaults 30, clamped ≤ 100; rows newest-first, stable ordering).
//     Bare arrays and {files|data: []} also accepted, defensively.
// Why here and not api.ts: the frozen api.ts `listFiles` still claims a bare
// FileMeta[], but the endpoint has answered with the paging envelope all
// along (PUB-CONTRACT-SWEEP #3). api.ts stays untouched; the gallery calls
// this tolerant helper instead — it also surfaces total/page for "load more".

export interface FilePage {
  items: FileMeta[];
  total: number;
  page: number;
  per: number;
}

const FILE_KINDS = new Set(['upload', 'artifact', 'image']);

function normFileMeta(f: Raw): FileMeta | null {
  const id = firstStr(f.id);
  const url = firstStr(f.url);
  if (!id || !url) return null; // keyed grids need both
  const mime = firstStr(f.mime);
  const kind = firstStr(f.kind);
  return {
    id,
    name: firstStr(f.name) || 'file',
    mime,
    size: firstNum(f.size, f.bytes),
    kind: (FILE_KINDS.has(kind)
      ? kind
      : mime.startsWith('image/')
        ? 'image'
        : 'artifact') as FileMeta['kind'],
    url,
  };
}

export async function listFilesPage(opts: { kind?: string; page?: number; per?: number } = {}): Promise<FilePage> {
  const qs = new URLSearchParams();
  if (opts.kind) qs.set('kind', opts.kind);
  if (opts.page) qs.set('page', String(opts.page));
  if (opts.per) qs.set('per', String(opts.per));
  const data = await req<unknown>('GET', `/api/files${qs.toString() ? `?${qs}` : ''}`);
  const items = listOf<Raw>(data, 'items', 'files', 'data')
    .map(normFileMeta)
    .filter((f): f is FileMeta => f !== null);
  const p = (Array.isArray(data) ? {} : (data ?? {})) as Raw;
  return {
    items,
    total: firstNum(p.total, items.length),
    page: firstNum(p.page, 1),
    per: firstNum(p.per, items.length),
  };
}

/**
 * Provider dropdown options for the generate box: distinct providers of
 * image-ish models when any model looks image-capable, otherwise every
 * distinct provider (auto-select on the server picks the model).
 */
const IMAGE_MODEL_RE = /(image|dall|imagen|flux|stable.?diffusion|sdxl|sd[-_]?3|firefly)/i;

export function hasImageModels(models: ModelEntry[]): boolean {
  return models.some((m) => IMAGE_MODEL_RE.test(`${m.model_id} ${m.display}`));
}

export function imageProviderOptions(models: ModelEntry[]): { value: string; label: string }[] {
  const img = models.filter((m) => IMAGE_MODEL_RE.test(`${m.model_id} ${m.display}`));
  const seen = new Map<string, string>();
  for (const m of img.length > 0 ? img : models) {
    if (!seen.has(m.provider_id)) seen.set(m.provider_id, m.provider || m.provider_id);
  }
  return [...seen].map(([value, label]) => ({ value, label }));
}
