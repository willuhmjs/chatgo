// Shared reactive stores for the chat view (Svelte 5 runes module).
// Model cache, capability probe, lightbox + read-aloud state, hljs-ready flag.
import { listModels } from '../api';
import { probeSearchEnabled, lsGet, lsSet, LS_KEYS, stripForTTS } from '../chatview-api';
import { isHljsReady, onHljsReady } from './markdown';
import { isHidden } from './model-prefs.svelte.js';

// ---------- hljs readiness (markdown.ts is runes-free; bridge lives here) ----------

export const mdStore = $state({ hljsReady: isHljsReady() });
onHljsReady(() => {
  mdStore.hljsReady = true;
});

// ---------- models (GET /api/models, cached app-wide) ----------

export const models = $state({
  /** @type {import('../api').ModelEntry[]} */
  items: [],
  loading: false,
  loaded: false,
  error: /** @type {string | null} */ (null),
});

/** @type {Promise<void> | null} */
let modelsPromise = null;

/** Idempotent; refetches after a failure. */
export function loadModels(force = false) {
  if (modelsPromise && !force) return modelsPromise;
  models.loading = true;
  models.error = null;
  modelsPromise = listModels()
    .then((items) => {
      models.items = items ?? [];
      models.loaded = true;
    })
    .catch((err) => {
      models.error = String(err?.message ?? err);
      modelsPromise = null; // allow retry
    })
    .finally(() => {
      models.loading = false;
    });
  return modelsPromise;
}

/** Silent forced re-GET for freshness without a page reload (admin edits,
 *  picker open). Never touches loading/error — no spinner, no flicker, an
 *  open menu stays open with its filter text — and on failure the previously
 *  loaded list is kept quietly. Consumers all read models.items, so their
 *  $derived (pickerModels/modelsByProvider groups…) recompute on the swap.
 *  Selection state is untouched. Safe to call fire-and-forget. */
export function refreshModels() {
  return listModels()
    .then((items) => {
      if (!items) return; // tolerate an older/odd server response
      models.items = items;
      models.loaded = true;
      models.error = null;
    })
    .catch(() => {
      /* offline / hiccup — keep the last known list */
    });
}

// Admin→picker invalidation bus (same tab, no reload): ProvidersTab dispatches
// `chatgo:models-changed` after any mutation that moves rows in/out of
// GET /api/models (model toggles, syncs, provider enable/create/delete);
// this module refreshes the shared list. Armed once at module load —
// CommandPalette is mounted app-wide in App.svelte, so the listener exists
// from app boot; the guard makes double-init unstackable.
let modelsBusArmed = false;

function armModelsBus() {
  if (modelsBusArmed || typeof window === 'undefined') return;
  modelsBusArmed = true;
  window.addEventListener('chatgo:models-changed', () => {
    void refreshModels();
  });
}
armModelsBus();

/** @returns {import('../api').ModelEntry | undefined} */
export function findModel(providerId, modelId) {
  if (!modelId) return undefined;
  return models.items.find((m) => m.model_id === modelId && m.provider_id === providerId)
    ?? models.items.find((m) => m.model_id === modelId);
}

/** Canonical model identity key — "providerID:modelID", used everywhere. */
export const modelKey = (/** @type {import('../api').ModelEntry} */ m) =>
  `${m.provider_id}:${m.model_id}`;

/** Case-insensitive substring search over display, model_id and provider
 *  (same haystack ModelPicker has always filtered on). Empty query ⇒ true.
 *  Typed to the minimal shape: admin provider/user model editors reuse this
 *  for rows without a provider leg (provider-local lists), which then match
 *  on display + model_id only. */
export function matchesQuery(/** @type {{ display?: string, model_id: string, provider?: string, provider_id?: string }} */ m, /** @type {string} */ q) {
  const f = (q ?? '').trim().toLowerCase();
  if (!f) return true;
  return `${m.display} ${m.model_id} ${m.provider || m.provider_id || ''}`.toLowerCase().includes(f);
}

// Non-chat model families (embedding / rerank / audio / image …), mirroring
// the backend imggen selection heuristics. Matched at word-ish boundaries as
// word PREFIXES: `embed` hits text-embedding-3-large, `transcri` hits
// gpt-4o-transcribe, `dall` hits dall-e-3 — without swallowing words that
// merely contain the substring (e.g. a hypothetical "searchocr" id).
const NON_CHAT_WORDS = [
  'embed',
  'rerank',
  'asr',
  'tts',
  'whisper',
  'audio',
  'voice',
  'ocr',
  'moderation',
  'image',
  'dall',
  'imagen',
  'flux',
  'sd3',
  'stable-diffusion',
  'diffusion',
  'aligner',
  'transcri',
];
const NON_CHAT_RES = NON_CHAT_WORDS.map((w) => new RegExp(`(?:^|[^a-z0-9])${w}`));

/** Heuristic: plausibly a chat/completions model? Excludes embedding,
 *  rerank, TTS/ASR, moderation and image-generation families by id + display. */
export function isLikelyChatModel(/** @type {import('../api').ModelEntry} */ m) {
  const hay = `${m.model_id} ${m.display ?? ''}`.toLowerCase();
  return !NON_CHAT_RES.some((re) => re.test(hay));
}

/**
 * Default picker list: drop hidden models, then drop non-chat families — but
 * only when that actually trims ≥1 item AND at least one chat model survives
 * (i.e. the provider list is mixed). The caller gets the heuristic-dropped
 * count so it can render a "show the rest" footer that lifts it per session.
 * @param {import('../api').ModelEntry[]} items
 * @param {{ showAll?: boolean }} [opts]
 */
export function pickerModels(items, opts = {}) {
  const visible = items.filter((m) => !isHidden(modelKey(m)));
  if (opts.showAll) return { models: visible, hiddenByHeuristic: 0 };
  const chat = visible.filter(isLikelyChatModel);
  const dropped = visible.length - chat.length;
  if (dropped >= 1 && chat.length >= 1) return { models: chat, hiddenByHeuristic: dropped };
  return { models: visible, hiddenByHeuristic: 0 };
}

/**
 * Group models by PROVIDER ID (stable order) — keying by display name used to
 * merge two providers that share a name and render every model twice. Labels
 * are the display name; when two ids share one label the id is appended in
 * parens. Exact provider_id:model_id repeats are dropped defensively.
 * @param {import('../api').ModelEntry[]} [items]
 * @returns {{ id: string; label: string; models: import('../api').ModelEntry[] }[]}
 */
export function modelsByProvider(items = models.items) {
  /** @type {Map<string, { id: string; label: string; models: import('../api').ModelEntry[] }>} */
  const map = new Map();
  /** @type {Set<string>} */
  const seen = new Set();
  for (const m of items) {
    const key = modelKey(m);
    if (seen.has(key)) continue; // server handed us the same model twice
    seen.add(key);
    const id = m.provider_id || m.provider || 'unknown';
    let g = map.get(id);
    if (!g) {
      g = { id, label: m.provider || m.provider_id || 'unknown', models: [] };
      map.set(id, g);
    }
    g.models.push(m);
  }
  const groups = [...map.values()];
  /** @type {Map<string, number>} */
  const labelCount = new Map();
  for (const g of groups) labelCount.set(g.label, (labelCount.get(g.label) ?? 0) + 1);
  for (const g of groups) {
    if ((labelCount.get(g.label) ?? 0) > 1) g.label = `${g.label} (${g.id})`;
  }
  return groups;
}

// ---------- capabilities (web-search toggle visibility) ----------

export const capabilities = $state({
  search: lsGet(LS_KEYS.capSearch) === '1',
});

let capProbed = false;

/** Probe once per app start (cache in localStorage avoids toggle flicker). */
export function refreshCapabilities(userRole) {
  if (capProbed) return Promise.resolve(capabilities.search);
  capProbed = true;
  return probeSearchEnabled(userRole).then((ok) => {
    capabilities.search = ok;
    lsSet(LS_KEYS.capSearch, ok ? '1' : '0');
    return ok;
  });
}

// ---------- image lightbox ----------

export const lightbox = $state({ src: '' });

export function openLightbox(src) {
  if (src) lightbox.src = src;
}
export function closeLightbox() {
  lightbox.src = '';
}

// ---------- read-aloud (speechSynthesis) ----------

export const speechSupported =
  typeof window !== 'undefined' && 'speechSynthesis' in window && 'SpeechSynthesisUtterance' in window;

export const speech = $state({
  /** id of the assistant message currently being read aloud */
  id: /** @type {string | null} */ (null),
});

export function stopSpeaking() {
  if (!speechSupported) return;
  window.speechSynthesis.cancel();
  speech.id = null;
}

/** Toggle reading `msg`; cancels any previous utterance. */
export function toggleSpeak(msg) {
  if (!speechSupported) return;
  if (speech.id === msg.id) {
    stopSpeaking();
    return;
  }
  window.speechSynthesis.cancel();
  const text = stripForTTS(msg.content ?? '');
  if (!text) return;
  const utter = new SpeechSynthesisUtterance(text);
  utter.lang = navigator.language || 'en-US';
  utter.onend = () => {
    if (speech.id === msg.id) speech.id = null;
  };
  utter.onerror = utter.onend;
  speech.id = msg.id;
  window.speechSynthesis.speak(utter);
}
