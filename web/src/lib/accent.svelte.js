// Accent store — companion to $lib/theme.svelte.js (which owns html.dark; this
// owns html[data-accent]). Server-synced via the me-settings key "accent":
// setAccent writes through optimistically (localStorage first, PUT fire-and-
// forget), and $lib/accent-sync.svelte.js mirrors the server value back at
// boot. Precedence: server > localStorage > FALLBACK (blue) for a first-time
// visitor with neither. syncAccentFromServer is that boot hook's applier.

import { setMeSettings } from './api';

const KEY = 'chatgo.accent';
const FALLBACK = 'blue';
/** One-time migration flag (see migrateLegacyDefaultAccent). */
const MIGRATED_KEY = 'chatgo:accent-migrated';
/** The pre-v3 default. Stored copies of this value are suspect: in v2 violet
    was BOTH the default and the first swatch, so "leave it as-is" clicks
    persisted it like a deliberate pick. */
const LEGACY_DEFAULT = 'violet';

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
    Safe before mount; called from initTheme() so App needs no new wiring.

    Migration preview: a stored 'violet' with no migration flag yet renders
    blue immediately (no violet flash) but is NOT rewritten here — bootAccent
    makes the final call once it knows whether the server has an accent. */
export function initAccent() {
  try {
    const saved = localStorage.getItem(KEY);
    if (saved && ACCENTS.includes(/** @type {any} */ (saved))) {
      accent.name = saved === LEGACY_DEFAULT && !isAccentMigrationPending() ? 'blue' : /** @type {any} */ (saved);
    }
  } catch {
    /* private mode etc. — stay blue */
  }
  apply();
}

/** True when this device still holds the OLD default ('violet') under
    localStorage and the one-time migration has not run.

    Honesty note (verified against git history of this file, v2 → v3): the
    old initAccent never wrote unconditionally — only setAccent/syncAccent-
    FromServer persist. So every stored 'violet' technically came from a
    click on the swatch… but in v2 violet WAS the default and the highlighted
    first swatch, so many of those "clicks" were users confirming what looked
    like the preselected option, not a preference for violet. The heuristic
    below treats stored violet on an account with NO server-side accent as
    implicit. Residual risk accepted: someone who genuinely loved violet but
    never synced it gets flipped to blue once and can re-pick (which marks
    the flag and sticks forever). */
export function isAccentMigrationPending() {
  try {
    return localStorage.getItem(KEY) === LEGACY_DEFAULT && !localStorage.getItem(MIGRATED_KEY);
  } catch {
    return false;
  }
}

/** Close the migration window forever (any authoritative value counts as a
    decision: an explicit pick, or the server's stored preference). */
export function markAccentMigrated() {
  try {
    localStorage.setItem(MIGRATED_KEY, '1');
  } catch {
    /* ignore */
  }
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
  markAccentMigrated(); // an explicit pick is a decision — never migrate again
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
  markAccentMigrated(); // the server holds an authoritative value — no migration
  if (name === accent.name) return;
  accent.name = /** @type {typeof ACCENTS[number]} */ (name);
  try {
    localStorage.setItem(KEY, String(name));
  } catch {
    /* ignore */
  }
  apply();
}
