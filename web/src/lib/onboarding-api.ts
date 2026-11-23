// AG-2 (auth-gate + onboarding) — new API surface from docs/AUTHGATE-CONTRACT.md.
// api.ts / api-shell.ts are frozen for this dispatch; anything NOT already typed
// there gets a hand-rolled fetch here (same convention as lib/api-shell.ts).
// Endpoints that already exist in api.ts are wrapped, not re-implemented.

import { register } from './api';
import type { User } from './api';

export interface AuthStatus {
  authenticated: boolean;
  first_run: boolean;
}

/**
 * GET /api/auth/status (public, contract §2): { authenticated, first_run }.
 * Mid-integration fallback for servers without the route (404/any error):
 * probe /api/auth/me instead — 200 ⇒ authenticated with first_run unknown
 * (assume false); 401/anything else ⇒ unauthenticated.
 */
export async function authStatus(): Promise<AuthStatus> {
  try {
    const res = await fetch('/api/auth/status');
    if (res.ok) {
      const j = (await res.json()) as Partial<AuthStatus>;
      return { authenticated: !!j.authenticated, first_run: !!j.first_run };
    }
  } catch {
    /* endpoint missing / network blip — fall through to the /me probe */
  }
  try {
    const me = await fetch('/api/auth/me');
    return { authenticated: me.ok, first_run: false };
  } catch {
    return { authenticated: false, first_run: false };
  }
}

/**
 * First-run admin bootstrap (contract §2): while `first_run_done` is unset the
 * server allows EXACTLY ONE POST /api/auth/register regardless of the
 * registration policy; it returns the same shape as login (User + session
 * cookie) and flips first_run_done. Wraps api.register.
 */
export function registerAdmin(name: string, email: string, password: string): Promise<User> {
  return register(email, password, name);
}
