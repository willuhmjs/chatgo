<script lang="ts">
  // ChatView — FE-chat centerpiece (contract §Frontend). Route /c/{id}.
  // Loads chat+messages, streams via lib/sse.svelte.js with rAF-batched
  // token buffering, owns regenerate/edit-retry semantics, param drawer and
  // the composer. The empty/new-chat ("/") state belongs to Home (FE-shell):
  // with no id this component renders a safe redirect stub and never crashes.
  import type { Chat, SendMessageBody, StreamMeta } from '$lib/api';
  import type {
    StreamTokenData,
    StreamTitleData,
    StreamSourcesData,
    StreamErrorData,
    StreamUsageData,
  } from '$lib/chatview-api';
  import type { UIMessage } from '$lib/chat/group';
  import * as api from '$lib/api';
  import { navigate } from '$lib/router.svelte.js';
  import { stream, send, regenerate, abort } from '$lib/sse.svelte.js';
  import { session } from '$lib/session.svelte.js';
  import { takePendingPrompt } from '$lib/api-shell';
  import MessageList from '$lib/chat/MessageList.svelte';
  import Composer from '$lib/chat/Composer.svelte';
  import ParamDrawer from '$lib/chat/ParamDrawer.svelte';
  import Lightbox from '$lib/chat/Lightbox.svelte';
  import IconButton from '$lib/chat/ui/IconButton.svelte';
  import { refreshCapabilities, stopSpeaking } from '$lib/chat/state.svelte.js';

  // fires the staged new-chat prompt exactly once per component instance
  let firedPending = false;
  import { uid, isLocalId } from '$lib/chat/group';
  import SlidersHorizontal from 'lucide-svelte/icons/sliders-horizontal';
  import TriangleAlert from 'lucide-svelte/icons/triangle-alert';
  import { push as toast } from '$lib/toast.svelte.js';

  let { id = '' }: { id?: string } = $props();

  let chat = $state<Chat | null>(null);
  let messages = $state<UIMessage[]>([]);
  let phase = $state<'loading' | 'ready' | 'notfound' | 'error'>('loading');
  let loadError = $state('');
  let drawerOpen = $state(false);

  // stream bookkeeping (proxy refs; content mutates notify through the proxy)
  let streamMsg: UIMessage | null = null;
  let tokenBuf = '';
  let rafId = 0;
  let aborted = false;

  // ---------------- load ----------------

  let loadSeq = 0;
  async function load(): Promise<void> {
    if (!id) return;
    const seq = ++loadSeq;
    phase = 'loading';
    loadError = '';
    chat = null;
    messages = [];
    try {
      const [c, ms] = await Promise.all([api.getChat(id), api.listMessages(id)]);
      if (seq !== loadSeq) return;
      chat = c;
      messages = ms.map((m) => ({ ...m }));
      phase = 'ready';
      // First-run handoff: sidebar/new-chat stages a prompt for this chat
      // (takePendingPrompt, see docs/FE-SHELL-UI.md).
      if (!firedPending) {
        firedPending = true;
        const pending = takePendingPrompt(id);
        if (pending) void Promise.resolve().then(() => submit({ content: pending }));
      }
    } catch (err) {
      if (seq !== loadSeq) return;
      const status = (err as api.ApiError)?.status;
      if (status === 404 || status === 401 || status === 403) {
        phase = 'notfound'; // contract: other users' chats answer 404; don't leak more
      } else {
        phase = 'error';
        loadError = String((err as Error)?.message ?? err);
      }
    }
  }

  $effect(() => {
    if (id) void load();
  });

  // abort in-flight work when switching chats / unmounting
  $effect(() => {
    void id;
    return () => {
      if (stream.active) abort();
      if (rafId) {
        cancelAnimationFrame(rafId);
        rafId = 0;
      }
      tokenBuf = '';
      streamMsg = null;
      aborted = false;
      stopSpeaking();
    };
  });

  // title → document.title (shell mirrors the tab; sidebar listens on the
  // chatgo:title event — see docs/FE-CHAT-UI.md)
  $effect(() => {
    const t = chat?.title?.trim() || (phase === 'ready' ? 'New chat' : 'Chat');
    document.title = `${t} — ChatGo`;
  });
  $effect(() => () => {
    document.title = 'ChatGo';
  });

  // capability probe (search toggle visibility) once auth state is known
  $effect(() => {
    const u = session.user;
    if (session.ready) void refreshCapabilities(u?.role);
  });

  // ---------------- streaming ----------------

  function flushTokens(): void {
    rafId = 0;
    if (!streamMsg || !tokenBuf) return;
    streamMsg.content += tokenBuf;
    tokenBuf = '';
  }

  function queueToken(t: string): void {
    if (!t) return;
    tokenBuf += t;
    if (!rafId) rafId = requestAnimationFrame(flushTokens);
  }

  function appendPlaceholder(): UIMessage {
    messages.push({
      id: uid(),
      chat_id: id,
      role: 'assistant',
      content: '',
      created_at: Math.floor(Date.now() / 1000),
      streaming: true,
    });
    streamMsg = messages[messages.length - 1];
    return streamMsg;
  }

  function makeHandlers() {
    return {
      meta: (data: unknown) => {
        const d = data as Partial<StreamMeta> | null;
        if (streamMsg && d?.message_id) {
          streamMsg.id = d.message_id;
          if (d.chat_id) streamMsg.chat_id = d.chat_id;
        }
      },
      title: (data: unknown) => {
        const t = (data as StreamTitleData | null)?.title?.trim();
        if (!t) return;
        if (chat) chat.title = t;
        window.dispatchEvent(new CustomEvent('chatgo:title', { detail: { chat_id: id, title: t } }));
      },
      sources: (data: unknown) => {
        const items = (data as StreamSourcesData | null)?.items;
        if (items?.length && streamMsg) streamMsg.sources = items;
      },
      token: (data: unknown) => queueToken((data as StreamTokenData | null)?.content ?? ''),
      done: (data: unknown) => {
        const u = (data as StreamUsageData | null)?.usage;
        if (u && streamMsg) {
          streamMsg.tokens_in = u.in;
          streamMsg.tokens_out = u.out;
        }
      },
      error: (data: unknown) => {
        if (streamMsg) streamMsg.error = (data as StreamErrorData | null)?.message || 'Generation failed';
      },
    };
  }

  function finishStream(userMsg?: UIMessage): void {
    if (rafId) {
      cancelAnimationFrame(rafId);
      rafId = 0;
    }
    flushTokens();
    if (userMsg) userMsg.pending = false;
    if (streamMsg) {
      streamMsg.streaming = false;
      if (aborted) {
        if (!streamMsg.content) {
          const i = messages.indexOf(streamMsg);
          if (i >= 0) messages.splice(i, 1);
        } else {
          streamMsg.stopped = true;
        }
      } else if (stream.error && !streamMsg.error) {
        streamMsg.error = stream.error; // transport failure, not an `error` event
      }
    }
    streamMsg = null;
    tokenBuf = '';
    aborted = false;
  }

  async function runStream(p: Promise<void>, userMsg?: UIMessage): Promise<void> {
    aborted = false;
    try {
      await p;
    } finally {
      finishStream(userMsg);
    }
  }

  function submit(body: SendMessageBody): void {
    // streamMsg stays set until the previous stream settles (abort race guard)
    if (!id || !chat || stream.active || streamMsg) return;
    const now = Math.floor(Date.now() / 1000);
    messages.push({
      id: uid(),
      chat_id: id,
      role: 'user',
      content: body.content,
      created_at: now,
      pending: true,
    });
    const userMsg = messages[messages.length - 1];
    appendPlaceholder();
    void runStream(send(id, body, makeHandlers()), userMsg);
  }

  /** Regenerate starting at this assistant message (drops it + the tail). */
  function retry(m: UIMessage): void {
    if (!id || stream.active || streamMsg) return;
    const i = messages.indexOf(m);
    if (i < 0) return;
    messages.splice(i);
    appendPlaceholder();
    const search = (m.sources?.length ?? 0) > 0 ? true : undefined;
    void runStream(regenerate(id, isLocalId(m.id) ? undefined : m.id, { search }, makeHandlers()));
  }

  /**
   * Edit a user message: optimistic replace, drop everything after it, then
   * POST /regenerate?from=<userMsgId> with the new content (see
   * docs/FE-CHAT-UI.md “regenerate semantics”).
   */
  function edit(m: UIMessage, content: string): void {
    if (!id || stream.active || streamMsg) return;
    const i = messages.indexOf(m);
    if (i < 0) return;
    m.content = content;
    messages.splice(i + 1);
    appendPlaceholder();
    void runStream(regenerate(id, isLocalId(m.id) ? undefined : m.id, { content }, makeHandlers()));
  }

  function stop(): void {
    aborted = true;
    abort();
  }

  function chatUpdated(c: Chat): void {
    chat = c;
  }
</script>

{#if id}
  {#key id}
    <div class="flex h-full min-h-0">
      <section class="flex min-w-0 flex-1 flex-col">
        <header
          class="flex h-11 flex-none items-center gap-2 border-b px-4"
          style="border-color:var(--border); background:var(--bg)"
        >
          {#if phase === 'ready'}
            <h1 class="min-w-0 flex-1 truncate text-sm font-medium">{chat?.title || 'New chat'}</h1>
            <IconButton title="Chat parameters" active={drawerOpen} onclick={() => (drawerOpen = !drawerOpen)}>
              <SlidersHorizontal size={15} />
            </IconButton>
          {:else}
            <div class="flex-1"></div>
          {/if}
        </header>

        {#if phase === 'loading'}
          <div class="mx-auto w-full max-w-[48rem] flex-1 px-4 pt-8" aria-busy="true" aria-label="Loading chat">
            {#each [96, 168, 120, 144] as h (h)}
              <div class="mb-6 animate-pulse rounded-xl" style="height:{h * 0.55}px; background:var(--panel-2)"></div>
            {/each}
          </div>
        {:else if phase === 'notfound'}
          <div class="flex flex-1 flex-col items-center justify-center gap-2 px-6 text-center">
            <TriangleAlert size={26} style="color:var(--muted)" />
            <p class="text-sm font-medium">Chat not found</p>
            <p class="text-xs" style="color:var(--muted)">It may have been deleted.</p>
            <a
              class="mt-2 text-sm"
              style="color:var(--accent)"
              href="/"
              onclick={(e) => {
                e.preventDefault();
                navigate('/');
              }}>Start a new chat</a
            >
          </div>
        {:else if phase === 'error'}
          <div class="flex flex-1 flex-col items-center justify-center gap-2 px-6 text-center">
            <TriangleAlert size={26} style="color:var(--danger)" />
            <p class="text-sm font-medium">Couldn’t load this chat</p>
            <p class="max-w-sm text-xs" style="color:var(--muted)">{loadError}</p>
            <button
              class="mt-2 rounded-lg border px-3 py-1.5 text-sm"
              style="border-color:var(--border); background:var(--panel); color:var(--text)"
              onclick={() => void load()}>Try again</button
            >
          </div>
        {:else}
          {#if messages.length}
            <MessageList {messages} busy={stream.active} onretry={retry} onedit={edit} />
          {:else}
            <div class="flex flex-1 flex-col items-center justify-center gap-2 px-6 text-center">
              <div
                class="mb-1 flex h-12 w-12 items-center justify-center rounded-2xl text-white"
                style="background:linear-gradient(135deg, var(--accent), #8b5cf6)"
              >
                ✦
              </div>
              <p class="text-lg font-semibold">Start the conversation</p>
              <p class="max-w-xs text-sm" style="color:var(--muted)">
                Pick a model, attach images or paste screenshots, toggle web search — then say hi.
              </p>
            </div>
          {/if}
          <Composer chat={chat} busy={stream.active} onsend={submit} onstop={stop} onupdated={chatUpdated} />
        {/if}
      </section>

      <ParamDrawer
        chatId={id}
        params={chat?.params}
        open={drawerOpen}
        onclose={() => (drawerOpen = false)}
        onupdated={chatUpdated}
      />
      <Lightbox />
    </div>
  {/key}
{:else}
  <!-- New-chat "/" is Home's job (FE-shell). Route param is always present for
       /c/:id, so this is only a defensive fallback — never crash, never crash. -->
  <div class="flex h-full items-center justify-center" style="color:var(--muted)">
    <a href="/" style="color:var(--accent)">Start a new chat</a>
  </div>
{/if}
