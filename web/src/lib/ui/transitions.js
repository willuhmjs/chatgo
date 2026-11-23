// Shared motion helpers (generic — any page can import; POLISH-KB baseline).
// svelte/transition runs JS-driven animations, so the global CSS kill-switch
// in app.css §9 (prefers-reduced-motion) does NOT reach them. Components get
// the guard for free by spreading these config factories into transition:fly /
// transition:fade / transition:slide directives.

/** True when the OS asks for reduced motion. Safe during SSR (returns false). */
export function reduced() {
  return typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
}

/** Standard ease-out cubic, matches the CSS entrances in app.css. Must be an
 *  easing FUNCTION (svelte/transition calls easing(t)) — a CSS cubic-bezier
 *  string would type-error and crash at runtime. */
export const EASE_OUT = (/** @type {number} */ t) => 1 - Math.pow(1 - t, 3);

/** Stagger step for list entrances (ms) — capped so long lists don't crawl. */
export function stepDelay(i, step = 45, cap = 10) {
  return reduced() ? 0 : Math.min(i, cap) * step;
}

/** fly() config: rise `y` px into place. Usage: transition:fly={rise(8, 250)} */
export function rise(y = 8, duration = 250, delay = 0) {
  return reduced() ? { y: 0, duration: 0, delay: 0 } : { y, duration, delay, easing: EASE_OUT };
}

/** fly() config: slide in from the right (slide-overs, drawers). */
export function slideIn(x = 28, duration = 260, delay = 0) {
  return reduced() ? { x: 0, duration: 0, delay: 0 } : { x, duration, delay, easing: EASE_OUT };
}

/** fade()/slide() config: just duration/delay, instant when reduced. */
export function quick(duration = 160, delay = 0) {
  return reduced() ? { duration: 0, delay: 0 } : { duration, delay };
}
