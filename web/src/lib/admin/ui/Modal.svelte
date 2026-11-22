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
      class="relative w-full {wide ? 'max-w-2xl' : 'max-w-md'} overflow-hidden rounded-2xl border shadow-2xl"
      style="background:var(--panel); border-color:var(--border)"
    >
      <div class="flex items-center justify-between border-b px-5 py-3" style="border-color:var(--border)">
        <h2 class="text-sm font-semibold">{title}</h2>
        <button
          class="rounded-md p-1 text-[var(--muted)] transition-colors hover:bg-[var(--panel-2)] hover:text-[var(--text)]"
          aria-label="Close"
          onclick={() => (open = false)}
        >
          <X size={16} />
        </button>
      </div>
      <div class="max-h-[70vh] overflow-y-auto px-5 py-4">{@render children?.()}</div>
      {#if footer}
        <div class="flex items-center justify-end gap-2 border-t px-5 py-3" style="border-color:var(--border)">
          {@render footer()}
        </div>
      {/if}
    </div>
  </div>
{/if}
