<script lang="ts">
  // Minimal ghost icon button (local FE-chat primitive — FE-shell's lib/ui/*
  // lands in parallel, so we ship our own to stay independent).
  // Spec §5/§6 (docs/DESIGN-POLISH.md): quiet --muted icon, circular fill on
  // hover (--fill-hover), no borders; active = full-strength text.
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
    border-radius: 999px;
    border: 0;
    background: transparent;
    color: var(--muted, #a8a8a8);
    cursor: pointer;
    transition:
      background 120ms ease,
      color 120ms ease;
  }
  .iconbtn:hover:not(:disabled) {
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
    color: var(--text);
  }
  .iconbtn:disabled {
    opacity: 0.35;
    cursor: not-allowed;
  }
  .iconbtn.active {
    color: var(--acc, var(--text));
  }
  .iconbtn.danger:hover:not(:disabled) {
    color: var(--danger);
  }
</style>
