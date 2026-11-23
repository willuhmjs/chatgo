<script lang="ts">
  // /knowledge — RAG collections (FE-PAGES). Endpoints land in parallel
  // (docs/FEATURES-V2.md · BE-RAG): a 404 on load degrades quietly to empty
  // states; action failures surface as toasts. Shape normalization + all
  // assumptions live in $lib/pages-api.ts.
  import { onMount } from 'svelte';
  import { fly, slide } from 'svelte/transition';
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
  import SlideOver from '$lib/ui/SlideOver.svelte';
  import { quick, rise, stepDelay } from '$lib/ui/transitions.js';
  import {
    BookOpen,
    ChevronRight,
    CircleCheck,
    FileText,
    FileUp,
    Inbox,
    LoaderCircle,
    Plus,
    Search,
    Trash2,
    X,
  } from 'lucide-svelte';

  const ACCEPT = '.txt,.md,.markdown,.csv,.json,.log,.yaml,.yml,.xml,.c,.cc,.cpp,.go,.rs,.py,.ts,.tsx,.js,.jsx,.sh,.sql';

  type UploadRow = { uid: number; name: string; pct: number; status: 'uploading' | 'done' | 'error'; error?: string };
  type Seg = { text: string; hit: boolean };

  let collections = $state<pages.KbCollection[]>([]);
  let loading = $state(true);
  let open = $state<Record<string, boolean>>({});

  let files = $state<Record<string, pages.KbFile[]>>({});
  let filesLoading = $state<Record<string, boolean>>({});
  let uploads = $state<Record<string, UploadRow[]>>({});
  let dragCol = $state<string | null>(null);
  let inputEls = $state<Record<string, HTMLInputElement | undefined>>({});

  // citation slide-over (search across one collection, terms highlighted)
  let panelOpen = $state(false);
  let panelId = $state('');
  let panelFile = $state('');
  let panelQ = $state('');
  let panelBusy = $state<Record<string, boolean>>({});
  /** undefined = never searched this collection, [] = no matches. */
  let panelHits = $state<Record<string, pages.KbSearchHit[] | undefined>>({});
  let panelErr = $state('');
  let panelOpenHit = $state<Record<string, boolean>>({});
  let panelInput = $state<HTMLInputElement | undefined>();

  let newOpen = $state(false);
  let newName = $state('');
  let creating = $state(false);

  let uploadSeq = 0;

  let panelCol = $derived(collections.find((c) => c.id === panelId));
  let busy = $derived(panelId ? !!panelBusy[panelId] : false);
  let hits = $derived(panelId ? panelHits[panelId] : undefined);
  let panelRe = $derived(buildRe(panelQ));

  function fmtDate(ts: number): string {
    if (!ts) return '';
    return new Date(ts > 1e12 ? ts : ts * 1000).toLocaleDateString(undefined, { dateStyle: 'medium' });
  }

  const countOf = (c: pages.KbCollection) => (files[c.id] ? files[c.id].length : c.file_count);

  function metaOf(c: pages.KbCollection): string {
    const parts: string[] = [];
    const n = countOf(c);
    if (n !== null) parts.push(`${n} file${n === 1 ? '' : 's'}`);
    const d = fmtDate(c.created_at);
    if (d) parts.push(d);
    return parts.join(' · ');
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
      if (panelId === c.id) panelOpen = false;
      push('Collection deleted', 'success');
    } catch (err) {
      if (!pages.isMissing(err)) push(pages.errMessage(err, 'Delete failed'), 'error');
    }
  }

  function setRow(colId: string, uid: number, patch: Partial<UploadRow>) {
    uploads[colId] = (uploads[colId] ?? []).map((r) => (r.uid === uid ? { ...r, ...patch } : r));
  }

  function dropRow(colId: string, uid: number) {
    uploads[colId] = (uploads[colId] ?? []).filter((r) => r.uid !== uid);
  }

  /** Sequential uploads — one spinner→check row per file, auto-retired on success. */
  async function uploadFiles(colId: string, picked: File[]) {
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
        setTimeout(() => dropRow(colId, uid), 1400);
        await loadFiles(colId);
      } catch (err) {
        setRow(colId, uid, { status: 'error', error: pages.errMessage(err, 'Upload failed') });
        push(`Upload failed: ${f.name}`, 'error');
      }
    }
  }

  function onPick(colId: string, input: HTMLInputElement) {
    const picked = [...(input.files ?? [])];
    input.value = ''; // allow re-picking the same file later
    void uploadFiles(colId, picked);
  }

  function onDrop(colId: string, e: DragEvent) {
    e.preventDefault();
    dragCol = null;
    if (e.dataTransfer) void uploadFiles(colId, [...e.dataTransfer.files]);
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

  // --- citation slide-over ----------------------------------------------------

  function openPanel(c: pages.KbCollection, f?: pages.KbFile) {
    panelId = c.id;
    panelFile = f ? f.name : '';
    panelQ = f ? f.name.replace(/\.[^.]+$/, '') : ''; // stem of the doc makes a decent first query
    panelErr = '';
    panelOpenHit = {};
    panelOpen = true;
    if (panelQ) void runPanel();
  }

  async function runPanel() {
    const id = panelId;
    const q = panelQ.trim();
    if (!id || !q || panelBusy[id]) return;
    panelBusy[id] = true;
    panelErr = '';
    panelOpenHit = {};
    try {
      panelHits[id] = await pages.searchKb(id, q, 8);
    } catch (err) {
      panelHits[id] = [];
      if (!pages.isMissing(err)) panelErr = pages.errMessage(err, 'Search failed');
    } finally {
      panelBusy[id] = false;
    }
  }

  function closePanel() {
    panelFile = '';
    panelQ = '';
    panelErr = '';
  }

  // focus the query field whenever the panel opens
  $effect(() => {
    if (!panelOpen) return;
    const t = setTimeout(() => {
      panelInput?.focus();
      panelInput?.select();
    }, 80);
    return () => clearTimeout(t);
  });

  // collection vanished under an open panel (deleted elsewhere) → close it
  $effect(() => {
    if (panelOpen && panelId && !collections.some((c) => c.id === panelId)) panelOpen = false;
  });

  // --- search-term highlighting ------------------------------------------------

  function buildRe(q: string): RegExp | null {
    const terms = [...new Set(q.trim().split(/\s+/).filter(Boolean))];
    if (!terms.length) return null;
    const esc = terms.map((t) => t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
    try {
      return new RegExp(`(${esc.join('|')})`, 'gi');
    } catch {
      return null;
    }
  }

  function segments(text: string, re: RegExp | null): Seg[] {
    if (!re) return text ? [{ text, hit: false }] : [];
    const out: Seg[] = [];
    let last = 0;
    re.lastIndex = 0;
    let m: RegExpExecArray | null;
    while ((m = re.exec(text))) {
      if (m.index > last) out.push({ text: text.slice(last, m.index), hit: false });
      out.push({ text: m[0], hit: true });
      last = m.index + m[0].length;
      if (m.index === re.lastIndex) re.lastIndex++;
    }
    if (last < text.length) out.push({ text: text.slice(last), hit: false });
    return out;
  }
</script>

<div class="flex h-full flex-col" style="background:var(--bg); color:var(--text)">
  <header
    class="flex shrink-0 flex-wrap items-center gap-3 border-b px-4 py-3"
    style="border-color:var(--border)"
    transition:fly={rise(8, 250)}
  >
    <span class="tile"><BookOpen size={17} /></span>
    <div class="min-w-0">
      <h1 class="text-[20px] leading-tight font-semibold tracking-tight">Knowledge</h1>
      <p class="text-xs text-[var(--muted)]">Ground your chats in your own documents.</p>
    </div>
    {#if !loading && collections.length > 0}
      <span class="count-pill">{collections.length} collection{collections.length === 1 ? '' : 's'}</span>
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
            <div class="rounded-2xl border p-4" style="background:var(--panel); border-color:var(--border)">
              <Skeleton w="40%" h="0.9rem" />
              <div class="mt-2.5"><Skeleton w="25%" h="0.7rem" /></div>
            </div>
          {/each}
        </div>
      {:else if collections.length === 0}
        <div transition:fly={rise(10, 280)}>
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
        </div>
      {:else}
        <div class="space-y-3">
          {#each collections as c, ci (c.id)}
            <section class="kb-card" transition:fly={rise(8, 260, stepDelay(ci))}>
              <!-- card header -->
              <div class="flex items-center gap-2 px-4 py-3.5">
                <button
                  type="button"
                  class="row-btn min-w-0 flex-1"
                  aria-expanded={!!open[c.id]}
                  onclick={() => toggle(c.id)}
                >
                  <span class="tile tile-sm"><BookOpen size={15} /></span>
                  <span class="min-w-0 flex-1">
                    <span class="block truncate text-sm font-medium">{c.name}</span>
                    {#if metaOf(c)}
                      <span class="mt-0.5 block truncate text-xs text-[var(--muted)]">{metaOf(c)}</span>
                    {/if}
                  </span>
                  <span class={`chev${open[c.id] ? ' open' : ''}`} aria-hidden="true">
                    <ChevronRight size={15} />
                  </span>
                </button>
                <IconButton label={`Delete ${c.name}`} onclick={() => void removeCollection(c)}>
                  <Trash2 class="size-4" />
                </IconButton>
              </div>

              {#if open[c.id]}
                <div
                  class="space-y-4 border-t px-4 py-4"
                  style="border-color:var(--border)"
                  transition:slide={quick(220)}
                >
                  <!-- files -->
                  {#if filesLoading[c.id]}
                    <div class="space-y-2">
                      {#each Array(2) as _i}<Skeleton w="100%" h="1.1rem" />{/each}
                    </div>
                  {:else if (files[c.id] ?? []).length === 0 && (uploads[c.id] ?? []).length === 0}
                    <p class="flex items-center justify-center gap-2 rounded-xl border border-dashed px-3 py-4 text-xs text-[var(--muted)]" style="border-color:var(--border)">
                      <Inbox class="size-4 shrink-0 text-[var(--faint)]" />
                      No files yet — drop text files below and they'll be chunked + indexed.
                    </p>
                  {:else}
                    <ul>
                      {#each files[c.id] ?? [] as f (f.id)}
                        <li class="group flex items-center gap-2">
                          <button
                            type="button"
                            class="file-btn min-w-0 flex-1"
                            title={`Preview “${f.name}” in a search of this collection`}
                            onclick={() => openPanel(c, f)}
                          >
                            <span class="grid size-7 shrink-0 place-items-center rounded-md" style="background:var(--fill-hover); color:var(--muted)">
                              <FileText class="size-3.5" />
                            </span>
                            <span class="min-w-0 flex-1 truncate text-left text-sm">{f.name}</span>
                          </button>
                          <span class="hidden shrink-0 text-xs tabular-nums text-[var(--muted)] sm:inline">{formatBytes(f.size)}</span>
                          <span class="hidden shrink-0 text-xs text-[var(--muted)] lg:inline">{fmtDate(f.created_at)}</span>
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

                  <!-- upload rows (spinner → progress bar → check per file) -->
                  {#if (uploads[c.id] ?? []).length > 0}
                    <ul class="space-y-2">
                      {#each uploads[c.id] ?? [] as u (u.uid)}
                        <li class="px-0.5" transition:fly={rise(4, 160)}>
                          <div class="flex items-center gap-2.5 text-xs">
                            {#if u.status === 'uploading'}
                              <LoaderCircle class="size-3.5 shrink-0 animate-spin text-[var(--acc)]" />
                            {:else if u.status === 'done'}
                              <CircleCheck class="size-3.5 shrink-0" style="color:var(--ok)" />
                            {:else}
                              <span class="grid size-3.5 shrink-0 place-items-center rounded-full font-bold" style="background:color-mix(in srgb, var(--danger) 15%, transparent); color:var(--danger)">!</span>
                            {/if}
                            <span class="min-w-0 flex-1 truncate text-[var(--muted)]">{u.name}</span>
                            {#if u.status === 'uploading'}
                              <span class="shrink-0 tabular-nums text-[var(--faint)]">{u.pct}%</span>
                            {:else if u.status === 'error'}
                              <span class="shrink-0 truncate" style="color:var(--danger)" title={u.error}>{u.error}</span>
                              <button type="button" class="shrink-0 text-[var(--faint)] hover:text-[var(--text)]" aria-label="Dismiss" onclick={() => dropRow(c.id, u.uid)}>
                                <X class="size-3" />
                              </button>
                            {:else}
                              <span class="shrink-0" style="color:var(--ok)">Indexed</span>
                            {/if}
                          </div>
                          {#if u.status !== 'done'}
                            <div class="up-bar mt-1.5">
                              <span
                                class="up-fill"
                                class:up-err={u.status === 'error'}
                                style="width:{u.status === 'error' ? 100 : u.pct}%"
                              ></span>
                            </div>
                          {/if}
                        </li>
                      {/each}
                    </ul>
                  {/if}

                  <!-- drop zone (click = browse, drop = upload) -->
                  <div
                    class="dropzone"
                    class:dragging={dragCol === c.id}
                    role="button"
                    tabindex="0"
                    aria-label="Add files to {c.name}"
                    onclick={() => inputEls[c.id]?.click()}
                    onkeydown={(e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault();
                        inputEls[c.id]?.click();
                      }
                    }}
                    ondragover={(e) => {
                      e.preventDefault();
                      if (dragCol !== c.id) dragCol = c.id;
                    }}
                    ondragleave={(e) => {
                      if (!e.currentTarget.contains(e.relatedTarget as Node | null)) dragCol = null;
                    }}
                    ondrop={(e) => onDrop(c.id, e)}
                  >
                    <span class="dz-icon" class:dz-hot={dragCol === c.id}><FileUp size={16} /></span>
                    <p class="mt-2 text-sm text-[var(--muted)]">
                      Drop files here or <span class="font-medium text-[var(--text)] underline decoration-[var(--border)] underline-offset-4">browse</span>
                    </p>
                    <p class="mt-0.5 text-xs text-[var(--faint)]">txt · md · csv · json · log · code — up to 5 MiB each</p>
                    <input
                      type="file"
                      multiple
                      accept={ACCEPT}
                      class="hidden"
                      bind:this={inputEls[c.id]}
                      onchange={(e) => onPick(c.id, e.currentTarget)}
                    />
                  </div>

                  <!-- citation slide-over entry -->
                  <div class="flex items-center justify-between gap-3 rounded-xl px-3 py-2.5" style="background:var(--panel-2)">
                    <p class="min-w-0 truncate text-xs text-[var(--muted)]">
                      Click a file to preview what chats will cite — or search everything at once.
                    </p>
                    <Button variant="outline" size="sm" onclick={() => openPanel(c)}>
                      <Search size={13} /> Search
                    </Button>
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

<!-- citation preview: search across one collection, matched terms highlighted -->
<SlideOver
  bind:open={panelOpen}
  title={panelCol?.name ?? 'Collection'}
  subtitle={panelFile ? `Previewing “${panelFile}”` : 'Search across this collection'}
  width="460px"
  onclose={closePanel}
>
  <div class="px-4 py-4">
    <div class="flex gap-2">
      <input
        type="search"
        class="h-8 min-w-0 flex-1 rounded-lg border-0 px-2.5 text-sm outline-none placeholder:text-[var(--faint)] focus-visible:outline-2 focus-visible:-outline-offset-1 focus-visible:outline-[var(--accent-soft)]"
        style="background:var(--panel); color:var(--text)"
        placeholder="Search this collection…"
        bind:value={panelQ}
        bind:this={panelInput}
        onkeydown={(e) => {
          if (e.key === 'Enter') void runPanel();
        }}
      />
      <Button variant="secondary" size="sm" disabled={busy || !panelQ.trim()} onclick={() => void runPanel()}>
        {#if busy}
          <LoaderCircle class="size-3.5 animate-spin" />
        {/if}
        Search
      </Button>
    </div>

    {#if panelErr}
      <p class="mt-3 text-xs" style="color:var(--danger)">{panelErr}</p>
    {/if}

    {#if busy}
      <div class="mt-4 space-y-2.5">
        {#each Array(3) as _i}<Skeleton w="100%" h="3.2rem" rounded="0.75rem" />{/each}
      </div>
    {:else if hits === undefined}
      <p class="mt-4 flex items-center justify-center gap-2 py-6 text-xs text-[var(--faint)]">
        <Search class="size-3.5" /> Type something above — matches are highlighted below.
      </p>
    {:else if hits.length === 0}
      <p class="mt-4 text-xs text-[var(--muted)]">No matches — files may still be indexing.</p>
    {:else}
      <ul class="mt-3 space-y-2">
        {#each hits as hit, i (`${panelId}#${i}`)}
          {@const key = `${panelId}#${i}`}
          <li class="rounded-xl border px-3 py-2.5" style="background:var(--panel); border-color:var(--border)" transition:fly={rise(6, 220, stepDelay(i, 30, 8))}>
            <div class="flex items-center gap-2 text-[11px]">
              <span class="src-chip" class:src-hit={!!panelFile && hit.file_name === panelFile}>
                <FileText size={11} class="shrink-0" />
                {#each segments(hit.file_name || 'chunk', panelRe) as s}
                  <span class={s.hit ? 'kb-mark' : undefined}>{s.text}</span>
                {/each}
              </span>
              <span class="min-w-0 flex-1"></span>
              <span class="h-1 w-14 shrink-0 overflow-hidden rounded-full" style="background:color-mix(in srgb, var(--text) 10%, transparent)">
                <span
                  class="block h-full rounded-full"
                  style="width:{Math.max(4, Math.min(100, Math.round(hit.score * 100)))}%; background:var(--acc)"
                ></span>
              </span>
              <span class="shrink-0 tabular-nums text-[var(--muted)]">{hit.score.toFixed(2)}</span>
            </div>
            <!-- excerpt, click to expand/collapse; query terms marked -->
            <button
              type="button"
              class="mt-1.5 block w-full text-left text-[13px] leading-relaxed whitespace-pre-wrap text-[var(--text)] {panelOpenHit[key] ? '' : 'line-clamp-4'}"
              onclick={() => (panelOpenHit[key] = !panelOpenHit[key])}
            >
              {#each segments(hit.content, panelRe) as s}
                <span class={s.hit ? 'kb-mark' : undefined}>{s.text}</span>
              {/each}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
</SlideOver>

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

<style>
  /* ── accent-tinted icon tiles (the page's only color, per quiet-color law) ── */
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
  .tile-sm {
    width: 2rem;
    height: 2rem;
    border-radius: 0.625rem;
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

  /* ── collection cards: hover lift (+ shadow), token-driven ── */
  .kb-card {
    border: 1px solid var(--border);
    border-radius: 1rem;
    background: var(--panel);
    overflow: hidden;
    transition:
      transform 0.16s ease,
      box-shadow 0.16s ease,
      border-color 0.16s ease;
  }
  .kb-card:hover {
    transform: translateY(-1px);
    border-color: color-mix(in srgb, var(--acc) 28%, var(--border));
    box-shadow: 0 8px 24px rgb(0 0 0 / 0.09);
  }
  :global(html.dark) .kb-card:hover {
    box-shadow: 0 8px 24px rgb(0 0 0 / 0.4);
  }

  .row-btn,
  .file-btn {
    display: flex;
    align-items: center;
    gap: 0.625rem;
    border: 0;
    background: transparent;
    border-radius: 0.625rem;
    cursor: pointer;
    text-align: left;
    color: var(--text);
    padding: 0.25rem 0.375rem;
    margin: -0.25rem -0.375rem;
    transition: background-color 0.12s ease;
  }
  .row-btn:hover,
  .file-btn:hover {
    background: var(--fill-hover);
  }
  .row-btn:focus-visible,
  .file-btn:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--indigo) 55%, transparent);
    outline-offset: 1px;
  }

  .chev {
    color: var(--faint);
    transition:
      transform 0.18s ease,
      color 0.18s ease;
  }
  .chev.open {
    transform: rotate(90deg);
    color: var(--acc);
  }

  /* ── upload progress ── */
  .up-bar {
    height: 3px;
    border-radius: 999px;
    overflow: hidden;
    background: color-mix(in srgb, var(--text) 10%, transparent);
  }
  .up-fill {
    display: block;
    height: 100%;
    border-radius: 999px;
    background: var(--acc);
    transition: width 0.18s ease;
  }
  .up-fill.up-err {
    background: var(--danger);
  }

  /* ── drop zone: dashed, quiet; accent glow while dragging over ── */
  .dropzone {
    border: 1.5px dashed var(--border);
    border-radius: 0.875rem;
    padding: 1.1rem 1rem;
    text-align: center;
    cursor: pointer;
    transition:
      border-color 0.15s ease,
      background-color 0.15s ease,
      box-shadow 0.15s ease;
  }
  .dropzone:hover {
    border-color: color-mix(in srgb, var(--acc) 45%, var(--border));
    background: color-mix(in srgb, var(--acc) 4%, transparent);
  }
  .dropzone:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--indigo) 55%, transparent);
    outline-offset: 2px;
  }
  .dropzone.dragging {
    border-color: var(--acc);
    background: var(--acc-tint);
    box-shadow:
      0 0 0 3px color-mix(in srgb, var(--acc) 12%, transparent),
      0 6px 18px color-mix(in srgb, var(--acc) 20%, transparent);
  }
  .dz-icon {
    display: inline-grid;
    place-items: center;
    width: 2.25rem;
    height: 2.25rem;
    border-radius: 999px;
    background: var(--panel-2);
    color: var(--muted);
    transition:
      transform 0.15s ease,
      background-color 0.15s ease,
      color 0.15s ease;
  }
  .dropzone:hover .dz-icon {
    transform: translateY(-1px);
  }
  .dz-icon.dz-hot {
    background: var(--acc-tint-strong);
    color: var(--acc);
    transform: translateY(-2px) scale(1.05);
  }

  /* ── citation slide-over bits ── */
  .src-chip {
    display: inline-flex;
    min-width: 0;
    max-width: 65%;
    align-items: center;
    gap: 0.3rem;
    border-radius: 999px;
    padding: 0.15rem 0.5rem;
    background: var(--panel-2);
    color: var(--muted);
  }
  .src-chip.src-hit {
    background: var(--acc-tint);
    color: var(--acc);
    border: 1px solid color-mix(in srgb, var(--acc) 30%, transparent);
  }
  .src-chip span {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .kb-mark {
    background: var(--acc-tint-strong);
    color: inherit;
    border-radius: 3px;
    padding: 0 1.5px;
  }
</style>
