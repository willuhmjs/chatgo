<script lang="ts">
  // Home shell (FE-shell will own real Sidebar/folders/tags — scaffold ships
  // the working skeleton so the app never flashes blank).
  import { navigate } from '$lib/router.svelte.js';
  import { session } from '$lib/session.svelte.js';
  import { toggleTheme } from '$lib/theme.svelte.js';

  let query = $state('');
</script>

<div class="flex h-full">
  <!-- sidebar skeleton (260px per design brief) -->
  <aside
    class="hidden w-[var(--sidebar-w)] shrink-0 flex-col border-r md:flex"
    style="background:var(--panel); border-color:var(--border)"
  >
    <div class="p-3">
      <button
        class="w-full rounded-lg px-3 py-2 text-sm font-medium"
        style="background:var(--accent); color:var(--accent-fg)"
        onclick={() => navigate('/')}
      >
        + New chat
      </button>
    </div>
    <div class="px-3 pb-2">
      <input
        type="search"
        placeholder="Search chats…"
        class="w-full rounded-lg border px-3 py-1.5 text-sm outline-none"
        style="background:var(--panel-2); border-color:var(--border); color:var(--text)"
        bind:value={query}
      />
    </div>
    <nav class="flex-1 overflow-y-auto px-3 py-2 text-sm text-[var(--muted)]">
      <p class="mb-1 font-medium">Chats</p>
      <p class="px-1 py-6 text-center opacity-70">No chats yet.</p>
    </nav>
    <div class="flex items-center gap-2 border-t p-3 text-sm" style="border-color:var(--border)">
      <span class="flex-1 truncate">{session.user?.name ?? session.user?.email ?? 'Signed out'}</span>
      <button class="opacity-70 hover:opacity-100" onclick={toggleTheme}>theme</button>
      <a href="/settings" class="opacity-70 hover:opacity-100">settings</a>
    </div>
  </aside>

  <!-- main pane: centered 48rem column per brief -->
  <section class="flex min-w-0 flex-1 flex-col">
    <div class="flex flex-1 items-center justify-center overflow-y-auto">
      <div class="w-full max-w-[48rem] px-6 text-center">
        <h1 class="mb-2 text-2xl font-semibold">ChatGo</h1>
        <p class="text-sm text-[var(--muted)]">
          Multi-user, OpenAI-compatible chat. Pick a model and start talking.
        </p>
      </div>
    </div>

    <!-- sticky composer placeholder (FE-chat replaces) -->
    <div class="sticky bottom-0 border-t p-4" style="background:var(--bg); border-color:var(--border)">
      <div class="mx-auto w-full max-w-[48rem]">
        <div
          class="flex items-end gap-2 rounded-xl border p-2"
          style="background:var(--panel); border-color:var(--border)"
        >
          <textarea
            rows="1"
            placeholder="Message ChatGo…"
            class="max-h-40 flex-1 resize-none bg-transparent px-2 py-1.5 text-sm outline-none"
          ></textarea>
          <button
            class="rounded-lg px-3 py-1.5 text-sm font-medium disabled:opacity-50"
            style="background:var(--accent); color:var(--accent-fg)"
            disabled
          >
            Send
          </button>
        </div>
        <div class="mt-2 flex items-center gap-3 text-xs text-[var(--muted)]">
          <span class="rounded-full border px-2 py-0.5" style="border-color:var(--border)">no model</span>
          <span>⌘K palette · ⌘N new chat · ⌘↵ send</span>
        </div>
      </div>
    </div>
  </section>
</div>
