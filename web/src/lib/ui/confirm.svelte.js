// Promise-based confirm dialog store (FE-shell-owned; lives under lib/ui per
// ownership rules). Usage: `if (await confirmDialog({ title, message })) { … }`
// Mount <ConfirmDialog/> once in App.

export const confirmState = $state({
  open: false,
  title: '',
  message: '',
  confirmLabel: 'Confirm',
  danger: false,
  /** @type {((v: boolean) => void) | null} */
  resolve: null,
});

/**
 * @param {{ title: string, message?: string, confirmLabel?: string, danger?: boolean }} opts
 * @returns {Promise<boolean>}
 */
export function confirmDialog(opts) {
  // Auto-resolve a previous dialog (if any) as cancelled.
  settleConfirm(false);
  confirmState.title = opts.title;
  confirmState.message = opts.message ?? '';
  confirmState.confirmLabel = opts.confirmLabel ?? 'Confirm';
  confirmState.danger = opts.danger ?? false;
  confirmState.open = true;
  return new Promise((resolve) => {
    confirmState.resolve = resolve;
  });
}

/** @param {boolean} value */
export function settleConfirm(value) {
  if (!confirmState.open && !confirmState.resolve) return;
  confirmState.open = false;
  const r = confirmState.resolve;
  confirmState.resolve = null;
  r?.(value);
}
