<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title,
    description = '',
    icon,
    children,
  }: {
    title: string;
    description?: string;
    icon?: Snippet;
    /** Optional action area (buttons/links). */
    children?: Snippet;
  } = $props();
</script>

<div class="flex flex-col items-center gap-2 px-6 py-12 text-center">
  {#if icon}
    <span
      class="mb-1 grid size-11 place-items-center rounded-xl border text-[var(--muted)]"
      style="background:var(--panel-2); border-color:var(--border)"
    >
      {@render icon()}
    </span>
  {/if}
  <p class="text-sm font-medium">{title}</p>
  {#if description}
    <p class="max-w-xs text-xs leading-relaxed text-[var(--muted)]">{description}</p>
  {/if}
  {@render children?.()}
</div>
