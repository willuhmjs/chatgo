<script lang="ts">
  // Home route '/' — hero + model quickstart + suggestion cards + quick
  // composer. Sending creates a chat via api-shell.startChat, stashes the
  // prompt (takePendingPrompt) and navigates to /c/{id} for ChatView (FE-chat)
  // to auto-send. See docs/FE-SHELL-UI.md.
  import * as api from '$lib/api';
  import type { ModelEntry } from '$lib/api';
  import { navigate } from '$lib/router.svelte.js';
  import { session } from '$lib/session.svelte.js';
  import { ApiError } from '$lib/api';
  import { push } from '$lib/toast.svelte.js';
  import {
    getHomeModel,
    setHomeModel,
    getSendOnEnter,
    startChat,
    PREFS_EVENT,
  } from '$lib/api-shell';
  import { firstName } from '$lib/ui/chat-grouping';
  import Button from '$lib/ui/Button.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import { Clock, SendHorizontal, Sparkles } from 'lucide-svelte';

  const user = $derived(session.user);
  const pending = $derived(user?.role === 'pending');

  let models = $state<ModelEntry[]>([]);
  let modelsLoading = $state(true);
  let selectedId = $state(getHomeModel());
  const model = $derived(models.find((m) => `${m.provider_id}:${m.model_id}` === selectedId) ?? models[0]);

  let draft = $state('');
  let sending = $state(false);
  let sendOnEnter = $state(getSendOnEnter());
  let ta = $state<HTMLTextAreaElement | undefined>();

  const hour = new Date().getHours();
  const greeting = hour < 5 ? 'Burning the midnight oil' : hour < 12 ? 'Good morning' : hour < 18 ? 'Good afternoon' : 'Good evening';
  const hello = $derived(user ? `${greeting}, ${firstName(user.name || user.email)}` : greeting);

  const suggestions = [
    {
      title: 'Explain a concept',
      hint: 'Break something down simply',
      prompt: 'Explain how HTTPS and TLS certificates work, as if I know basic networking but no crypto.',
    },
    {
      title: 'Draft an email',
      hint: 'Tone and length adjustable',
      prompt: 'Draft a friendly email asking my project team for a two-day deadline extension, with a short reason and a new proposed date.',
    },
    {
      title: 'Debug code',
      hint: 'Paste it in and go',
      prompt: 'This function is not behaving as expected — walk me through what could be wrong and how to fix it:\n\n',
    },
  ];

  $effect(() => {
    if (!user || pending) {
      modelsLoading = false;
      return;
    }
    let cancelled = false;
    void api
      .listModels()
      .then((ms) => {
        if (cancelled) return;
        models = ms;
        if (!models.some((m) => `${m.provider_id}:${m.model_id}` === selectedId) && models[0]) {
          selectedId = `${models[0].provider_id}:${models[0].model_id}`;
        }
      })
      .catch(() => {
        if (!cancelled) models = [];
      })
      .finally(() => {
        if (!cancelled) modelsLoading = false;
      });
    return () => {
      cancelled = true;
    };
  });

  $effect(() => {
    const onPrefs = () => (sendOnEnter = getSendOnEnter());
    window.addEventListener(PREFS_EVENT, onPrefs);
    return () => window.removeEventListener(PREFS_EVENT, onPrefs);
  });

  function pickModel(m: ModelEntry) {
    selectedId = `${m.provider_id}:${m.model_id}`;
    setHomeModel(selectedId);
    ta?.focus();
  }

  function useSuggestion(prompt: string) {
    draft = prompt;
    ta?.focus();
    // put the caret at the end
    requestAnimationFrame(() => ta?.setSelectionRange(draft.length, draft.length));
  }

  async function send() {
    const text = draft.trim();
    if (!text || sending) return;
    sending = true;
    try {
      const chat = await startChat(text, model);
      navigate(`/c/${chat.id}`);
    } catch (e) {
      push(e instanceof ApiError ? e.message : 'Could not start chat', 'error');
    } finally {
      sending = false;
    }
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key !== 'Enter') return;
    const wantSend = sendOnEnter ? !e.shiftKey : e.metaKey || e.ctrlKey;
    if (wantSend) {
      e.preventDefault();
      void send();
    }
  }
</script>

<section class="h-full overflow-y-auto">
  <div class="mx-auto flex min-h-full w-full max-w-[48rem] flex-col px-5 py-10 sm:py-14">
    {#if !session.ready}
      <div class="flex flex-1 flex-col justify-center gap-4">
        <Skeleton w="60%" h="2rem" rounded="0.5rem" />
        <Skeleton w="40%" h="1rem" />
        <Skeleton w="100%" h="3.5rem" rounded="1rem" />
      </div>
    {:else if pending}
      <div class="flex flex-1 items-center justify-center">
        <EmptyState title="Your account is awaiting approval" description="You’ll get access to chats as soon as an administrator approves you.">
          {#snippet icon()}<Clock class="size-5" />{/snippet}
        </EmptyState>
      </div>
    {:else}
      <div class="flex flex-1 flex-col justify-center">
        <!-- hero -->
        <h1 class="text-2xl font-semibold tracking-tight sm:text-3xl">{hello}</h1>
        <p class="mt-1.5 text-sm text-[var(--muted)]">Pick a model, ask something, or grab a starter below.</p>

        <!-- model pill picker -->
        <div class="mt-5 flex flex-wrap items-center gap-1.5" aria-label="Choose a model">
          {#if modelsLoading}
            {#each Array(3) as _i}<Skeleton w="6rem" h="1.75rem" rounded="999px" />{/each}
          {:else if models.length === 0}
            <span
              class="rounded-full border px-3 py-1 text-xs text-[var(--muted)]"
              style="border-color:var(--border)"
            >
              no models configured
            </span>
          {:else}
            {#each models as m (`${m.provider_id}:${m.model_id}`)}
              <button
                type="button"
                aria-pressed={model?.model_id === m.model_id && model?.provider_id === m.provider_id}
                onclick={() => pickModel(m)}
                class="rounded-full border px-3 py-1 text-xs transition-colors focus-visible:outline-2 focus-visible:outline-[color-mix(in_srgb,var(--accent)_70%,transparent)] {model?.model_id === m.model_id && model?.provider_id === m.provider_id
                  ? 'border-[var(--accent)] text-[var(--accent)]'
                  : 'text-[var(--muted)] hover:text-[var(--text)]'}"
                style="border-color:{model?.model_id === m.model_id && model?.provider_id === m.provider_id
                  ? 'var(--accent)'
                  : 'var(--border)'}; background:{model?.model_id === m.model_id && model?.provider_id === m.provider_id
                  ? 'color-mix(in srgb, var(--accent) 12%, transparent)'
                  : 'transparent'}"
              >
                {m.display || m.model_id}
              </button>
            {/each}
          {/if}
        </div>

        <!-- quick composer -->
        <div
          class="mt-6 rounded-2xl border p-2 shadow-lg"
          style="background:var(--panel); border-color:var(--border)"
        >
          <textarea
            bind:this={ta}
            bind:value={draft}
            rows={2}
            placeholder="Message ChatGo…"
            aria-label="Message"
            class="max-h-40 min-h-11 w-full resize-none bg-transparent px-2.5 py-2 text-sm outline-none placeholder:text-[var(--muted)]"
            onkeydown={onKeydown}
          ></textarea>
          <div class="flex items-center justify-between gap-2 px-1 pb-0.5">
            <span class="truncate text-[11px] text-[var(--muted)]">
              {model ? model.display || model.model_id : 'default model'} · {sendOnEnter ? '↵ send · ⇧↵ newline' : '⌘↵ send'}
            </span>
            <Button size="sm" variant="primary" disabled={!draft.trim() || sending} onclick={() => void send()}>
              <SendHorizontal class="size-4" /> Send
            </Button>
          </div>
        </div>

        <!-- suggestion cards -->
        <div class="mt-6 grid gap-3 sm:grid-cols-3">
          {#each suggestions as s (s.title)}
            <button
              type="button"
              onclick={() => useSuggestion(s.prompt)}
              class="group flex flex-col items-start gap-1 rounded-xl border p-3.5 text-left transition-colors hover:border-[color-mix(in_srgb,var(--accent)_55%,var(--border))] focus-visible:outline-2 focus-visible:outline-[color-mix(in_srgb,var(--accent)_70%,transparent)]"
              style="background:var(--panel); border-color:var(--border)"
            >
              <span class="text-[var(--muted)] group-hover:text-[var(--accent)]">
                <Sparkles class="size-4" />
              </span>
              <span class="text-sm font-medium">{s.title}</span>
              <span class="text-xs leading-relaxed text-[var(--muted)]">{s.hint}</span>
            </button>
          {/each}
        </div>

        <p class="mt-8 text-center text-[11px] text-[var(--muted)]">
          ⌘B sidebar · ⌘N new chat · ⌘K palette (FE-extra)
        </p>
      </div>
    {/if}
  </div>
</section>
