// Accent store — companion to $lib/theme.svelte.js (which owns html.dark; this
// owns html[data-accent]). Wave-1 is localStorage-only per docs/FEATURES-V2.md
// ("integrator promotes to server later"): setAccent persists locally, and
// syncAccentFromServer below is the single hook the integrator wires to the
// me-settings key "accent" when the server allowlist lands. Nothing else calls
// the API from here.

const KEY = 'chatgo.accent';
const FALLBACK = 'violet';

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

/** Read persisted choice and set data-accent (always — defaults to violet).
    Safe before mount; called from initTheme() so App needs no new wiring. */
export function initAccent() {
  try {
    const saved = localStorage.getItem(KEY);
    if (saved && ACCENTS.includes(/** @type {any} */ (saved))) {
      accent.name = /** @type {typeof ACCENTS[number]} */ (saved);
    }
  } catch {
    /* private mode etc. — stay violet */
  }
  apply();
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
  // INTEGRATOR: add server sync here (PUT me-settings { accent }) — one call,
  // local persistence already handled above.
}

/** INTEGRATOR: single server→client sync point for the future me-settings
    "accent" key. Server value wins over stale local storage; unknown values
    are ignored so a bad key can never break theming. */
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
