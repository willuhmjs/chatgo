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
import type { AuthConfig, User } from '$lib/api';
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

export async function getSystemSettings(): Promise<SystemSettings> {
  const all = await api.getSettings();
  return {
    task_model: all['task_model'] ?? '',
    system_prompt: all['system_prompt'] ?? '',
    search_url: all['search_url'] ?? '',
    registration: isRegistration(all['registration']) ? all['registration'] : 'approve',
  };
}

/** PUT only the allowlisted keys; returns the persisted value for rebasing dirty state. */
export async function putSystemSettings(s: SystemSettings): Promise<SystemSettings> {
  const body: Partial<Record<SettingsKey, string>> = {};
  for (const k of SETTINGS_KEYS) body[k] = s[k];
  await api.setSettings(body as Record<string, string>);
  return s;
}

// ---------- users helpers ----------

/** Pending approval queue. CONTRACT-DEVIATION: no /api/users/pending route in
 *  the frozen surface, so this filters the admin list client-side. */
export async function listPendingUsers(): Promise<User[]> {
  return (await api.listUsers()).filter((u) => u.role === 'pending');
}

// ---------- capability flags ----------

// CONTRACT-DEVIATION: the contract surface has no PATCH/PUT /api/models/…
// endpoint, so display-name rename and per-model enable toggles render
// read-only in the providers tab. Flip this to true (no other change needed)
// once the backend ships model edits.
export const CAN_EDIT_MODELS = false;
