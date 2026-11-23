// Global keyboard shortcut registry (scaffold-owned).
// Combos: 'mod+k', 'mod+n', 'mod+enter', 'escape', 'slash' …
// ⌘/Ctrl combos always fire; bare keys skip when typing in inputs.

/** @type {Map<string, Set<Function>>} */
const handlers = new Map();

function comboOf(e) {
  const mod = e.metaKey || e.ctrlKey;
  const parts = [];
  if (mod) parts.push('mod');
  if (e.shiftKey) parts.push('shift');
  if (e.altKey) parts.push('alt');
  let key = e.key.toLowerCase();
  if (key === 'escape') key = 'escape';
  parts.push(key);
  return parts.join('+');
}

function isTyping(target) {
  if (!target || !(target instanceof HTMLElement)) return false;
  const tag = target.tagName;
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || target.isContentEditable;
}

/**
 * Register a handler. Returns an unregister fn.
 * @param {string} combo e.g. 'mod+k'
 * @param {(e: KeyboardEvent) => void} fn
 */
export function onShortcut(combo, fn) {
  const key = combo.toLowerCase();
  if (!handlers.has(key)) handlers.set(key, new Set());
  handlers.get(key).add(fn);
  return () => handlers.get(key)?.delete(fn);
}

function onKeyDown(e) {
  const combo = comboOf(e);
  const set = handlers.get(combo);
  if (!set || set.size === 0) return;
  const bare = !combo.includes('+');
  if (bare && (isTyping(e.target) || e.metaKey || e.ctrlKey || e.altKey)) return;
  // escape must work while typing (drawers/composables) — allow anyway:
  if (combo === 'escape') {
    for (const fn of [...set]) fn(e);
    return;
  }
  e.preventDefault();
  for (const fn of [...set]) fn(e);
}

let installed = false;

/** Install the global listener (idempotent). Call once from App. */
export function initShortcuts() {
  if (installed || typeof window === 'undefined') return;
  window.addEventListener('keydown', onKeyDown);
  installed = true;
}
