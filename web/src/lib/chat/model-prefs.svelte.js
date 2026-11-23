// Server-synced model preferences (Svelte 5 runes module): default chat model
// + models hidden from the pickers. Backed by GET/PUT /api/me/settings with
// the parallel BE contract keys: "default_model" — JSON string "" or
// "providerID:modelID"; "hidden_models" — JSON string containing a JSON array
// of "providerID:modelID". One fetch shared via this module; degrades
// gracefully against older servers that don't know the keys yet (PUT 400s and
// GET misses are swallowed — local state still applies for the session).
import { getMeSettings, setMeSettings } from '../api';

export const modelPrefs = $state({
  /** canonical "providerID:modelID" key, '' = none */
  defaultModel: /** @type {string} */ (''),
  /** @type {string[]} */
  hiddenModels: [],
  loaded: false,
});

const DEFAULT_KEY = 'default_model';
const HIDDEN_KEY = 'hidden_models';

/** @type {Promise<void> | null} */
let loadPromise = null;

/** Contract: a JSON string; tolerate double-encoding just in case. */
function parseDefault(/** @type {unknown} */ raw) {
  if (typeof raw !== 'string') return '';
  let v = raw.trim();
  if (v.startsWith('"')) {
    try {
      const inner = JSON.parse(v);
      if (typeof inner === 'string') v = inner.trim();
    } catch {
      /* keep raw */
    }
  }
  return v;
}

/** Contract: a JSON string containing a JSON array; tolerate a parsed one. */
function parseHidden(/** @type {unknown} */ raw) {
  let v = raw;
  if (typeof v === 'string') {
    try {
      v = JSON.parse(v);
    } catch {
      return [];
    }
  }
  return Array.isArray(v) ? v.filter((x) => typeof x === 'string') : [];
}

/** Idempotent; failure keeps `loaded` false and allows a retry (e.g. a
 *  later mount or the next sign-in). Safe to call fire-and-forget. */
export function load() {
  if (loadPromise) return loadPromise;
  loadPromise = getMeSettings()
    .then((s) => {
      modelPrefs.defaultModel = parseDefault(s?.[DEFAULT_KEY]);
      modelPrefs.hiddenModels = parseHidden(s?.[HIDDEN_KEY]);
      modelPrefs.loaded = true;
    })
    .catch(() => {
      loadPromise = null; // allow retry
    });
  return loadPromise;
}

/** Fire-and-forget persist; unknown-key 400s from an older server are fine. */
function persist(/** @type {string} */ key, /** @type {string} */ value) {
  return setMeSettings({ [key]: value }).catch(() => {
    /* server side lacks the key yet — local state still applied */
  });
}

/** @param {string} key "providerID:modelID" */
export function isHidden(key) {
  return modelPrefs.hiddenModels.includes(key);
}

/** Set the default model ("providerID:modelID"; '' clears). */
export function setDefault(/** @type {string} */ key) {
  modelPrefs.defaultModel = key;
  return persist(DEFAULT_KEY, key);
}

/** Hide a model from the pickers. @param {string} key */
export function hide(/** @type {string} */ key) {
  if (isHidden(key)) return Promise.resolve();
  modelPrefs.hiddenModels = [...modelPrefs.hiddenModels, key];
  return persist(HIDDEN_KEY, JSON.stringify(modelPrefs.hiddenModels));
}

/** @param {string} key */
export function unhide(/** @type {string} */ key) {
  if (!isHidden(key)) return Promise.resolve();
  modelPrefs.hiddenModels = modelPrefs.hiddenModels.filter((k) => k !== key);
  return persist(HIDDEN_KEY, JSON.stringify(modelPrefs.hiddenModels));
}

export function unhideAll() {
  modelPrefs.hiddenModels = [];
  return persist(HIDDEN_KEY, JSON.stringify([]));
}

/**
 * Bulk show/hide with ONE PUT (Settings' per-provider "Hide all / Show all").
 * Optimistic write-through like hide/unhide: merges or removes `keys` in
 * hidden_models locally first, then persists. No request when nothing changes.
 * @param {string[]} keys canonical "providerID:modelID" keys
 * @param {boolean} hidden true ⇒ hide them all, false ⇒ show them all
 */
export function setHiddenAll(/** @type {string[]} */ keys, /** @type {boolean} */ hidden) {
  const cur = new Set(modelPrefs.hiddenModels);
  const next = new Set(cur);
  for (const k of keys) {
    if (hidden) next.add(k);
    else next.delete(k);
  }
  const same = next.size === cur.size && [...next].every((k) => cur.has(k));
  if (same) return Promise.resolve();
  modelPrefs.hiddenModels = [...next];
  return persist(HIDDEN_KEY, JSON.stringify(modelPrefs.hiddenModels));
}
