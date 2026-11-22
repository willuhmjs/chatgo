<script lang="ts">
  // Model picker (docs/DESIGN-POLISH.md §6): quiet "name + ⌄" trigger inside
  // the composer pill; menu is rounded-xl --panel, grouped by PROVIDER ID
  // (label = display name, id-disambiguated on collision — keying by label
  // used to merge same-named providers and double-render rows), checkmark on
  // the right, context length as a 12px --muted line, and a short
  // scale(0.97→1)+fade pop. Selection persists per-chat via
  // PATCH /api/chats/{id}. Star = account default (me-settings), eye-slash =
  // hide model (me-settings hidden_models).
  import type { Chat, ModelEntry } from '$lib/api';
  import * as api from '$lib/api';
  import {
    models,
    loadModels,
    modelsByProvider,
    matchesQuery,
    modelKey,
    pickerModels,
  } from '$lib/chat/state.svelte.js';
  import { modelPrefs, setDefault, hide } from '$lib/chat/model-prefs.svelte.js';
  import { formatContextLen } from '$lib/chatview-api';
  import { hueColor } from '$lib/hue';
  import { push as toast } from '$lib/toast.svelte.js';
  import Check from 'lucide-svelte/icons/check';
  import ChevronDown from 'lucide-svelte/icons/chevron-down';
  import EyeOff from 'lucide-svelte/icons/eye-off';
  import Search from 'lucide-svelte/icons/search';
  import Star from 'lucide-svelte/icons/star';

  let {
    chat,
    onupdated,
  }: {
    chat: Chat | null | undefined;
    onupdated?: (c: Chat) => void;
  } = $props();

  let open = $state(false);
  let filter = $state('');
  // lifts the chat-model heuristic for the session once the user asks to see
  // the "other" (embeddings, audio, images…) models
  let showAll = $state(false);
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
  const defaultKey = $derived(modelPrefs.defaultModel);

  const groups = $derived.by(() => {
    const { models: list, hiddenByHeuristic } = pickerModels(models.items, { showAll });
    const f = filter.trim();
    const keep = f ? list.filter((m) => matchesQuery(m, f)) : list;
    return { groups: modelsByProvider(keep), hiddenByHeuristic };
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

  /** STAR: set/clear the account default model (me-settings, no toast). */
  function toggleDefault(m: ModelEntry): void {
    const key = modelKey(m);
    void setDefault(defaultKey === key ? '' : key);
  }

  /** EYE-SLASH: hide model from every picker; closes the menu. */
  function hideModel(m: ModelEntry): void {
    void hide(modelKey(m));
    open = false;
    toast('Hidden — manage in Settings', 'info', 3000);
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
        {:else if !groups.groups.length}
          <p class="px-3 py-3 text-xs" style="color:var(--muted)">
            {models.loaded ? 'No models — ask an admin to add a provider.' : 'No match.'}
          </p>
        {:else}
          {#each groups.groups as g (g.id)}
            <p class="group-label">
              <span class="dot" style="background:{hueColor(g.id)}" aria-hidden="true"></span>{g.label}
            </p>
            {#each g.models as m (modelKey(m))}
              {@const key = modelKey(m)}
              {@const isSel = current?.model_id === m.model_id && current?.provider_id === m.provider_id}
              {@const isDef = key === defaultKey}
              <div class="row" class:sel={isSel}>
                <button
                  type="button"
                  class="pick"
                  onclick={() => pick(m)}
                  role="option"
                  aria-selected={isSel}
                >
                  <span class="dot" style="background:{hueColor(g.id)}" aria-hidden="true"></span>
                  <span class="min-w-0 flex-1">
                    <span class="name">{m.display || m.model_id}</span>
                    {#if m.display && m.display !== m.model_id}
                      <span class="meta mono">{m.model_id}</span>
                    {/if}
                    {#if m.context_len}
                      <span class="meta">{formatContextLen(m.context_len)} context</span>
                    {/if}
                  </span>
                  {#if isSel}
                    <Check size={14} style="flex:none" />
                  {/if}
                </button>
                <div class="actions" class:sticky={isDef}>
                  <button
                    type="button"
                    class="act"
                    aria-label={isDef ? 'Clear default model' : 'Set as default model'}
                    aria-pressed={isDef}
                    title={isDef ? 'Clear default model' : 'Set as default model'}
                    onclick={(e) => {
                      e.stopPropagation();
                      toggleDefault(m);
                    }}
                  >
                    <Star size={13} fill={isDef ? 'currentColor' : 'none'} style={isDef ? 'color:var(--acc)' : ''} />
                  </button>
                  <button
                    type="button"
                    class="act"
                    aria-label="Hide model"
                    title="Hide model — manage in Settings"
                    onclick={(e) => {
                      e.stopPropagation();
                      hideModel(m);
                    }}
                  >
                    <EyeOff size={13} />
                  </button>
                </div>
              </div>
            {/each}
          {/each}
        {/if}
      </div>
      {#if groups.hiddenByHeuristic > 0}
        <button
          type="button"
          class="foot"
          onclick={() => (showAll = true)}
        >
          Show {groups.hiddenByHeuristic} other model{groups.hiddenByHeuristic === 1 ? '' : 's'}
          (embeddings, audio, images…)
        </button>
      {/if}
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
  /* deterministic provider-hue dots (FE-COLOR): 8px, painted from hueColor() */
  .group-label .dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    margin-right: 0.4rem;
    border-radius: 999px;
    vertical-align: baseline;
  }
  .row {
    display: flex;
    align-items: center;
    width: 100%;
    border-radius: 0.625rem;
    transition: background 120ms ease;
  }
  .row:hover,
  .row.sel {
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
  }
  .pick {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    min-width: 0;
    flex: 1;
    padding: 0.4rem 0.65rem;
    border: 0;
    background: transparent;
    text-align: left;
    cursor: pointer;
  }
  .row .dot {
    width: 8px;
    height: 8px;
    flex: none;
    border-radius: 999px;
  }
  .name {
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.875rem;
    color: var(--text);
  }
  /* context length + raw model id as quiet 12px lines under the name (§6) */
  .meta {
    display: block;
    font-size: 0.75rem;
    color: var(--muted, #a8a8a8);
    font-variant-numeric: tabular-nums;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .meta.mono {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.6875rem;
    color: var(--faint, var(--muted, #a8a8a8));
  }
  /* hover/inline star + eye-slash actions; the star stays visible while its
     model is the account default (filled) */
  .actions {
    display: flex;
    align-items: center;
    gap: 0.125rem;
    padding-right: 0.4rem;
    opacity: 0;
    transition: opacity 120ms ease;
  }
  .row:hover .actions,
  .row:focus-within .actions,
  .actions.sticky {
    opacity: 1;
  }
  .act {
    display: inline-flex;
    padding: 0.25rem;
    border: 0;
    border-radius: 0.5rem;
    background: transparent;
    color: var(--muted, #a8a8a8);
    cursor: pointer;
    transition: background 120ms ease, color 120ms ease;
  }
  .act:hover {
    background: var(--panel-2, rgb(255 255 255 / 0.08));
    color: var(--text);
  }
  .act:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--acc) 60%, transparent);
    outline-offset: 1px;
  }
  .foot {
    display: block;
    width: 100%;
    padding: 0.5rem 0.75rem;
    border: 0;
    border-top: 1px solid var(--border, rgb(255 255 255 / 0.08));
    background: transparent;
    text-align: left;
    font-size: 0.75rem;
    color: var(--acc);
    cursor: pointer;
    transition: background 120ms ease;
  }
  .foot:hover {
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
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
