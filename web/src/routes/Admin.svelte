<script lang="ts">
  // /admin (+tabs providers|users|system) — FE-admin owns the real CRUD pages.
  import { session } from '$lib/session.svelte.js';
  import { navigate } from '$lib/router.svelte.js';
</script>

<div class="mx-auto h-full w-full max-w-[48rem] overflow-y-auto px-6 py-8">
  <h1 class="mb-2 text-xl font-semibold">Admin</h1>
  {#if session.user?.role !== 'admin'}
    <p class="text-sm text-[var(--muted)]">
      Admins only. <button class="text-[var(--accent)]" onclick={() => navigate('/login')}>Sign in</button>
    </p>
  {:else}
    <div class="mt-4 flex gap-2 text-sm">
      {#each ['providers', 'users', 'system'] as tab (tab)}
        <span
          class="cursor-default rounded-full border px-3 py-1 capitalize"
          style="border-color:var(--border); color:var(--muted)"
        >{tab}</span>
      {/each}
    </div>
    <p class="mt-6 text-sm text-[var(--muted)]">Admin consoles land with the FE-admin agent.</p>
  {/if}
</div>
