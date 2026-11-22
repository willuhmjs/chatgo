// Shared reactive stores for the chat view (Svelte 5 runes module).
// Model cache, capability probe, lightbox + read-aloud state, hljs-ready flag.
import { listModels } from '../api';
import { probeSearchEnabled, lsGet, lsSet, LS_KEYS, stripForTTS } from '../chatview-api';
import { isHljsReady, onHljsReady } from './markdown';

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

/** @returns {import('../api').ModelEntry | undefined} */
export function findModel(providerId, modelId) {
  if (!modelId) return undefined;
  return models.items.find((m) => m.model_id === modelId && m.provider_id === providerId)
    ?? models.items.find((m) => m.model_id === modelId);
}

/** Group models by provider display name (stable order). */
export function modelsByProvider() {
  /** @type {Map<string, import('../api').ModelEntry[]>} */
  const map = new Map();
  for (const m of models.items) {
    const key = m.provider || m.provider_id || 'unknown';
    if (!map.has(key)) map.set(key, []);
    map.get(key).push(m);
  }
  return [...map.entries()];
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
