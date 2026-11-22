<script lang="ts">
  // /admin — FE-admin owned. The router has no /admin/:tab pattern and its
  // location tracks pathname only, so the active tab is internal state
  // mirrored into ?tab=… with history.replaceState (never pushState — keeps
  // the scaffold router's pathname-only location authoritative). Deep links
  // like /admin?tab=users resolve on mount.
  import { Server, Settings, Shield, ShieldX, Users } from 'lucide-svelte';
  import { isAdmin, session } from '$lib/session.svelte.js';
  import ProvidersTab from '$lib/admin/tabs/ProvidersTab.svelte';
  import UsersTab from '$lib/admin/tabs/UsersTab.svelte';
  import SystemTab from '$lib/admin/tabs/SystemTab.svelte';
  import Spinner from '$lib/admin/ui/Spinner.svelte';

  const TABS = ['providers', 'users', 'system'] as const;
  type Tab = (typeof TABS)[number];

  function tabFromUrl(): Tab {
    const t = new URLSearchParams(window.location.search).get('tab');
    return TABS.find((x) => x === t) ?? 'providers';
  }

  let tab = $state<Tab>(tabFromUrl());

  // Mirror tab → URL for shareable state; router never sees the query.
  $effect(() => {
    history.replaceState({}, '', tab === 'providers' ? '/admin' : `/admin?tab=${tab}`);
  });

  const tabs: { id: Tab; label: string; icon: typeof Server }[] = [
    { id: 'providers', label: 'Providers', icon: Server },
    { id: 'users', label: 'Users', icon: Users },
    { id: 'system', label: 'System', icon: Settings },
  ];
</script>

{#if !session.ready}
  <div class="flex h-full items-center justify-center gap-2 text-sm text-[var(--muted)]">
    <Spinner size={16} /> Checking session…
  </div>
{:else if !session.user || !isAdmin()}
  <!-- friendly 403 (unauthenticated users land on the same card w/ a sign-in CTA) -->
  <div class="flex h-full items-center justify-center px-4" style="background:var(--bg)">
    <div class="w-full max-w-md rounded-2xl p-8 text-center" style="background:var(--panel); color:var(--text)">
      <span class="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-full" style="background:var(--panel-2); color:var(--muted)">
        {#if session.user}<ShieldX size={24} />{:else}<Shield size={24} />{/if}
      </span>
      <h1 class="text-[17px] font-semibold tracking-tight">
        {session.user ? 'Admins only' : 'Sign in required'}
      </h1>
      <p class="mt-2 text-sm leading-relaxed text-[var(--muted)]">
        {#if session.user}
          You're signed in as <span class="font-medium text-[var(--text)]">{session.user.email}</span>, which isn't an
          administrator account. The provider, user and system consoles live behind the admin role — chat away happily
          though, this door just doesn't open for you (yet).
        {:else}
          The admin console needs an administrator session. Sign in and try again, or head back to the chat.
        {/if}
      </p>
      <div class="mt-6 flex items-center justify-center gap-2">
        <a
          href="/"
          class="inline-flex items-center gap-1.5 rounded-[10px] px-4 py-2 text-sm font-medium"
          style="background:var(--accent); color:var(--accent-fg)"
        >Back to chat</a>
        {#if !session.user}
          <a
            href="/login"
            class="inline-flex items-center gap-1.5 rounded-[10px] px-4 py-2 text-sm font-medium transition-colors hover:bg-[var(--fill-hover)]"
            style="background:transparent; color:var(--text); border:1px solid var(--border)"
          >Sign in</a>
        {/if}
      </div>
    </div>
  </div>
{:else}
  <div class="mx-auto h-full w-full max-w-5xl overflow-y-auto px-6 py-8" style="background:var(--bg); color:var(--text)">
    <header class="mb-6 flex flex-wrap items-center justify-between gap-3">
      <div>
        <h1 class="flex items-center gap-2 text-lg font-semibold tracking-tight">
          <Shield size={19} class="text-[var(--muted)]" /> Admin
        </h1>
        <p class="mt-0.5 text-sm text-[var(--muted)]">Providers, people and instance settings.</p>
      </div>
      <!-- quiet segmented pill (§5): active = #3a3a3a fill, no accent bar -->
      <nav class="flex items-center gap-0.5 rounded-full p-1" style="background:var(--panel)" aria-label="Admin sections">
        {#each tabs as t (t.id)}
          <button
            class="flex items-center gap-1.5 rounded-full px-3 py-1.5 text-sm transition-colors"
            style={tab === t.id
              ? 'background:var(--panel-2); color:var(--text); font-weight:500'
              : 'background:transparent; color:var(--muted)'}
            aria-current={tab === t.id ? 'page' : undefined}
            onclick={() => (tab = t.id)}
          >
            <t.icon size={15} /> {t.label}
          </button>
        {/each}
      </nav>
    </header>

    {#if tab === 'providers'}
      <ProvidersTab />
    {:else if tab === 'users'}
      <UsersTab />
    {:else}
      <SystemTab />
    {/if}
  </div>
{/if}
