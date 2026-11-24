// Instance brand name (Open WebUI-style whitelabel): the admin-set settings
// "title" surfaced on the public /api/auth/config payload. Falls back to the
// built-in "ChatGo" whenever unset, the fetch fails, or before boot resolves.
// One cached fetch per tab; SystemTab dispatches 'chatgo:brand-changed' after
// saving the title so an admin sees the rename without a reload.
import { authConfig } from './api';

export const DEFAULT_BRAND = 'ChatGo';

export const brand = $state({
  /** @type {string} */ title: '',
  loaded: false,
});

/** Display name: admin title when set, else the default wordmark. */
export function brandName() {
  return brand.title || DEFAULT_BRAND;
}

let started = false;

/** Idempotent; safe pre-login (the config endpoint is public). */
export function loadBrand(force = false) {
  if (started && !force) return;
  started = true;
  authConfig()
    .then((cfg) => {
      brand.title = String(cfg?.title ?? '').trim();
      brand.loaded = true;
    })
    .catch(() => {
      started = false; // older server / offline — retry on the next call
    });
}

if (typeof window !== 'undefined') {
  window.addEventListener('chatgo:brand-changed', () => loadBrand(true));
}
