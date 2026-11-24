<script lang="ts">
  // Next-message suggestions (Open WebUI parity): sparkle → task model proposes
  // 2-5 questions you might ask next; click one to send it. Stateless per
  // exchange — ChatView re-keys this component on the chain's last message id,
  // so completing a turn (or switching branch) resets it. Errors degrade to a
  // quiet toast; the conversation never blocks on them.
  import { suggestNext } from '$lib/chat-suggest-api';
  import { ApiError } from '$lib/api';
  import { push } from '$lib/toast.svelte.js';
  import { Loader2, RefreshCw, Sparkles } from 'lucide-svelte';

  let {
    chatId,
    onselect,
  }: {
    chatId: string;
    onselect: (text: string) => void;
  } = $props();

  let items = $state<string[]>([]);
  let shown = $state(false);
  let busy = $state(false);

  async function fetchSuggestions(): Promise<void> {
    if (busy) return;
    busy = true;
    try {
      items = await suggestNext(chatId);
      shown = true;
    } catch (e) {
      const msg =
        e instanceof ApiError && e.status === 503
          ? 'No task model available for suggestions (set one in Admin → System)'
          : e instanceof ApiError && e.status === 429
            ? 'A suggestion request is already running — try again in a moment'
            : 'Could not get suggestions';
      push(msg, 'info');
    } finally {
      busy = false;
    }
  }
</script>

<div class="mx-auto w-full max-w-3xl px-4 pb-1">
  {#if shown && items.length}
    <div class="flex flex-wrap items-center gap-1.5">
      {#each items as s (s)}
        <button
          type="button"
          class="chip"
          onclick={() => {
            shown = false;
            items = [];
            onselect(s);
          }}>{s}</button
        >
      {/each}
      <button type="button" class="ghost" title="More suggestions" aria-label="More suggestions" onclick={() => void fetchSuggestions()}>
        {#if busy}<Loader2 class="size-3.5 animate-spin" />{:else}<RefreshCw class="size-3.5" />{/if}
      </button>
    </div>
  {:else}
    <button type="button" class="ghost" disabled={busy} onclick={() => void fetchSuggestions()}>
      {#if busy}<Loader2 class="size-3.5 animate-spin" />{:else}<Sparkles class="size-3.5" />{/if}
      Suggest what to ask next
    </button>
  {/if}
</div>

<style>
  .chip {
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--panel);
    color: var(--text);
    font-size: 0.8125rem;
    line-height: 1.3;
    padding: 0.4rem 0.85rem;
    cursor: pointer;
    transition:
      border-color 120ms ease,
      background 120ms ease;
  }
  .chip:hover {
    border-color: color-mix(in srgb, var(--acc) 45%, var(--border));
    background: var(--acc-tint, var(--fill-hover));
  }
  .ghost {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    border: 0;
    border-radius: 999px;
    background: transparent;
    color: var(--muted);
    font-size: 0.8125rem;
    padding: 0.35rem 0.6rem;
    cursor: pointer;
    transition:
      color 120ms ease,
      background 120ms ease;
  }
  .ghost:hover {
    color: var(--text);
    background: var(--fill-hover);
  }
  .ghost:disabled {
    opacity: 0.6;
    cursor: default;
  }
</style>
