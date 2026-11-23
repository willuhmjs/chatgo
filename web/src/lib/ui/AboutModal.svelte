<script lang="ts">
  // About ChatGo — version, license, shortcut recap. Version comes from the
  // public /healthz envelope (no auth needed, never leaks anything secret).
  import Modal from '$lib/ui/Modal.svelte';
  import { aboutState } from '$lib/ui/about.svelte.js';
  import { Github, Sparkles } from 'lucide-svelte';

  let version = $state('');
  $effect(() => {
    if (!aboutState.open || version) return;
    void fetch('/healthz')
      .then((r) => (r.ok ? r.json() : null))
      .then((j) => {
        if (j && typeof j.version === 'string') version = j.version;
      })
      .catch(() => {
        /* version stays hidden */
      });
  });

  const keys: Array<[string, string]> = [
    ['⌘K', 'Command palette'],
    ['⌘B', 'Toggle sidebar'],
    ['⌘↵', 'Send message'],
    ['/', 'Focus composer'],
    ['?', 'This shortcut list'],
    ['Esc Esc', 'Retract the last exchange'],
  ];
</script>

<Modal bind:open={aboutState.open} title="About ChatGo" width="max-w-lg">
  <div class="flex flex-col gap-5 text-sm">
    <div class="flex items-center gap-3">
      <span
        class="grid size-11 shrink-0 place-items-center rounded-xl text-[var(--accent-fg)]"
        style="background:var(--acc-grad)"
      >
        <Sparkles class="size-5" />
      </span>
      <div class="min-w-0">
        <p class="text-[15px] font-semibold text-[var(--text)]">ChatGo{version ? ` v${version}` : ''}</p>
        <p class="text-[13px] text-[var(--muted)]">Self-hosted multi-user AI chat — one small Go binary.</p>
      </div>
    </div>

    <div>
      <p class="mb-2 text-[11px] font-semibold uppercase tracking-wider text-[var(--faint)]">Keyboard</p>
      <div class="grid grid-cols-1 gap-x-6 gap-y-1.5 sm:grid-cols-2">
        {#each keys as [k, what] (k)}
          <div class="flex items-center justify-between gap-3">
            <span class="text-[var(--muted)]">{what}</span>
            <kbd
              class="rounded border border-[var(--border)] bg-[var(--panel)] px-1.5 py-0.5 text-[11px]
                text-[var(--muted)]">{k}</kbd
            >
          </div>
        {/each}
      </div>
    </div>

    <p class="text-[12px] leading-relaxed text-[var(--faint)]">
      Apache-2.0 licensed, original implementation — not affiliated with Open WebUI or OpenAI.
      <a
        class="inline-flex items-center gap-1 text-[var(--acc)] hover:underline"
        href="https://github.com"
        rel="noreferrer noopener"
        target="_blank"><Github class="size-3.5" />Source</a
      >
    </p>
  </div>
</Modal>
