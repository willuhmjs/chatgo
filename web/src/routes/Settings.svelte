<script lang="ts">
  // Settings shell — OWUI-style full-area overlay: a left icon-tab rail picks
  // the section, content scrolls on the right (see open-webui SettingsModal:
  // w-[15rem] rail, border-r divider, quiet pill for the active tab). Route
  // semantics are unchanged (/settings renders this over the app); the X (or
  // any rail navigation away) returns to the chat. Admins keep their explicit
  // link into /admin (FE-admin owns that route).
  import { session, isAdmin } from '$lib/session.svelte.js';
  import { navigate } from '$lib/router.svelte.js';
  import SettingsMe from './SettingsMe.svelte';
  import SettingsPrompts from './SettingsPrompts.svelte';
  import { BookOpen, KeyRound, ShieldCheck, SlidersHorizontal, User, X } from 'lucide-svelte';

  type Tab = 'profile' | 'preferences' | 'library' | 'account';
  let tab = $state<Tab>('profile');

  const TABS: { id: Tab; label: string; icon: typeof User }[] = [
    { id: 'profile', label: 'Profile', icon: User },
    { id: 'preferences', label: 'Preferences', icon: SlidersHorizontal },
    { id: 'library', label: 'Library', icon: BookOpen },
    { id: 'account', label: 'Account', icon: KeyRound },
  ];

  const tabClass = (active: boolean) =>
    `flex h-8 shrink-0 items-center gap-2 rounded-lg px-2.5 text-[13px] transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--accent-soft)] md:w-full ${
      active ? 'font-medium text-[var(--text)]' : 'text-[var(--muted)] hover:bg-[var(--fill-hover)] hover:text-[var(--text)]'
    }`;
</script>

<div
  class="settings-overlay fixed inset-0 z-40 flex flex-col md:flex-row"
  style="background:var(--bg); color:var(--text)"
  role="dialog"
  aria-modal="true"
  aria-label="Settings"
>
  <!-- ── left icon-tab rail (horizontal scroll strip on mobile, OWUI pattern) -->
  <nav
    class="flex shrink-0 flex-col border-b md:w-60 md:border-b-0 md:border-r"
    style="border-color:var(--border); background:var(--bg-deep)"
  >
    <div class="flex h-12 shrink-0 items-center justify-between gap-2 px-3.5">
      <span class="truncate text-sm font-semibold tracking-tight">Settings</span>
      <button
        type="button"
        class="rounded-lg p-1.5 text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--accent-soft)]"
        aria-label="Close settings"
        title="Close"
        onclick={() => navigate('/')}
      >
        <X class="size-4" />
      </button>
    </div>

    <div
      class="flex min-w-0 flex-1 gap-0.5 overflow-x-auto p-2 md:flex-col md:overflow-x-hidden"
      role="tablist"
      aria-label="Settings sections"
    >
      {#each TABS as t (t.id)}
        {@const Icon = t.icon}
        <button
          type="button"
          role="tab"
          aria-selected={tab === t.id}
          class={tabClass(tab === t.id)}
          style={tab === t.id ? 'background:var(--fill-hover)' : undefined}
          onclick={() => (tab = t.id)}
        >
          <Icon class="size-4 shrink-0" />
          <span class="truncate">{t.label}</span>
        </button>
      {/each}
    </div>

    {#if isAdmin()}
      <div class="hidden shrink-0 px-2 pb-2 md:block">
        <div class="mx-2 my-2 h-px" style="background:var(--border)" role="presentation"></div>
        <a
          href="/admin"
          class={tabClass(false)}
          title="Open admin console"
        >
          <ShieldCheck class="size-4 shrink-0" />
          <span class="truncate">Admin console</span>
        </a>
      </div>
    {/if}
  </nav>

  <!-- ── section content ───────────────────────────────────────────────── -->
  <div class="min-h-0 min-w-0 flex-1 overflow-y-auto" role="tabpanel">
    <div class="mx-auto w-full max-w-[44rem] px-5 py-8">
      {#if tab === 'library'}
        <SettingsPrompts />
      {:else if session.user}
        <SettingsMe {tab} />
      {:else}
        <p class="rounded-2xl px-4 py-3 text-sm text-[var(--muted)]" style="background:var(--panel)">
          Not signed in — <a href="/login" class="text-[var(--text)] underline underline-offset-2">sign in</a> to manage settings.
        </p>
      {/if}
    </div>
  </div>
</div>

<style>
  /* quiet fade-in — the app.css §9 kill-switch flattens this on request */
  .settings-overlay {
    animation: settings-fade 140ms ease-out;
  }
  @keyframes settings-fade {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }
</style>
