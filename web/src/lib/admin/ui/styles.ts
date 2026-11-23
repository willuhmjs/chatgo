// Shared class strings + inline styles so every admin surface looks the same
// while $lib/ui (FE-shell) is still being built. Swap-import-able once that
// kit lands (same names, same intent).
//
// Design-polish tokens (docs/DESIGN-POLISH.md §1): quiet monochrome — fills
// over borders, white primary with near-black text, 16px cards, 10px rows'
// hover fill.

export const btn =
  'inline-flex items-center justify-center gap-1.5 rounded-[10px] px-3 py-1.5 text-sm font-medium transition-[background,color,opacity] hover:bg-[var(--fill-hover)] hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--accent-soft)]';

export const btnPrimaryStyle = 'background:var(--accent); color:var(--accent-fg)';
export const btnGhostStyle = 'background:transparent; color:var(--text); border:1px solid var(--border)';
export const btnDangerStyle = 'background:color-mix(in srgb, var(--danger) 12%, transparent); color:var(--danger)';

export const input =
  'w-full rounded-[10px] border px-3 py-2 text-sm outline-none transition-[border-color,box-shadow] placeholder:text-[var(--faint)] focus:border-[rgba(255,255,255,0.22)] focus:shadow-[0_0_0_3px_var(--accent-soft)]';
export const inputStyle = 'background:var(--panel-2); border-color:var(--border); color:var(--text)';

export const mono = 'font-mono text-xs';
/** Table headers — 12px muted (§5). */
export const th = 'px-3 py-1.5 text-left text-xs font-medium text-[var(--muted)]';
export const td = 'px-3 py-2 align-middle';
/** tbody rows: 40px tall, quiet hover fill (§5). */
export const row = 'h-10 transition-colors hover:bg-[var(--fill-hover)]';
export const rowBorder = 'border-top:1px solid var(--border)';
export const panel = 'rounded-2xl border';
export const panelStyle = 'background:var(--panel); border-color:var(--border)';
export const panel2Style = 'background:var(--panel-2); border-color:var(--border)';
/** Quiet red-tinted inline error banner (§1/§7). */
export const errorBannerStyle =
  'background:color-mix(in srgb, var(--danger) 9%, transparent); color:var(--danger)';
