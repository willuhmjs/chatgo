<script lang="ts">
  // Minimal modal primitive (local to FE-chat; see IconButton note).
  // Polish round §7: darker full-bleed backdrop, quiet chrome, 150ms fade.
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
    class="scrim fixed inset-0 z-50 flex items-center justify-center p-4"
    role="presentation"
    onclick={(e) => {
      if (e.target === e.currentTarget) onclose();
    }}
  >
    <div
      class="pop relative max-h-full w-full max-w-fit outline-none"
      role="dialog"
      aria-modal="true"
      aria-labelledby={labelledby}
    >
      <button
        type="button"
        class="close"
        title="Close (Esc)"
        aria-label="Close"
        onclick={onclose}
      >
        <X size={18} />
      </button>
      {@render children()}
    </div>
  </div>
{/if}

<style>
  .scrim {
    background: rgb(0 0 0 / 0.85);
    animation: modal-fade 150ms ease-out;
  }
  .pop {
    animation: modal-pop 150ms ease-out;
  }
  .close {
    position: absolute;
    top: -2.5rem;
    right: 0;
    display: inline-flex;
    padding: 0.35rem;
    border: 0;
    border-radius: 999px;
    background: transparent;
    color: var(--muted, #a8a8a8);
    cursor: pointer;
    transition:
      color 120ms ease,
      background 120ms ease;
  }
  .close:hover {
    color: var(--text);
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
  }
  @keyframes modal-fade {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }
  @keyframes modal-pop {
    from {
      opacity: 0;
      transform: scale(0.98);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .scrim,
    .pop {
      animation: none;
    }
  }
</style>
