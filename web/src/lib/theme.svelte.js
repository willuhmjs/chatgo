// Theme store (scaffold-owned): dark default, persisted, toggles .dark on <html>.
// Also boots the accent system (html[data-accent]) so App.svelte needs no new
// wiring — see $lib/accent.svelte.js.

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
}

export function toggleTheme() {
  setTheme(theme.mode === 'dark' ? 'light' : 'dark');
}
