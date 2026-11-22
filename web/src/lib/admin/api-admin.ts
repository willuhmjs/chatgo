// Admin API facade — one typed entry point for everything under $lib/admin.
//
// CONTRACT-DEVIATION: the FE-admin brief asked this module to provide
// "providers CRUD/sync, settings, users list/approve/role, tokens" because
// they were assumed missing from api.ts. They are all already there, and
// api.ts is scaffold-owned and the project's single fetch wrapper. Rather
// than duplicate fetch plumbing, this file re-exports them as one admin
// surface and adds only what genuinely doesn't exist elsewhere:
//   * system-settings allowlist + typed get/put (GET/PUT /api/settings)
//   * pending-user queue helper
//   * capability flags for endpoints absent from the frozen contract surface
import * as api from '$lib/api';
import { ApiError } from '$lib/api';

export { ApiError } from '$lib/api';
export {
  authConfig,
  oidcLoginURL,
  // providers (admin)
  listProviders,
  createProvider,
  updateProvider,
  deleteProvider,
  syncProvider,
  listModels,
  // settings
  getSettings,
  setSettings,
  // users (admin)
  listUsers,
  approveUser,
  setUserRole,
  // api tokens
  listTokens,
  createToken,
  deleteToken,
} from '$lib/api';

export type { ApiToken, AuthConfig, ModelEntry, Provider, User } from '$lib/api';
import type { AuthConfig, Provider, User } from '$lib/api';
export type Registration = AuthConfig['registration'];

// ---------- system settings (admin) ----------
//
// CONTRACT-DEVIATION: docs/CONTRACTS.md never literally enumerates the
// /api/settings key allowlist. These four keys mirror the config fields in
// §Config (TaskModel, SearXNGURL, Registration) plus the system-prompt knob
// the brief calls for. Keys are snake_case; unknown keys are never written.

export const SETTINGS_KEYS = ['task_model', 'system_prompt', 'search_url', 'registration'] as const;
export type SettingsKey = (typeof SETTINGS_KEYS)[number];

export interface SystemSettings {
  task_model: string;
  system_prompt: string;
  search_url: string;
  registration: Registration;
}

function isRegistration(v: string | undefined): v is Registration {
  return v === 'open' || v === 'approve' || v === 'closed';
}

// ---------- users helpers ----------

/** Pending approval queue. CONTRACT-DEVIATION: no /api/users/pending route in
 *  the frozen surface, so this filters the admin list client-side. */
export async function listPendingUsers(): Promise<User[]> {
  return (await api.listUsers()).filter((u) => u.role === 'pending');
}

// ---------- provider model management (admin) ----------
//
// api.ts is scaffold-frozen and its req() is private, so these typed calls
// live here (the admin module owns additions). Same envelope as the rest of
// the API: errors are {error}, success is JSON — hence a faithful mirror of
// api.ts's req/errorFrom instead of hand-rolled per-component fetches.

async function adminReq<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    let msg = res.statusText || `HTTP ${res.status}`;
    try {
      const j = (await res.json()) as { error?: string };
      if (j.error) msg = j.error;
    } catch {
      /* body wasn't JSON */
    }
    throw new ApiError(res.status, msg);
  }
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

/** Provider row as returned by GET/POST/PUT /api/providers — the admin list
 *  adds a key-status dot and cached-model tallies on top of api.ts's type. */
export type ProviderAdmin = Provider & {
  has_key?: boolean;
  model_count: number;
  models_enabled: number;
};

/** One cached model row of a provider, INCLUDING admin-disabled ones
 *  (GET /api/models stays the enabled-only chat surface). */
export interface ProviderModel {
  model_id: string;
  display: string;
  context_len: number;
  enabled: boolean;
}

export interface ModelToggle {
  model_id: string;
  enabled: boolean;
}

export const listProvidersAdmin = () => adminReq<ProviderAdmin[]>('GET', '/api/providers');

/** Full cached model list of one provider (admin; incl. disabled rows). */
export const listProviderModels = (id: string) =>
  adminReq<ProviderModel[]>('GET', `/api/providers/${id}/models`);

/** Bulk enable/disable — one call applies the whole batch atomically and
 *  answers the refreshed full list (server truth for optimistic UIs). Any
 *  unknown model_id in the batch → 404 ApiError, nothing applied. */
export const setProviderModels = (id: string, toggles: ModelToggle[]) =>
  adminReq<ProviderModel[]>('PUT', `/api/providers/${id}/models`, toggles);

/** syncProvider with the richer envelope: count = cached rows after sync,
 *  added = models new to the cache (admin enable choices are preserved). */
export interface SyncResult {
  count: number;
  added: number;
}
export const syncProviderAdmin = (id: string) =>
  adminReq<SyncResult>('POST', `/api/providers/${id}/sync`);

// ---------- per-user model prefs (admin) ----------
//
// Admin-side view of the SAME me.<uid>.{default_model,hidden_models} rows the
// user edits in their own Settings — the values are raw stored strings
// (hidden_models is a JSON array literal as text; "" = never written). Same
// validators server-side, and no lock: user and admin overwrite each other
// last-writer-wins, so the UI must present this as a starting point.

export interface UserPrefs {
  default_model: string;
  hidden_models: string;
}

export const getUserModelPrefs = (id: string) =>
  adminReq<UserPrefs>('GET', `/api/users/${id}/model-prefs`);

/** Either key optional. The PUT validates every key before writing any and
 *  answers the resulting prefs (server truth, like setProviderModels). */
export const setUserModelPrefs = (id: string, patch: Partial<UserPrefs>) =>
  adminReq<UserPrefs>('PUT', `/api/users/${id}/model-prefs`, patch);
