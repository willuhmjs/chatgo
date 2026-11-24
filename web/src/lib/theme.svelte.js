// Theme store (scaffold-owned): dark default, persisted, toggles .dark on <html>.
// Also boots the accent system (html[data-accent]) so App.svelte needs no new
// wiring — see $lib/accent.svelte.js. Since the Open WebUI parity wave the
// choice additionally rides the me-settings "theme" key (server allowlisted,
// free text), so it follows the account across devices — same precedence as
// accent: server > localStorage > dark default.

import { setMeSettings } from './api';
import { initAccent } from './accent.svelte.js';

const KEY = 'chatgo-theme';

export const theme = $state({
  /** @type {'dark'|'light'} */
  mode: 'dark',
});

function apply() {
  if (typeof document === 'undefined') return;
  document.documentElement.classList.toggle('dark', theme.mode === 'dark');
  document.documentElement.style.colorScheme = theme.mode;
}

/** Read persisted preference and apply. Safe before mount. */
export function initTheme() {
  try {
    const saved = localStorage.getItem(KEY);
    if (saved === 'light' || saved === 'dark') theme.mode = saved;
  } catch {
    /* private mode etc. — stay dark */
  }
  apply();
  initAccent();
}

export function setTheme(mode) {
  theme.mode = mode;
  try {
    localStorage.setItem(KEY, mode);
  } catch {
    /* ignore */
  }
  apply();
  // Optimistic write-through (me-settings "theme"), mirroring setAccent: an
  // anonymous toggle or an older server without the key degrades to
  // localStorage-only for this device; the next boot pull just won't find it.
  void setMeSettings({ theme: mode }).catch(() => {
    /* not signed in / server lacks the key — local choice still applied */
  });
}

/** Boot hook ($lib/accent-sync.svelte.js reuses the accent me-settings fetch):
    server→client sync for the "theme" key. Server value wins over stale local
    storage; unknown values are ignored. No write-back (avoids a redundant
    PUT immediately after the GET that produced this value). */
export function syncThemeFromServer(/** @type {unknown} */ mode) {
  if (mode !== 'dark' && mode !== 'light') return;
  if (mode === theme.mode) return;
  theme.mode = mode;
  try {
    localStorage.setItem(KEY, mode);
  } catch {
    /* ignore */
  }
  apply();
}

export function toggleTheme() {
  setTheme(theme.mode === 'dark' ? 'light' : 'dark');
}
