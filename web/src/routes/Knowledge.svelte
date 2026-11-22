<script lang="ts">
  // /knowledge — RAG collections (FE-PAGES). Endpoints land in parallel
  // (docs/FEATURES-V2.md · BE-RAG): a 404 on load degrades quietly to empty
  // states; action failures surface as toasts. Shape normalization + all
  // assumptions live in $lib/pages-api.ts.
  import { onMount } from 'svelte';
  import * as pages from '$lib/pages-api';
  import { formatBytes } from '$lib/extra-api';
  import { push } from '$lib/toast.svelte.js';
  import { confirmDialog } from '$lib/ui/confirm.svelte.js';
  import Button from '$lib/ui/Button.svelte';
  import IconButton from '$lib/ui/IconButton.svelte';
  import Input from '$lib/ui/Input.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import {
    BookOpen,
    ChevronDown,
    ChevronRight,
    CircleCheck,
    FileUp,
    LoaderCircle,
    Plus,
    Search,
    Trash2,
  } from 'lucide-svelte';

  const ACCEPT = '.txt,.md,.markdown,.csv,.json,.log,.yaml,.yml,.xml,.c,.cc,.cpp,.go,.rs,.py,.ts,.tsx,.js,.jsx,.sh,.sql';

  type UploadRow = { uid: number; name: string; pct: number; status: 'uploading' | 'done' | 'error'; error?: string };

  let collections = $state<pages.KbCollection[]>([]);
  let loading = $state(true);
  let open = $state<Record<string, boolean>>({});

  let files = $state<Record<string, pages.KbFile[]>>({});
  let filesLoading = $state<Record<string, boolean>>({});
  let uploads = $state<Record<string, UploadRow[]>>({});

  let searchQ = $state<Record<string, string>>({});
  let searching = $state<Record<string, boolean>>({});
  /** undefined = never searched, [] = no matches. */
  let results = $state<Record<string, pages.KbSearchHit[] | undefined>>({});
  let openHit = $state<Record<string, boolean>>({});

  let newOpen = $state(false);
  let newName = $state('');
  let creating = $state(false);

  let uploadSeq = 0;

  function fmtDate(ts: number): string {
    if (!ts) return '';
    return new Date(ts > 1e12 ? ts : ts * 1000).toLocaleDateString(undefined, { dateStyle: 'medium' });
  }

  async function load() {
    loading = true;
    try {
      collections = await pages.listKb();
    } catch (err) {
      collections = [];
      if (!pages.isMissing(err)) push(pages.errMessage(err, 'Failed to load collections'), 'error');
    } finally {
      loading = false;
    }
  }
  onMount(load);

  async function loadFiles(id: string) {
    filesLoading[id] = true;
    try {
      files[id] = await pages.listKbFiles(id);
    } catch (err) {
      files[id] = [];
      if (!pages.isMissing(err)) push(pages.errMessage(err, 'Failed to load files'), 'error');
    } finally {
      filesLoading[id] = false;
    }
  }

  function toggle(id: string) {
    const next = !open[id];
    open[id] = next;
    if (next && !(id in files)) void loadFiles(id);
  }

  const countOf = (c: pages.KbCollection) => (files[c.id] ? files[c.id].length : c.file_count);

  async function createCollection() {
    const name = newName.trim();
    if (!name || creating) return;
    creating = true;
    try {
      const c = await pages.createKb(name);
      collections = c.id ? [c, ...collections] : collections;
      if (!c.id) await load(); // server didn't echo the row — refetch
      newName = '';
      newOpen = false;
      push('Collection created', 'success');
    } catch (err) {
      push(pages.errMessage(err, 'Could not create collection'), 'error');
    } finally {
      creating = false;
    }
  }

  async function removeCollection(c: pages.KbCollection) {
    const ok = await confirmDialog({
      title: 'Delete collection?',
      message: `“${c.name}” and all of its files will be permanently deleted.`,
      confirmLabel: 'Delete',
      danger: true,
    });
    if (!ok) return;
    try {
      await pages.deleteKb(c.id);
      collections = collections.filter((x) => x.id !== c.id);
      push('Collection deleted', 'success');
    } catch (err) {
      if (!pages.isMissing(err)) push(pages.errMessage(err, 'Delete failed'), 'error');
    }
  }

  function setRow(colId: string, uid: number, patch: Partial<UploadRow>) {
    uploads[colId] = (uploads[colId] ?? []).map((r) => (r.uid === uid ? { ...r, ...patch } : r));
  }

  /** Sequential uploads — one spinner→check row per file. */
  async function onPick(colId: string, input: HTMLInputElement) {
    const picked = [...(input.files ?? [])];
    input.value = ''; // allow re-picking the same file later
    for (const f of picked) {
      if (f.size > pages.MAX_UPLOAD_BYTES) {
        push(`“${f.name}” is over the 5 MiB limit`, 'error');
        continue;
      }
      const uid = ++uploadSeq;
      uploads[colId] = [...(uploads[colId] ?? []), { uid, name: f.name, pct: 0, status: 'uploading' }];
      try {
        await pages.uploadKbFile(colId, f, (pct) => setRow(colId, uid, { pct }));
        setRow(colId, uid, { status: 'done', pct: 100 });
        await loadFiles(colId);
      } catch (err) {
        setRow(colId, uid, { status: 'error', error: pages.errMessage(err, 'Upload failed') });
        push(`Upload failed: ${f.name}`, 'error');
      }
    }
  }

  async function removeFile(colId: string, f: pages.KbFile) {
    try {
      await pages.deleteKbFile(colId, f.id);
      files[colId] = (files[colId] ?? []).filter((x) => x.id !== f.id);
      push('File removed', 'success');
    } catch (err) {
      if (!pages.isMissing(err)) push(pages.errMessage(err, 'Delete failed'), 'error');
    }
  }

  async function runSearch(colId: string) {
    const q = (searchQ[colId] ?? '').trim();
    if (!q || searching[colId]) return;
    searching[colId] = true;
    try {
      results[colId] = await pages.searchKb(colId, q, 5);
    } catch (err) {
      results[colId] = [];
      if (!pages.isMissing(err)) push(pages.errMessage(err, 'Search failed'), 'error');
    } finally {
      searching[colId] = false;
    }
  }
</script>

<div class="flex h-full flex-col" style="background:var(--bg); color:var(--text)">
  <header class="flex shrink-0 flex-wrap items-center gap-3 border-b px-4 py-3" style="border-color:var(--border)">
    <BookOpen size={16} class="text-[var(--muted)]" />
    <h1 class="text-[20px] font-semibold tracking-tight">Knowledge</h1>
    {#if !loading && collections.length > 0}
      <span class="text-xs text-[var(--muted)]">{collections.length} collection{collections.length === 1 ? '' : 's'}</span>
    {/if}
    <span class="min-w-0 flex-1"></span>
    <Button variant="primary" size="md" onclick={() => (newOpen = true)}>
      <Plus size={15} /> New collection
    </Button>
  </header>

  <div class="min-h-0 flex-1 overflow-y-auto p-4">
    <div class="mx-auto w-full max-w-3xl">
      {#if loading}
        <div class="space-y-3">
          {#each Array(3) as _i}
            <div class="rounded-2xl p-4" style="background:var(--panel)">
              <Skeleton w="40%" h="0.9rem" />
              <div class="mt-2.5"><Skeleton w="25%" h="0.7rem" /></div>
            </div>
          {/each}
        </div>
      {:else if collections.length === 0}
        <EmptyState
          title="No collections yet"
          description="Group text files into collections so chats can search them for grounded answers."
        >
          <icon>
            <BookOpen size={18} />
          </icon>
          <Button variant="primary" size="sm" onclick={() => (newOpen = true)}>
            <Plus size={14} /> New collection
          </Button>
        </EmptyState>
      {:else}
        <div class="space-y-3">
          {#each collections as c (c.id)}
            {@const hits = results[c.id]}
            <section class="overflow-hidden rounded-2xl" style="background:var(--panel)">
              <!-- card header: quiet row, chevron expands inline -->
              <div class="flex items-center gap-2 px-4 py-3.5">
                <button
                  type="button"
                  class="flex min-w-0 flex-1 items-center gap-2.5 rounded-lg text-left focus-visible:outline-2 focus-visible:-outline-offset-1 focus-visible:outline-[var(--accent-soft)]"
                  aria-expanded={!!open[c.id]}
                  onclick={() => toggle(c.id)}
                >
                  {#if open[c.id]}
                    <ChevronDown class="size-4 shrink-0 text-[var(--acc)]" />
                  {:else}
                    <ChevronRight class="size-4 shrink-0 text-[var(--muted)]" />
                  {/if}
                  <span class="min-w-0">
                    <span class="block truncate text-sm font-medium">{c.name}</span>
                    <span class="block text-xs text-[var(--muted)]">
                      {fmtDate(c.created_at) || '—'}
                      {#if countOf(c) !== null}· {countOf(c)} file{countOf(c) === 1 ? '' : 's'}{/if}
                    </span>
                  </span>
                </button>
                <IconButton label={`Delete ${c.name}`} onclick={() => void removeCollection(c)}>
                  <Trash2 class="size-4" />
                </IconButton>
              </div>

              {#if open[c.id]}
                <div class="space-y-4 border-t px-4 py-4" style="border-color:var(--border)">
                  <!-- files -->
                  {#if filesLoading[c.id]}
                    <div class="space-y-2">
                      {#each Array(2) as _i}<Skeleton w="100%" h="1.1rem" />{/each}
                    </div>
                  {:else if (files[c.id] ?? []).length === 0 && (uploads[c.id] ?? []).length === 0}
                    <p class="text-xs text-[var(--muted)]">
                      No files yet — add text files and they'll be chunked + indexed for search.
                    </p>
                  {:else}
                    <ul class="divide-y" style="border-color:var(--border)">
                      {#each files[c.id] ?? [] as f (f.id)}
                        <li class="group flex items-center gap-3 py-2">
                          <span class="min-w-0 flex-1 truncate text-sm">{f.name}</span>
                          <span class="shrink-0 text-xs tabular-nums text-[var(--muted)]">{formatBytes(f.size)}</span>
                          <span class="hidden shrink-0 text-xs text-[var(--muted)] sm:block">{fmtDate(f.created_at)}</span>
                          <button
                            type="button"
                            class="shrink-0 rounded-md p-1.5 text-[var(--faint)] opacity-0 transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--danger)] focus-visible:opacity-100 group-hover:opacity-100"
                            aria-label={`Delete ${f.name}`}
                            onclick={() => void removeFile(c.id, f)}
                          >
                            <Trash2 class="size-3.5" />
                          </button>
                        </li>
                      {/each}
                    </ul>
                  {/if}

                  <!-- upload rows (spinner → check per file) -->
                  {#if (uploads[c.id] ?? []).length > 0}
                    <ul class="space-y-1.5">
                      {#each uploads[c.id] ?? [] as u (u.uid)}
                        <li class="flex items-center gap-2.5 text-xs">
                          {#if u.status === 'uploading'}
                            <LoaderCircle class="size-3.5 shrink-0 animate-spin text-[var(--acc)]" />
                          {:else if u.status === 'done'}
                            <CircleCheck class="size-3.5 shrink-0" style="color:var(--ok)" />
                          {:else}
                            <span class="shrink-0 font-semibold" style="color:var(--danger)">!</span>
                          {/if}
                          <span class="min-w-0 flex-1 truncate text-[var(--muted)]">{u.name}</span>
                          {#if u.status === 'uploading'}
                            <span class="shrink-0 tabular-nums text-[var(--faint)]">{u.pct}%</span>
                          {:else if u.status === 'error'}
                            <span class="shrink-0 truncate" style="color:var(--danger)" title={u.error}>{u.error}</span>
                          {/if}
                        </li>
                      {/each}
                    </ul>
                  {/if}

                  <!-- label-styled quiet upload button -->
                  <div class="flex flex-wrap items-center gap-3">
                    <label
                      class="inline-flex h-9 cursor-pointer items-center gap-1.5 rounded-full px-3.5 text-sm font-medium transition-colors hover:bg-[var(--fill-hover)]"
                      style="background:var(--panel-2)"
                    >
                      <FileUp class="size-4 text-[var(--muted)]" /> Add files
                      <input
                        type="file"
                        multiple
                        accept={ACCEPT}
                        class="hidden"
                        onchange={(e) => void onPick(c.id, e.currentTarget)}
                      />
                    </label>
                    <p class="text-xs text-[var(--muted)]">txt · md · csv · json · log · code — up to 5 MiB each</p>
                  </div>

                  <!-- test search -->
                  <div class="rounded-xl p-3" style="background:var(--panel-2)">
                    <p class="mb-2 flex items-center gap-1.5 text-xs font-medium text-[var(--muted)]">
                      <Search class="size-3.5" /> Test search
                    </p>
                    <div class="flex gap-2">
                      <input
                        type="search"
                        class="h-8 min-w-0 flex-1 rounded-lg border-0 px-2.5 text-sm outline-none placeholder:text-[var(--faint)] focus-visible:outline-2 focus-visible:-outline-offset-1 focus-visible:outline-[var(--accent-soft)]"
                        style="background:var(--panel); color:var(--text)"
                        placeholder="Ask this collection something…"
                        value={searchQ[c.id] ?? ''}
                        oninput={(e) => (searchQ[c.id] = e.currentTarget.value)}
                        onkeydown={(e) => {
                          if (e.key === 'Enter') void runSearch(c.id);
                        }}
                      />
                      <Button
                        variant="secondary"
                        size="sm"
                        disabled={searching[c.id] || !(searchQ[c.id] ?? '').trim()}
                        onclick={() => void runSearch(c.id)}
                      >
                        {#if searching[c.id]}
                          <LoaderCircle class="size-3.5 animate-spin" />
                        {/if}
                        Search
                      </Button>
                    </div>
                    {#if hits && hits.length > 0}
                      <ul class="mt-3 space-y-2">
                        {#each hits as hit, i (`${c.id}#${i}`)}
                          {@const key = `${c.id}#${i}`}
                          <li class="rounded-lg px-2.5 py-2" style="background:var(--panel)">
                            <div class="flex items-center gap-2 text-[11px] text-[var(--muted)]">
                              <span class="min-w-0 flex-1 truncate">{hit.file_name || 'chunk'}</span>
                              <span
                                class="h-1 w-16 shrink-0 overflow-hidden rounded-full"
                                style="background:color-mix(in srgb, var(--text) 10%, transparent)"
                              >
                                <span
                                  class="block h-full rounded-full"
                                  style="width:{Math.max(4, Math.min(100, Math.round(hit.score * 100)))}%; background:var(--acc)"
                                ></span>
                              </span>
                              <span class="shrink-0 tabular-nums">{hit.score.toFixed(2)}</span>
                            </div>
                            <!-- 3-line excerpt, click to expand -->
                            <button
                              type="button"
                              class="mt-1 block w-full text-left text-[13px] leading-relaxed whitespace-pre-wrap text-[var(--text)] {openHit[key] ? '' : 'line-clamp-3'}"
                              onclick={() => (openHit[key] = !openHit[key])}
                            >{hit.content}</button>
                          </li>
                        {/each}
                      </ul>
                    {:else if hits && hits.length === 0 && !searching[c.id]}
                      <p class="mt-2.5 text-xs text-[var(--muted)]">No matches — files may still be indexing.</p>
                    {/if}
                  </div>
                </div>
              {/if}
            </section>
          {/each}
        </div>
      {/if}
    </div>
  </div>
</div>

<Modal bind:open={newOpen} title="New collection" width="max-w-sm">
  <form
    class="space-y-3"
    onsubmit={(e) => {
      e.preventDefault();
      void createCollection();
    }}
  >
    <Input label="Name" placeholder="e.g. Product docs" bind:value={newName} autocomplete="off" />
    <div class="flex justify-end gap-2 pt-1">
      <Button variant="secondary" size="sm" onclick={() => (newOpen = false)}>Cancel</Button>
      <Button variant="primary" size="sm" type="submit" disabled={!newName.trim() || creating}>
        Create
      </Button>
    </div>
  </form>
</Modal>
