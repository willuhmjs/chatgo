// Session/auth store (scaffold-owned; FE-shell may extend UI, keep the API).
// AG-2 (docs/AUTHGATE-CONTRACT.md): adds first-run + auth-gate state and the
// client-side redirects that complement the server's 302 shell guard.
// Pending users stay authenticated (session.user set, role 'pending') so the
// sidebar pending-banner UX keeps working untouched.
import * as api from './api';
import { authStatus } from './onboarding-api';
import { location, navigate } from './router.svelte.js';

export const session = $state({
  /** @type {import('./api').User | null} */
  user: null,
  /** bootstrap()/refresh() finished at least once. */
  ready: false,
  /** /api/auth.status.authenticated (mirrors `user !== null` post-sync). */
  authenticated: false,
  /** /api/auth.status.first_run — no admin exists yet. */
  firstRun: false,
});

export function isAdmin() {
  return session.user?.role === 'admin';
}

/** Anonymous visitors may browse these without being bounced to /login. */
function isPublicPath(/** @type {string} */ path) {
  return path === '/login' || path === '/register' || path.startsWith('/s/');
}

/**
 * Client-side redirect rules (contract §FE, complements the server 302):
 *  - first run (anonymous OR authenticated) ⇒ /welcome
 *  - anonymous everywhere else              ⇒ /login (register stays reachable)
 *  - signed in while on /welcome or /login  ⇒ home
 * @returns {void}
 */
function applyAuthRedirects() {
  if (!session.ready) return;
  const path = location.path;
  if (path.startsWith('/s/')) return; // public read-only share pages
  if (!session.authenticated) {
    if (session.firstRun) {
      if (path !== '/welcome') navigate('/welcome', { replace: true });
    } else if (!isPublicPath(path)) {
      navigate('/login', { replace: true });
    }
    return;
  }
  if (session.firstRun) {
    if (path !== '/welcome') navigate('/welcome', { replace: true });
    return;
  }
  if (path === '/welcome' || path === '/login') navigate('/', { replace: true });
}

/** Pull {authenticated, first_run} + the current user into the store. */
async function sync() {
  const status = await authStatus();
  session.authenticated = status.authenticated;
  session.firstRun = status.first_run;
  if (status.authenticated) {
    try {
      session.user = await api.me();
    } catch {
      session.user = null;
      session.authenticated = false;
    }
  } else {
    session.user = null;
  }
  session.ready = true;
}

/**
 * Fetch /api/auth/status + /api/auth/me (anonymous on 401), then apply the
 * auth-gate redirects. Called once at app start.
 */
export async function bootstrap() {
  await sync();
  applyAuthRedirects();
}

/** Re-pull status + user (after first-run registration, OIDC, etc.). */
export async function refresh() {
  await sync();
  applyAuthRedirects();
}

/** @returns {Promise<import('./api').User>} */
export async function login(/** @type {string} */ email, /** @type {string} */ password) {
  session.user = await api.login(email, password);
  session.authenticated = true;
  return session.user;
}

export async function logout() {
  try {
    await api.logout();
  } finally {
    session.user = null;
    session.authenticated = false;
  }
}

/**
 * Guard helper for route components/integrators: true when a signed-in user
 * may stay; on false the visitor is being redirected out (first run ⇒
 * /welcome, else /login).
 * @returns {boolean}
 */
export function requireAuth() {
  if (!session.ready) return false;
  if (session.authenticated) return true;
  applyAuthRedirects();
  return false;
}

/**
 * Loop-proofing for the /onboarding nudge: once the wizard is finished (or
 * skipped) we stop nudging for the rest of the page load, even if persisting
 * setup_done failed server-side — otherwise App.svelte's nudge $effect would
 * bounce the admin straight back into the wizard forever. A reload clears it.
 */
let onboardDismissed = false;

/** Call from the wizard's finish() before navigating away. */
export function dismissOnboard() {
  onboardDismissed = true;
}

/** True once dismissOnboard() ran; callers should skip the nudge entirely. */
export function isOnboardDismissed() {
  return onboardDismissed;
}

/**
 * Admin landing nudge (contract §3): a fresh admin who has not finished the
 * setup wizard should be routed to /onboarding. Read-only helper — the
 * integrator (App/Home) owns the navigate() call.
 * @param {import('./api').User | null} user
 * @param {Record<string, unknown> | null | undefined} meSettings - GET /api/me/settings
 * @param {{ authenticated?: boolean, first_run?: boolean } | null | undefined} status - authStatus()
 * @returns {boolean}
 */
export function shouldOnboard(
  /** @type {import('./api').User | null} */ user,
  /** @type {Record<string, unknown> | null | undefined} */ meSettings,
  /** @type {{ authenticated?: boolean, first_run?: boolean } | null | undefined} */ status,
) {
  return (
    !onboardDismissed &&
    !!user &&
    user.role === 'admin' &&
    String(meSettings?.setup_done ?? '') !== 'true' &&
    status?.first_run === false
  );
}
