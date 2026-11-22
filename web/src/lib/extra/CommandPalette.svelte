<script lang="ts">
  // ⌘K command palette (FE-extra). Self-registering: mounting <CommandPalette/>
  // anywhere enables the global mod+k binding — see docs/FE-EXTRA-UI.md §MOUNTS.
  // Searches recent chats, app actions and models; model picks dispatch the
  // `chatgo:pick-model` window event (see $lib/extra-api.ts //INTEGRATION).

  import { onMount, onDestroy } from 'svelte';
  import * as api from '$lib/api';
  import { navigate, currentRoute } from '$lib/router.svelte.js';
  import { onShortcut } from '$lib/shortcuts.svelte.js';
  import { session, isAdmin, logout } from '$lib/session.svelte.js';
  import { toggleTheme } from '$lib/theme.svelte.js';
  import { fuzzyScore } from './fuzzy';
  import { modelLabel, saveLastModel, dispatchPickModel } from '$lib/extra-api';
  import { pickerModels } from '$lib/chat/state.svelte.js';
  import { ok as toastOk, fail as toastFail } from './toasts';
  import { Command, Search, Sparkles, SunMoon, LogOut, Settings, GitCompare, Images, X } from 'lucide-svelte';

  interface Item {
    id: string;
    section: string;
    label: string;
    hint?: string;
    run: () => void;
  }

  let open = $state(false);
  let query = $state('');
  let sel = $state(0);
  let models = $state<api.ModelEntry[]>([]);
  let chats = $state<api.Chat[]>([]);
  let loading = $state(false);

  let inputEl = $state<HTMLInputElement | null>(null);
  let rowEls: (HTMLButtonElement | null)[] = $state([]);

  function openPalette() {
    open = true;
    query = '';
    sel = 0;
    void loadContext();
  }
  function closePalette() {
    open = false;
  }
  function togglePalette() {
    if (open) closePalette();
    else openPalette();
  }

  async function loadContext() {
    loading = true;
    try {
      const [chatPage, modelList] = await Promise.all([
        api.listChats({ per: 8 }),
        models.length ? Promise.resolve(models) : api.listModels(),
      ]);
      chats = [...chatPage.chats].sort((a, b) => b.updated_at - a.updated_at).slice(0, 8);
      models = modelList;
    } catch (err) {
      toastFail(err instanceof api.ApiError ? err.message : 'Failed to load palette data');
    } finally {
      loading = false;
    }
  }

  function goChat(c: api.Chat) {
    navigate(`/c/${c.id}`);
    closePalette();
  }

  function pickModel(m: api.ModelEntry) {
    saveLastModel(m);
    dispatchPickModel(m); //INTEGRATION — ChatView listens & persists like ModelPicker
    const r = currentRoute();
    if (r.name === 'chat') {
      api
        .patchChat(r.params.id as string, { provider_id: m.provider_id, model_id: m.model_id })
        .then(() => toastOk(`Model switched to ${modelLabel(m)}`))
        .catch(() => toastFail('Could not save model on this chat'));
    } else {
      toastOk(`${modelLabel(m)} set as your preferred model`);
    }
    closePalette();
  }

  function doLogout() {
    void logout().then(() => {
      navigate('/login');
      closePalette();
    });
  }

  // Static action table (order = display order when unfiltered).
  const actions = $derived<Item[]>(
    [
      { id: 'a-new', section: 'Actions', label: 'New chat', hint: 'go to home', run: () => (navigate('/'), closePalette()) },
      { id: 'a-theme', section: 'Actions', label: 'Toggle theme', hint: session.user ? 'dark ↔ light' : 'dark ↔ light', run: () => (toggleTheme(), closePalette()) },
      { id: 'a-compare', section: 'Actions', label: 'Go to Compare', hint: '/compare', run: () => (navigate('/compare'), closePalette()) },
      { id: 'a-gallery', section: 'Actions', label: 'Go to Gallery', hint: '/gallery', run: () => (navigate('/gallery'), closePalette()) },
      { id: 'a-knowledge', section: 'Actions', label: 'Go to Knowledge', hint: '/knowledge', run: () => (navigate('/knowledge'), closePalette()) },
      { id: 'a-images', section: 'Actions', label: 'Go to Images', hint: '/images', run: () => (navigate('/images'), closePalette()) },
      { id: 'a-settings', section: 'Actions', label: 'Go to Settings', hint: '/settings', run: () => (navigate('/settings'), closePalette()) },
      isAdmin() && { id: 'a-admin', section: 'Actions', label: 'Go to Admin', hint: '/admin', run: () => (navigate('/admin'), closePalette()) },
      session.user && { id: 'a-logout', section: 'Actions', label: 'Log out', run: doLogout },
    ].filter(Boolean) as Item[],
  );

  // Filtered + scored flat list with section labels for headers.
  const items = $derived.by<Item[]>(() => {
    const q = query.trim();
    const sections: { name: string; items: Item[] }[] = [
      {
        name: 'Recent chats',
        items: chats.map((c) => ({
          id: `c-${c.id}`,
          section: 'Recent chats',
          label: c.title || 'Untitled chat',
          hint: new Date(c.updated_at * 1000).toLocaleDateString(),
          run: () => goChat(c),
        })),
      },
      { name: 'Actions', items: actions },
      {
        name: 'Models',
        // same hidden + chat-heuristic filtering as the model pickers; the
        // palette is a chat-model switcher, so no "show others" escape hatch
        items: pickerModels(models).models.map((m) => ({
          id: `m-${m.provider_id}:${m.model_id}`,
          section: 'Models',
          label: modelLabel(m),
          hint: m.provider ?? m.provider_id,
          run: () => pickModel(m),
        })),
      },
    ];
    const out: Item[] = [];
    for (const sec of sections) {
      const scored = sec.items
        .map((it) => ({ it, score: q ? fuzzyScore(q, it.label) : 0 }))
        .filter((x): x is { it: Item; score: number } => x.score !== null)
        .sort((a, b) => b.score - a.score);
      out.push(...scored.map((x) => x.it));
    }
    return out;
  });

  // clamp selection whenever the list changes
  $effect(() => {
    if (sel >= items.length) sel = Math.max(0, items.length - 1);
  });
  $effect(() => {
    if (open) rowEls[sel]?.scrollIntoView({ block: 'nearest' });
  });

  function run(i: number) {
    items[i]?.run();
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      sel = items.length ? (sel + 1) % items.length : 0;
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      sel = items.length ? (sel - 1 + items.length) % items.length : 0;
    } else if (e.key === 'Enter') {
      e.preventDefault();
      run(sel);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      closePalette();
    }
  }

  let offShortcut: (() => void) | undefined;
  onMount(() => {
    offShortcut = onShortcut('mod+k', togglePalette);
    return () => offShortcut?.();
  });
  onDestroy(() => offShortcut?.());

  // focus search box each time we open
  $effect(() => {
    if (open) {
      void inputEl?.focus();
      void inputEl?.select();
    }
  });
</script>

{#if open}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="fixed inset-0 z-[60] flex justify-center bg-black/50 px-4 pt-[15vh]"
    role="presentation"
    onclick={closePalette}
  >
    <div
      class="pop flex max-h-[70vh] w-full max-w-xl flex-col overflow-hidden rounded-2xl"
      style="background:var(--panel); color:var(--text); box-shadow:var(--shadow-menu)"
      role="dialog"
      aria-modal="true"
      tabindex="-1"
      aria-label="Command palette"
      onclick={(e) => e.stopPropagation()}
      onkeydown={onKey}
    >
      <div class="flex items-center gap-2.5 px-4 pb-2 pt-3.5">
        <Search size={16} class="shrink-0 text-[var(--muted)]" />
        <input
          bind:this={inputEl}
          type="text"
          class="w-full bg-transparent py-1.5 text-[15px] outline-none placeholder:text-[var(--faint)]"
          style="color:var(--text)"
          placeholder="Search chats, actions, models…"
          aria-label="Search commands"
          bind:value={query}
          oninput={() => (sel = 0)}
          autocapitalize="off"
          autocomplete="off"
          spellcheck="false"
        />
        <kbd class="hidden shrink-0 text-[11px] text-[var(--muted)] sm:block">esc</kbd>
        <button
          class="rounded-lg p-1 text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)] sm:hidden"
          aria-label="Close palette"
          onclick={closePalette}
        >
          <X size={16} />
        </button>
      </div>

      <div class="min-h-0 flex-1 overflow-y-auto px-2 py-1.5">
        {#if !query.trim()}
          <p class="flex items-center gap-2 px-2.5 py-4 text-sm text-[var(--muted)]">
            <Command size={14} /> Start typing…
          </p>
        {/if}
        {#if query.trim() && items.length === 0 && !loading}
          <p class="px-2 py-6 text-center text-sm text-[var(--muted)]">
            No matches for “{query.trim()}”.
          </p>
        {/if}
        {#each items as it, i (it.id)}
          {#if i === 0 || items[i - 1]?.section !== it.section}
            <p class="px-2.5 pb-1 pt-2.5 text-xs font-medium text-[var(--muted)]">
              {it.section}
            </p>
          {/if}
          <button
            class="flex h-9 w-full items-center gap-2.5 rounded-lg px-2.5 text-left text-sm transition-colors"
            style={i === sel ? 'background:var(--acc-tint); box-shadow:inset 2px 0 0 var(--acc)' : ''}
            bind:this={rowEls[i]}
            onmouseenter={() => (sel = i)}
            onclick={() => run(i)}
          >
            <span class="shrink-0 text-[var(--muted)]">
              {#if it.section === 'Recent chats'}
                <Sparkles size={14} />
              {:else if it.section === 'Models'}
                <Images size={14} />
              {:else if it.id === 'a-theme'}
                <SunMoon size={14} />
              {:else if it.id === 'a-compare'}
                <GitCompare size={14} />
              {:else if it.id === 'a-gallery'}
                <Images size={14} />
              {:else if it.id === 'a-settings'}
                <Settings size={14} />
              {:else if it.id === 'a-logout'}
                <LogOut size={14} />
              {:else}
                <Command size={14} />
              {/if}
            </span>
            <span class="min-w-0 flex-1 truncate">{it.label}</span>
            {#if it.hint}
              <span class="shrink-0 truncate text-xs text-[var(--muted)]">{it.hint}</span>
            {/if}
          </button>
        {/each}
      </div>

      <div class="flex items-center gap-3 border-t px-4 py-2.5 text-xs text-[var(--muted)]" style="border-color:var(--border)">
        <span>↑↓ navigate</span>
        <span>↵ select</span>
        <span>esc close</span>
      </div>
    </div>
  </div>
{/if}

<style>
  /* §7: floating layer entrance — 150ms fade + rise */
  .pop {
    animation: pop-in 150ms ease-out;
  }
  @keyframes pop-in {
    from {
      opacity: 0;
      transform: translateY(2px);
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
