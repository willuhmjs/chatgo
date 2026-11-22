<script lang="ts">
  // Model pill + grouped dropdown (by provider, context-length badge).
  // Selection persists per-chat via PATCH /api/chats/{id}.
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
    class="pill"
    class:activep={open}
    title="Pick model (persisted for this chat)"
    aria-haspopup="listbox"
    aria-expanded={open}
    onclick={(e) => {
      e.stopPropagation();
      open = !open;
    }}
  >
    <span class="max-w-[15rem] truncate">{label}</span>
    {#if current?.context_len}
      <span class="badge">{formatContextLen(current.context_len)}</span>
    {/if}
    <ChevronDown size={13} style={open ? 'transform: rotate(180deg)' : ''} class="transition-transform" />
  </button>

  {#if open}
    <div class="pop" role="listbox" aria-label="Models">
      <div class="flex items-center gap-2 border-b px-2.5 py-2" style="border-color:var(--border)">
        <Search size={13} style="color:var(--muted)" />
        <input
          bind:this={input}
          bind:value={filter}
          class="w-full bg-transparent text-xs outline-none"
          style="color:var(--text)"
          placeholder="Filter models…"
          aria-label="Filter models"
        />
      </div>
      <div class="max-h-72 overflow-y-auto py-1">
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
            <p class="px-3 pb-0.5 pt-2 text-[10px] font-semibold uppercase tracking-wider" style="color:var(--muted)">
              {provider}
            </p>
            {#each items as m (m.provider_id + ':' + m.model_id)}
              <button
                type="button"
                class="row"
                class:sel={current?.model_id === m.model_id && current?.provider_id === m.provider_id}
                onclick={() => pick(m)}
                role="option"
                aria-selected={current?.model_id === m.model_id}
              >
                <span class="min-w-0 flex-1 truncate">{m.display || m.model_id}</span>
                {#if m.context_len}
                  <span class="badge">{formatContextLen(m.context_len)}</span>
                {/if}
                {#if current?.model_id === m.model_id && current?.provider_id === m.provider_id}
                  <Check size={13} style="color:var(--accent)" />
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
  .pill {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    border-radius: 999px;
    border: 1px solid var(--border);
    background: var(--panel);
    color: var(--text);
    font-size: 0.75rem;
    padding: 0.3rem 0.7rem;
    cursor: pointer;
    transition: border-color 120ms ease;
  }
  .pill:hover,
  .activep {
    border-color: color-mix(in oklab, var(--accent) 55%, var(--border));
  }
  .badge {
    border-radius: 999px;
    border: 1px solid var(--border);
    color: var(--muted);
    font-size: 0.65rem;
    line-height: 1;
    padding: 0.18rem 0.4rem;
    font-variant-numeric: tabular-nums;
  }
  .pop {
    position: absolute;
    bottom: calc(100% + 8px);
    left: 0;
    z-index: 30;
    width: 21rem;
    min-width: 16rem;
    border-radius: 0.75rem;
    border: 1px solid var(--border);
    background: var(--panel);
    box-shadow: 0 12px 32px rgb(0 0 0 / 0.4);
    overflow: hidden;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    padding: 0.4rem 0.75rem;
    font-size: 0.8125rem;
    color: var(--text);
    background: transparent;
    border: 0;
    text-align: left;
    cursor: pointer;
  }
  .row:hover {
    background: var(--panel-2);
  }
  .row.sel {
    background: color-mix(in oklab, var(--accent) 10%, transparent);
  }
</style>
