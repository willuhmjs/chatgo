<script lang="ts">
  // Right slide-over for per-chat sampling params → PATCH /api/chats/{id}.
  // Changes are debounced onto the server; Reset clears params_json.
  // Polish pass (§1/§7): sheet on --bg-deep, no border (fill contrast),
  // 12px uppercase --muted section labels, sliders via tokens, quiet reset.
  import * as api from '$lib/api';
  import {
    compactParams,
    parseStopList,
    formatStopList,
    normalizeParams,
    MAX_TOKENS_BOUNDS,
    type ChatParams,
  } from '$lib/chatview-api';
  import { memoryCountCached } from '$lib/chat-features-api';
  import { navigate } from '$lib/router.svelte.js';
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
  // Per-chat system prompt (Open WebUI's per-model "system" knob): lives in
  // chats.params."system"; the server prefers it over the instance default.
  let systemPrompt = $state('');

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
    systemPrompt = typeof params?.system === 'string' ? params.system : '';
    queueMicrotask(() => (syncing = false));
  });

  // Non-sampling keys inside chats.params (e.g. kb_collection_ids set by the
  // composer's 📚 toggle) must survive every params PATCH — merge them back.
  // "system" is managed by THIS drawer (the textarea below), so it rides the
  // explicit write, not the passthrough merge.
  const SAMPLING_KEYS = new Set(['temperature', 'top_p', 'max_tokens', 'stop', 'system']);
  function extraParams(): Record<string, unknown> {
    const rest: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(params ?? {})) if (!SAMPLING_KEYS.has(k)) rest[k] = v;
    return rest;
  }

  function apply(): void {
    if (!chatId) return;
    const p: ChatParams = {
      temperature,
      top_p: topP,
      max_tokens: maxTokens ?? undefined,
      stop: parseStopList(stopText),
    };
    const sys = systemPrompt.trim();
    const body: Record<string, unknown> = { ...extraParams(), ...compactParams(p) };
    if (sys) body.system = sys;
    api
      .patchChat(chatId, { params: body })
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
    void systemPrompt;
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
    systemPrompt = '';
    api
      .patchChat(chatId, { params: extraParams() })
      .then((c) => {
        onupdated?.(c);
        toast('Params reset to provider defaults', 'success');
      })
      .catch((err: unknown) => toast(`Reset failed: ${String((err as Error)?.message ?? err)}`, 'error'));
  }

  // ---------- memory (read-only count chip; hidden if endpoint absent) ----------
  let memCount = $state<number | null>(null);
  let memTried = false;
  $effect(() => {
    if (!open || memTried) return;
    memTried = true;
    memoryCountCached().then(
      (n) => (memCount = n),
      () => {
        /* 404/no endpoint — quietly omit the row */
      },
    );
  });

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
  <div class="scrim fixed inset-0 z-40 drawer-fade" role="presentation" onclick={onclose}></div>
  <aside
    class="drawer fixed right-0 top-0 z-40 flex h-full w-[20rem] max-w-[92vw] flex-col"
    aria-label="Chat parameters"
  >
    <header class="flex flex-none items-center gap-1 px-4 pb-2 pt-4">
      <h2 class="flex-1 text-[15px] font-medium">Parameters</h2>
      <button type="button" class="quiet" title="Reset to defaults" onclick={reset}>
        <RotateCcw size={13} /> Reset
      </button>
      <button type="button" class="iconbtn" title="Close (Esc)" aria-label="Close parameters" onclick={onclose}>
        <X size={15} />
      </button>
    </header>

    <div class="flex-1 space-y-6 overflow-y-auto px-4 pb-6 pt-2">
      {#each sliders as s (s.label)}
        <div>
          <div class="mb-2 flex items-center justify-between gap-2">
            <label class="label" for={`p-${s.label}`}>{s.label}</label>
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
          <p class="mt-1.5 text-[11px]" style="color:var(--muted)">{s.hint}</p>
        </div>
      {/each}

      <div>
        <div class="mb-2 flex items-center justify-between gap-2">
          <label class="label" for="p-maxtok">Max tokens</label>
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
        <label class="label mb-2 block" for="p-stop">Stop sequences</label>
        <textarea
          id="p-stop"
          rows="3"
          class="ta"
          placeholder="One per line (or comma-separated)"
          bind:value={stopText}></textarea>
        <p class="mt-1.5 text-[11px]" style="color:var(--muted)">Up to 8 sequences; generation halts on match</p>
      </div>

      <div>
        <label class="label mb-2 block" for="p-system">System prompt</label>
        <textarea
          id="p-system"
          rows="4"
          class="ta"
          placeholder="Optional override for this chat — e.g. “Answer in British English, always cite sources.”"
          bind:value={systemPrompt}></textarea>
        <p class="mt-1.5 text-[11px]" style="color:var(--muted)">
          Replaces the assistant persona for this chat only; empty = instance default.
        </p>
      </div>

      {#if memCount !== null}
        <!-- read-only memory status (wave 2); manager lives in Settings → account -->
        <div class="flex items-center gap-2 border-t pt-4" style="border-color:var(--border)">
          <p class="min-w-0 flex-1 text-xs" style="color:var(--muted)">
            Memory · {memCount} {memCount === 1 ? 'memory' : 'memories'} on
          </p>
          <a
            class="quiet"
            href="/settings"
            onclick={(e) => {
              e.preventDefault();
              navigate('/settings');
            }}>Manage</a
          >
        </div>
      {/if}

      <p class="text-[11px]" style="color:var(--faint, #6e6e6e)">
        Saved automatically to this chat. Provider defaults apply to unset fields.
      </p>
    </div>
  </aside>
{/if}

<style>
  .scrim {
    background: rgb(0 0 0 / 0.5);
  }
  /* sheet on --bg-deep; fill contrast only, no border (§1) */
  .drawer {
    background: var(--bg-deep, #171717);
    animation: slidein 160ms ease-out;
    box-shadow: -12px 0 32px rgb(0 0 0 / 0.45);
  }
  .drawer-fade {
    animation: fadein 160ms ease-out;
  }

  .label {
    font-size: 0.75rem;
    font-weight: 500;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--muted, #a8a8a8);
  }

  .num {
    width: 5.5rem;
    border: 1px solid transparent;
    background: var(--panel);
    color: var(--text);
    border-radius: 0.5rem;
    font-size: 0.75rem;
    padding: 0.3rem 0.45rem;
    outline: none;
    font-variant-numeric: tabular-nums;
    transition: box-shadow 120ms ease;
  }
  .num:focus {
    box-shadow: 0 0 0 2px var(--accent-soft, rgb(99 102 241 / 0.18));
  }
  .ta {
    width: 100%;
    resize: vertical;
    border: 1px solid transparent;
    background: var(--panel);
    color: var(--text);
    border-radius: 0.75rem;
    font-size: 0.8125rem;
    padding: 0.5rem 0.65rem;
    outline: none;
    font-family: inherit;
    transition: box-shadow 120ms ease;
  }
  .ta:focus {
    box-shadow: 0 0 0 2px var(--accent-soft, rgb(99 102 241 / 0.18));
  }

  /* token-styled slider: hairline track, accent thumb (FE-COLOR) */
  input[type='range'] {
    -webkit-appearance: none;
    appearance: none;
    height: 4px;
    border-radius: 999px;
    background: var(--panel-2, #3a3a3a);
    accent-color: var(--acc, var(--accent));
    outline: none;
    cursor: pointer;
  }
  input[type='range']::-webkit-slider-thumb {
    -webkit-appearance: none;
    appearance: none;
    width: 14px;
    height: 14px;
    border-radius: 999px;
    border: 0;
    background: var(--acc, var(--accent, #ffffff));
    cursor: pointer;
  }
  input[type='range']::-moz-range-thumb {
    width: 14px;
    height: 14px;
    border-radius: 999px;
    border: 0;
    background: var(--acc, var(--accent, #ffffff));
    cursor: pointer;
  }
  input[type='range']:focus-visible::-webkit-slider-thumb {
    box-shadow: 0 0 0 3px var(--acc-tint-strong, var(--accent-soft, rgb(99 102 241 / 0.18)));
  }

  /* quiet text buttons in the header (§5 "Reset quiet text button") */
  .quiet {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    border: 0;
    background: transparent;
    color: var(--muted, #a8a8a8);
    border-radius: 999px;
    font-size: 0.75rem;
    padding: 0.3rem 0.6rem;
    cursor: pointer;
    transition:
      color 120ms ease,
      background 120ms ease;
  }
  .quiet:hover {
    color: var(--text);
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
  }
  .iconbtn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.75rem;
    height: 1.75rem;
    border: 0;
    border-radius: 999px;
    background: transparent;
    color: var(--muted, #a8a8a8);
    cursor: pointer;
    transition:
      color 120ms ease,
      background 120ms ease;
  }
  .iconbtn:hover {
    color: var(--text);
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
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
  @media (prefers-reduced-motion: reduce) {
    .drawer,
    .drawer-fade {
      animation: none;
    }
  }
</style>
