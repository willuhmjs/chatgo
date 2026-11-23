<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    label,
    hint,
    children,
  }: {
    label: string;
    hint?: string;
    children: Snippet;
  } = $props();
</script>

<label class="block text-sm">
  <span class="mb-1 block font-medium">{label}</span>
  {@render children()}
  {#if hint}
    <span class="mt-1 block text-xs leading-relaxed text-[var(--muted)]">{hint}</span>
  {/if}
</label>
