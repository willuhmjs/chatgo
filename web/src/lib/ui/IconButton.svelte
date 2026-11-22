<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    label,
    variant = 'ghost',
    type = 'button',
    disabled = false,
    active = false,
    onclick,
    children,
    class: cls = '',
  }: {
    /** aria-label (required — icon-only buttons must be labelled). */
    label: string;
    variant?: 'ghost' | 'outline' | 'danger';
    type?: 'button' | 'submit';
    disabled?: boolean;
    active?: boolean;
    onclick?: (e: MouseEvent) => void;
    children: Snippet;
    class?: string;
  } = $props();
</script>

<button
  {type}
  {disabled}
  {onclick}
  aria-label={label}
  aria-pressed={active || undefined}
  class="icon-btn icon-btn-{variant} {active ? 'active' : ''} {cls}"
>
  {@render children()}
</button>

<style>
  .icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 2rem;
    height: 2rem;
    border-radius: 0.625rem;
    border: 1px solid transparent;
    color: var(--muted);
    background: transparent;
    cursor: pointer;
    flex-shrink: 0;
    transition:
      background-color 0.12s ease,
      color 0.12s ease,
      border-color 0.12s ease;
  }
  .icon-btn:hover:not(:disabled) {
    color: var(--text);
    background: var(--fill-hover);
  }
  .icon-btn:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--indigo) 60%, transparent);
    outline-offset: 1px;
    color: var(--text);
  }
  .icon-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .icon-btn.active {
    color: var(--text);
    background: var(--accent-soft);
  }
  .icon-btn-outline {
    border-color: var(--border);
  }
  .icon-btn-danger:hover:not(:disabled) {
    color: var(--danger);
    background: color-mix(in srgb, var(--danger) 12%, transparent);
  }
  /* 44px touch targets on coarse pointers */
  @media (pointer: coarse) {
    .icon-btn {
      width: 2.75rem;
      height: 2.75rem;
    }
  }
</style>
