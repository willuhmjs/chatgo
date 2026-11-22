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

<div class="flex h-full flex-col">
  <header
    class="flex shrink-0 flex-wrap items-center gap-2 border-b px-3 py-2"
    style="border-color:var(--border); background:var(--panel)"
  >
    <GitCompare size={16} class="text-[var(--muted)]" />
    <h1 class="text-sm font-semibold">Compare models</h1>
    <div class="ml-2 flex items-center overflow-hidden rounded-lg border text-xs" style="border-color:var(--border)">
      {#each [2, 3] as n (n)}
        <button
          class="px-2.5 py-1 {panes.length === n ? 'font-semibold' : 'text-[var(--muted)]'}"
          style={panes.length === n ? 'background:var(--panel-2); color:var(--text)' : ''}
          aria-pressed={panes.length === n}
          onclick={() => setPaneCount(n)}
        >
          {n}×
        </button>
      {/each}
    </div>
    <button
      class="ml-auto flex items-center gap-1 rounded-lg border px-2.5 py-1 text-xs text-[var(--muted)] hover:text-[var(--text)]"
      style="border-color:var(--border)"
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
      <div
        class="w-full max-w-md rounded-xl border p-6 text-center"
        style="background:var(--panel); border-color:var(--border)"
      >
        <Sparkles size={20} class="mx-auto mb-3 text-[var(--muted)]" />
        <p class="text-sm font-medium">No models loaded yet</p>
        <p class="mt-1 text-xs text-[var(--muted)]">
          Compare needs at least one model. Add a provider and sync its models first.
        </p>
        <button
          class="mt-4 inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium"
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
    <div
      class="grid min-h-0 flex-1 gap-2 overflow-y-auto p-2 md:overflow-hidden {panes.length === 3
        ? 'md:grid-cols-2 xl:grid-cols-3'
        : 'md:grid-cols-2'}"
    >
      {#each panes as p, i (p.id)}
        <section
          class="flex h-[65vh] min-h-0 flex-col overflow-hidden rounded-xl border md:h-auto"
          style="background:var(--panel); border-color:var(--border)"
        >
          <div class="flex shrink-0 items-center gap-2 border-b px-2.5 py-2" style="border-color:var(--border)">
            <select
              class="min-w-0 flex-1 truncate rounded-lg border bg-transparent px-2 py-1 text-xs outline-none"
              style="background:var(--panel-2); border-color:var(--border); color:var(--text)"
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
              <span
                class="shrink-0 animate-pulse rounded-full border px-2 py-0.5 text-[10px]"
                style="border-color:var(--accent); color:var(--accent)"
              >
                generating
              </span>
            {:else if p.latencyMs != null || p.usage}
              <span
                class="flex shrink-0 items-center gap-2 rounded-full border px-2 py-0.5 text-[10px] text-[var(--muted)]"
                style="border-color:var(--border)"
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
            class="min-h-0 flex-1 overflow-y-auto px-3 py-2"
          >
            {#if p.msgs.length === 0}
              <p class="py-8 text-center text-xs text-[var(--muted)]">
                Send a prompt below — this pane answers with <span class="text-[var(--text)]">{p.model ? modelLabel(p.model) : '…'}</span>.
              </p>
            {/if}
            {#each p.msgs as m (m.id)}
              <div class="mb-3 {m.role === 'user' ? 'text-right' : ''}">
                {#if m.role === 'user'}
                  <div
                    class="inline-block max-w-[90%] rounded-xl px-3 py-2 text-left text-sm whitespace-pre-wrap"
                    style="background:var(--panel-2)"
                  >
                    {m.content}
                  </div>
                {:else}
                  <div
                    class="inline-block max-w-full rounded-xl border px-3 py-2 text-left"
                    style="border-color:var(--border)"
                  >
                    {#if m.error}
                      <p class="text-xs" style="color:var(--danger)">{m.error}</p>
                    {:else if m.content}
                      <div class="md">{@html renderMarkdown(m.content)}</div>
                    {:else}
                      <p class="text-xs text-[var(--muted)]">…</p>
                    {/if}
                  </div>
                {/if}
              </div>
            {/each}
            {#if p.error}
              <p class="text-xs" style="color:var(--danger)">{p.error}</p>
            {/if}
          </div>
        </section>
      {/each}
    </div>

    <!-- shared composer: one prompt → all panes concurrently -->
    <div class="sticky bottom-0 shrink-0 border-t p-3" style="background:var(--bg); border-color:var(--border)">
      <div class="flex items-end gap-2">
        <textarea
          rows="2"
          class="max-h-40 min-w-0 flex-1 resize-none rounded-xl border px-3 py-2 text-sm outline-none"
          style="background:var(--panel); border-color:var(--border); color:var(--text)"
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
            class="flex items-center gap-1.5 rounded-xl border px-4 py-2.5 text-sm font-medium"
            style="border-color:var(--danger); color:var(--danger)"
            onclick={stopAll}
          >
            <Square size={14} /> Stop all
          </button>
        {:else}
          <button
            class="flex items-center gap-1.5 rounded-xl px-4 py-2.5 text-sm font-medium disabled:opacity-50"
            style="background:var(--accent); color:var(--accent-fg)"
            disabled={!draft.trim()}
            onclick={sendAll}
          >
            <Send size={14} /> Send to {panes.length}
          </button>
        {/if}
      </div>
    </div>
  {/if}
</div>
