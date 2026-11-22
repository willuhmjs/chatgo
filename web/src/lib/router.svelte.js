// pushState router (scaffold-owned, Svelte 5 runes).
// Route table per docs/CONTRACTS.md §Frontend routes.

const ROUTES = [
  { name: 'home', pattern: '/' },
  { name: 'chat', pattern: '/c/:id' },
  { name: 'compare', pattern: '/compare' },
  { name: 'gallery', pattern: '/gallery' },
  { name: 'settings', pattern: '/settings' },
  { name: 'admin', pattern: '/admin' },
  { name: 'login', pattern: '/login' },
  { name: 'register', pattern: '/register' },
  { name: 'welcome', pattern: '/welcome' },
  { name: 'onboarding', pattern: '/onboarding' },
  { name: 'share', pattern: '/s/:token' },
];

/** Reactive location; `path` is the current pathname (no query/hash). */
export const location = $state({ path: typeof window === 'undefined' ? '/' : window.location.pathname });

/**
 * @typedef {{ name: string, path: string, params: Record<string,string> }} Route
 */

/**
 * @param {string} pattern
 * @param {string} path
 * @returns {Record<string,string> | null}
 */
function matchOne(pattern, path) {
  const pp = pattern.split('/').filter(Boolean);
  const sp = path.split('/').filter(Boolean);
  if (pp.length !== sp.length) return null;
  const params = {};
  for (let i = 0; i < pp.length; i++) {
    if (pp[i].startsWith(':')) params[pp[i].slice(1)] = decodeURIComponent(sp[i]);
    else if (pp[i] !== sp[i]) return null;
  }
  return params;
}

/** @returns {Route} */
function matchPath(path) {
  for (const r of ROUTES) {
    const params = matchOne(r.pattern, path);
    if (params) return { name: r.name, path, params };
  }
  return { name: 'notfound', path, params: {} };
}

/**
 * Current route `{ name, path, params }`. Call from a component (or wrap in
 * `$derived`) — Svelte 5 forbids exporting derived state from modules.
 * @returns {Route}
 */
export function currentRoute() {
  return matchPath(location.path);
}

/** navigate('/c/abc') — pushes history and updates the route store. */
export function navigate(to, { replace = false } = {}) {
  const [path] = to.split(/[?#]/);
  if (path === location.path && !replace) return;
  if (replace) history.replaceState({}, '', to);
  else history.pushState({}, '', to);
  location.path = path;
}

/** Wire popstate + initial sync. Returns a cleanup fn (use inside $effect). */
export function startRouter() {
  const sync = () => {
    location.path = window.location.pathname;
  };
  sync();
  window.addEventListener('popstate', sync);
  return () => window.removeEventListener('popstate', sync);
}
