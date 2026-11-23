<script lang="ts">
  // /compare — side-by-side model comparison (FE-extra).
  // Each pane owns an EPHEMERAL chat (POST /api/chats, soft-deleted on leave)
  // and its own SSE stream: $lib/sse.svelte.js is a single global controller,
  // so concurrent panes call api.streamMessage directly with per-pane
  // AbortControllers instead.
  import * as api from '$lib/api';
  import { navigate } from '$lib/router.svelte.js';
  import { onMount } from 'svelte';
  import { renderMarkdown } from '$lib/extra/md';
  import { modelKey, findModel, modelLabel } from '$lib/extra-api';
  import { ok as toastOk, fail as toastFail } from '$lib/extra/toasts';
  import '$lib/extra/markdown.css';
  import { Clock, GitCompare, Square, Send, Settings, Sparkles, Trash2, Zap } from 'lucide-svelte';

  interface Msg {
    id: string;
    role: 'user' | 'assistant';
    content: string;
    error: string | null;
  }
  interface Pane {
    id: number;
    model: api.ModelEntry | null;
    chatID: string | null;
    chatKey: string | null; // model the server-side temp chat is currently set to
    msgs: Msg[];
    streaming: boolean;
    usage: { in: number; out: number } | null;
    latencyMs: number | null;
    error: string | null;
  }

  let models = $state<api.ModelEntry[]>([]);
  let modelsLoading = $state(true);
  let panes = $state<Pane[]>([]);
  let draft = $state('');
  let paneSeq = 0;

  // AbortControllers live OUTSIDE $state — Svelte proxies would break the
  // native AbortSignal identity that fetch() requires.
  const controllers = new Map<number, AbortController>();

  const anyStreaming = $derived(panes.some((p) => p.streaming));
  const groupedModels = $derived(
    (() => {
      const byProvider = new Map<string, api.ModelEntry[]>();
      for (const m of models) {
        const key = m.provider ?? m.provider_id;
        let list = byProvider.get(key);
        if (!list) byProvider.set(key, (list = []));
        list.push(m);
      }
      return [...byProvider.entries()];
    })(),
  );

  const bodies: (HTMLDivElement | null)[] = [];
  function newPane(idx: number): Pane {
    return {
      id: ++paneSeq,
      model: models[Math.min(idx, Math.max(models.length - 1, 0))] ?? null,
      chatID: null,
      chatKey: null,
      msgs: [],
      streaming: false,
      usage: null,
      latencyMs: null,
      error: null,
    };
  }
  function setPaneCount(n: number) {
    if (n === panes.length) return;
    while (panes.length > n) {
      const gone = panes.pop();
      if (gone) dropPane(gone);
    }
    while (panes.length < n) panes.push(newPane(panes.length));
  }
  function dropPane(p: Pane) {
    controllers.get(p.id)?.abort();
    controllers.delete(p.id);
    if (p.chatID) void api.deleteChat(p.chatID).catch(() => {});
  }

  async function loadModels() {
    modelsLoading = true;
    try {
      models = await api.listModels();
      if (panes.length === 0) setPaneCount(2);
      for (const p of panes) if (!p.model) p.model = models[0] ?? null;
    } catch (err) {
      toastFail(err instanceof api.ApiError ? err.message : 'Failed to load models');
    } finally {
      modelsLoading = false;
    }
  }

  async function ensureChat(p: Pane): Promise<string> {
    if (!p.model) throw new Error('No model selected');
    const key = modelKey(p.model);
    if (!p.chatID) {
      // Ephemeral compare chat — visible in sidebar as "compare:" until the
      // route unmounts and DELETEs it (hard-refresh may orphan one; harmless).
      const c = await api.createChat({
        title: 'compare: side-by-side (temp)',
        provider_id: p.model.provider_id,
        model_id: p.model.model_id,
      });
      p.chatID = c.id;
      p.chatKey = key;
    } else if (p.chatKey !== key) {
      await api.patchChat(p.chatID, { provider_id: p.model.provider_id, model_id: p.model.model_id });
      p.chatKey = key;
    }
    return p.chatID;
  }

  async function sendToPane(paneID: number, text: string) {
    const p = panes.find((x) => x.id === paneID);
    if (!p || !p.model || p.streaming) return;
    p.streaming = true;
    p.error = null;
    p.usage = null;
    p.latencyMs = null;
    const t0 = performance.now();
    let ai = -1;
    try {
      const chatID = await ensureChat(p);
      p.msgs.push({ id: `u${Date.now()}-${paneID}`, role: 'user', content: text, error: null });
      p.msgs.push({ id: `a${Date.now()}-${paneID}`, role: 'assistant', content: '', error: null });
      ai = p.msgs.length - 1;
      const controller = new AbortController();
      controllers.set(paneID, controller);
      await api.streamMessage(
        chatID,
        { content: text },
        (ev) => {
          const cur = panes.find((x) => x.id === paneID);
          if (!cur) return;
          if (ev.event === 'token') {
            const d = ev.data as { content?: string };
            cur.msgs[ai].content += d.content ?? '';
          } else if (ev.event === 'done') {
            const d = ev.data as api.StreamDone;
            cur.usage = d.usage ?? null;
            cur.latencyMs = Math.round(performance.now() - t0);
          } else if (ev.event === 'error') {
            const d = ev.data as { message?: string };
            cur.msgs[ai].error = d.message ?? 'Stream error';
          }
          // meta/title/sources ignored — temp chats are discarded anyway.
        },
        controller.signal,
      );
    } catch (err) {
      if ((err as Error)?.name !== 'AbortError') {
        p.error = err instanceof api.ApiError ? err.message : String((err as Error)?.message ?? err);
      }
    } finally {
      controllers.delete(paneID);
      const cur = panes.find((x) => x.id === paneID);
      if (cur) cur.streaming = false;
    }
  }

  function sendAll() {
    const text = draft.trim();
    if (!text || anyStreaming) return;
    draft = '';
    for (const p of [...panes]) void sendToPane(p.id, text);
  }

  function stopAll() {
    for (const c of controllers.values()) c.abort();
  }

  function clearPanes() {
    stopAll();
    for (const p of panes) {
      if (p.chatID) void api.deleteChat(p.chatID).catch(() => {});
      p.chatID = null;
      p.chatKey = null;
      p.msgs = [];
      p.usage = null;
      p.latencyMs = null;
      p.error = null;
    }
    toastOk('Panes cleared');
  }

  function onModelPick(p: Pane, e: Event) {
    const key = (e.target as HTMLSelectElement).value;
    const m = findModel(models, key);
    if (m) p.model = m;
  }

  // auto-scroll every pane body as tokens arrive
  $effect(() => {
    for (const p of panes) void p.msgs.map((m) => m.content.length).join();
    for (const el of bodies) if (el) el.scrollTop = el.scrollHeight;
  });

  onMount(() => {
    void loadModels();
    return () => {
      for (const c of controllers.values()) c.abort();
      for (const p of panes) if (p.chatID) void api.deleteChat(p.chatID).catch(() => {});
      controllers.clear();
    };
  });
</script>

<div class="flex h-full flex-col" style="background:var(--bg); color:var(--text)">
  <header class="flex shrink-0 flex-wrap items-center gap-2 border-b px-4 py-2.5" style="border-color:var(--border)">
    <GitCompare size={15} class="text-[var(--muted)]" />
    <h1 class="text-[15px] font-medium tracking-tight">Compare models</h1>
    <!-- quiet segmented pill -->
    <div class="ml-2 flex items-center gap-0.5 rounded-full p-0.5" style="background:var(--panel)">
      {#each [2, 3] as n (n)}
        <button
          class="rounded-full px-2.5 py-1 text-xs transition-colors {panes.length === n ? 'font-medium' : ''}"
          style={panes.length === n ? 'background:var(--panel-2); color:var(--text)' : 'color:var(--muted)'}
          aria-pressed={panes.length === n}
          onclick={() => setPaneCount(n)}
        >
          {n}×
        </button>
      {/each}
    </div>
    <button
      class="ml-auto flex items-center gap-1.5 rounded-lg px-2 py-1 text-xs transition-colors hover:bg-[var(--fill-hover)] text-[var(--muted)] hover:text-[var(--text)]"
      onclick={clearPanes}
    >
      <Trash2 size={13} /> Clear panes
    </button>
  </header>

  {#if modelsLoading}
    <div class="flex flex-1 items-center justify-center text-sm text-[var(--muted)]">Loading models…</div>
  {:else if models.length === 0}
    <!-- Guard: nothing to compare until a provider is configured -->
    <div class="flex flex-1 items-center justify-center p-6">
      <div class="w-full max-w-md rounded-2xl p-6 text-center" style="background:var(--panel)">
        <Sparkles size={20} class="mx-auto mb-3 text-[var(--muted)]" />
        <p class="text-sm font-medium">No models loaded yet</p>
        <p class="mt-1 text-xs text-[var(--muted)]">
          Compare needs at least one model. Add a provider and sync its models first.
        </p>
        <button
          class="mt-4 inline-flex items-center gap-1.5 rounded-[10px] px-3 py-1.5 text-xs font-medium"
          style="background:var(--accent); color:var(--accent-fg)"
          onclick={() => navigate('/admin')}
        >
          <Settings size={13} /> Open Admin
        </button>
        <button class="mt-4 block w-full text-xs text-[var(--muted)] hover:text-[var(--text)]" onclick={loadModels}>
          Retry
        </button>
      </div>
    </div>
  {:else}
    <!-- panes on the canvas, hairline dividers instead of cards -->
    <div
      class="grid min-h-0 flex-1 divide-y divide-[var(--border)] overflow-y-auto md:divide-y-0 md:divide-x md:overflow-hidden {panes.length === 3
        ? 'md:grid-cols-2 xl:grid-cols-3'
        : 'md:grid-cols-2'}"
    >
      {#each panes as p, i (p.id)}
        <section class="flex h-[60vh] min-h-0 flex-col md:h-auto" style="background:var(--bg)">
          <div class="flex shrink-0 items-center gap-2 border-b px-2.5 py-2" style="border-color:var(--border)">
            <select
              class="-ml-2 min-w-0 flex-1 cursor-pointer truncate rounded-lg bg-transparent px-2 py-1 text-[13px] font-medium outline-none transition-colors hover:bg-[var(--fill-hover)] disabled:opacity-60 focus-visible:[box-shadow:0_0_0_3px_var(--accent-soft)]"
              style="color:var(--text)"
              value={p.model ? modelKey(p.model) : ''}
              disabled={p.streaming}
              onchange={(e) => onModelPick(p, e)}
              aria-label="Pane {i + 1} model"
            >
              {#each groupedModels as [provider, list] (provider)}
                <optgroup label={provider}>
                  {#each list as m (modelKey(m))}
                    <option value={modelKey(m)}>{modelLabel(m)}</option>
                  {/each}
                </optgroup>
              {/each}
            </select>
            {#if p.streaming}
              <span class="flex shrink-0 items-center gap-1.5 text-xs text-[var(--muted)]">
                <span class="h-1.5 w-1.5 animate-pulse rounded-full" style="background:var(--accent)"></span>
                generating
              </span>
            {:else if p.latencyMs != null || p.usage}
              <span
                class="flex shrink-0 items-center gap-2 text-xs text-[var(--muted)]"
                title="tokens in/out · time to completion"
              >
                {#if p.usage}
                  <span class="flex items-center gap-0.5"><Zap size={10} />{p.usage.in}↑ {p.usage.out}↓</span>
                {/if}
                {#if p.latencyMs != null}
                  <span class="flex items-center gap-0.5"><Clock size={10} />{(p.latencyMs / 1000).toFixed(1)}s</span>
                {/if}
              </span>
            {/if}
          </div>

          <div
            bind:this={bodies[i]}
            class="min-h-0 flex-1 overflow-y-auto px-4 py-3"
          >
            {#if p.msgs.length === 0}
              <p class="py-8 text-center text-[13px] text-[var(--muted)]">
                Send a prompt below — this pane answers with <span class="text-[var(--text)]">{p.model ? modelLabel(p.model) : '…'}</span>.
              </p>
            {/if}
            {#each p.msgs as m (m.id)}
              <div class="mb-5 {m.role === 'user' ? 'text-right' : ''}">
                {#if m.role === 'user'}
                  <!-- own copy of the §5 look: right-aligned pill -->
                  <div
                    class="inline-block max-w-[85%] rounded-[20px] px-4 py-2.5 text-left text-sm leading-relaxed whitespace-pre-wrap"
                    style="background:var(--panel)"
                  >
                    {m.content}
                  </div>
                {:else}
                  <!-- own copy of the §5 look: assistant = plain text on canvas -->
                  <div class="msg-plain">
                    {#if m.error}
                      <p class="text-[13px]" style="color:var(--danger)">{m.error}</p>
                    {:else if m.content}
                      <div class="md">{@html renderMarkdown(m.content)}</div>
                    {:else}
                      <span class="inline-block h-3 w-1.5 animate-pulse rounded-sm" style="background:var(--faint)"></span>
                    {/if}
                  </div>
                {/if}
              </div>
            {/each}
            {#if p.error}
              <p class="mb-3 text-[13px]" style="color:var(--danger)">{p.error}</p>
            {/if}
          </div>
        </section>
      {/each}
    </div>

    <!-- shared composer: one prompt → all panes concurrently. Own copy of the
         §6 ChatGPT pill. -->
    <div class="shrink-0 px-4 pb-4 pt-2" style="background:var(--bg)">
      <div class="pill mx-auto flex max-w-[48rem] items-end gap-2 rounded-[26px] px-4 pt-3 pb-3">
        <textarea
          rows="2"
          class="max-h-40 min-w-0 flex-1 resize-none bg-transparent text-base outline-none placeholder:text-[var(--faint)]"
          style="color:var(--text)"
          placeholder="Ask all {panes.length} models the same thing… (Enter to send, Shift+Enter newline)"
          bind:value={draft}
          onkeydown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
              e.preventDefault();
              sendAll();
            }
          }}
        ></textarea>
        {#if anyStreaming}
          <button
            class="flex size-8 shrink-0 items-center justify-center rounded-full transition-opacity hover:opacity-90"
            style="background:var(--accent); color:var(--accent-fg)"
            aria-label="Stop all streams"
            title="Stop all"
            onclick={stopAll}
          >
            <Square size={12} />
          </button>
        {:else}
          <button
            class="flex size-8 shrink-0 items-center justify-center rounded-full transition-opacity hover:opacity-90 disabled:opacity-25 disabled:hover:opacity-25"
            style="background:var(--accent); color:var(--accent-fg)"
            aria-label="Send to {panes.length} models"
            title="Send to {panes.length} models"
            disabled={!draft.trim()}
            onclick={sendAll}
          >
            <Send size={14} />
          </button>
        {/if}
      </div>
    </div>
  {/if}
</div>

<style>
  /* composer pill (§6): --panel fill, soft shadow, brighter on focus, no border */
  .pill {
    background: var(--panel);
    box-shadow: 0 2px 12px rgba(0, 0, 0, 0.25);
    transition: background 150ms ease;
  }
  .pill:focus-within {
    /* fill-hover over the panel fill — solid, theme-aware (§6 #353535) */
    background: linear-gradient(var(--fill-hover), var(--fill-hover)) var(--panel);
  }
  /* assistant body inherits the plain-text look */
  .msg-plain :global(.md) {
    font-size: 15px;
    line-height: 1.7;
  }
</style>

