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
    muted: 'background:var(--panel-2); color:var(--muted); border:1px solid var(--border)',
    accent: 'background:color-mix(in srgb, var(--accent) 16%, transparent); color:var(--accent)',
    success: 'background:color-mix(in srgb, #34d399 14%, transparent); color:#34d399',
    danger: 'background:color-mix(in srgb, var(--danger) 14%, transparent); color:var(--danger)',
  };
</script>

<span
  class="inline-flex items-center gap-1 whitespace-nowrap rounded-full px-2 py-0.5 text-[11px] font-medium"
  style={styles[kind]}
>{@render children()}</span>
