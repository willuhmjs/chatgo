// Shared class strings + inline styles so every admin surface looks the same
// while $lib/ui (FE-shell) is still being built. Swap-import-able once that
// kit lands (same names, same intent).

export const btn =
  'inline-flex items-center justify-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50';

export const btnPrimaryStyle = 'background:var(--accent); color:var(--accent-fg)';
export const btnGhostStyle = 'background:transparent; color:var(--text); border:1px solid var(--border)';
export const btnDangerStyle = 'background:var(--danger); color:#fff';

export const input =
  'w-full rounded-lg border px-3 py-2 text-sm outline-none transition-colors focus:border-[var(--accent)]';
export const inputStyle = 'background:var(--panel-2); border-color:var(--border); color:var(--text)';

export const mono = 'font-mono text-xs';
export const th = 'px-3 py-2 text-left font-medium';
export const td = 'px-3 py-2 align-middle';
export const rowBorder = 'border-top:1px solid var(--border)';
export const panel = 'rounded-xl border';
export const panelStyle = 'background:var(--panel); border-color:var(--border)';
export const panel2Style = 'background:var(--panel-2); border-color:var(--border)';
