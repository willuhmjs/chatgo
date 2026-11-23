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
  import {
    listRatings,
    setRating,
    deleteRating,
    forkChat,
    type RatingsMap,
  } from '$lib/chat-features-api';
  import MessageList from '$lib/chat/MessageList.svelte';
  import Composer from '$lib/chat/Composer.svelte';
  import ParamDrawer from '$lib/chat/ParamDrawer.svelte';
  import Lightbox from '$lib/chat/Lightbox.svelte';
  import IconButton from '$lib/chat/ui/IconButton.svelte';
  import ChatActionsMenu from '$lib/chat/ChatActionsMenu.svelte';
  import { refreshCapabilities, stopSpeaking, models, loadModels } from '$lib/chat/state.svelte.js';

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

  // wave-2 features (FE-CHATTER): 👍/👎 map hydrated per chat; affordances
  // hide quietly when their endpoint 404s on this server build
  let ratings = $state<RatingsMap>({});
  let ratingsOk = $state(true);
  let forkOk = $state(true);

  // stream bookkeeping (proxy refs; content mutates notify through the proxy)
  let streamMsg: UIMessage | null = null;
  let tokenBuf = '';
  let rafId = 0;
  let aborted = false;

  // Esc-Esc retract request from the Composer (armed for the CURRENT stream
  // only; the stop button never sets it). retractChat pins the chat that
  // armed it — the settle handler may run after a route switch.
  let retractPending = false;
  let retractChat = '';

  // ---------------- load ----------------

  let loadSeq = 0;
  async function load(): Promise<void> {
    if (!id) return;
    const seq = ++loadSeq;
    phase = 'loading';
    loadError = '';
    chat = null;
    messages = [];
    ratings = {};
    ratingsOk = true;
    // Ratings hydrate in parallel — never gates the chat from opening.
    void listRatings(id)
      .then((r) => {
        if (seq === loadSeq) ratings = r;
      })
      .catch((err: unknown) => {
        if (seq === loadSeq && (err as api.ApiError)?.status === 404) ratingsOk = false;
      });
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
    if (!id) return;
    // Prime the shared model cache alongside the chat fetch so the picker
    // trigger and assistant wordmark resolve display names on a fresh load
    // (loadModels is idempotent — ModelPicker reuses the same promise).
    void loadModels();
    void load();
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

  // Esc-Esc in the Composer: stop AND retract the cancelled exchange once the
  // stream settles. Deliberately NOT fired from the keypress itself — the
  // server persists the partial as the stream dies, so the delete must wait
  // for the local settle (runStream's finally) and rides the endpoint's 409
  // retry for the server-side persist to finish. The stop button dispatches
  // nothing: stop-and-keep-the-partial stays ChatGPT parity.
  $effect(() => {
    const onRetract = () => {
      retractPending = true;
      retractChat = id;
    };
    window.addEventListener('chatgo:retract-request', onRetract);
    return () => window.removeEventListener('chatgo:retract-request', onRetract);
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
      // Settle point: the SSE pump is fully done (stream.active false, tokens
      // flushed). If Esc-Esc armed a retract for THIS stream, consume the
      // request here — after finishStream, so the local rows exist in their
      // final shape before the endpoint call deletes them.
      const cid = retractPending ? retractChat : '';
      retractPending = false;
      const target = cid ? streamMsg : null;
      finishStream(userMsg);
      if (cid && target) void retractExchange(cid, target, cid === id ? userMsg : undefined);
    }
  }

  /**
   * Delete the cancelled exchange (Esc-Esc). `assistant` carries the server id
   * from the SSE `meta` event; without one (abort before meta) there is nothing
   * to key the delete on, so skip silently. The endpoint answers 409 until the
   * server has finished persisting the partial of the just-killed stream —
   * back off a few times rather than silently losing the retract. Only on a
   * confirmed 2xx do we drop the rows locally (identity-based, never a blind
   * tail splice, so a fast resend during the await survives); failures stay
   * honest: console.warn and keep the rows.
   */
  async function retractExchange(cid: string, assistant: UIMessage, userMsg?: UIMessage): Promise<void> {
    const mid = assistant.id;
    if (isLocalId(mid)) return;
    // Small grace for the server to finish draining the canceled turn: its
    // persist runs detached and lands a beat after our abort, during which the
    // endpoint answers 409 (and Chrome logs any 409 fetch as a console error).
    // The retry loop below stays as the honest guard, not the normal path.
    await new Promise((r) => setTimeout(r, 250));
    for (let attempt = 0; ; attempt++) {
      try {
        await api.retractMessage(cid, mid);
        break;
      } catch (err) {
        const status = (err as api.ApiError)?.status;
        if (status === 409 && attempt < 4) {
          await new Promise((r) => setTimeout(r, 120 * (attempt + 1)));
          continue;
        }
        console.warn('chatgo: retract failed', err);
        return;
      }
    }
    const ai = messages.indexOf(assistant);
    if (ai >= 0) messages.splice(ai, 1);
    const ui = userMsg ? messages.indexOf(userMsg) : -1;
    if (ui >= 0) messages.splice(ui, 1);
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

  // ---------------- ratings (FE-CHATTER wave 2) ----------------

  /** Optimistic 👍/👎 flip; same icon again clears. Rolls back on failure. */
  function rate(m: UIMessage, value: 1 | -1): void {
    if (!id || isLocalId(m.id)) return;
    const prev = ratings[m.id];
    const restore = (): void => {
      if (prev) ratings[m.id] = prev;
      else delete ratings[m.id];
    };
    if (prev?.value === value) {
      delete ratings[m.id];
      deleteRating(id, m.id).catch((err: unknown) => {
        restore();
        if ((err as api.ApiError)?.status === 404) ratingsOk = false;
      });
    } else {
      ratings[m.id] = { value };
      setRating(id, m.id, value).catch((err: unknown) => {
        restore();
        if ((err as api.ApiError)?.status === 404) ratingsOk = false;
      });
    }
  }

  /** Attach (or replace) the optional 👎 comment on the current rating. */
  function saveRatingComment(m: UIMessage, comment: string): void {
    if (!id || isLocalId(m.id)) return;
    const prev = ratings[m.id];
    const value = prev?.value ?? -1;
    ratings[m.id] = { value, comment };
    setRating(id, m.id, value, comment).catch((err: unknown) => {
      if (prev) ratings[m.id] = prev;
      else delete ratings[m.id];
      if ((err as api.ApiError)?.status === 404) ratingsOk = false;
    });
  }

  // ---------------- fork ----------------

  /** POST /fork copies messages ≤ this one into a new chat, then switches. */
  function forkFrom(m: UIMessage): void {
    if (!id || isLocalId(m.id) || stream.active || streamMsg) return;
    forkChat(id, m.id)
      .then((c) => {
        toast('Forked', 'success', 2000);
        // sidebar refreshes itself on location.path changes
        navigate(`/c/${c.id}`);
      })
      .catch((err: unknown) => {
        if ((err as api.ApiError)?.status === 404) {
          forkOk = false; // endpoint absent — hide the affordance quietly
          return;
        }
        toast(`Fork failed: ${String((err as Error)?.message ?? err)}`, 'error');
      });
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
            <ChatActionsMenu {chat} onupdated={chatUpdated} />
          {:else}
            <div class="flex-1"></div>
          {/if}
        </header>

        {#if phase === 'loading'}
          <div class="mx-auto w-full max-w-[48rem] flex-1 px-4 pt-8" aria-busy="true" aria-label="Loading chat">
            {#each [96, 168, 120, 144] as h (h)}
              <div class="mb-7 animate-pulse rounded-xl" style="height:{h * 0.55}px; background:var(--panel)"></div>
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
              class="mt-2 rounded-full px-3.5 py-1.5 text-sm"
              style="background:var(--panel); color:var(--text)"
              onclick={() => void load()}>Try again</button
            >
          </div>
        {:else}
          {#if messages.length}
            <MessageList
              {messages}
              wordmark={(() => {
                const mid = chat?.model_id;
                if (!mid) return 'ChatGo';
                const e =
                  models.items.find((x) => x.provider_id === chat?.provider_id && x.model_id === mid) ??
                  models.items.find((x) => x.model_id === mid);
                return e?.display || mid;
              })()}
              {ratings}
              busy={stream.active}
              onretry={retry}
              onedit={edit}
              onrate={ratingsOk ? rate : undefined}
              onratecomment={ratingsOk ? saveRatingComment : undefined}
              onfork={forkOk ? forkFrom : undefined}
            />
          {:else}
            <div class="flex flex-1 flex-col items-center justify-center gap-2 px-6 text-center">
              <p class="text-[26px] font-semibold tracking-tight">What can I help with?</p>
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
