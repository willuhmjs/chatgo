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
    <span class="text-[var(--accent)]">✓</span>
  {/if}
</button>

<style>
  .menu-item {
    display: flex;
    width: 100%;
    align-items: center;
    gap: 0.5rem;
    padding: 0.4rem 0.75rem;
    font-size: 0.8125rem;
    color: var(--text);
    background: transparent;
    border: 0;
    cursor: pointer;
    text-align: left;
  }
  .menu-item:hover {
    background: color-mix(in srgb, var(--text) 8%, transparent);
  }
  .menu-item:focus-visible {
    outline: none;
    background: color-mix(in srgb, var(--accent) 16%, transparent);
  }
  .menu-danger {
    color: var(--danger);
  }
  .menu-danger:hover {
    background: color-mix(in srgb, var(--danger) 12%, transparent);
  }
</style>
