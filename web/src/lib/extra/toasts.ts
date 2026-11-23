// Thin typed shorthands over the scaffold toast store (toast.svelte.js keeps
// its API untouched; stacking + timeout already live there — ToastHost.svelte
// adds the visual polish).

import { push } from '$lib/toast.svelte.js';

export { push as toast };

/** Green confirmation, auto-dismiss. */
export const ok = (message: string) => push(message, 'success');

/** Red failure, lingers a bit longer. */
export const fail = (message: string) => push(message, 'error', 6000);

/** Sticky info toast (no timeout; dismiss manually). */
export const sticky = (message: string, kind: 'info' | 'success' | 'error' = 'info') =>
  push(message, kind, 0);
