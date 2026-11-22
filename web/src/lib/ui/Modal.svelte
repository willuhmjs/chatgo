<script lang="ts">
  import type { Snippet } from 'svelte';
  import { X } from 'lucide-svelte';
  import IconButton from '$lib/ui/IconButton.svelte';

  let {
    open = $bindable(false),
    title = '',
    width = 'max-w-md',
    onclose,
    children,
    footer,
  }: {
    open?: boolean;
    title?: string;
    /** Tailwind max-w-* class for the panel. */
    width?: string;
    /** Called whenever the modal dismisses itself (esc/backdrop/X). */
    onclose?: () => void;
    children?: Snippet;
    footer?: Snippet;
  } = $props();

  let panel = $state<HTMLDivElement | undefined>();

  function close() {
    open = false;
    onclose?.();
  }

  $effect(() => {
    if (!open) return;
    const prev = document.activeElement as HTMLElement | null;
    document.body.style.overflow = 'hidden';
    panel?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        close();
      }
    };
    // capture so the modal swallows Esc before global shortcuts see it
    window.addEventListener('keydown', onKey, true);
    return () => {
      window.removeEventListener('keydown', onKey, true);
      document.body.style.overflow = '';
      prev?.focus?.();
    };
  });
</script>

{#if open}
  <div class="fixed inset-0 z-50 grid place-items-center p-4" role="presentation">
    <div
      class="absolute inset-0 bg-black/60 backdrop-blur-[2px]"
      role="presentation"
      onclick={close}
    ></div>
    <div
      bind:this={panel}
      role="dialog"
      aria-modal="true"
      aria-label={title || undefined}
      tabindex={-1}
      class="relative w-full {width} rounded-xl border shadow-2xl outline-none"
      style="background:var(--panel); border-color:var(--border)"
    >
      <div class="flex items-center justify-between border-b px-4 py-3" style="border-color:var(--border)">
        <h2 class="text-sm font-semibold">{title}</h2>
        <IconButton label="Close" onclick={close}>
          <X class="size-4" />
        </IconButton>
      </div>
      <div class="px-4 py-4">{@render children?.()}</div>
      {#if footer}
        <div
          class="flex items-center justify-end gap-2 border-t px-4 py-3"
          style="border-color:var(--border)"
        >
          {@render footer()}
        </div>
      {/if}
    </div>
  </div>
{/if}
