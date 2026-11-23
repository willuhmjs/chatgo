<script lang="ts">
  // Right-edge slide-over panel (generic: detail / preview / search surfaces).
  // ChatGPT drawer pattern: quiet backdrop + flat --bg panel sliding in from
  // the right; Esc / backdrop / ✕ dismiss. Content via `title`/`subtitle` +
  // children snippet. Reduced-motion-safe via $lib/ui/transitions.js.
  import type { Snippet } from 'svelte';
  import { fade, fly } from 'svelte/transition';
  import { X } from 'lucide-svelte';
  import { quick, slideIn } from './transitions.js';
  import IconButton from './IconButton.svelte';

  let {
    open = $bindable(false),
    title = '',
    subtitle = '',
    width = '440px',
    onclose,
    children,
  }: {
    open?: boolean;
    title?: string;
    /** Quiet meta line under the title. */
    subtitle?: string;
    /** CSS width; clamped to the viewport on narrow screens. */
    width?: string;
    /** Called whenever the panel dismisses itself (esc/backdrop/✕). */
    onclose?: () => void;
    children?: Snippet;
  } = $props();

  function close() {
    open = false;
    onclose?.();
  }

  $effect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return;
      e.stopPropagation();
      close();
    };
    document.body.style.overflow = 'hidden';
    // capture so the panel swallows Esc before global shortcuts see it
    window.addEventListener('keydown', onKey, true);
    return () => {
      document.body.style.overflow = '';
      window.removeEventListener('keydown', onKey, true);
    };
  });
</script>

{#if open}
  <div class="fixed inset-0 z-50">
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <div class="absolute inset-0 bg-black/40" role="presentation" transition:fade={quick(150)} onclick={close}></div>
    <div
      class="absolute inset-y-0 right-0 flex flex-col border-l"
      style="width:min(100%, {width}); background:var(--bg); border-color:var(--border); box-shadow:-24px 0 48px rgb(0 0 0 / 0.18)"
      role="dialog"
      aria-modal="true"
      aria-label={title || undefined}
      transition:fly={slideIn(28, 260)}
    >
      <div class="flex shrink-0 items-start gap-3 border-b px-4 py-3.5" style="border-color:var(--border)">
        <span class="min-w-0 flex-1">
          {#if title}
            <span class="block truncate text-[15px] font-medium text-[var(--text)]">{title}</span>
          {/if}
          {#if subtitle}
            <span class="mt-0.5 block truncate text-xs text-[var(--muted)]">{subtitle}</span>
          {/if}
        </span>
        <IconButton label="Close panel" onclick={close}>
          <X class="size-4" />
        </IconButton>
      </div>
      <div class="min-h-0 flex-1 overflow-y-auto">
        {@render children?.()}
      </div>
    </div>
  </div>
{/if}
