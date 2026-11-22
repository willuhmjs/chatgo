<script lang="ts">
  import { currentRoute, startRouter } from '$lib/router.svelte.js';
  import { initTheme } from '$lib/theme.svelte.js';
  import { initShortcuts } from '$lib/shortcuts.svelte.js';
  import { bootstrap } from '$lib/session.svelte.js';
  import { toasts, dismiss } from '$lib/toast.svelte.js';

  import Home from './routes/Home.svelte';
  import ChatView from './routes/ChatView.svelte';
  import Compare from './routes/Compare.svelte';
  import Gallery from './routes/Gallery.svelte';
  import Settings from './routes/Settings.svelte';
  import Admin from './routes/Admin.svelte';
  import Login from './routes/Login.svelte';
  import Register from './routes/Register.svelte';
  import Share from './routes/Share.svelte';

  const route = $derived(currentRoute());

  $effect(() => {
    initTheme();
    initShortcuts();
    void bootstrap();
    return startRouter();
  });
</script>

<main class="h-full">
  {#if route.name === 'home'}
    <Home />
  {:else if route.name === 'chat'}
    <ChatView id={route.params.id} />
  {:else if route.name === 'compare'}
    <Compare />
  {:else if route.name === 'gallery'}
    <Gallery />
  {:else if route.name === 'settings'}
    <Settings />
  {:else if route.name === 'admin'}
    <Admin />
  {:else if route.name === 'login'}
    <Login />
  {:else if route.name === 'register'}
    <Register />
  {:else if route.name === 'share'}
    <Share token={route.params.token} />
  {:else}
    <div class="flex h-full items-center justify-center text-[var(--muted)]">
      Not found — <a class="ml-1 text-[var(--accent)]" href="/">go home</a>
    </div>
  {/if}
</main>

<!-- toast stack (FE-extra will theme; baseline ships here so nothing flashes silent) -->
<div class="pointer-events-none fixed bottom-4 right-4 z-50 flex w-80 flex-col gap-2">
  {#each toasts as t (t.id)}
    <button
      class="pointer-events-auto rounded-lg border px-3 py-2 text-left text-sm shadow-lg"
      style="background:var(--panel); border-color:var(--border); color:var(--text)"
      onclick={() => dismiss(t.id)}
    >
      {t.message}
    </button>
  {/each}
</div>
