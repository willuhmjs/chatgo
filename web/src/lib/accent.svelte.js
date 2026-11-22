// Accent store — companion to $lib/theme.svelte.js (which owns html.dark; this
// owns html[data-accent]). Server-synced via the me-settings key "accent":
// setAccent writes through optimistically (localStorage first, PUT fire-and-
// forget), and $lib/accent-sync.svelte.js mirrors the server value back at
// boot. Precedence: server > localStorage > FALLBACK (blue) for a first-time
// visitor with neither. syncAccentFromServer is that boot hook's applier.

import { setMeSettings } from './api';

const KEY = 'chatgo.accent';
const FALLBACK = 'blue';

/** Picker options, in display order (Settings → Accent color). */
export const ACCENTS = /** @type {const} */ (['violet', 'blue', 'teal', 'green', 'orange', 'pink']);

export const accent = $state({
  /** @type {typeof ACCENTS[number]} */
  name: FALLBACK,
});

function apply() {
  if (typeof document === 'undefined') return;
  document.documentElement.dataset.accent = accent.name;
}

/** Read persisted choice and set data-accent (always — defaults to blue).
    Safe before mount; called from initTheme() so App needs no new wiring. */
export function initAccent() {
  try {
    const saved = localStorage.getItem(KEY);
    if (saved && ACCENTS.includes(/** @type {any} */ (saved))) {
      accent.name = /** @type {typeof ACCENTS[number]} */ (saved);
    }
  } catch {
    /* private mode etc. — stay blue */
  }
  apply();
}

/** The explicit device-local choice ('' when never picked). A pre-existing
    localStorage value counts as an explicit user choice — bootAccent uses
    this to migrate it to the server when the account has none. */
export function readStoredAccent() {
  try {
    const saved = localStorage.getItem(KEY);
    return saved && ACCENTS.includes(/** @type {any} */ (saved)) ? saved : '';
  } catch {
    return '';
  }
}

export function setAccent(name) {
  if (!ACCENTS.includes(/** @type {any} */ (name))) return;
  accent.name = name;
  try {
    localStorage.setItem(KEY, String(name));
  } catch {
    /* ignore */
  }
  apply();
  // Optimistic write-through, same shape as chat/model-prefs persist():
  // offline or an older server without the "accent" allowlist entry degrades
  // to localStorage-only for this device.
  void setMeSettings({ accent: String(name) }).catch(() => {
    /* server-side lacks the key yet — local choice still applied */
  });
}

/** Boot hook ($lib/accent-sync.svelte.js): server→client sync for the
    me-settings "accent" key. Server value wins over stale local storage;
    unknown values are ignored so a bad key can never break theming. */
export function syncAccentFromServer(name) {
  if (!ACCENTS.includes(/** @type {any} */ (name))) return;
  if (name === accent.name) return;
  accent.name = /** @type {typeof ACCENTS[number]} */ (name);
  try {
    localStorage.setItem(KEY, String(name));
  } catch {
    /* ignore */
  }
  apply();
}
