<script lang="ts">
  // Slash-prompt popover shared by Composer and Home's pill composer.
  // Menu chrome mirrors ModelPicker's .pop (--panel r14 shadow-menu, opens
  // upward, 90ms scale pop). Keyboard nav lives in the parent (it owns the
  // textarea); rows hover-track the parent's `active` index and never steal
  // focus (mousedown is preventDefault'd).
  import type { PromptEntry } from '$lib/chat-features-api';
  import { promptPreview } from '$lib/chat-features-api';

  let {
    items,
    active = 0,
    loading = false,
    onpick,
    onhover,
  }: {
    items: PromptEntry[];
    active?: number;
    loading?: boolean;
    onpick: (p: PromptEntry) => void;
    onhover: (i: number) => void;
  } = $props();
</script>

<div class="pop" role="listbox" aria-label="Prompts">
  <div class="max-h-64 overflow-y-auto px-1 py-1.5">
    {#if loading && !items.length}
      <p class="px-3 py-3 text-xs" style="color:var(--muted)">Loading prompts…</p>
    {:else if !items.length}
      <p class="px-3 py-3 text-xs" style="color:var(--muted)">No matching prompts.</p>
    {:else}
      {#each items as p, i (p.id)}
        <button
          type="button"
          class="row"
          class:sel={i === active}
          role="option"
          aria-selected={i === active}
          onmousedown={(e) => e.preventDefault()}
          onclick={() => onpick(p)}
          onmouseenter={() => onhover(i)}
        >
          <span class="min-w-0">
            <span class="name">{p.title || 'Untitled'}</span>
            {#if promptPreview(p) && promptPreview(p) !== p.title}
              <span class="meta">{promptPreview(p)}</span>
            {/if}
          </span>
        </button>
      {/each}
    {/if}
  </div>
</div>

<style>
  .pop {
    position: absolute;
    bottom: calc(100% + 8px);
    left: 0;
    z-index: 30;
    width: min(22rem, 92vw);
    border-radius: 0.875rem;
    border: 0;
    background: var(--panel, #2f2f2f);
    box-shadow: var(--shadow-menu, 0 8px 28px rgb(0 0 0 / 0.45));
    overflow: hidden;
    animation: menu-pop 90ms ease-out;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    padding: 0.4rem 0.65rem;
    border: 0;
    border-radius: 0.625rem;
    background: transparent;
    text-align: left;
    cursor: pointer;
    transition: background 120ms ease;
  }
  .row:hover,
  .row.sel {
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
  }
  .name {
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.875rem;
    color: var(--text);
  }
  /* first-line preview as a quiet 12px line (ModelPicker meta recipe) */
  .meta {
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.75rem;
    color: var(--muted, #a8a8a8);
  }
  @keyframes menu-pop {
    from {
      opacity: 0;
      transform: scale(0.97);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .pop {
      animation: none;
    }
  }
</style>
