<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    onclick,
    close,
    danger = false,
    selected = false,
    icon,
    children,
  }: {
    onclick?: () => void;
    /** close() passed from Dropdown's items snippet. */
    close?: () => void;
    danger?: boolean;
    selected?: boolean;
    icon?: Snippet;
    children: Snippet;
  } = $props();
</script>

<button
  role="menuitem"
  type="button"
  aria-checked={selected || undefined}
  class="menu-item {danger ? 'menu-danger' : ''}"
  onclick={() => {
    onclick?.();
    close?.();
  }}
>
  {#if icon}
    <span class="flex size-4 shrink-0 items-center justify-center opacity-80">{@render icon()}</span>
  {/if}
  <span class="min-w-0 flex-1 truncate text-left">{@render children()}</span>
  {#if selected}
    <span style="color:var(--acc, var(--indigo))">✓</span>
  {/if}
</button>

<style>
  /* §7: h-9 items, rounded-lg, quiet hover fill; danger in --danger */
  .menu-item {
    display: flex;
    width: 100%;
    height: 2.25rem;
    align-items: center;
    gap: 0.5rem;
    padding: 0 0.625rem;
    border-radius: 0.5rem;
    font-size: 0.875rem;
    color: var(--text);
    background: transparent;
    border: 0;
    cursor: pointer;
    text-align: left;
    transition: background-color 0.1s ease;
  }
  .menu-item:hover {
    background: var(--fill-hover);
  }
  .menu-item:focus-visible {
    outline: none;
    background: var(--accent-soft);
  }
  .menu-danger {
    color: var(--danger);
  }
  .menu-danger:hover {
    background: color-mix(in srgb, var(--danger) 12%, transparent);
  }
</style>
