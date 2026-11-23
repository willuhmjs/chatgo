<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    kind = 'muted',
    children,
  }: {
    kind?: 'muted' | 'accent' | 'success' | 'danger';
    children: Snippet;
  } = $props();

  const styles: Record<string, string> = {
    muted: 'background:var(--panel-2); color:var(--muted)',
    accent: 'background:var(--panel-2); color:var(--text)',
    success: 'background:color-mix(in srgb, #10a37f 14%, transparent); color:#10a37f',
    danger: 'background:color-mix(in srgb, var(--danger) 14%, transparent); color:var(--danger)',
  };
</script>

<span
  class="inline-flex items-center gap-1 whitespace-nowrap rounded-full px-2 py-0.5 text-[11px] font-medium"
  style={styles[kind]}
>{@render children()}</span>
