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
      class="mb-1 grid size-10 place-items-center rounded-full text-[var(--muted)]"
      style="background:var(--panel-2)"
    >
      {@render icon()}
    </span>
  {/if}
  <p class="text-[15px] font-medium text-[var(--text)]">{title}</p>
  {#if description}
    <p class="max-w-xs text-sm leading-relaxed text-[var(--muted)]">{description}</p>
  {/if}
  {@render children?.()}
</div>
