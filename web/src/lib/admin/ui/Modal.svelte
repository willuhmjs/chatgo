<script lang="ts">
  import type { Snippet } from 'svelte';
  import { X } from 'lucide-svelte';

  let {
    open = $bindable(false),
    title = '',
    children,
    footer,
    wide = false,
  }: {
    open?: boolean;
    title?: string;
    children?: Snippet;
    footer?: Snippet;
    wide?: boolean;
  } = $props();

  $effect(() => {
    if (!open) return;
    const h = (e: KeyboardEvent) => {
      if (e.key === 'Escape') open = false;
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  });
</script>

{#if open}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
    <button
      class="absolute inset-0 cursor-default bg-black/60"
      aria-label="Dismiss"
      tabindex="-1"
      onclick={() => (open = false)}
    ></button>
    <div
      role="dialog"
      aria-modal="true"
      class="pop relative w-full {wide ? 'max-w-2xl' : 'max-w-md'} overflow-hidden rounded-2xl"
      style="background:var(--panel); color:var(--text); box-shadow:var(--shadow-menu)"
    >
      <div class="flex items-center justify-between px-5 pb-1 pt-4">
        <h2 class="text-sm font-semibold">{title}</h2>
        <button
          class="rounded-lg p-1 text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)]"
          aria-label="Close"
          onclick={() => (open = false)}
        >
          <X size={16} />
        </button>
      </div>
      <div class="max-h-[70vh] overflow-y-auto px-5 py-3">{@render children?.()}</div>
      {#if footer}
        <div class="flex items-center justify-end gap-2 border-t px-5 py-3" style="border-color:var(--border)">
          {@render footer()}
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  /* §7: 120–160ms fade + rise on floating layers */
  .pop {
    animation: pop-in 150ms ease-out;
  }
  @keyframes pop-in {
    from {
      opacity: 0;
      transform: translateY(2px);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .pop {
      animation: none;
    }
  }
</style>
