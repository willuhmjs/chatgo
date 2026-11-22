<script lang="ts">
  // /gallery — artifacts + generated images (FE-extra).
  // Shows files with kind `image`/`artifact` (uploads are inputs, not output,
  // so they're excluded). Thumbnails use the original /files URL: the backend
  // keeps a 512px .thumb on disk but CONTRACTS.md exposes no thumb endpoint —
  // see $lib/extra-api.ts thumbURL() //INTEGRATION. <img> works because
  // /files/{id}/{name} is same-origin and sends the session cookie.
  import * as api from '$lib/api';
  import { navigate } from '$lib/router.svelte.js';
  import { onMount } from 'svelte';
  import { isImageFile, isTextFile, formatBytes, thumbURL, fetchTextPreview } from '$lib/extra-api';
  import { fail as toastFail } from '$lib/extra/toasts';
  import { Download, File, FileText, Images, LayoutGrid, Rows3, Sparkles, X } from 'lucide-svelte';

  let files = $state<api.FileMeta[]>([]);
  let loading = $state(true);
  let filter = $state<'all' | 'images' | 'files'>('all');
  let density = $state<'cozy' | 'compact'>('cozy');
  let sel = $state<api.FileMeta | null>(null);
  let preview = $state<{ text: string; loading: boolean; error: string } | null>(null);

  const outputs = $derived(files.filter((f) => f.kind !== 'upload'));
  const shown = $derived(
    filter === 'all'
      ? outputs
      : outputs.filter((f) => (filter === 'images' ? isImageFile(f) : !isImageFile(f))),
  );
  const nImages = $derived(outputs.filter(isImageFile).length);
  const nFiles = $derived(outputs.length - nImages);
  const chips = $derived([
    { key: 'all' as const, label: `All (${outputs.length})` },
    { key: 'images' as const, label: `Images (${nImages})` },
    { key: 'files' as const, label: `Files (${nFiles})` },
  ]);

  async function load() {
    loading = true;
    try {
      files = await api.listFiles(); // all kinds; filtered client-side
    } catch (err) {
      toastFail(err instanceof api.ApiError ? err.message : 'Failed to load gallery');
    } finally {
      loading = false;
    }
  }

  function openFile(f: api.FileMeta) {
    sel = f;
    preview = null;
    if (!isImageFile(f) && isTextFile(f)) {
      preview = { text: '', loading: true, error: '' };
      fetchTextPreview(f.url)
        .then((text) => (preview = { text, loading: false, error: '' }))
        .catch(() => (preview = { text: '', loading: false, error: 'Preview failed — download instead.' }));
    }
  }
  function closeFile() {
    sel = null;
    preview = null;
  }

  $effect(() => {
    if (!sel) return;
    const h = (e: KeyboardEvent) => {
      if (e.key === 'Escape') closeFile();
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  });

  onMount(load);
</script>

<div class="flex h-full flex-col" style="background:var(--bg); color:var(--text)">
  <header class="flex shrink-0 flex-wrap items-center gap-2 border-b px-4 py-3" style="border-color:var(--border)">
    <Images size={15} class="text-[var(--muted)]" />
    <h1 class="text-[15px] font-medium tracking-tight">Gallery</h1>
    <span class="text-xs text-[var(--muted)]">{shown.length} item{shown.length === 1 ? '' : 's'}</span>

    <!-- quiet filter pills; active = white fill -->
    <div class="ml-3 flex items-center gap-1.5">
      {#each chips as c (c.key)}
        <button
          class="rounded-full px-3 py-1 text-xs transition-colors {filter === c.key ? 'font-medium' : 'text-[var(--muted)] hover:bg-[var(--fill-hover)]'}"
          style={filter === c.key ? 'background:var(--accent); color:var(--accent-fg)' : 'background:var(--panel)'}
          aria-pressed={filter === c.key}
          onclick={() => (filter = c.key)}
        >
          {c.label}
        </button>
      {/each}
    </div>

    <!-- density: quiet segmented pill -->
    <div class="ml-auto flex items-center gap-0.5 rounded-full p-0.5" style="background:var(--panel)">
      <button
        class="rounded-full p-1.5 transition-colors"
        style={density === 'cozy' ? 'background:var(--panel-2); color:var(--text)' : 'color:var(--muted)'}
        aria-pressed={density === 'cozy'}
        title="Comfortable grid"
        onclick={() => (density = 'cozy')}
      >
        <LayoutGrid size={14} />
      </button>
      <button
        class="rounded-full p-1.5 transition-colors"
        style={density === 'compact' ? 'background:var(--panel-2); color:var(--text)' : 'color:var(--muted)'}
        aria-pressed={density === 'compact'}
        title="Compact masonry"
        onclick={() => (density = 'compact')}
      >
        <Rows3 size={14} />
      </button>
    </div>
  </header>

  <div class="min-h-0 flex-1 overflow-y-auto p-4">
    {#if loading}
      <p class="py-16 text-center text-sm text-[var(--muted)]">Loading…</p>
    {:else if shown.length === 0}
      <div class="mx-auto max-w-sm py-16 text-center">
        <Sparkles size={22} class="mx-auto mb-3 text-[var(--muted)]" />
        <p class="text-sm font-medium">
          {outputs.length === 0 ? 'Nothing here yet' : 'Nothing matches this filter'}
        </p>
        <p class="mt-1 text-xs text-[var(--muted)]">
          {outputs.length === 0
            ? 'Images generated in chats and files you upload will show up here.'
            : 'Try the other filter chip.'}
        </p>
        {#if outputs.length === 0}
          <button
            class="mt-5 rounded-[10px] px-4 py-2 text-sm font-medium"
            style="background:var(--accent); color:var(--accent-fg)"
            onclick={() => navigate('/')}
          >
            Start a chat
          </button>
        {/if}
      </div>
    {:else}
      <!-- masonry-lite: CSS multi-column flow. Cards are borderless and take
           a quiet fill on hover (§3-style). -->
      <div style="column-width:{density === 'compact' ? '10.5rem' : '15rem'}; column-gap:0.75rem">
        {#each shown as f (f.id)}
          <button
            class="card mb-3 block w-full break-inside-avoid overflow-hidden rounded-[14px] text-left align-top"
            onclick={() => openFile(f)}
          >
            {#if isImageFile(f)}
              <img src={thumbURL(f)} alt={f.name} class="w-full" loading="lazy" decoding="async" />
              <p class="truncate px-2.5 py-1.5 text-xs text-[var(--muted)]">{f.name}</p>
            {:else}
              <div class="flex items-center gap-3 px-3 py-3.5">
                <span class="shrink-0 text-[var(--muted)]">
                  {#if f.mime === 'application/pdf' || isTextFile(f)}
                    <FileText size={22} />
                  {:else}
                    <File size={22} />
                  {/if}
                </span>
                <span class="min-w-0">
                  <span class="block truncate text-xs font-medium">{f.name}</span>
                  <span class="block truncate text-[11px] text-[var(--muted)]"
                    >{f.mime || 'file'} · {formatBytes(f.size)}</span
                  >
                </span>
              </div>
            {/if}
          </button>
        {/each}
      </div>
    {/if}
  </div>
</div>

{#if sel}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="fixed inset-0 z-50 bg-black/50" role="presentation" onclick={closeFile}>
    <!-- side sheet (§7): --panel, rounded, shadow-menu, quiet header -->
    <!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role -->
    <aside
      class="sheet absolute right-0 top-0 flex h-full w-full max-w-2xl flex-col"
      style="background:var(--panel); color:var(--text); box-shadow:var(--shadow-menu)"
      role="dialog"
      aria-modal="true"
      tabindex="-1"
      aria-label={sel.name}
      onclick={(e) => e.stopPropagation()}
    >
      <div class="flex shrink-0 items-center gap-3 px-4 py-3">
        <div class="min-w-0 flex-1">
          <p class="truncate text-sm font-medium">{sel.name}</p>
          <p class="truncate text-xs text-[var(--muted)]">{sel.mime || 'file'} · {formatBytes(sel.size)}</p>
        </div>
        <!-- download only: sharing an artifact URL is a separate feature -->
        <a
          class="flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition-colors hover:bg-[var(--fill-hover)]"
          href={sel.url}
          download={sel.name}
        >
          <Download size={13} /> Download
        </a>
        <button
          class="rounded-lg p-1.5 text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)]"
          aria-label="Close preview"
          onclick={closeFile}
        >
          <X size={16} />
        </button>
      </div>

      <div class="min-h-0 flex-1 overflow-auto px-4 pb-4">
        {#if isImageFile(sel)}
          <img src={sel.url} alt={sel.name} class="mx-auto max-h-full max-w-full rounded-xl" />
        {:else if preview}
          {#if preview.loading}
            <p class="py-10 text-center text-sm text-[var(--muted)]">Loading preview…</p>
          {:else if preview.error}
            <p class="py-10 text-center text-sm" style="color:var(--danger)">{preview.error}</p>
          {:else}
            <pre
              class="rounded-xl p-3 text-xs leading-relaxed whitespace-pre-wrap"
              style="background:var(--bg-deep); color:var(--text); font-family:ui-monospace, Menlo, monospace"
            >{preview.text}</pre>
          {/if}
        {:else}
          <div class="py-16 text-center text-sm text-[var(--muted)]">
            <FileText size={28} class="mx-auto mb-3" />
            No inline preview for this file type — use Download.
          </div>
        {/if}
      </div>
    </aside>
  </div>
{/if}

<style>
  .card {
    transition: background 150ms ease;
  }
  .card:hover {
    background: var(--fill-hover);
  }
  .card:focus-visible {
    outline: 2px solid var(--accent-soft);
    outline-offset: 2px;
  }
  .sheet {
    animation: sheet-in 160ms ease-out;
  }
  @keyframes sheet-in {
    from {
      opacity: 0;
      transform: translateY(4px);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .sheet {
      animation: none;
    }
  }
</style>
