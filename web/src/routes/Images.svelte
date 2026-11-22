<script lang="ts">
  // /images — prompt → generate → grid + history (FE-PAGES). BE-SEC imggen
  // lands in parallel; 404 degrades quietly. Response-shape assumptions are
  // documented + normalized in $lib/pages-api.ts (accepts {files:[…]} and
  // {images:[…]} shapes). /api/models is an existing endpoint used via
  // api.listModels() to build the provider dropdown.
  import { onMount } from 'svelte';
  import * as api from '$lib/api';
  import * as pages from '$lib/pages-api';
  import { push } from '$lib/toast.svelte.js';
  import Button from '$lib/ui/Button.svelte';
  import IconButton from '$lib/ui/IconButton.svelte';
  import Select from '$lib/ui/Select.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import { Ban, Image as ImageIcon, LoaderCircle, Sparkles, X } from 'lucide-svelte';

  let models = $state<api.ModelEntry[]>([]);
  let prompt = $state('');
  let providerId = $state('');
  let size = $state('1024');
  let count = $state('1');

  let generating = $state(false);
  let genError = $state('');
  let results = $state<pages.GeneratedImage[]>([]);
  let history = $state<(api.FileMeta | pages.GeneratedImage)[]>([]);
  let historyLoading = $state(true);
  let preview = $state<string | null>(null);
  let abortCtl: AbortController | null = null;

  // Provider options: distinct providers of image-ish models; if no model
  // looks image-capable, every provider (server auto-picks the model).
  const providerOptions = $derived([{ value: '', label: 'Auto' }, ...pages.imageProviderOptions(models)]);

  const historyItems = $derived(history.map((h) => ({ id: h.id || h.url, url: h.url, name: h.name })));

  async function generate() {
    const p = prompt.trim();
    if (!p || generating) return;
    generating = true;
    genError = '';
    abortCtl = new AbortController();
    // backend proxy caps at 120s (docs/FEATURES-V2.md); give it a little slack
    const timer = setTimeout(() => abortCtl?.abort(), 150_000);
    try {
      const imgs = await pages.generateImages(
        { prompt: p, size: Number(size), n: Number(count), providerId: providerId || undefined },
        abortCtl.signal,
      );
      if (imgs.length === 0) push('No images came back — try rephrasing the prompt', 'info');
      results = [...imgs, ...results];
      // newest-generated also belongs in the history grid
      history = [...imgs.map((i) => ({ id: i.id, url: i.url, name: i.name, mime: 'image/png', size: 0, kind: 'image' as const })), ...history];
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') {
        if (abortCtl?.signal.aborted && generating) genError = 'Generation cancelled or timed out.';
      } else if (pages.isMissing(err)) {
        genError = 'Image generation is not available on this server yet.';
      } else {
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

  onMount(async () => {
    try {
      models = await api.listModels();
    } catch {
      models = []; // no models → provider select stays "Auto" only
    }
    try {
      history = await pages.listImages();
    } catch {
      history = []; // 404 (parallel build) or auth hiccup → history section just hides
    } finally {
      historyLoading = false;
    }
  });

  // Esc closes the fullscreen preview
  $effect(() => {
    if (!preview) return;
    const h = (e: KeyboardEvent) => {
      if (e.key === 'Escape') preview = null;
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  });
</script>

<div class="flex h-full flex-col" style="background:var(--bg); color:var(--text)">
  <header class="flex shrink-0 items-center gap-3 border-b px-4 py-3" style="border-color:var(--border)">
    <ImageIcon size={16} class="text-[var(--muted)]" />
    <h1 class="text-[20px] font-semibold tracking-tight">Images</h1>
    {#if historyItems.length > 0}
      <span class="text-xs text-[var(--muted)]">{historyItems.length} generated</span>
    {/if}
  </header>

  <div class="min-h-0 flex-1 overflow-y-auto p-4">
    <div class="mx-auto w-full max-w-4xl space-y-6">
      <!-- prompt card -->
      <div class="rounded-2xl p-3" style="background:var(--panel)">
        <textarea
          rows={3}
          class="w-full resize-y border-0 bg-transparent p-1 text-[15px] leading-relaxed outline-none placeholder:text-[var(--faint)]"
          style="color:var(--text)"
          placeholder="Describe an image… e.g. “watercolor lighthouse at dusk, warm palette”"
          bind:value={prompt}
          onkeydown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) void generate();
          }}
        ></textarea>
        <div class="mt-2 flex flex-wrap items-end gap-3">
          <div class="w-36">
            <Select label="Provider" bind:value={providerId} options={providerOptions} disabled={generating} />
          </div>
          <div class="w-28">
            <Select
              label="Size"
              bind:value={size}
              options={[
                { value: '256', label: '256' },
                { value: '512', label: '512' },
                { value: '1024', label: '1024' },
              ]}
              disabled={generating}
            />
          </div>
          <div class="w-24">
            <Select
              label="Count"
              bind:value={count}
              options={[
                { value: '1', label: '1' },
                { value: '2', label: '2' },
                { value: '3', label: '3' },
                { value: '4', label: '4' },
              ]}
              disabled={generating}
            />
          </div>
          <Button variant="primary" size="md" disabled={!prompt.trim() || generating} onclick={() => void generate()}>
            <Sparkles size={15} /> Generate
          </Button>
          {#if generating}
            <Button variant="outline" size="md" onclick={cancel}>
              <Ban size={14} /> Abort
            </Button>
          {/if}
        </div>
        {#if generating}
          <p class="mt-2.5 flex items-center gap-2 text-sm text-[var(--muted)]">
            <LoaderCircle class="size-4 animate-spin text-[var(--acc)]" /> Generating… can take a minute
          </p>
        {/if}
        {#if genError}
          <p class="mt-2.5 text-sm" style="color:var(--danger)">{genError}</p>
        {/if}
      </div>

      <!-- fresh results -->
      {#if results.length > 0}
        <section>
          <h2 class="mb-2.5 text-xs font-medium text-[var(--muted)]">Results</h2>
          <div class="grid gap-3" style="grid-template-columns:repeat(auto-fill,minmax(9.5rem,1fr))">
            {#each results as img, i (`${img.url}#${i}`)}
              <button
                type="button"
                class="overflow-hidden rounded-xl transition-colors hover:bg-[var(--fill-hover)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--accent-soft)]"
                style="background:var(--panel)"
                aria-label={`Preview ${img.name}`}
                onclick={() => (preview = img.url)}
              >
                <img src={img.url} alt="" class="aspect-square w-full object-cover" loading="lazy" decoding="async" />
              </button>
            {/each}
          </div>
        </section>
      {/if}

      <!-- history -->
      <section>
        <h2 class="mb-2.5 text-xs font-medium text-[var(--muted)]">History</h2>
        {#if historyLoading}
          <div class="grid gap-3" style="grid-template-columns:repeat(auto-fill,minmax(9.5rem,1fr))">
            {#each Array(4) as _i}<Skeleton w="100%" h="9.5rem" rounded="0.75rem" />{/each}
          </div>
        {:else if historyItems.length === 0}
          <EmptyState title="Nothing generated yet" description="Images you generate show up here (newest 50).">
            <icon><ImageIcon size={18} /></icon>
          </EmptyState>
        {:else}
          <div class="grid gap-3" style="grid-template-columns:repeat(auto-fill,minmax(9.5rem,1fr))">
            {#each historyItems as h (h.id)}
              <button
                type="button"
                class="overflow-hidden rounded-xl transition-colors hover:bg-[var(--fill-hover)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--accent-soft)]"
                style="background:var(--panel)"
                aria-label={`Preview ${h.name}`}
                onclick={() => (preview = h.url)}
              >
                <img src={h.url} alt="" class="aspect-square w-full object-cover" loading="lazy" decoding="async" />
              </button>
            {/each}
          </div>
        {/if}
      </section>
    </div>
  </div>
</div>

{#if preview}
  <!-- fullscreen preview (own minimal overlay; backdrop click or Esc closes) -->
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="fixed inset-0 z-50 grid place-items-center bg-black/80 p-6 backdrop-blur-[2px]" role="presentation" onclick={() => (preview = null)}>
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <img
      src={preview}
      alt="Enlarged preview"
      class="pop max-h-full max-w-full rounded-xl"
      onclick={(e) => e.stopPropagation()}
    />
    <span class="absolute right-4 top-4">
      <IconButton label="Close preview" onclick={() => (preview = null)}>
        <X class="size-5" />
      </IconButton>
    </span>
  </div>
{/if}
