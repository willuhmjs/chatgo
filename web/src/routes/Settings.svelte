<script lang="ts">
  // Settings shell (FE-shell owns the real me/settings page).
  import { session } from '$lib/session.svelte.js';
  import { theme, toggleTheme } from '$lib/theme.svelte.js';
</script>

<div class="mx-auto h-full w-full max-w-[48rem] overflow-y-auto px-6 py-8">
  <h1 class="mb-6 text-xl font-semibold">Settings</h1>

  <section
    class="mb-4 flex items-center justify-between rounded-xl border px-4 py-3"
    style="background:var(--panel); border-color:var(--border)"
  >
    <div>
      <p class="text-sm font-medium">Theme</p>
      <p class="text-xs text-[var(--muted)]">Currently: {theme.mode} (persisted)</p>
    </div>
    <button
      class="rounded-lg border px-3 py-1.5 text-sm"
      style="border-color:var(--border)"
      onclick={toggleTheme}
    >
      Toggle
    </button>
  </section>

  <section
    class="rounded-xl border px-4 py-3 text-sm"
    style="background:var(--panel); border-color:var(--border)"
  >
    <p class="font-medium">Account</p>
    {#if session.user}
      <p class="mt-1 text-[var(--muted)]">{session.user.email} · role {session.user.role}</p>
    {:else}
      <p class="mt-1 text-[var(--muted)]">Not signed in.</p>
    {/if}
  </section>
</div>
