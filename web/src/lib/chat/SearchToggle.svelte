<script lang="ts">
  // OWUI web-search toggle: labeled pill (globe + "Search") in the composer
  // footer — far more discoverable than a bare icon. Active state tints the
  // pill with the accent and shows a check. Rendered by Home and Composer
  // only when capabilities.search is true (server probes search_url setting
  // / SEARXNG_URL; see internal/apimgr/admin.go handleMeGet).
  import Globe from 'lucide-svelte/icons/globe';
  import Check from 'lucide-svelte/icons/check';

  let { checked = $bindable(false) }: { checked?: boolean } = $props();
</script>

<button
  type="button"
  role="switch"
  aria-checked={checked}
  class="searchpill"
  class:on={checked}
  title={checked ? 'Web search on — results are cited in the answer' : 'Search the web before answering'}
  onclick={() => (checked = !checked)}
>
  <Globe size={14} style="flex:none" />
  <span>Search</span>
  {#if checked}<Check size={13} style="flex:none" />{/if}
</button>

<style>
  .searchpill {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    height: 1.75rem;
    padding: 0 0.6rem;
    border: 0;
    border-radius: 999px;
    background: transparent;
    color: var(--muted, #a8a8a8);
    font-size: 0.8125rem;
    cursor: pointer;
    transition:
      background 120ms ease,
      color 120ms ease;
  }
  .searchpill:hover {
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
    color: var(--text);
  }
  .searchpill:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--acc, var(--accent)) 60%, transparent);
    outline-offset: 1px;
  }
  .searchpill.on {
    background: color-mix(in srgb, var(--acc, var(--accent, #ffffff)) 14%, transparent);
    color: var(--acc, var(--accent, #ffffff));
  }
  .searchpill.on:hover {
    background: color-mix(in srgb, var(--acc, var(--accent, #ffffff)) 20%, transparent);
    color: var(--acc, var(--accent, #ffffff));
  }
</style>
