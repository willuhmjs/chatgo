<script lang="ts">
  // /n/:token — public read-only shared note (FE-extra). Bare page, no app
  // chrome; anonymous visitors land here fine (same situation as Share.svelte —
  // see its INTEGRATION note about route guards excluding shared URLs).
  import { onMount } from 'svelte';
  import * as api from '$lib/api';
  import Markdown from '$lib/chat/Markdown.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import { StickyNote } from 'lucide-svelte';

  let { token }: { token: string } = $props();

  let note = $state<api.Note | null>(null);
  let loading = $state(true);
  let notFound = $state(false);
  let error = $state('');

  onMount(async () => {
    try {
      note = await api.getSharedNote(token);
    } catch (err) {
      if (err instanceof api.ApiError && err.status === 404) notFound = true;
      else error = err instanceof api.ApiError && err.message ? err.message : 'Failed to load shared note';
    } finally {
      loading = false;
    }
  });

  const dateLabel = $derived(
    note ? new Date(note.updated_at * 1000).toLocaleDateString(undefined, { dateStyle: 'medium' }) : '',
  );
</script>

<svelte:head>
  <title>{note ? `${note.title || 'Untitled note'} — ChatGo` : 'Shared note — ChatGo'}</title>
</svelte:head>

<div class="flex h-full w-full flex-col" style="background:var(--bg); color:var(--text)">
  <div class="mx-auto flex h-full w-full max-w-[760px] flex-col px-6 py-8 md:px-8">
    <header class="shrink-0">
      <a
        href="/"
        class="inline-flex items-center gap-1.5 text-xs text-[var(--muted)] transition-colors hover:text-[var(--text)]"
      >
        <StickyNote size={13} /> Shared note · ChatGo
      </a>
    </header>

    <main class="mt-8 min-h-0 flex-1 overflow-y-auto">
      {#if loading}
        <div class="space-y-3" aria-label="Loading note">
          <Skeleton w="45%" h="1.75rem" rounded="0.5rem" />
          <Skeleton w="22%" h="0.7rem" />
          <div class="mt-8 space-y-2.5">
            {#each Array(5) as _i}
              <Skeleton w="100%" h="0.9rem" />
            {/each}
            <Skeleton w="60%" h="0.9rem" />
          </div>
        </div>
      {:else if notFound}
        <div class="grid min-h-full place-items-center">
          <EmptyState
            title="Note not found"
            description="This link may have been revoked, or the note was deleted."
          >
            <icon>
              <StickyNote size={18} />
            </icon>
            <a href="/" class="mt-1 text-xs text-[var(--acc)] underline underline-offset-4">
              Go to ChatGo
            </a>
          </EmptyState>
        </div>
      {:else if error}
        <div
          class="rounded-xl px-4 py-6 text-center text-sm"
          style="background:color-mix(in srgb, var(--danger) 8%, transparent); color:var(--danger)"
        >
          {error}
          <p class="mt-1 text-xs text-[var(--muted)]">Please try again later.</p>
        </div>
      {:else if note}
        <article>
          <h1 class="text-2xl font-semibold leading-tight tracking-tight">
            {note.title || 'Untitled note'}
          </h1>
          {#if dateLabel}
            <p class="mt-1.5 text-xs text-[var(--muted)]">Updated {dateLabel}</p>
          {/if}
          <div class="mt-6">
            <Markdown src={note.content} />
          </div>
        </article>
      {/if}
    </main>
  </div>
</div>
