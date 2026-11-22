<script lang="ts">
  // Auth shell (FE-admin owned) — clean two-column per docs/DESIGN-POLISH.md §1:
  // quiet wordmark column on --bg-deep, form on --bg. No gradients; fill
  // contrast instead of borders.
  import type { Snippet } from 'svelte';
  import { MessageSquare } from 'lucide-svelte';

  let { children }: { children: Snippet } = $props();

  const features = [
    'Every model in one place — any OpenAI-compatible endpoint, synced live.',
    'Search grounding, folders, tags, sharing and gallery artifacts.',
    'Self-hosted in one small binary. Prompts never leave your infrastructure.',
  ];
</script>

<div class="flex h-full w-full overflow-y-auto" style="background:var(--bg)">
  <!-- quiet wordmark column -->
  <aside class="hidden w-[42%] shrink-0 flex-col justify-between px-12 py-10 lg:flex xl:px-16" style="background:var(--bg-deep)">
    <div class="flex items-center gap-2">
      <span class="flex h-7 w-7 items-center justify-center rounded-lg" style="background:var(--accent); color:var(--accent-fg)">
        <MessageSquare size={15} />
      </span>
      <span class="text-[15px] font-medium tracking-tight" style="color:var(--text)">ChatGo</span>
    </div>

    <ul class="flex max-w-sm flex-col gap-3.5">
      {#each features as text (text)}
        <li class="text-sm leading-relaxed" style="color:var(--muted)">{text}</li>
      {/each}
    </ul>

    <p class="text-xs" style="color:var(--faint)">Apache-2.0 · single binary · runs in &lt; 128&nbsp;MiB</p>
  </aside>

  <!-- form column -->
  <main class="flex min-w-0 flex-1 items-center justify-center px-6 py-10">
    <div class="w-full max-w-sm">{@render children()}</div>
  </main>
</div>
