<script lang="ts">
  // Model picker (docs/DESIGN-POLISH.md §6): quiet "name + ⌄" trigger inside
  // the composer pill; menu is rounded-xl --panel, grouped by provider,
  // checkmark on the right, context length as a 12px --muted line, and a
  // short scale(0.97→1)+fade pop. Selection persists per-chat via
  // PATCH /api/chats/{id}.
  import type { Chat, ModelEntry } from '$lib/api';
  import * as api from '$lib/api';
  import { models, loadModels, modelsByProvider } from '$lib/chat/state.svelte.js';
  import { formatContextLen } from '$lib/chatview-api';
  import { push as toast } from '$lib/toast.svelte.js';
  import Check from 'lucide-svelte/icons/check';
  import ChevronDown from 'lucide-svelte/icons/chevron-down';
  import Search from 'lucide-svelte/icons/search';

  let {
    chat,
    onupdated,
  }: {
    chat: Chat | null | undefined;
    onupdated?: (c: Chat) => void;
  } = $props();

  let open = $state(false);
  let filter = $state('');
  let root = $state<HTMLDivElement | null>(null);
  let input = $state<HTMLInputElement | null>(null);

  const current = $derived(
    chat?.model_id
      ? models.items.find(
          (m) => m.model_id === chat.model_id && m.provider_id === chat.provider_id,
        ) ?? models.items.find((m) => m.model_id === chat.model_id)
      : undefined,
  );
  const label = $derived(current?.display || current?.model_id || 'no model');

  const groups = $derived.by(() => {
    const f = filter.trim().toLowerCase();
    const groups: [string, ModelEntry[]][] = [];
    for (const [provider, items] of modelsByProvider()) {
      const keep = f
        ? items.filter((m) =>
            `${m.display} ${m.model_id} ${provider}`.toLowerCase().includes(f),
          )
        : items;
      if (keep.length) groups.push([provider, keep]);
    }
    return groups;
  });

  $effect(() => {
    if (open) {
      loadModels();
      const t = setTimeout(() => input?.focus(), 0);
      const onDoc = (e: MouseEvent) => {
        if (root && !root.contains(e.target as Node)) open = false;
      };
      const onKey = (e: KeyboardEvent) => {
        if (e.key === 'Escape') {
          e.stopPropagation();
          open = false;
        }
      };
      document.addEventListener('click', onDoc, true);
      document.addEventListener('keydown', onKey, true);
      return () => {
        clearTimeout(t);
        document.removeEventListener('click', onDoc, true);
        document.removeEventListener('keydown', onKey, true);
      };
    }
  });

  function pick(m: ModelEntry): void {
    open = false;
    if (!chat || (m.provider_id === chat.provider_id && m.model_id === chat.model_id)) return;
    api
      .patchChat(chat.id, { provider_id: m.provider_id, model_id: m.model_id })
      .then((c) => {
        onupdated?.(c);
        window.dispatchEvent(new CustomEvent('chatgo:chat-updated', { detail: { chat: c } }));
      })
      .catch((err: unknown) => toast(`Could not switch model: ${String((err as Error)?.message ?? err)}`, 'error'));
  }
</script>

<div bind:this={root} class="relative">
  <button
    type="button"
    class="trigger"
    class:activep={open}
    title="Pick model (persisted for this chat)"
    aria-haspopup="listbox"
    aria-expanded={open}
    onclick={(e) => {
      e.stopPropagation();
      open = !open;
    }}
  >
    <span class="max-w-[14rem] truncate">{label}</span>
    <ChevronDown size={13} style={open ? 'transform: rotate(180deg)' : ''} class="transition-transform" />
  </button>

  {#if open}
    <div class="pop" role="listbox" aria-label="Models">
      <div class="flex items-center gap-2 px-3 pb-1.5 pt-2.5">
        <Search size={13} style="color:var(--muted)" />
        <input
          bind:this={input}
          bind:value={filter}
          class="w-full bg-transparent text-[13px] outline-none"
          style="color:var(--text)"
          placeholder="Filter models…"
          aria-label="Filter models"
        />
      </div>
      <div class="max-h-72 overflow-y-auto px-1 pb-1.5">
        {#if models.loading && !models.items.length}
          <p class="px-3 py-3 text-xs" style="color:var(--muted)">Loading models…</p>
        {:else if models.error}
          <p class="px-3 py-3 text-xs" style="color:var(--danger)">{models.error}</p>
        {:else if !groups.length}
          <p class="px-3 py-3 text-xs" style="color:var(--muted)">
            {models.loaded ? 'No models — ask an admin to add a provider.' : 'No match.'}
          </p>
        {:else}
          {#each groups as [provider, items] (provider)}
            <p class="group-label">{provider}</p>
            {#each items as m (m.provider_id + ':' + m.model_id)}
              <button
                type="button"
                class="row"
                class:sel={current?.model_id === m.model_id && current?.provider_id === m.provider_id}
                onclick={() => pick(m)}
                role="option"
                aria-selected={current?.model_id === m.model_id}
              >
                <span class="min-w-0 flex-1">
                  <span class="name">{m.display || m.model_id}</span>
                  {#if m.context_len}
                    <span class="meta">{formatContextLen(m.context_len)} context</span>
                  {/if}
                </span>
                {#if current?.model_id === m.model_id && current?.provider_id === m.provider_id}
                  <Check size={14} style="flex:none" />
                {/if}
              </button>
            {/each}
          {/each}
        {/if}
      </div>
    </div>
  {/if}
</div>

<style>
  /* quiet ghost trigger inside the composer pill (§6) */
  .trigger {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    border-radius: 999px;
    border: 0;
    background: transparent;
    color: var(--text);
    font-size: 0.8125rem;
    font-weight: 500;
    padding: 0.3rem 0.55rem;
    cursor: pointer;
    transition: background 120ms ease;
  }
  .trigger:hover,
  .activep {
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
  }

  .pop {
    position: absolute;
    bottom: calc(100% + 8px);
    left: 0;
    z-index: 30;
    width: 21rem;
    min-width: 16rem;
    border-radius: 0.875rem;
    border: 0;
    background: var(--panel, #2f2f2f);
    box-shadow: var(--shadow-menu, 0 8px 28px rgb(0 0 0 / 0.45));
    overflow: hidden;
    animation: menu-pop 90ms ease-out;
  }
  .group-label {
    padding: 0.55rem 0.75rem 0.15rem;
    font-size: 0.75rem;
    font-weight: 500;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--muted, #a8a8a8);
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
  .row:hover {
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
  }
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
  /* context length as a quiet 12px line under the model name (§6) */
  .meta {
    display: block;
    font-size: 0.75rem;
    color: var(--muted, #a8a8a8);
    font-variant-numeric: tabular-nums;
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
