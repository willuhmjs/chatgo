<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    variant = 'primary',
    size = 'md',
    type = 'button',
    disabled = false,
    full = false,
    onclick,
    children,
    class: cls = '',
  }: {
    variant?: 'primary' | 'gradient' | 'secondary' | 'ghost' | 'outline' | 'danger';
    size?: 'sm' | 'md' | 'lg';
    type?: 'button' | 'submit';
    disabled?: boolean;
    full?: boolean;
    onclick?: (e: MouseEvent) => void;
    children?: Snippet;
    class?: string;
  } = $props();
</script>

<button
  {type}
  {disabled}
  {onclick}
  class="btn btn-{variant} btn-{size} {full ? 'w-full' : ''} {cls}"
>
  {@render children?.()}
</button>

<style>
  /* §1/§5: primary = solid white (--accent) with --accent-fg label. Quiet ghosts
     elsewhere; pill radius like ChatGPT buttons. */
  .btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    border-radius: 999px;
    border: 1px solid transparent;
    font-weight: 500;
    line-height: 1;
    cursor: pointer;
    user-select: none;
    white-space: nowrap;
    transition:
      background-color 0.12s ease,
      border-color 0.12s ease,
      filter 0.12s ease,
      opacity 0.12s ease;
  }
  .btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .btn:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--acc) 60%, transparent);
    outline-offset: 2px;
  }
  @media (pointer: coarse) {
    .btn {
      min-height: 44px;
    }
  }

  .btn-sm {
    height: 2rem;
    padding: 0 0.875rem;
    font-size: 0.8125rem;
  }
  .btn-md {
    height: 2.25rem;
    padding: 0 1rem;
    font-size: 0.8125rem;
  }
  .btn-lg {
    height: 2.75rem;
    padding: 0 1.25rem;
    font-size: 0.875rem;
  }

  .btn-primary {
    background: var(--accent);
    color: var(--accent-fg);
  }
  .btn-primary:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent), var(--muted) 14%);
  }
  /* gradient variant retired (§3 "no big gradient button") → same solid primary */
  .btn-gradient {
    background: var(--accent);
    color: var(--accent-fg);
  }
  .btn-gradient:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent), var(--muted) 14%);
  }
  .btn-secondary {
    background: var(--panel-2);
    color: var(--text);
  }
  .btn-secondary:hover:not(:disabled) {
    background: color-mix(in srgb, var(--panel-2), var(--text) 10%);
  }
  .btn-ghost {
    background: transparent;
    color: var(--text);
  }
  .btn-ghost:hover:not(:disabled) {
    background: var(--fill-hover);
  }
  .btn-outline {
    background: transparent;
    color: var(--text);
    border-color: var(--border);
  }
  .btn-outline:hover:not(:disabled) {
    background: var(--fill-hover);
  }
  .btn-danger {
    background: var(--danger);
    color: #fff;
  }
  .btn-danger:hover:not(:disabled) {
    filter: brightness(1.1);
  }
</style>
