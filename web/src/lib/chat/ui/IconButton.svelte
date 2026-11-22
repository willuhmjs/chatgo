<script lang="ts">
  // Minimal icon button (local FE-chat primitive — FE-shell's lib/ui/* lands
  // in parallel, so we ship our own to stay independent).
  import type { Snippet } from 'svelte';

  let {
    title,
    onclick,
    disabled = false,
    active = false,
    danger = false,
    children,
  }: {
    title: string;
    onclick?: (e: MouseEvent) => void;
    disabled?: boolean;
    active?: boolean;
    danger?: boolean;
    children: Snippet;
  } = $props();
</script>

<button
  type="button"
  class="iconbtn"
  class:active
  class:danger
  {title}
  {disabled}
  {onclick}
  aria-label={title}
>
  {@render children()}
</button>

<style>
  .iconbtn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.75rem;
    height: 1.75rem;
    border-radius: 0.5rem;
    border: 1px solid transparent;
    background: transparent;
    color: var(--muted);
    cursor: pointer;
    transition:
      background 120ms ease,
      color 120ms ease,
      border-color 120ms ease;
  }
  .iconbtn:hover:not(:disabled) {
    background: var(--panel-2);
    color: var(--text);
    border-color: var(--border);
  }
  .iconbtn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .iconbtn.active {
    background: color-mix(in oklab, var(--accent) 14%, transparent);
    color: var(--accent);
    border-color: color-mix(in oklab, var(--accent) 35%, transparent);
  }
  .iconbtn.danger:hover:not(:disabled) {
    color: var(--danger);
  }
</style>
