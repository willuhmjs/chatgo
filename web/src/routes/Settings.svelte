<script lang="ts">
  // Settings shell (FE-shell): tab bar for non-admin sections; admins get an
  // explicit link into /admin (FE-admin owns that route).
  import { session, isAdmin } from '$lib/session.svelte.js';
  import SettingsMe from './SettingsMe.svelte';

  type Tab = 'profile' | 'preferences' | 'account';
  let tab = $state<Tab>('profile');

  const TABS: { id: Tab; label: string }[] = [
    { id: 'profile', label: 'Profile' },
    { id: 'preferences', label: 'Preferences' },
    { id: 'account', label: 'Account' },
  ];
</script>

<div class="h-full overflow-y-auto" style="background:var(--bg); color:var(--text)">
  <div class="mx-auto w-full max-w-[44rem] px-5 py-8">
    <div class="mb-5 flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-lg font-semibold tracking-tight">Settings</h1>
      {#if isAdmin()}
        <a
          href="/admin"
          class="rounded-lg px-3 py-1.5 text-sm font-medium text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--accent-soft)]"
        >
          Open admin console →
        </a>
      {/if}
    </div>

    <!-- quiet segmented pill (§5): active = panel-2 fill, no underline -->
    <div class="mb-5 inline-flex items-center gap-0.5 rounded-full p-1" role="tablist" aria-label="Settings sections" style="background:var(--panel)">
      {#each TABS as t (t.id)}
        <button
          type="button"
          role="tab"
          aria-selected={tab === t.id}
          class="rounded-full px-3.5 py-1.5 text-sm transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--accent-soft)]"
          style={tab === t.id ? 'background:var(--panel-2); color:var(--text); font-weight:500' : 'color:var(--muted)'}
          onclick={() => (tab = t.id)}
        >
          {t.label}
        </button>
      {/each}
    </div>

    {#if session.user}
      <SettingsMe {tab} />
    {:else}
      <p class="rounded-2xl px-4 py-3 text-sm text-[var(--muted)]" style="background:var(--panel)">
        Not signed in — <a href="/login" class="text-[var(--text)] underline underline-offset-2">sign in</a> to manage settings.
      </p>
    {/if}
  </div>
</div>
