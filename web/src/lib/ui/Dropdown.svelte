<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    children,
    items,
    label = 'Menu',
    align = 'end',
    disabled = false,
    buttonClass = '',
  }: {
    /** Trigger content (usually an icon). */
    children: Snippet;
    /** Menu content; receives { close } so items can dismiss the menu. */
    items?: Snippet<[{ close: () => void }]>;
    /** aria-label for the trigger. */
    label?: string;
    align?: 'start' | 'end';
    disabled?: boolean;
    buttonClass?: string;
  } = $props();

  let open = $state(false);
  let root = $state<HTMLDivElement | undefined>();
  let trigger = $state<HTMLButtonElement | undefined>();
  let menuEl = $state<HTMLDivElement | undefined>();
  let pos = $state({ top: 0, bottom: 0, left: 0, right: 0, anchorEnd: false });

  function openMenu() {
    const r = trigger?.getBoundingClientRect();
    if (r)
      pos = { top: r.bottom + 4, bottom: window.innerHeight - r.top + 4, left: r.left, right: r.right, anchorEnd: align === 'end' };
    open = true;
  }

  function close() {
    open = false;
    trigger?.focus();
  }

  $effect(() => {
    if (!open) return;
    const onDown = (e: PointerEvent) => {
      const t = e.target as Node | null;
      if (root && t && !root.contains(t) && !menuEl?.contains(t)) open = false;
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        open = false;
      }
    };
    const dismiss = () => (open = false);
    document.addEventListener('pointerdown', onDown);
    window.addEventListener('keydown', onKey, true);
    window.addEventListener('resize', dismiss);
    window.addEventListener('scroll', dismiss, true);
    menuEl?.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
    return () => {
      document.removeEventListener('pointerdown', onDown);
      window.removeEventListener('keydown', onKey, true);
      window.removeEventListener('resize', dismiss);
      window.removeEventListener('scroll', dismiss, true);
    };
  });

  /** Menu is rendered position:fixed so it escapes scroll-container clipping. */
  function menuStyle(p: typeof pos): string {
    const horiz = p.anchorEnd ? `right:calc(100vw - ${p.right}px);` : `left:${p.left}px;`;
    // flip upward when there's little room below and more room above
    const flip = window.innerHeight - p.top < 240 && p.bottom > window.innerHeight - p.top;
    return (flip ? `bottom:${p.bottom}px;` : `top:${p.top}px;`) + horiz;
  }
</script>

<div bind:this={root} class="relative inline-flex">
  <button
    bind:this={trigger}
    type="button"
    aria-haspopup="menu"
    aria-expanded={open}
    aria-label={label}
    {disabled}
    class={buttonClass ||
      'inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--muted)] hover:bg-[color-mix(in_srgb,var(--text)_10%,transparent)] hover:text-[var(--text)] focus-visible:outline-2 focus-visible:outline-[color-mix(in_srgb,var(--accent)_70%,transparent)]'}
    onclick={() => (open ? close() : openMenu())}
  >
    {@render children()}
  </button>
</div>

{#if open && items}
  <div
    bind:this={menuEl}
    role="menu"
    aria-label={label}
    class="fixed z-[60] min-w-44 overflow-hidden rounded-lg border py-1 shadow-xl"
    style="background:var(--panel); border-color:var(--border); {menuStyle(pos)}"
  >
    {@render items({ close })}
  </div>
{/if}
