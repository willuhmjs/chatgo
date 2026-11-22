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
  import { safeHref } from '$lib/safe-url';
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

<div class="share mx-auto flex h-full w-full max-w-[48rem] flex-col" style="background:var(--bg); color:var(--text)">
  <header class="chrome flex shrink-0 items-center gap-x-2.5 gap-y-1 border-b px-6 py-3.5" style="border-color:var(--border)">
    <span class="flex items-center gap-1.5 text-sm font-medium">
      <Sparkles size={14} /> ChatGo
    </span>
    <span class="text-xs text-[var(--muted)]">Shared chat</span>
    {#if chat}
      <span class="text-xs text-[var(--muted)]">· {dateLabel}</span>
      <span
        class="ml-auto max-w-[14rem] truncate text-xs text-[var(--muted)]"
        title="Model{chat.provider_id ? ` · ${chat.provider_id}` : ''}"
      >
        {chat.model_id || 'unknown model'}
      </span>
    {/if}
  </header>

  <main class="min-h-0 flex-1 overflow-y-auto px-6 py-8">
    {#if error}
      <div class="rounded-xl px-4 py-6 text-center text-sm" style="background:color-mix(in srgb, var(--danger) 8%, transparent); color:var(--danger)">
        {error}
        <p class="mt-1 text-xs text-[var(--muted)]">This link may have been revoked or the chat deleted.</p>
      </div>
    {:else if chat}
      <h1 class="mb-7 text-lg font-semibold tracking-tight">{chat.title}</h1>
      <div class="flex flex-col gap-6">
        {#each messages as m (m.id)}
          <article class={m.role === 'assistant' ? '' : 'text-right'}>
            <p class="mb-1.5 text-xs text-[var(--muted)]">
              {m.role === 'assistant' ? (chat.model_id || 'Assistant') : m.role}
            </p>
            {#if m.error}
              <p class="text-sm" style="color:var(--danger)">{m.error}</p>
            {:else if m.role === 'assistant'}
              <!-- assistant: plain text on canvas, 16px (§5) -->
              <div class="md">{@html renderMarkdown(m.content)}</div>
            {:else}
              <p class="inline-block max-w-[85%] rounded-[20px] px-4 py-2.5 text-left text-[15px] leading-relaxed whitespace-pre-wrap" style="background:var(--panel)">
                {m.content}
              </p>
            {/if}
            {#if m.sources?.length}
              <details class="mt-2 text-xs text-[var(--muted)]">
                <summary class="cursor-pointer">Sources ({m.sources.length})</summary>
                <ul class="mt-1 list-disc pl-5">
                  {#each m.sources as s (s.url)}
                    <li>
                      <a href={safeHref(s.url)} target="_blank" rel="noopener noreferrer" class="underline"
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
    <span
      class="inline-flex items-center gap-1 rounded-full px-3 py-1 text-xs text-[var(--muted)]"
      style="background:var(--panel)"
      >made with <span class="font-medium text-[var(--text)]">ChatGo</span></span
    >
  </footer>
</div>

<style>
  /* assistant prose at the canvas reading size (§2/§5) */
  main :global(.md) {
    font-size: 16px;
    line-height: 1.7;
  }

  /* print-friendly: drop chrome, expand column, black on white */
  @media print {
    .chrome {
      display: none !important;
    }
    .share {
      height: auto;
      max-width: none;
      background: #fff !important;
      color: #000;
    }
  }
</style>
