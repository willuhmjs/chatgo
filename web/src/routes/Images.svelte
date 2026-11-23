<script lang="ts">
  // /images — prompt → generate → gallery (FE-PAGES, POLISH-IMG pass).
  // Endpoints (internal/imggen): POST /api/images/generate {prompt,size,n,
  // provider_id?} → {images:[{id,name,mime,size,url,created_at}]};
  // GET /api/images → newest-50 own images, same shape (no cursor — 'load
  // more' n/a); DELETE /api/images/{id} → 204 (real delete: row + bytes).
  // Server
  // errors are already user-friendly one-liners (response-cap hint, rate
  // limit, "no image-capable provider found — pass provider_id explicitly",
  // upstream credential/429/5xx maps) — we surface them verbatim in an
  // inline banner instead of pre-probing configuration. Shapes are
  // normalized in $lib/pages-api.ts; 404 degrades quietly.
  import { onMount } from 'svelte';
  import * as api from '$lib/api';
  import * as pages from '$lib/pages-api';
  import { push } from '$lib/toast.svelte.js';
  import { confirmDialog } from '$lib/ui/confirm.svelte.js';
  import { relTime } from '$lib/admin/format';
  import Button from '$lib/ui/Button.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Select from '$lib/ui/Select.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import { enter, popIn, reducedMotion, soft, stagger } from '$lib/ui/motion.js';
  import {
    Ban,
    ChevronLeft,
    ChevronRight,
    CircleAlert,
    Download,
    Image as ImageIcon,
    LoaderCircle,
    Sparkles,
    Trash2,
    X,
  } from 'lucide-svelte';

  // mirrors maxPromptLen in internal/imggen/imggen.go
  const MAX_PROMPT = 4000;
  const SIZES = ['256', '512', '1024'];
  const COUNTS = ['1', '2', '3', '4'];
  const EXAMPLES = [
    'a watercolor fox curled up in an autumn forest, soft morning light',
    'cinematic photo of a neon rain-soaked alley at night, shallow depth of field',
    'flat pastel poster of a paper-moon rocket, minimal grain',
    'isometric cozy bookshop interior on a rainy afternoon',
  ];

  // One renderable tile. `session` items were made in this browser tab;
  // both kinds carry a server id, so the trash button is a real DELETE.
  interface Tile {
    id: string;
    url: string;
    name: string;
    /** the prompt that produced it (session items only) */
    prompt: string;
    /** left meta: provider (session) or file name (history) */
    metaL: string;
    /** right meta: relative age (server time when known) */
    metaR: string;
    session: boolean;
    /** index into viewerItems for the lightbox */
    idx: number;
  }

  interface SessionImage extends pages.GeneratedImage {
    prompt: string;
    /** unix seconds, captured client-side at generation */
    at: number;
    provider: string;
  }

  let models = $state<api.ModelEntry[]>([]);
  let prompt = $state('');
  let providerId = $state('');
  let size = $state('1024');
  let count = $state('1');

  let generating = $state(false);
  let genError = $state('');
  let results = $state<SessionImage[]>([]);
  let history = $state<pages.ImageFile[]>([]);
  let historyLoading = $state(true);
  /** urls already removed this session — keeps derived lists honest mid-delete */
  let hidden = $state<Set<string>>(new Set());
  let deleting = $state(false);
  let viewer = $state(-1);
  /** 1s heartbeat while the lightbox is open / results age */
  let tick = $state(Date.now());

  let ta = $state<HTMLTextAreaElement | undefined>();
  let scroller = $state<HTMLDivElement | undefined>();

  // Provider options: distinct providers of image-ish models; if no model
  // looks image-capable, every provider (server auto-picks the model).
  const providerOptions = $derived([{ value: '', label: 'Auto' }, ...pages.imageProviderOptions(models)]);
  const providerLabel = $derived(
    providerOptions.find((o) => o.value === providerId)?.label ?? 'Auto',
  );
  const n = $derived(Number(count));

  const sessionTiles = $derived<Tile[]>(results.map((r, i) => ({
    id: r.id,
    url: r.url,
    name: r.name,
    prompt: r.prompt,
    metaL: r.provider,
    metaR: r.at ? ago(r.at) : '',
    session: true,
    idx: i,
  })));
  const historyTiles = $derived<Tile[]>(
    history
      .filter((h) => !hidden.has(h.url) && !results.some((r) => r.url === h.url))
      .map((h, i) => ({
        id: h.id,
        url: h.url,
        name: h.name,
        prompt: '',
        metaL: h.name,
        metaR: h.created_at ? ago(h.created_at) : sizeLabel(h.size),
        session: false,
        idx: sessionTiles.length + i,
      })),
  );
  const viewerItems = $derived<Tile[]>([...sessionTiles, ...historyTiles]);

  /** relTime() reads Date.now() internally; touching `tick` re-anchors it. */
  function ago(ts: number): string {
    void tick;
    return relTime(ts);
  }

  function sizeLabel(bytes: number): string {
    if (!bytes) return '';
    if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} MB`;
    return `${Math.max(1, Math.round(bytes / 1024))} KB`;
  }

  function autosize() {
    if (!ta) return;
    ta.style.height = 'auto';
    ta.style.height = `${Math.min(ta.scrollHeight, 280)}px`;
  }

  function useExample(p: string) {
    prompt = p;
    scroller?.scrollTo({ top: 0, behavior: reducedMotion() ? 'auto' : 'smooth' });
    ta?.focus();
    autosize();
  }

  let abortCtl: AbortController | null = null;

  async function generate() {
    const p = prompt.trim();
    if (!p || generating) return;
    generating = true;
    genError = '';
    const via = providerLabel;
    const ctl = (abortCtl = new AbortController());
    // backend proxy caps at 120s (internal/imggen generateTimeout); slack it
    const timer = setTimeout(() => ctl.abort(), 150_000);
    try {
      const imgs = await pages.generateImages(
        { prompt: p, size: Number(size), n: Number(count), providerId: providerId || undefined },
        ctl.signal,
      );
      if (imgs.length === 0) {
        push('No images came back — try rephrasing the prompt', 'info');
      } else {
        const at = Math.floor(Date.now() / 1000);
        results = [
          ...imgs.map((im) => ({ ...im, prompt: p, at, provider: via })),
          ...results,
        ];
        push(imgs.length === 1 ? 'Image generated' : `${imgs.length} images generated`, 'success');
      }
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') {
        if (ctl.signal.aborted && generating) genError = 'Generation cancelled or timed out.';
      } else if (pages.isMissing(err)) {
        genError = 'Image generation is not available on this server yet.';
      } else {
        // cap hint, rate limit, provider probing etc. — server text is the UX
        genError = pages.errMessage(err, 'Generation failed');
      }
    } finally {
      clearTimeout(timer);
      abortCtl = null;
      generating = false;
    }
  }

  function cancel() {
    abortCtl?.abort();
  }

  /** Drop a url from every local list (tiles, session results, history). */
  function dropLocal(url: string) {
    if (viewer >= 0) closeViewer();
    hidden = new Set([...hidden, url]);
    results = results.filter((r) => r.url !== url);
    history = history.filter((h) => h.url !== url);
  }

  async function remove(t: Tile) {
    // No server id (defensive: legacy payload) → page-local removal only.
    if (!t.id) {
      dropLocal(t.url);
      push('Removed from this page', 'info');
      return;
    }
    const ok = await confirmDialog({
      title: 'Delete image?',
      message:
        'This permanently deletes the image and its stored file from your library. This cannot be undone.',
      confirmLabel: 'Delete',
      danger: true,
    });
    if (!ok || deleting) return;
    deleting = true;
    try {
      await pages.deleteImage(t.id);
      dropLocal(t.url);
      push('Image deleted', 'success');
    } catch (err) {
      push(pages.errMessage(err, 'Delete failed'), 'error');
    } finally {
      deleting = false;
    }
  }

  /** Focus the lightbox overlay on mount so Esc/arrows land immediately. */
  function focusOnce(node: HTMLDivElement) {
    node.focus();
  }

  function openViewer(i: number) {
    viewer = i;
  }
  function closeViewer() {
    viewer = -1;
  }
  function nav(dir: number) {
    const c = viewerItems.length;
    if (c < 2) return;
    viewer = (viewer + dir + c) % c;
  }

  onMount(async () => {
    try {
      models = await api.listModels();
    } catch {
      models = []; // no models → provider select stays "Auto" only
    }
    try {
      history = await pages.listImages();
    } catch {
      history = []; // 404 (parallel build) or auth hiccup → gallery just empties
    } finally {
      historyLoading = false;
    }
  });

  // lightbox keyboard + scroll-lock; heartbeat also keeps "2m ago" honest
  $effect(() => {
    const open = viewer >= 0;
    if (!open && results.length === 0) return;
    const iv = setInterval(() => (tick = Date.now()), open ? 1000 : 30_000);
    if (!open) return () => clearInterval(iv);
    const prev = document.activeElement as HTMLElement | null;
    document.body.style.overflow = 'hidden';
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') closeViewer();
      else if (e.key === 'ArrowLeft') nav(-1);
      else if (e.key === 'ArrowRight') nav(1);
    };
    window.addEventListener('keydown', onKey);
    return () => {
      clearInterval(iv);
      window.removeEventListener('keydown', onKey);
      document.body.style.overflow = '';
      prev?.focus?.();
    };
  });
</script>

<div class="flex h-full flex-col" style="background:var(--bg); color:var(--text)">
  <header class="flex shrink-0 items-center gap-3 border-b px-4 py-3" style="border-color:var(--border)">
    <span class="grid size-7 place-items-center rounded-full" style="background:var(--acc-tint)">
      <ImageIcon size={14} style="color:var(--acc)" />
    </span>
    <h1 class="text-[20px] font-semibold tracking-tight">Images</h1>
    {#if !historyLoading && viewerItems.length > 0}
      <span
        class="inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs text-[var(--muted)]"
        style="border-color:var(--border)"
      >
        <span class="size-1.5 rounded-full" style="background:var(--acc)"></span>
        {viewerItems.length} generated
      </span>
    {/if}
  </header>

  <div bind:this={scroller} class="min-h-0 flex-1 overflow-y-auto p-4 sm:p-6">
    <div class="mx-auto w-full max-w-4xl space-y-8">
      <!-- generation panel -->
      <section
        class="rounded-2xl border p-4 transition-shadow duration-200 focus-within:shadow-[0_0_0_3px_var(--acc-tint)]"
        style="background:var(--panel); border-color:var(--border)"
        transition:soft
      >
        <textarea
          bind:this={ta}
          rows={3}
          maxlength={MAX_PROMPT}
          class="w-full resize-none border-0 bg-transparent p-1 text-[15px] leading-relaxed outline-none placeholder:text-[var(--faint)]"
          style="color:var(--text)"
          placeholder="Describe an image… e.g. “watercolor lighthouse at dusk, warm palette” (⌘/Ctrl+Enter to generate)"
          bind:value={prompt}
          oninput={autosize}
          onkeydown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) void generate();
          }}
        ></textarea>

        <div class="mt-3 flex flex-wrap items-end gap-x-4 gap-y-3">
          <div class="w-36">
            <Select label="Provider" bind:value={providerId} options={providerOptions} disabled={generating} />
          </div>
          <div class="flex flex-col gap-1">
            <span class="text-xs font-medium text-[var(--muted)]">Size</span>
            <div class="seg" role="group" aria-label="Image size">
              {#each SIZES as s (s)}
                <button
                  type="button"
                  class="seg-btn"
                  aria-pressed={size === s}
                  disabled={generating}
                  onclick={() => (size = s)}
                >{s}</button>
              {/each}
            </div>
          </div>
          <div class="flex flex-col gap-1">
            <span class="text-xs font-medium text-[var(--muted)]">Images</span>
            <div class="seg" role="group" aria-label="Number of images">
              {#each COUNTS as c (c)}
                <button
                  type="button"
                  class="seg-btn"
                  aria-pressed={count === c}
                  disabled={generating}
                  onclick={() => (count = c)}
                >{c}</button>
              {/each}
            </div>
          </div>
          <div class="ml-auto flex items-center gap-2">
            {#if generating}
              <Button variant="outline" size="md" onclick={cancel}><Ban size={14} /> Abort</Button>
            {/if}
            <Button variant="primary" size="md" disabled={!prompt.trim() || generating} onclick={() => void generate()}>
              {#if generating}
                <LoaderCircle size={15} class="animate-spin" /> Generating…
              {:else}
                <Sparkles size={15} /> Generate
              {/if}
            </Button>
          </div>
        </div>

        <div class="mt-2 flex items-center justify-between gap-3">
          <span class="text-xs text-[var(--faint)]">
            {#if generating}
              {n > 1 ? `${n} images` : 'One image'} via {providerLabel} — typically 10–60 seconds
            {:else}
              sent as size {size}, {n > 1 ? `${n}×` : 'once'}
            {/if}
          </span>
          {#if prompt.length > 0}
            <span
              class="text-xs tabular-nums"
              style="color:{prompt.length >= MAX_PROMPT ? 'var(--danger)' : 'var(--faint)'}"
            >{prompt.length.toLocaleString()} / {MAX_PROMPT.toLocaleString()}</span>
          {/if}
        </div>

        {#if genError}
          <div
            class="mt-3 flex items-start gap-2 rounded-xl border px-3 py-2.5 text-sm leading-relaxed"
            style="border-color:color-mix(in srgb, var(--danger) 30%, transparent); background:color-mix(in srgb, var(--danger) 7%, transparent)"
            role="alert"
            transition:soft
          >
            <CircleAlert size={15} class="mt-0.5 shrink-0" style="color:var(--danger)" />
            <span>{genError}</span>
          </div>
        {/if}
      </section>

      <!-- generating: shimmer tiles stand in for the coming grid -->
      {#if generating}
        <section aria-busy="true" aria-label="Generating images">
          <h2 class="mb-2.5 flex items-center gap-2 text-xs font-medium text-[var(--muted)]">
            <LoaderCircle size={12} class="animate-spin" style="color:var(--acc)" /> Painting…
          </h2>
          <div class="tiles">
            {#each Array(n) as _, i}
              <div class="gen-tile"><span class="sheen" style="animation-delay:{stagger(i, 180)}ms"></span></div>
            {/each}
          </div>
        </section>
      {/if}

      <!-- fresh from this session -->
      {#if sessionTiles.length > 0}
        <section>
          <h2 class="mb-2.5 text-xs font-medium text-[var(--muted)]">This session</h2>
          <div class="tiles">
            {#each sessionTiles as t, i (t.url)}
              <figure class="tile" transition:enter={{ delay: stagger(i) }}>
                <button
                  type="button"
                  class="tile-open"
                  aria-label={`Open image: ${t.prompt || t.name}`}
                  onclick={() => openViewer(t.idx)}
                >
                  <img src={t.url} alt={t.prompt || t.name} loading="lazy" decoding="async" />
                  <span class="tile-cap">
                    <span class="line-clamp-2">{t.prompt || t.name}</span>
                  </span>
                </button>
                <div class="tile-actions">
                  <a class="tile-btn" href={t.url} download={t.name} aria-label="Download image" title="Download">
                    <Download size={14} />
                  </a>
                  <button type="button" class="tile-btn" aria-label="Delete image" title="Delete" onclick={() => void remove(t)}
                    disabled={deleting}>
                    <Trash2 size={14} />
                  </button>
                </div>
                <figcaption class="tile-meta">
                  <span class="truncate">{t.metaL}</span>
                  <span class="shrink-0">{t.metaR}</span>
                </figcaption>
              </figure>
            {/each}
          </div>
        </section>
      {/if}

      <!-- stored gallery -->
      <section>
        <h2 class="mb-2.5 text-xs font-medium text-[var(--muted)]">Gallery</h2>
        {#if historyLoading}
          <div class="tiles">
            {#each Array(4) as _i}<Skeleton w="100%" h="10rem" rounded="0.875rem" />{/each}
          </div>
        {:else if viewerItems.length === 0}
          <div class="rounded-2xl border" style="border-color:var(--border)" transition:soft>
            <EmptyState
              title="No images yet"
              description="Describe something above — or start from one of these:"
            >
              <div class="mt-2 flex flex-wrap justify-center gap-2">
                {#each EXAMPLES as ex}
                  <button type="button" class="chip" onclick={() => useExample(ex)}>{ex}</button>
                {/each}
              </div>
            </EmptyState>
          </div>
        {:else if historyTiles.length > 0}
          <div class="tiles">
            {#each historyTiles as t, i (t.url)}
              <figure class="tile" transition:enter={{ delay: stagger(i) }}>
                <button
                  type="button"
                  class="tile-open"
                  aria-label={`Open image: ${t.name}`}
                  onclick={() => openViewer(t.idx)}
                >
                  <img src={t.url} alt={t.name} loading="lazy" decoding="async" />
                  <span class="tile-cap">
                    <span class="line-clamp-2">{t.name}</span>
                  </span>
                </button>
                <div class="tile-actions">
                  <a class="tile-btn" href={t.url} download={t.name} aria-label="Download image" title="Download">
                    <Download size={14} />
                  </a>
                  <button type="button" class="tile-btn" aria-label="Delete image" title="Delete" onclick={() => void remove(t)}
                    disabled={deleting}>
                    <Trash2 size={14} />
                  </button>
                </div>
                <figcaption class="tile-meta">
                  <span class="truncate">{t.metaL}</span>
                  <span class="shrink-0">{t.metaR}</span>
                </figcaption>
              </figure>
            {/each}
          </div>
        {:else}
          <p class="text-sm text-[var(--faint)]">Everything above is from this session — reload brings back your saved gallery.</p>
        {/if}
      </section>
    </div>
  </div>
</div>

<!-- lightbox -->
{#if viewer >= 0 && viewerItems.length > 0}
  {@const it = viewerItems[Math.min(viewer, viewerItems.length - 1)]}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="lb"
    role="dialog"
    aria-modal="true"
    aria-label="Image preview"
    tabindex="-1"
    use:focusOnce
    transition:soft
    onclick={closeViewer}
  >
    <button type="button" class="lb-x" aria-label="Close preview" onclick={(e) => { e.stopPropagation(); closeViewer(); }}>
      <X size={18} />
    </button>
    {#if viewerItems.length > 1}
      <button type="button" class="lb-nav lb-prev" aria-label="Previous image" onclick={(e) => { e.stopPropagation(); nav(-1); }}>
        <ChevronLeft size={20} />
      </button>
      <button type="button" class="lb-nav lb-next" aria-label="Next image" onclick={(e) => { e.stopPropagation(); nav(1); }}>
        <ChevronRight size={20} />
      </button>
      <span class="lb-count" aria-live="polite">{Math.min(viewer + 1, viewerItems.length)} / {viewerItems.length}</span>
    {/if}
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    {#key it.url}
      <figure class="lb-fig" transition:popIn onclick={(e) => e.stopPropagation()}>
        <img src={it.url} alt={it.prompt || it.name} />
        <figcaption class="lb-cap">
          <span class="line-clamp-2 flex-1">{it.prompt || it.name}</span>
          <span class="lb-meta">{[it.metaL, it.metaR].filter(Boolean).join(' · ')}</span>
          <a class="lb-dl" href={it.url} download={it.name} aria-label="Download image">
            <Download size={14} /> Download
          </a>
        </figcaption>
      </figure>
    {/key}
  </div>
{/if}

<style>
  /* ---------- tile grid ---------- */
  .tiles {
    display: grid;
    gap: 0.75rem;
    grid-template-columns: repeat(auto-fill, minmax(10rem, 1fr));
  }
  .tile {
    margin: 0;
    position: relative;
    border-radius: 0.875rem;
    overflow: hidden;
    background: var(--panel);
    border: 1px solid var(--border);
    transition:
      box-shadow 0.15s ease,
      border-color 0.15s ease;
  }
  .tile:hover,
  .tile:focus-within {
    border-color: var(--acc-tint-strong);
    box-shadow: 0 0 0 3px var(--acc-tint), var(--shadow-menu);
  }
  .tile-open {
    display: block;
    width: 100%;
    padding: 0;
    border: 0;
    background: none;
    cursor: zoom-in;
    position: relative;
  }
  .tile-open img {
    display: block;
    width: 100%;
    aspect-ratio: 1;
    object-fit: cover;
  }
  /* prompt snippet, quiet until hover */
  .tile-cap {
    position: absolute;
    inset-inline: 0;
    bottom: 0;
    display: block;
    padding: 1.75rem 0.625rem 0.5rem;
    font-size: 0.75rem;
    line-height: 1.35;
    text-align: left;
    color: #fff;
    background: linear-gradient(to top, rgba(0, 0, 0, 0.62), transparent);
    opacity: 0;
    transform: translateY(4px);
    transition:
      opacity 0.15s ease,
      transform 0.15s ease;
    pointer-events: none;
  }
  .tile:hover .tile-cap,
  .tile:focus-within .tile-cap {
    opacity: 1;
    transform: translateY(0);
  }
  .tile-actions {
    position: absolute;
    top: 0.375rem;
    right: 0.375rem;
    display: flex;
    gap: 0.25rem;
    opacity: 0;
    transition: opacity 0.15s ease;
  }
  .tile:hover .tile-actions,
  .tile:focus-within .tile-actions {
    opacity: 1;
  }
  .tile-btn {
    display: grid;
    place-items: center;
    width: 1.75rem;
    height: 1.75rem;
    border-radius: 0.5rem;
    border: 1px solid rgba(255, 255, 255, 0.14);
    color: #e8e8e8;
    background: rgba(0, 0, 0, 0.55);
    cursor: pointer;
    transition:
      background-color 0.12s ease,
      color 0.12s ease;
  }
  .tile-btn:hover {
    background: rgba(0, 0, 0, 0.75);
    color: #fff;
  }
  .tile-meta {
    display: flex;
    justify-content: space-between;
    gap: 0.5rem;
    padding: 0.4rem 0.55rem;
    font-size: 0.6875rem;
    color: var(--faint);
  }

  /* ---------- generation panel bits ---------- */
  .seg {
    display: inline-flex;
    gap: 2px;
    padding: 2px;
    border-radius: 999px;
    background: var(--panel-2);
    border: 1px solid var(--border);
  }
  .seg-btn {
    padding: 0.3rem 0.7rem;
    border: 0;
    border-radius: 999px;
    background: transparent;
    font-size: 0.8125rem;
    line-height: 1.2;
    color: var(--muted);
    cursor: pointer;
    transition:
      background-color 0.12s ease,
      color 0.12s ease,
      box-shadow 0.12s ease;
  }
  .seg-btn:hover:not(:disabled) {
    color: var(--text);
  }
  .seg-btn[aria-pressed='true'] {
    background: var(--bg);
    color: var(--text);
    box-shadow:
      0 0 0 1px var(--acc-tint-strong),
      0 1px 2px rgba(0, 0, 0, 0.12);
  }
  .seg-btn:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }
  .seg-btn:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--indigo) 60%, transparent);
    outline-offset: 1px;
  }
  .chip {
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    padding: 0.375rem 0.75rem;
    border-radius: 999px;
    border: 1px solid var(--border);
    background: var(--panel-2);
    color: var(--muted);
    font-size: 0.8125rem;
    cursor: pointer;
    transition:
      border-color 0.12s ease,
      color 0.12s ease,
      box-shadow 0.12s ease;
  }
  .chip:hover {
    color: var(--text);
    border-color: var(--acc-tint-strong);
    box-shadow: 0 0 0 3px var(--acc-tint);
  }
  .chip:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--indigo) 60%, transparent);
  }

  /* ---------- generating shimmer (CSS-only; guarded) ---------- */
  .gen-tile {
    position: relative;
    aspect-ratio: 1;
    overflow: hidden;
    border-radius: 0.875rem;
    background: var(--panel);
    border: 1px solid var(--border);
  }
  .sheen {
    position: absolute;
    inset: 0;
    transform: translateX(-100%);
    background: linear-gradient(
      100deg,
      transparent 30%,
      color-mix(in srgb, var(--text) 8%, transparent) 50%,
      transparent 70%
    );
    animation: imggen-shimmer 1.5s ease-in-out infinite;
  }
  @keyframes imggen-shimmer {
    to {
      transform: translateX(100%);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .sheen {
      animation: none;
      transform: none;
      opacity: 0.5;
    }
  }

  /* ---------- lightbox ---------- */
  .lb {
    position: fixed;
    inset: 0;
    z-index: 50;
    display: grid;
    place-items: center;
    padding: 1.5rem;
    background: rgba(0, 0, 0, 0.78);
    backdrop-filter: blur(6px);
  }
  .lb:focus-visible {
    outline: none;
  }
  .lb-fig {
    margin: 0;
    max-width: min(92vw, 72rem);
    display: flex;
    flex-direction: column;
    gap: 0.625rem;
  }
  .lb-fig img {
    display: block;
    max-height: 76vh;
    max-width: 100%;
    margin: 0 auto;
    border-radius: 0.875rem;
    box-shadow: 0 24px 80px rgba(0, 0, 0, 0.6);
  }
  .lb-cap {
    display: flex;
    align-items: center;
    gap: 0.875rem;
    color: rgba(255, 255, 255, 0.86);
    font-size: 0.8125rem;
    line-height: 1.4;
  }
  .lb-meta {
    flex-shrink: 0;
    color: rgba(255, 255, 255, 0.55);
    font-size: 0.75rem;
  }
  .lb-dl {
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding: 0.375rem 0.75rem;
    border-radius: 999px;
    border: 1px solid rgba(255, 255, 255, 0.22);
    color: #fff;
    font-size: 0.75rem;
    transition: background-color 0.12s ease;
  }
  .lb-dl:hover {
    background: rgba(255, 255, 255, 0.14);
  }
  .lb-x,
  .lb-nav {
    position: absolute;
    display: grid;
    place-items: center;
    border-radius: 999px;
    border: 1px solid rgba(255, 255, 255, 0.18);
    background: rgba(255, 255, 255, 0.08);
    color: #fff;
    cursor: pointer;
    transition: background-color 0.12s ease;
  }
  .lb-x:hover,
  .lb-nav:hover {
    background: rgba(255, 255, 255, 0.18);
  }
  .lb-x {
    top: 1rem;
    right: 1rem;
    width: 2.25rem;
    height: 2.25rem;
  }
  .lb-nav {
    top: 50%;
    transform: translateY(-50%);
    width: 2.5rem;
    height: 2.5rem;
  }
  .lb-prev {
    left: 0.75rem;
  }
  .lb-next {
    right: 0.75rem;
  }
  .lb-count {
    position: absolute;
    top: 1.25rem;
    left: 50%;
    transform: translateX(-50%);
    font-size: 0.75rem;
    color: rgba(255, 255, 255, 0.6);
    font-variant-numeric: tabular-nums;
  }
</style>
