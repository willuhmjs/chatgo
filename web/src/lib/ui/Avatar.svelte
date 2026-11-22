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
    class="shrink-0 rounded-lg object-cover"
    style="width:{size}px; height:{size}px"
  />
{:else}
  <span
    aria-hidden="true"
    class="grid shrink-0 place-items-center rounded-lg font-semibold text-white select-none"
    style="width:{size}px; height:{size}px; font-size:{Math.round(size * 0.4)}px; background:linear-gradient(135deg,var(--accent),#8b5cf6)"
  >
    {initials(name)}
  </span>
{/if}
