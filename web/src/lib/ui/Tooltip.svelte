<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    text,
    side = 'top',
    children,
  }: {
    text: string;
    side?: 'top' | 'right';
    children: Snippet;
  } = $props();
</script>

<span class="tt" data-side={side}>
  <span class="tt-anchor">{@render children()}</span>
  <span class="tt-bubble" role="tooltip">{text}</span>
</span>

<style>
  .tt {
    position: relative;
    display: inline-flex;
  }
  .tt-anchor {
    display: inline-flex;
  }
  .tt-bubble {
    position: absolute;
    z-index: 70;
    pointer-events: none;
    opacity: 0;
    white-space: nowrap;
    font-size: 0.75rem;
    padding: 0.25rem 0.625rem;
    border-radius: 0.5rem;
    background: var(--panel-2);
    color: var(--text);
    box-shadow: var(--shadow-menu);
    transition: opacity 0.12s ease 0.15s;
  }
  .tt:hover .tt-bubble,
  .tt:focus-within .tt-bubble {
    opacity: 1;
  }
  .tt[data-side='top'] .tt-bubble {
    bottom: calc(100% + 6px);
    left: 50%;
    transform: translateX(-50%);
  }
  .tt[data-side='right'] .tt-bubble {
    left: calc(100% + 8px);
    top: 50%;
    transform: translateY(-50%);
  }
</style>
