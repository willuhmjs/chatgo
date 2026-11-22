// Session/auth store (scaffold-owned; FE-shell may extend UI, keep the API).
import * as api from './api';

export const session = $state({
  /** @type {import('./api').User | null} */
  user: null,
  ready: false,
});

export function isAdmin() {
  return session.user?.role === 'admin';
}

/** Fetch /api/auth/me; anonymous on 401. Called once at app start. */
export async function bootstrap() {
  try {
    session.user = await api.me();
  } catch {
    session.user = null;
  } finally {
    session.ready = true;
  }
}

/** @returns {Promise<import('./api').User>} */
export async function login(email, password) {
  session.user = await api.login(email, password);
  return session.user;
}

export async function logout() {
  try {
    await api.logout();
  } finally {
    session.user = null;
  }
}
