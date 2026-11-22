<script lang="ts">
  // /s/:token — public read-only shared chat (FE-extra).
  // //INTEGRATION (verified, no change needed): bootstrap() in
  // $lib/session.svelte.js swallows the 401 from /api/auth/me and never
  // redirects, and App.svelte renders routes without an auth gate — so
  // anonymous visitors land here fine. If FE-shell ever adds a route guard,
  // it must exclude /s/*.
  import * as api from '$lib/api';
  import { onMount } from 'svelte';
  import { renderMarkdown } from '$lib/extra/md';
  import '$lib/extra/markdown.css';
  import { Sparkles } from 'lucide-svelte';

  let { token }: { token: string } = $props();

  let chat = $state<api.Chat | null>(null);
  let messages = $state<api.Message[]>([]);
  let error = $state('');

  onMount(async () => {
    try {
      const payload = await api.getShare(token);
      chat = payload.chat;
      messages = payload.messages;
    } catch (err) {
      error = err instanceof api.ApiError ? err.message : 'Failed to load shared chat';
    }
  });

  const dateLabel = $derived(
    chat ? new Date(chat.created_at * 1000).toLocaleDateString(undefined, { dateStyle: 'medium' }) : '',
  );
</script>

<svelte:head>
  <title>{chat ? `${chat.title} — ChatGo` : 'Shared chat — ChatGo'}</title>
</svelte:head>

<div class="share mx-auto flex h-full w-full max-w-[48rem] flex-col">
  <header
    class="chrome flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-b px-6 py-4"
    style="border-color:var(--border)"
  >
    <span class="flex items-center gap-1.5 text-sm font-semibold" style="color:var(--accent)">
      <Sparkles size={15} /> ChatGo
    </span>
    <span
      class="rounded-full border px-2 py-0.5 text-[11px] text-[var(--muted)]"
      style="border-color:var(--border)"
    >
      Shared chat
    </span>
    {#if chat}
      <span class="text-xs text-[var(--muted)]">{dateLabel}</span>
      <span
        class="ml-auto rounded-full border px-2.5 py-0.5 text-xs"
        style="border-color:var(--border); color:var(--muted)"
        title="Model{chat.provider_id ? ` · ${chat.provider_id}` : ''}"
      >
        {chat.model_id || 'unknown model'}
      </span>
    {/if}
  </header>

  <main class="min-h-0 flex-1 overflow-y-auto px-6 py-6">
    {#if error}
      <div class="rounded-xl border px-4 py-6 text-center text-sm" style="border-color:var(--border)">
        <p style="color:var(--danger)">{error}</p>
        <p class="mt-1 text-xs text-[var(--muted)]">This link may have been revoked or the chat deleted.</p>
      </div>
    {:else if chat}
      <h1 class="mb-5 text-xl font-semibold">{chat.title}</h1>
      <div class="flex flex-col gap-5">
        {#each messages as m (m.id)}
          <article>
            <p class="mb-1 text-[11px] font-medium uppercase tracking-wider text-[var(--muted)]">
              {m.role === 'assistant' ? (chat.model_id || 'Assistant') : m.role}
            </p>
            {#if m.error}
              <p class="text-sm" style="color:var(--danger)">{m.error}</p>
            {:else if m.role === 'assistant'}
              <div class="md">{@html renderMarkdown(m.content)}</div>
            {:else}
              <p class="text-sm whitespace-pre-wrap">{m.content}</p>
            {/if}
            {#if m.sources?.length}
              <details class="mt-2 text-xs text-[var(--muted)]">
                <summary class="cursor-pointer">Sources ({m.sources.length})</summary>
                <ul class="mt-1 list-disc pl-5">
                  {#each m.sources as s (s.url)}
                    <li>
                      <a href={s.url} target="_blank" rel="noopener noreferrer" class="underline"
                        >{s.title || s.url}</a
                      >
                    </li>
                  {/each}
                </ul>
              </details>
            {/if}
          </article>
        {/each}
      </div>
    {:else}
      <p class="text-sm text-[var(--muted)]">Loading shared chat…</p>
    {/if}
    <!-- read-only by design: no composer on public share pages -->
  </main>

  <footer class="chrome shrink-0 px-6 pb-5 pt-2 text-center">
    <a
      href="#"
      class="inline-flex items-center gap-1.5 rounded-full border px-3.5 py-1.5 text-xs text-[var(--muted)] transition-colors hover:text-[var(--text)]"
      style="background:var(--panel); border-color:var(--border)"
      >made with <span style="color:var(--accent)" class="font-medium">ChatGo</span></a
    >
  </footer>
</div>

<style>
  /* print-friendly: drop chrome, expand column, black on white */
  @media print {
    .chrome {
      display: none !important;
    }
    .share {
      height: auto;
      max-width: none;
      color: #000;
    }
  }
</style>
