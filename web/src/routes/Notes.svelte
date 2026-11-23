<script lang="ts">
  // /notes — Open WebUI-style scratchpads (FE-extra). Two-pane workspace: list
  // column + autosaving markdown editor with preview, public share toggle
  // (rendered publicly by NoteShare.svelte at /n/{token}) and delete.
  // $lib/api is the only data path; saves debounce ~600ms after the last edit.
  import { onMount, tick } from 'svelte';
  import { fly } from 'svelte/transition';
  import * as api from '$lib/api';
  import Markdown from '$lib/chat/Markdown.svelte';
  import { fail, ok } from '$lib/extra/toasts';
  import { confirmDialog } from '$lib/ui/confirm.svelte.js';
  import { timeAgo } from '$lib/ui/chat-grouping';
  import Button from '$lib/ui/Button.svelte';
  import IconButton from '$lib/ui/IconButton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import { rise } from '$lib/ui/transitions.js';
  import {
    ArrowLeft,
    Eye,
    Link2,
    LoaderCircle,
    PencilLine,
    Plus,
    Share2,
    StickyNote,
    Trash2,
  } from 'lucide-svelte';

  const AUTOSAVE_MS = 600;

  let notes = $state<api.Note[]>([]);
  let loading = $state(true);
  let creating = $state(false);
  let selectedId = $state<string | null>(null);

  // editor drafts for the selected note (server row = last saved state)
  let titleDraft = $state('');
  let contentDraft = $state('');
  let preview = $state(false);
  let dirty = $state(false); // unsaved edits queued or in flight
  let saving = $state(false);
  let shareBusy = $state(false);

  let titleEl = $state<HTMLInputElement | undefined>();

  let saveTimer: ReturnType<typeof setTimeout> | undefined;

  const selected = $derived(notes.find((n) => n.id === selectedId) ?? null);
  const shared = $derived(!!selected?.share_token);
  const saveLabel = $derived(saving || dirty ? 'Saving…' : 'Saved');

  function msg(err: unknown, fallback: string): string {
    return err instanceof api.ApiError && err.message ? err.message : fallback;
  }

  async function load() {
    loading = true;
    try {
      const r = await api.listNotes();
      // server returns updated_at desc already — sort defensively anyway
      notes = [...(r.notes ?? [])].sort((a, b) => b.updated_at - a.updated_at);
    } catch (err) {
      notes = [];
      fail(msg(err, 'Failed to load notes'));
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void load();
    // navigating away mid-debounce → persist the pending edit
    return () => {
      if (dirty || saveTimer !== undefined) void doSave();
    };
  });

  // --- autosave ---------------------------------------------------------------

  function scheduleSave() {
    dirty = true;
    if (saveTimer !== undefined) return; // already queued
    saveTimer = setTimeout(() => {
      saveTimer = undefined;
      void doSave();
    }, AUTOSAVE_MS);
  }

  /** Push the current drafts to the server (synchronous start — safe to call
      right before switching notes so the old row is captured first). */
  async function doSave(): Promise<void> {
    if (saveTimer !== undefined) {
      clearTimeout(saveTimer);
      saveTimer = undefined;
    }
    const n = selected;
    if (!n) return;
    const sent = { title: titleDraft, content: contentDraft };
    if (sent.title === n.title && sent.content === n.content) {
      dirty = false;
      return;
    }
    saving = true;
    try {
      const updated = await api.updateNote(n.id, sent);
      notes = notes.map((x) => (x.id === updated.id ? updated : x));
    } catch (err) {
      fail(msg(err, 'Could not save note'));
    } finally {
      saving = false;
      // keep working on the note this save belonged to (drafts may have moved on)
      if (selectedId === n.id) {
        if (titleDraft !== sent.title || contentDraft !== sent.content) scheduleSave();
        else dirty = false;
      }
    }
  }

  // --- list / selection ---------------------------------------------------------

  function openNote(id: string) {
    if (id === selectedId) return;
    if (dirty || saveTimer !== undefined) void doSave();
    selectedId = id || null;
    const n = id ? notes.find((x) => x.id === id) : undefined;
    titleDraft = n?.title ?? '';
    contentDraft = n?.content ?? '';
    preview = false;
    dirty = false;
  }

  async function createNew() {
    if (creating) return;
    creating = true;
    try {
      const n = await api.createNote({ title: 'Untitled note', content: '' });
      notes = [n, ...notes];
      openNote(n.id);
      await tick();
      titleEl?.focus();
      titleEl?.select();
    } catch (err) {
      fail(msg(err, 'Could not create note'));
    } finally {
      creating = false;
    }
  }

  async function removeNote(n: api.Note) {
    const yes = await confirmDialog({
      title: 'Delete note?',
      message: `“${n.title || 'Untitled note'}” will be permanently deleted.`,
      confirmLabel: 'Delete',
      danger: true,
    });
    if (!yes) return;
    if (selectedId === n.id) {
      if (saveTimer !== undefined) clearTimeout(saveTimer);
      saveTimer = undefined;
      dirty = false;
    }
    try {
      await api.deleteNote(n.id);
      notes = notes.filter((x) => x.id !== n.id);
      if (selectedId === n.id) selectedId = null;
      ok('Note deleted');
    } catch (err) {
      fail(msg(err, 'Delete failed'));
    }
  }

  // --- sharing ------------------------------------------------------------------

  async function toggleShare() {
    const n = selected;
    if (!n || shareBusy) return;
    if (saveTimer !== undefined) void doSave(); // share the latest text
    shareBusy = true;
    try {
      const on = !n.share_token;
      const updated = await api.shareNote(n.id, on);
      notes = notes.map((x) => (x.id === updated.id ? updated : x));
      if (on && updated.share_token) {
        const url = `${location.origin}/n/${updated.share_token}`;
        try {
          await navigator.clipboard.writeText(url);
          ok('Share link copied to clipboard');
        } catch {
          ok('Note is shared — copy the link again once the page has permission');
        }
      } else {
        ok('Sharing turned off');
      }
    } catch (err) {
      fail(msg(err, 'Could not update sharing'));
    } finally {
      shareBusy = false;
    }
  }
</script>

<div class="flex h-full flex-col" style="background:var(--bg); color:var(--text)">
  <header
    class="flex shrink-0 flex-wrap items-center gap-3 border-b px-4 py-3"
    style="border-color:var(--border)"
    transition:fly={rise(8, 250)}
  >
    <span class="tile"><StickyNote size={17} /></span>
    <div class="min-w-0">
      <h1 class="text-[20px] leading-tight font-semibold tracking-tight">Notes</h1>
      <p class="text-xs text-[var(--muted)]">Private scratchpads — share one with a link any time.</p>
    </div>
    {#if !loading && notes.length > 0}
      <span class="count-pill">{notes.length} note{notes.length === 1 ? '' : 's'}</span>
    {/if}
    <span class="min-w-0 flex-1"></span>
    <Button variant="primary" size="md" disabled={creating} onclick={() => void createNew()}>
      <Plus size={15} /> New note
    </Button>
  </header>

  {#if loading}
    <div class="min-h-0 flex-1 overflow-y-auto p-4">
      <div class="max-w-72 space-y-2">
        {#each Array(4) as _i}
          <Skeleton w="100%" h="3rem" rounded="0.75rem" />
        {/each}
      </div>
    </div>
  {:else if notes.length === 0}
    <div class="min-h-0 flex-1 overflow-y-auto p-4">
      <div transition:fly={rise(10, 280)}>
        <EmptyState
          title="No notes yet"
          description="Jot things down here — notes autosave as you type and can be shared as a public link."
        >
          <icon>
            <StickyNote size={18} />
          </icon>
          <Button variant="primary" size="sm" disabled={creating} onclick={() => void createNew()}>
            <Plus size={14} /> New note
          </Button>
        </EmptyState>
      </div>
    </div>
  {:else}
    <div class="flex min-h-0 flex-1">
      <!-- list column; collapses into a two-pane flow on small screens -->
      <aside
        class="w-72 shrink-0 flex-col gap-1 overflow-y-auto border-r p-2 {selectedId
          ? 'hidden md:flex'
          : 'flex'}"
        style="border-color:var(--border); background:var(--bg-deep)"
        aria-label="Notes"
      >
        {#each notes as n (n.id)}
          <button
            type="button"
            class="note-row"
            class:active={selectedId === n.id}
            aria-current={selectedId === n.id ? 'true' : undefined}
            onclick={() => openNote(n.id)}
          >
            <span class="flex min-w-0 items-center gap-1.5">
              <span class="min-w-0 flex-1 truncate text-sm font-medium">
                {n.title || 'Untitled note'}
              </span>
              {#if n.share_token}
                <Link2 class="size-3 shrink-0 text-[var(--acc)]" aria-label="Shared" />
              {/if}
            </span>
            <span class="mt-0.5 block truncate text-[11px] text-[var(--faint)]">
              {timeAgo(n.updated_at)}
            </span>
          </button>
        {/each}
      </aside>

      <!-- editor pane -->
      <section class="min-w-0 flex-1 flex-col {selectedId ? 'flex' : 'hidden md:flex'}">
        {#if selected}
          <div
            class="flex shrink-0 flex-wrap items-center gap-2 border-b px-3 py-2.5"
            style="border-color:var(--border)"
          >
            <span class="md:hidden">
              <IconButton label="Back to notes" onclick={() => openNote('')}>
                <ArrowLeft class="size-4" />
              </IconButton>
            </span>
            <span class="text-[11px] text-[var(--faint)]" aria-live="polite">{saveLabel}</span>
            <span class="min-w-0 flex-1"></span>
            <Button variant="outline" size="sm" onclick={() => (preview = !preview)}>
              {#if preview}<PencilLine size={13} />{:else}<Eye size={13} />{/if}
              {preview ? 'Edit' : 'Preview'}
            </Button>
            <Button
              variant={shared ? 'secondary' : 'outline'}
              size="sm"
              disabled={shareBusy}
              onclick={() => void toggleShare()}
            >
              {#if shareBusy}
                <LoaderCircle class="size-3.5 animate-spin" />
              {:else if shared}
                <Link2 size={13} />
              {:else}
                <Share2 size={13} />
              {/if}
              {shared ? 'Shared' : 'Share'}
            </Button>
            <IconButton label="Delete note" variant="danger" onclick={() => void removeNote(selected)}>
              <Trash2 class="size-4" />
            </IconButton>
          </div>

          <input
            class="w-full shrink-0 border-0 bg-transparent px-4 pt-3 pb-1 text-[20px] font-semibold tracking-tight outline-none placeholder:text-[var(--faint)] md:px-8"
            placeholder="Untitled note"
            aria-label="Note title"
            autocomplete="off"
            bind:this={titleEl}
            bind:value={titleDraft}
            oninput={scheduleSave}
          />

          {#if preview}
            <div class="min-h-0 flex-1 overflow-y-auto px-4 py-3 md:px-8">
              {#if contentDraft.trim()}
                <Markdown src={contentDraft} />
              {:else}
                <p class="text-sm text-[var(--faint)]">Nothing to preview yet — switch back to Edit and write something.</p>
              {/if}
            </div>
          {:else}
            <textarea
              class="w-full min-h-0 flex-1 resize-none border-0 bg-transparent px-4 py-3 text-[15px] leading-relaxed outline-none placeholder:text-[var(--faint)] md:px-8"
              placeholder="Start writing… markdown works here."
              aria-label="Note content"
              spellcheck="true"
              bind:value={contentDraft}
              oninput={scheduleSave}></textarea>
          {/if}
        {:else}
          <!-- desktop only: nothing picked yet -->
          <div class="min-h-0 flex-1 overflow-y-auto p-4">
            <EmptyState
              title="No note selected"
              description="Pick a note on the left, or start a new one."
            >
              <icon>
                <StickyNote size={18} />
              </icon>
              <Button variant="secondary" size="sm" disabled={creating} onclick={() => void createNew()}>
                <Plus size={14} /> New note
              </Button>
            </EmptyState>
          </div>
        {/if}
      </section>
    </div>
  {/if}
</div>

<style>
  /* ── accent-tinted icon tile (Knowledge.svelte idiom) ── */
  .tile {
    display: grid;
    place-items: center;
    width: 2.25rem;
    height: 2.25rem;
    border-radius: 0.75rem;
    flex-shrink: 0;
    background: var(--acc-tint);
    color: var(--acc);
  }
  .count-pill {
    height: 1.375rem;
    display: inline-flex;
    align-items: center;
    padding: 0 0.5rem;
    border-radius: 999px;
    border: 1px solid color-mix(in srgb, var(--acc) 30%, transparent);
    background: var(--acc-tint);
    color: var(--acc);
    font-size: 0.6875rem;
    font-weight: 500;
    white-space: nowrap;
  }

  /* ── list rows: quiet pills, accent-tinted when selected ── */
  .note-row {
    display: block;
    width: 100%;
    border: 0;
    border-radius: 0.75rem;
    background: transparent;
    color: var(--text);
    text-align: left;
    padding: 0.55rem 0.75rem;
    cursor: pointer;
    transition:
      background-color 0.12s ease,
      color 0.12s ease;
  }
  .note-row:hover {
    background: var(--fill-hover);
  }
  .note-row:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--acc) 60%, transparent);
    outline-offset: 1px;
  }
  .note-row.active {
    background: var(--panel-2);
  }
</style>
