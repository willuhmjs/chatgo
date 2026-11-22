<script lang="ts">
  // Home route '/' — ChatGPT new-chat moment (docs/DESIGN-POLISH.md §4):
  // centered greeting + pill composer (§6) + quiet suggestion pills.
  // Sending creates a chat via api-shell.startChat, stashes the prompt
  // (takePendingPrompt) and navigates to /c/{id} for ChatView (FE-chat) to
  // auto-send. See docs/FE-SHELL-UI.md.
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
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import Dropdown from '$lib/ui/Dropdown.svelte';
  import MenuItem from '$lib/ui/MenuItem.svelte';
  import SlashPrompts from '$lib/chat/SlashPrompts.svelte';
  import {
    listPromptsCached,
    filterPrompts,
    type PromptEntry,
  } from '$lib/chat-features-api';
  import { hueColor } from '$lib/hue';
  import { ArrowUp, ChevronDown, Clock, LoaderCircle } from 'lucide-svelte';

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

  // autosize the pill textarea, 1 → 10 lines (§6)
  $effect(() => {
    void draft;
    if (!ta) return;
    ta.style.height = 'auto';
    ta.style.height = `${Math.min(ta.scrollHeight, 240)}px`;
  });

  function pickModel(m: ModelEntry) {
    selectedId = `${m.provider_id}:${m.model_id}`;
    setHomeModel(selectedId);
    ta?.focus();
  }

  // ---------- slash prompts ('/' popover over GET /api/prompts) ----------
  let prompts = $state<PromptEntry[]>([]);
  let promptsFetched = false;
  let promptsLoading = $state(false);
  let promptsAvailable = $state(true); // 404 → false: never show the popover
  let slashOpen = $state(false);
  let slashQuery = $state('');
  let slashIndex = $state(0);
  let slashDismissed = $state<string | null>(null); // value when Esc closed it

  const slashFiltered = $derived(filterPrompts(prompts, slashQuery));
  $effect(() => {
    void slashFiltered.length;
    slashIndex = 0;
  });

  function loadPrompts(): void {
    if (promptsFetched || promptsLoading || !promptsAvailable) return;
    promptsLoading = true;
    listPromptsCached()
      .then(
        (rows) => {
          prompts = rows;
          promptsFetched = true;
        },
        (err: unknown) => {
          if ((err as { status?: number })?.status === 404) promptsAvailable = false;
          slashOpen = false;
        },
      )
      .finally(() => (promptsLoading = false));
  }

  /** Recompute popover visibility from draft + caret (single-line guard). */
  function refreshSlash(): void {
    const v = draft;
    if (slashDismissed !== null && !v.startsWith(slashDismissed)) slashDismissed = null;
    const caret = ta ? ta.selectionStart : v.length;
    const ok =
      promptsAvailable &&
      v.startsWith('/') &&
      !v.slice(0, caret).includes('\n') &&
      slashDismissed !== v;
    if (!ok) {
      slashOpen = false;
      return;
    }
    slashQuery = v.slice(1, caret);
    loadPrompts();
    slashOpen = true;
  }

  function pickPrompt(p: PromptEntry): void {
    draft = p.content;
    slashOpen = false;
    slashDismissed = null;
    requestAnimationFrame(() => {
      ta?.focus();
      ta?.setSelectionRange(draft.length, draft.length);
    });
  }

  /** Returns true when the key belongs to the slash popover. */
  function handleSlashKey(e: KeyboardEvent): boolean {
    const n = slashFiltered.length;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (n) slashIndex = (slashIndex + (e.key === 'ArrowDown' ? 1 : n - 1)) % n;
      return true;
    }
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      slashDismissed = draft;
      slashOpen = false;
      return true;
    }
    if (e.key === 'Tab' && n) {
      e.preventDefault();
      pickPrompt(slashFiltered[slashIndex]);
      return true;
    }
    if (e.key === 'Enter' && n && !e.shiftKey && !e.metaKey && !e.ctrlKey) {
      e.preventDefault();
      pickPrompt(slashFiltered[slashIndex]);
      return true;
    }
    return false;
  }

  function onKeyup(e: KeyboardEvent): void {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Escape') return;
    refreshSlash();
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
    slashOpen = false;
    slashDismissed = null;
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
    if (slashOpen && handleSlashKey(e)) return;
    if (e.key !== 'Enter') return;
    const wantSend = sendOnEnter ? !e.shiftKey : e.metaKey || e.ctrlKey;
    if (wantSend) {
      e.preventDefault();
      void send();
    }
  }
</script>

<section class="h-full overflow-y-auto" style="background:var(--bg)">
  <div class="mx-auto flex min-h-full w-full max-w-[48rem] flex-col px-5 py-10 sm:py-14">
    {#if !session.ready}
      <div class="flex flex-1 flex-col items-center justify-center gap-4">
        <Skeleton w="18rem" h="1.875rem" rounded="0.75rem" />
        <Skeleton w="100%" h="6rem" rounded="26px" />
      </div>
    {:else if pending}
      <div class="flex flex-1 items-center justify-center">
        <EmptyState title="Your account is awaiting approval" description="You’ll get access to chats as soon as an administrator approves you.">
          {#snippet icon()}<Clock class="size-5" />{/snippet}
        </EmptyState>
      </div>
    {:else}
      <div class="-mt-[6vh] flex flex-1 flex-col justify-center">
        <!-- greeting (§4) -->
        <h1 class="text-center text-[30px] font-semibold tracking-tight">What can I help with?</h1>

        <!-- pill composer (§6) -->
        <div class="composer relative mt-7">
          {#if slashOpen}
            <SlashPrompts
              items={slashFiltered}
              active={slashIndex}
              loading={promptsLoading}
              onpick={pickPrompt}
              onhover={(i) => (slashIndex = i)}
            />
          {/if}
          <textarea
            bind:this={ta}
            bind:value={draft}
            rows={1}
            placeholder="Ask anything"
            aria-label="Message"
            class="max-h-60 w-full resize-none bg-transparent px-2.5 pb-2 pt-1.5 text-base text-[var(--text)] outline-none placeholder:text-[var(--faint)]"
            onkeydown={onKeydown}
            oninput={() => queueMicrotask(refreshSlash)}
            onkeyup={onKeyup}
            onblur={() => (slashOpen = false)}
          ></textarea>
          <div class="flex items-center justify-between gap-2 px-1 pb-1">
            <div class="flex min-w-0 items-center gap-2">
              <Dropdown
                label="Choose a model"
                align="start"
                disabled={modelsLoading || models.length === 0}
                buttonClass="flex h-8 min-w-0 max-w-56 items-center gap-1 rounded-full px-2.5 text-[13px] text-[var(--muted)] transition-colors hover:bg-[var(--panel-2)] hover:text-[var(--text)] focus-visible:outline-2 focus-visible:-outline-offset-1 focus-visible:outline-[color-mix(in_srgb,var(--indigo)_60%,transparent)] disabled:pointer-events-none disabled:opacity-50"
              >
                <span class="truncate">{modelsLoading ? 'Loading models…' : model ? model.display || model.model_id : 'no models configured'}</span>
                <ChevronDown class="size-3.5 shrink-0" />
                {#snippet items({ close })}
                  {#each models as m (`${m.provider_id}:${m.model_id}`)}
                    <MenuItem selected={m.provider_id === model?.provider_id && m.model_id === model?.model_id} onclick={() => pickModel(m)} {close}>
                      {#snippet icon()}<span class="mdot" style="background:{hueColor(m.provider || m.provider_id)}" aria-hidden="true"></span>{/snippet}
                      {m.display || m.model_id}
                    </MenuItem>
                  {/each}
                {/snippet}
              </Dropdown>
              <span class="hidden truncate text-xs text-[var(--faint)] sm:inline">
                {sendOnEnter ? '↵ send · ⇧↵ newline' : '⌘↵ send'}
              </span>
            </div>
            <button
              type="button"
              class="send"
              aria-label="Send message"
              disabled={!draft.trim() || sending}
              onclick={() => void send()}
            >
              {#if sending}
                <LoaderCircle class="size-4 animate-spin" />
              {:else}
                <ArrowUp class="size-4" />
              {/if}
            </button>
          </div>
        </div>

        <!-- suggestion pills (§4) -->
        <div class="mt-5 flex flex-wrap justify-center gap-2">
          {#each suggestions as s (s.title)}
            <button
              type="button"
              class="sug"
              aria-label="{s.title} — {s.hint}"
              onclick={() => useSuggestion(s.prompt)}
            >
              {s.title}
            </button>
          {/each}
        </div>

        <p class="mt-10 text-center text-[11px] text-[var(--faint)]">
          ⌘B sidebar · ⌘N new chat · ⌘K palette (FE-extra)
        </p>
      </div>
    {/if}
  </div>
</section>

<style>
  /* ChatGPT composer pill (§6): panel fill, 26px radius, soft shadow, no border */
  .composer {
    background: var(--panel);
    border-radius: 26px;
    padding: 10px 12px 8px;
    box-shadow: var(--shadow-composer);
    transition: background-color 0.15s ease;
  }
  .composer:focus-within {
    background: color-mix(in srgb, var(--panel), var(--text) 6%);
  }
  .send {
    display: grid;
    place-items: center;
    width: 32px;
    height: 32px;
    flex-shrink: 0;
    border: 0;
    border-radius: 999px;
    background: var(--acc-grad, var(--accent));
    color: var(--accent-fg);
    cursor: pointer;
    transition:
      opacity 0.12s ease,
      filter 0.12s ease,
      box-shadow 0.12s ease;
  }
  .send:disabled {
    opacity: 0.25;
    cursor: default;
  }
  /* FE-COLOR: hover brightens the accent gradient with a soft glow */
  .send:hover:not(:disabled) {
    filter: brightness(1.08);
    box-shadow: 0 0 12px color-mix(in srgb, var(--acc-strong, var(--accent)) 55%, transparent);
  }
  /* deterministic provider-hue dot in the model menu (FE-COLOR) */
  .mdot {
    display: block;
    width: 8px;
    height: 8px;
    border-radius: 999px;
  }
  .send:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--indigo) 60%, transparent);
    outline-offset: 2px;
  }
  .sug {
    border: 0;
    border-radius: 999px;
    background: var(--panel);
    color: var(--muted);
    padding: 0.5rem 0.875rem;
    font-size: 0.875rem;
    cursor: pointer;
    transition:
      background-color 0.12s ease,
      color 0.12s ease;
  }
  .sug:hover {
    background: var(--panel-2);
    color: var(--text);
  }
  .sug:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--indigo) 60%, transparent);
    outline-offset: 2px;
  }
</style>
