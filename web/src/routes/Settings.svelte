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

<div class="h-full overflow-y-auto">
  <div class="mx-auto w-full max-w-[44rem] px-5 py-8">
    <div class="mb-5 flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-xl font-semibold tracking-tight">Settings</h1>
      {#if isAdmin()}
        <a
          href="/admin"
          class="rounded-lg border px-3 py-1.5 text-sm font-medium text-[var(--accent)] transition-colors hover:bg-[color-mix(in_srgb,var(--accent)_10%,transparent)] focus-visible:outline-2 focus-visible:outline-[color-mix(in_srgb,var(--accent)_70%,transparent)]"
          style="border-color:var(--border)"
        >
          Open admin console →
        </a>
      {/if}
    </div>

    <div class="mb-4 flex gap-1 border-b" role="tablist" aria-label="Settings sections" style="border-color:var(--border)">
      {#each TABS as t (t.id)}
        <button
          type="button"
          role="tab"
          aria-selected={tab === t.id}
          class="-mb-px border-b-2 px-3.5 py-2 text-sm transition-colors focus-visible:outline-2 focus-visible:outline-[color-mix(in_srgb,var(--accent)_70%,transparent)] {tab === t.id
            ? 'border-[var(--accent)] font-medium text-[var(--text)]'
            : 'border-transparent text-[var(--muted)] hover:text-[var(--text)]'}"
          onclick={() => (tab = t.id)}
        >
          {t.label}
        </button>
      {/each}
    </div>

    {#if session.user}
      <SettingsMe {tab} />
    {:else}
      <p class="rounded-xl border px-4 py-3 text-sm text-[var(--muted)]" style="background:var(--panel); border-color:var(--border)">
        Not signed in — <a href="/login" class="text-[var(--accent)]">sign in</a> to manage settings.
      </p>
    {/if}
  </div>
</div>
