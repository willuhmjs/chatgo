<script lang="ts">
  // ChatView placeholder — FE-chat owns the real view (streaming, markdown,
  // composer, param drawer). Scaffold keeps the route mounted + styled.
  let { id }: { id: string } = $props();
</script>

<div class="mx-auto flex h-full w-full max-w-[48rem] flex-col items-center justify-center gap-2 px-6 text-center">
  <p class="text-sm text-[var(--muted)]">Chat <span class="font-mono text-[var(--text)]">{id}</span></p>
  <p class="text-xs text-[var(--muted)]">Message view lands with the FE-chat agent.</p>
</div>
