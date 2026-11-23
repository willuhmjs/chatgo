// Shared JS entrance transitions (POLISH-IMG). The app.css §9 kill-switch only
// silences CSS animations — svelte/transition runs on Web Animations, so JS
// entrances need their own guard. These factories check prefers-reduced-motion
// at call time (not module-load time, SSR/tests safe) and collapse to an
// instant fade when motion is reduced.

import { fade, fly, scale } from 'svelte/transition';
import { cubicOut } from 'svelte/easing';

/** @returns {boolean} true when the user asked the OS to reduce motion. */
export function reducedMotion() {
  return typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches;
}

/**
 * Per-item stagger delay (ms) for grid entrances; always 0 under reduced
 * motion so lists appear at once.
 * @param {number} i index of the item in the list
 * @param {number} [ms=30] step between items
 */
export function stagger(i, ms = 30) {
  return reducedMotion() ? 0 : i * ms;
}

/** 250ms rise + fade entrance for cards/tiles; pass { delay } to stagger. */
export function enter(node, opts = {}) {
  if (reducedMotion()) return fade(node, { duration: 0 });
  return fly(node, { y: 10, duration: 250, easing: cubicOut, ...opts });
}

/** Soft scale-in for lightbox-style media (pop feel without a jump-cut). */
export function popIn(node, opts = {}) {
  if (reducedMotion()) return fade(node, { duration: 0 });
  return scale(node, { start: 0.94, duration: 180, easing: cubicOut, ...opts });
}

/** Quick fade for overlays/hints that just appear and leave. */
export function soft(node, opts = {}) {
  if (reducedMotion()) return fade(node, { duration: 0 });
  return fade(node, { duration: 160, ...opts });
}
