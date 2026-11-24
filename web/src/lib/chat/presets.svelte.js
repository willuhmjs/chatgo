// Param presets (Open WebUI parity): named saves of system prompt + sampling
// params (+ model pin), stored per-user in the me-settings "presets" key as a
// JSON array — server-validated (validateMePresets, internal/apimgr/admin.go)
// and size-capped (30 entries / 8KB value). Same module shape as
// model-prefs.svelte.js: one idempotent fetch, fire-and-forget writes that
// degrade gracefully against older servers without the key.
import { getMeSettings, setMeSettings } from '../api';

const KEY = 'presets';
const MAX = 30;

/**
 * @typedef {Object} ChatPreset
 * @property {string} id
 * @property {string} name
 * @property {string} model canonical "providerID:modelID", '' = keep current
 * @property {string} system
 * @property {{ temperature?: number, top_p?: number, max_tokens?: number, stop?: string[] }} params
 */

export const presets = $state({
  /** @type {ChatPreset[]} */ items: [],
  loaded: false,
});

/** @type {Promise<void> | null} */
let loadPromise = null;

/** Tolerate a JSON string, a parsed array, or double-encoding. */
function parseItems(/** @type {unknown} */ raw) {
  let v = raw;
  if (typeof v === 'string') {
    try {
      v = JSON.parse(v);
    } catch {
      return [];
    }
  }
  if (typeof v === 'string') {
    try {
      v = JSON.parse(v);
    } catch {
      return [];
    }
  }
  if (!Array.isArray(v)) return [];
  return (
    /** @type {ChatPreset[]} */ (
      v
        .filter((x) => x && typeof x === 'object' && typeof (x/** @type {any} */).id === 'string' && typeof (x/** @type {any} */).name === 'string')
        .map((x) => ({
          id: String(x.id),
          name: String(x.name).slice(0, 64),
          model: typeof x.model === 'string' ? x.model : '',
          system: typeof x.system === 'string' ? x.system : '',
          params: x.params && typeof x.params === 'object' ? x.params : {},
        }))
    )
  ).slice(0, MAX);
}

/** Idempotent; failure keeps `loaded` false and allows a retry. */
export function load() {
  if (loadPromise) return loadPromise;
  loadPromise = getMeSettings()
    .then((s) => {
      presets.items = parseItems(s?.[KEY]);
      presets.loaded = true;
    })
    .catch(() => {
      loadPromise = null; // allow retry
    });
  return loadPromise;
}

function persist() {
  return setMeSettings({ [KEY]: JSON.stringify(presets.items) }).catch(() => {
    /* server lacks the key (or the payload 400'd) — local list still applies */
  });
}

/**
 * Add (or replace, same id) a preset.
 * @param {Omit<ChatPreset, 'id'> & { id?: string }} p
 * @returns {string} the preset's id
 */
export function save(p) {
  const id = p.id || `p${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`;
  const item = { ...p, id };
  const i = presets.items.findIndex((x) => x.id === id);
  if (i >= 0) presets.items[i] = item;
  else presets.items.push(item);
  if (presets.items.length > MAX) presets.items = presets.items.slice(-MAX);
  void persist();
  return id;
}

/** @param {string} id */
export function remove(id) {
  presets.items = presets.items.filter((x) => x.id !== id);
  void persist();
}
