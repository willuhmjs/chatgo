<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title,
    description = '',
    icon,
    actions,
  }: {
    title: string;
    description?: string;
    icon?: Snippet;
    actions?: Snippet;
  } = $props();
</script>

<div class="flex flex-col items-center gap-2 rounded-xl border border-dashed px-6 py-10 text-center" style="border-color:var(--border)">
  {#if icon}
    <span class="text-[var(--muted)]">{@render icon()}</span>
  {/if}
  <p class="text-sm font-medium">{title}</p>
  {#if description}
    <p class="max-w-sm text-xs leading-relaxed text-[var(--muted)]">{description}</p>
  {/if}
  {#if actions}
    <div class="mt-2 flex items-center gap-2">{@render actions()}</div>
  {/if}
</div>
