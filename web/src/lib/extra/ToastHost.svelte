<script lang="ts">
  // Polished toast host (§7) — renders the scaffold $lib/toast.svelte.js store
  // (kept untouched) as a bottom-CENTER single-pill ephemeral stack: quiet
  // dots per kind instead of heavy chrome, max 3 visible, 150ms fade+rise.
  // MOUNT: replace App.svelte's inline baseline toast stack with <Toasts/>
  // (see docs/FE-EXTRA-UI.md §MOUNTS).
  import { toasts, dismiss } from '$lib/toast.svelte.js';

  const KIND_DOT: Record<string, string> = {
    info: 'var(--acc, var(--muted))',
    success: '#10a37f',
    error: 'var(--danger)',
  };
</script>

<div
  class="pointer-events-none fixed bottom-6 left-1/2 z-[70] flex w-full max-w-[34rem] -translate-x-1/2 flex-col items-center gap-2 px-4"
  role="status"
  aria-live="polite"
>
  {#each toasts.slice(-3) as t (t.id)}
    <div
      class="toast-enter pointer-events-auto flex max-w-full items-center gap-2.5 rounded-full px-4 py-2"
      style="background:var(--panel); color:var(--text); box-shadow:var(--shadow-menu)"
    >
      <span class="h-1.5 w-1.5 shrink-0 rounded-full" style="background:{KIND_DOT[t.kind] ?? 'var(--muted)'}"></span>
      <p class="min-w-0 flex-1 whitespace-pre-wrap break-words text-[13px] leading-snug">{t.message}</p>
      <button
        class="shrink-0 rounded-full p-0.5 text-[var(--muted)] transition-colors hover:text-[var(--text)]"
        aria-label="Dismiss notification"
        onclick={() => dismiss(t.id)}
      >
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" aria-hidden="true">
          <path d="M18 6 6 18M6 6l12 12" />
        </svg>
      </button>
    </div>
  {/each}
</div>

<style>
  /* newest toast sits at the bottom edge; older ones rise above it */
  .toast-enter {
    animation: toast-in 150ms ease-out;
  }
  @keyframes toast-in {
    from {
      opacity: 0;
      transform: translateY(6px);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .toast-enter {
      animation: none;
    }
  }
</style>
