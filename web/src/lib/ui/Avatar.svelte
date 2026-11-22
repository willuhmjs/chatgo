<script lang="ts">
  import { initials } from '$lib/ui/chat-grouping';

  let {
    name,
    src = '',
    size = 30,
  }: {
    name: string;
    src?: string;
    size?: number;
  } = $props();
</script>

{#if src}
  <img
    src={src}
    alt={name}
    class="shrink-0 rounded-full object-cover"
    style="width:{size}px; height:{size}px"
  />
{:else}
  <span
    aria-hidden="true"
    class="grid shrink-0 place-items-center rounded-full font-medium text-[var(--muted)] select-none"
    style="width:{size}px; height:{size}px; font-size:{Math.round(size * 0.38)}px; background:var(--panel-2)"
  >
    {initials(name)}
  </span>
{/if}
