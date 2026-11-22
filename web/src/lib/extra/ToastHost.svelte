<script lang="ts">
  // Polished toast host — renders the scaffold $lib/toast.svelte.js store
  // (kept untouched) with kind icons, slide-in stacking and a dismiss X.
  // MOUNT: replace App.svelte's inline baseline toast stack with <Toasts/>
  // (see docs/FE-EXTRA-UI.md §MOUNTS).
  import { toasts, dismiss } from '$lib/toast.svelte.js';
  import { CircleCheck, CircleX, Info, X } from 'lucide-svelte';

  const KIND_COLOR: Record<string, string> = {
    info: 'var(--accent)',
    success: '#10b981',
    error: 'var(--danger)',
  };
</script>

<div
  class="pointer-events-none fixed bottom-4 right-4 z-[70] flex w-80 flex-col-reverse gap-2"
  role="status"
  aria-live="polite"
>
  {#each toasts as t (t.id)}
    <div
      class="toast-enter pointer-events-auto flex items-start gap-2.5 rounded-lg border py-2.5 pl-3 pr-2 shadow-lg backdrop-blur"
      style="background:var(--panel); border-color:var(--border); border-left:3px solid {KIND_COLOR[t.kind] ??
        'var(--accent)'}; color:var(--text)"
    >
      <span class="mt-0.5 shrink-0" style="color:{KIND_COLOR[t.kind] ?? 'var(--accent)'}">
        {#if t.kind === 'success'}
          <CircleCheck size={16} />
        {:else if t.kind === 'error'}
          <CircleX size={16} />
        {:else}
          <Info size={16} />
        {/if}
      </span>
      <p class="min-w-0 flex-1 whitespace-pre-wrap break-words text-sm leading-snug">{t.message}</p>
      <button
        class="shrink-0 rounded-md p-1 opacity-50 transition-opacity hover:opacity-100"
        aria-label="Dismiss notification"
        onclick={() => dismiss(t.id)}
      >
        <X size={14} />
      </button>
    </div>
  {/each}
</div>

<style>
  /* newest toast is always at the bottom edge; older ones push up = stack */
  .toast-enter {
    animation: toast-in 180ms ease-out;
  }
  @keyframes toast-in {
    from {
      opacity: 0;
      transform: translateY(8px) scale(0.97);
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
