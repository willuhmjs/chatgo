<script lang="ts">
  // Right slide-over for per-chat sampling params → PATCH /api/chats/{id}.
  // Changes are debounced onto the server; Reset clears params_json.
  import {
    patchChatParams,
    parseStopList,
    formatStopList,
    normalizeParams,
    MAX_TOKENS_BOUNDS,
  } from '$lib/chatview-api';
  import { push as toast } from '$lib/toast.svelte.js';
  import X from 'lucide-svelte/icons/x';
  import RotateCcw from 'lucide-svelte/icons/rotate-ccw';

  let {
    chatId,
    params,
    open,
    onclose,
    onupdated,
  }: {
    chatId: string;
    params?: Record<string, unknown>;
    open: boolean;
    onclose: () => void;
    onupdated?: (c: import('$lib/api').Chat) => void;
  } = $props();

  let temperature = $state(1);
  let topP = $state(1);
  let maxTokens = $state<number | null>(null);
  let stopText = $state('');

  let syncing = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let prevOpen = false;
  let syncedForChat = '';

  $effect(() => {
    const opened = open && (!prevOpen || chatId !== syncedForChat);
    prevOpen = open;
    if (!opened) return;
    syncedForChat = chatId;
    const p = normalizeParams(params);
    syncing = true;
    temperature = p.temperature ?? 1;
    topP = p.top_p ?? 1;
    maxTokens = p.max_tokens ?? null;
    stopText = formatStopList(p.stop);
    queueMicrotask(() => (syncing = false));
  });

  function apply(): void {
    if (!chatId) return;
    patchChatParams(chatId, {
      temperature,
      top_p: topP,
      max_tokens: maxTokens ?? undefined,
      stop: parseStopList(stopText),
    })
      .then((c) => {
        onupdated?.(c);
        window.dispatchEvent(new CustomEvent('chatgo:chat-updated', { detail: { chat: c } }));
      })
      .catch((err: unknown) => toast(`Params not saved: ${String((err as Error)?.message ?? err)}`, 'error'));
  }

  function schedule(): void {
    if (timer !== undefined) clearTimeout(timer);
    timer = setTimeout(apply, 400);
  }

  $effect(() => {
    if (!open || syncing) return;
    void temperature;
    void topP;
    void maxTokens;
    void stopText;
    schedule();
  });

  $effect(
    () => () => {
      if (timer !== undefined) clearTimeout(timer);
    },
  );

  function reset(): void {
    if (timer !== undefined) clearTimeout(timer);
    temperature = 1;
    topP = 1;
    maxTokens = null;
    stopText = '';
    patchChatParams(chatId, {})
      .then((c) => {
        onupdated?.(c);
        toast('Params reset to provider defaults', 'success');
      })
      .catch((err: unknown) => toast(`Reset failed: ${String((err as Error)?.message ?? err)}`, 'error'));
  }

  $effect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        onclose();
      }
    };
    document.addEventListener('keydown', onKey, true);
    return () => document.removeEventListener('keydown', onKey, true);
  });

  const sliders = [
    {
      label: 'Temperature',
      hint: 'Higher = creative, lower = deterministic',
      min: 0,
      max: 2,
      step: 0.05,
      get: () => temperature,
      set: (v: number) => (temperature = v),
    },
    {
      label: 'Top-p',
      hint: 'Nucleus sampling probability mass',
      min: 0,
      max: 1,
      step: 0.01,
      get: () => topP,
      set: (v: number) => (topP = v),
    },
  ] as const;
</script>

{#if open}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
  <div class="fixed inset-0 z-40 bg-black/40 drawer-fade" role="presentation" onclick={onclose}></div>
  <aside
    class="fixed right-0 top-0 z-40 flex h-full w-[20rem] max-w-[92vw] flex-col border-l drawer-slide"
    style="background:var(--panel); border-color:var(--border)"
    aria-label="Chat parameters"
  >
    <header class="flex items-center gap-2 border-b px-4 py-3" style="border-color:var(--border)">
      <h2 class="flex-1 text-sm font-semibold">Parameters</h2>
      <button type="button" class="mini" title="Reset to defaults" onclick={reset}>
        <RotateCcw size={13} /> Reset
      </button>
      <button type="button" class="mini" title="Close (Esc)" aria-label="Close parameters" onclick={onclose}>
        <X size={14} />
      </button>
    </header>

    <div class="flex-1 space-y-6 overflow-y-auto p-4">
      {#each sliders as s (s.label)}
        <div>
          <div class="mb-1.5 flex items-center justify-between gap-2">
            <label class="text-xs font-medium" for={`p-${s.label}`}>{s.label}</label>
            <input
              id={`p-${s.label}`}
              type="number"
              class="num"
              min={s.min}
              max={s.max}
              step={s.step}
              value={s.get()}
              oninput={(e) => s.set(Number(e.currentTarget.value))}
            />
          </div>
          <input
            type="range"
            class="w-full"
            min={s.min}
            max={s.max}
            step={s.step}
            value={s.get()}
            aria-label={s.label}
            oninput={(e) => s.set(Number(e.currentTarget.value))}
          />
          <p class="mt-1 text-[11px]" style="color:var(--muted)">{s.hint}</p>
        </div>
      {/each}

      <div>
        <div class="mb-1.5 flex items-center justify-between gap-2">
          <label class="text-xs font-medium" for="p-maxtok">Max tokens</label>
          <input
            id="p-maxtok"
            type="number"
            class="num"
            min={MAX_TOKENS_BOUNDS.min}
            max={MAX_TOKENS_BOUNDS.max}
            step={1}
            placeholder="default"
            bind:value={maxTokens}
          />
        </div>
        <p class="text-[11px]" style="color:var(--muted)">Response length cap; leave empty for model default</p>
      </div>

      <div>
        <label class="mb-1.5 block text-xs font-medium" for="p-stop">Stop sequences</label>
        <textarea
          id="p-stop"
          rows="3"
          class="ta"
          placeholder="One per line (or comma-separated)"
          bind:value={stopText}></textarea>
        <p class="mt-1 text-[11px]" style="color:var(--muted)">Up to 8 sequences; generation halts on match</p>
      </div>

      <p class="text-[11px]" style="color:var(--muted)">
        Saved automatically to this chat. Provider defaults apply to unset fields.
      </p>
    </div>
  </aside>
{/if}

<style>
  .num {
    width: 5.5rem;
    border: 1px solid var(--border);
    background: var(--panel-2);
    color: var(--text);
    border-radius: 0.5rem;
    font-size: 0.75rem;
    padding: 0.25rem 0.4rem;
    outline: none;
    font-variant-numeric: tabular-nums;
  }
  .num:focus {
    border-color: var(--accent);
  }
  .ta {
    width: 100%;
    resize: vertical;
    border: 1px solid var(--border);
    background: var(--panel-2);
    color: var(--text);
    border-radius: 0.625rem;
    font-size: 0.8125rem;
    padding: 0.5rem 0.65rem;
    outline: none;
    font-family: inherit;
  }
  .ta:focus {
    border-color: var(--accent);
  }
  input[type='range'] {
    accent-color: var(--accent);
  }
  .mini {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    border: 1px solid var(--border);
    background: transparent;
    color: var(--muted);
    border-radius: 0.5rem;
    font-size: 0.72rem;
    padding: 0.25rem 0.5rem;
    cursor: pointer;
  }
  .mini:hover {
    color: var(--text);
    border-color: var(--accent);
  }
  .drawer-slide {
    animation: slidein 160ms ease-out;
    box-shadow: -12px 0 32px rgb(0 0 0 / 0.3);
  }
  .drawer-fade {
    animation: fadein 160ms ease-out;
  }
  @keyframes slidein {
    from {
      transform: translateX(2rem);
      opacity: 0.4;
    }
    to {
      transform: translateX(0);
      opacity: 1;
    }
  }
  @keyframes fadein {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }
</style>
