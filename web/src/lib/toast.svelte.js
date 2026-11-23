// Toast store (scaffold-owned baseline; FE-extra extends, keep the API).

export const toasts = $state(
  /** @type {{ id: number, message: string, kind: 'info'|'success'|'error' }[]} */ ([]),
);

let seq = 0;

/**
 * Show a toast; auto-dismisses after `ms` (0 = sticky). Returns its id.
 * @param {string} message
 * @param {'info'|'success'|'error'} [kind]
 */
export function push(message, kind = 'info', ms = 4000) {
  const id = ++seq;
  toasts.push({ id, message, kind });
  if (ms > 0) setTimeout(() => dismiss(id), ms);
  return id;
}

export function dismiss(id) {
  const i = toasts.findIndex((t) => t.id === id);
  if (i >= 0) toasts.splice(i, 1);
}
