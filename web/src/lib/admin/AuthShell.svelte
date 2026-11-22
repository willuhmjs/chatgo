<script lang="ts">
  import type { Snippet } from 'svelte';
  import { MessageSquare, Server, ShieldCheck, Sparkles } from 'lucide-svelte';

  let { children }: { children: Snippet } = $props();

  const features = [
    { icon: Server, text: 'Every model in one place — any OpenAI-compatible endpoint, synced live.' },
    { icon: Sparkles, text: 'Web-search grounding, folders, tags, sharing and gallery artifacts.' },
    { icon: ShieldCheck, text: 'Self-hosted in one small binary. Prompts never leave your infrastructure.' },
    { icon: MessageSquare, text: 'Team-ready: OIDC single sign-on, approval queues and per-user API tokens.' },
  ];
</script>

<div class="flex h-full w-full overflow-y-auto">
  <!-- hero -->
  <aside
    class="hidden w-[46%] shrink-0 flex-col justify-between px-12 py-10 lg:flex xl:px-16"
    style="background: radial-gradient(70rem 42rem at 12% 18%, color-mix(in srgb, var(--accent) 14%, transparent), transparent 70%), var(--bg); border-right:1px solid var(--border)"
  >
    <div class="flex items-center gap-2.5">
      <span class="flex h-9 w-9 items-center justify-center rounded-xl" style="background:var(--accent); color:var(--accent-fg)">
        <MessageSquare size={18} />
      </span>
      <span class="text-lg font-semibold tracking-tight">ChatGo</span>
    </div>

    <div class="flex flex-col gap-8">
      <h2 class="max-w-md text-3xl font-semibold leading-snug tracking-tight">
        Team chat,<br />every model,<br /><span style="color:var(--accent)">one tiny binary.</span>
      </h2>
      <ul class="flex max-w-md flex-col gap-4">
        {#each features as f (f.text)}
          {@const Icon = f.icon}
          <li class="flex items-start gap-3 text-sm leading-relaxed text-[var(--muted)]">
            <span class="mt-0.5 shrink-0" style="color:var(--accent)"><Icon size={16} /></span>
            {f.text}
          </li>
        {/each}
      </ul>
    </div>

    <p class="text-xs text-[var(--muted)]">Apache-2.0 · single binary · runs in &lt; 128&nbsp;MiB</p>
  </aside>

  <!-- form column -->
  <main class="flex min-w-0 flex-1 items-center justify-center px-6 py-10">
    <div class="w-full max-w-sm">{@render children()}</div>
  </main>
</div>
