<script lang="ts">
  // Minimal modal primitive (local to FE-chat; see IconButton note).
  import type { Snippet } from 'svelte';
  import X from 'lucide-svelte/icons/x';

  let {
    open,
    onclose,
    children,
    labelledby = 'modal-title',
  }: {
    open: boolean;
    onclose: () => void;
    children: Snippet;
    labelledby?: string;
  } = $props();

  $effect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        onclose();
      }
    };
    document.addEventListener('keydown', onKey, true);
    return () => document.removeEventListener('keydown', onKey, true);
  });
</script>

{#if open}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4 backdrop-blur-[2px]"
    role="presentation"
    onclick={(e) => {
      if (e.target === e.currentTarget) onclose();
    }}
  >
    <div
      class="relative max-h-full w-full max-w-fit outline-none"
      role="dialog"
      aria-modal="true"
      aria-labelledby={labelledby}
    >
      <button
        type="button"
        class="absolute -top-9 right-0 rounded-lg p-1.5"
        style="color:var(--muted); background:var(--panel); border:1px solid var(--border)"
        title="Close (Esc)"
        aria-label="Close"
        onclick={onclose}
      >
        <X size={16} />
      </button>
      {@render children()}
    </div>
  </div>
{/if}
