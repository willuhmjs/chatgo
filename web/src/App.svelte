<script lang="ts">
  // App shell (FE-shell): sidebar + mobile top bar + routed main area + global
  // toasts/confirm. Scaffold-owned wiring (theme/shortcuts/session/router/toast
  // stack) is kept intact.
  import * as api from '$lib/api';
  import { currentRoute, location, navigate, startRouter } from '$lib/router.svelte.js';
  import { initTheme } from '$lib/theme.svelte.js';
  import { initShortcuts, onShortcut } from '$lib/shortcuts.svelte.js';
  import { bootstrap, isAdmin, isOnboardDismissed, session, shouldOnboard } from '$lib/session.svelte.js';
  import { load as loadModelPrefs } from '$lib/chat/model-prefs.svelte.js';
  import { refreshCapabilities } from '$lib/chat/state.svelte.js';
  import { bootAccent } from '$lib/accent-sync.svelte.js';
  import Toasts from '$lib/extra/ToastHost.svelte';
  import CommandPalette from '$lib/extra/CommandPalette.svelte';

  import Sidebar from '$lib/ui/Sidebar.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import IconButton from '$lib/ui/IconButton.svelte';

  import Home from './routes/Home.svelte';
  import ChatView from './routes/ChatView.svelte';
  import Compare from './routes/Compare.svelte';
  import Gallery from './routes/Gallery.svelte';
  import Knowledge from './routes/Knowledge.svelte';
  import Images from './routes/Images.svelte';
  import Settings from './routes/Settings.svelte';
  import Admin from './routes/Admin.svelte';
  import Login from './routes/Login.svelte';
  import Register from './routes/Register.svelte';
  import FirstRun from './routes/FirstRun.svelte';
  import Onboarding from './routes/Onboarding.svelte';
  import Share from './routes/Share.svelte';

  import { Menu, Plus, X } from 'lucide-svelte';

  const route = $derived(currentRoute());
  // auth + public pages render bare (no app chrome)
  const bare = $derived(['login', 'register', 'welcome', 'onboarding', 'share'].includes(route.name));

  // ----- sidebar state -----
  const COLLAPSE_KEY = 'chatgo:sidebar-collapsed';
  let collapsed = $state(
    (() => {
      try {
        return localStorage.getItem(COLLAPSE_KEY) === '1';
      } catch {
        return false;
      }
    })(),
  );
  function toggleCollapse() {
    collapsed = !collapsed;
    try {
      localStorage.setItem(COLLAPSE_KEY, collapsed ? '1' : '0');
    } catch {
      /* ignore */
    }
  }

  let isMobile = $state(
    typeof window !== 'undefined' ? window.matchMedia('(max-width: 767px)').matches : false,
  );
  let mobileOpen = $state(false);
  const rail = $derived(collapsed && !isMobile);

  // ----- top-bar title (chat route) -----
  let chatTitle = $state('');

  $effect(() => {
    const mq = window.matchMedia('(max-width: 767px)');
    const on = (e: MediaQueryListEvent) => {
      isMobile = e.matches;
      if (!e.matches) mobileOpen = false;
    };
    mq.addEventListener('change', on);
    return () => mq.removeEventListener('change', on);
  });

  $effect(() => {
    initTheme();
    initShortcuts();
    void bootstrap();
    return startRouter();
  });

  // close the drawer on navigation
  $effect(() => {
    void location.path;
    mobileOpen = false;
  });

  // Server-synced model prefs (default + hidden) — one fetch shared via the
  // model-prefs module; Home/ModelPicker/CommandPalette/Settings read it live.
  // Re-runs on login/logout; the module re-fetches after a failure.
  $effect(() => {
    if (!session.ready || !session.user) return;
    void loadModelPrefs();
  });

  // Server-synced accent (docs/FEATURES-V2.md): pulls the me-settings "accent"
  // key so the choice follows the account, and pushes a pre-existing local
  // choice up once. Separate module call rather than reusing the onboarding
  // getMeSettings below — that one only runs for admins on /home.
  $effect(() => {
    if (!session.ready || !session.user) return;
    void bootAccent();
  });

  // Probe the web-search capability once auth is known so BOTH the Home
  // composer and ChatView's composer can show the Search pill without waiting
  // for a chat route to mount. Idempotent per app start (cached + capProbed).
  $effect(() => {
    if (!session.ready || !session.user) return;
    void refreshCapabilities(session.user?.role);
  });

  // fresh-admin wizard nudge (AUTHGATE-CONTRACT §3): an admin on home whose
  // setup wizard is unfinished gets routed to /onboarding. The navigate()
  // below flips route.name, so this effect early-returns on re-run — no loop.
  // isOnboardDismissed() is the second lock: once the wizard finishes (even
  // if saving setup_done failed), never bounce the user back into it.
  $effect(() => {
    if (!session.ready || !isAdmin() || isOnboardDismissed() || route.name !== 'home') return;
    void api
      .getMeSettings()
      .then((s) => {
        // still on home when the response lands (avoid racing user navigation)
        if (route.name !== 'home') return;
        if (shouldOnboard(session.user, s, { first_run: session.firstRun })) {
          navigate('/onboarding', { replace: true });
        }
      })
      .catch(() => {
        /* settings unavailable — stay on home */
      });
  });

  // chat title for the mobile bar + document title
  $effect(() => {
    if (route.name !== 'chat') {
      chatTitle = '';
      document.title = 'ChatGo';
      return;
    }
    const id = route.params.id;
    let cancelled = false;
    chatTitle = 'Chat';
    void api
      .getChat(id)
      .then((c) => {
        if (cancelled) return;
        chatTitle = c.title || 'Chat';
        document.title = `${chatTitle} · ChatGo`;
      })
      .catch(() => {
        if (!cancelled) chatTitle = 'Chat';
      });
    return () => {
      cancelled = true;
    };
  });

  // Live re-title while streaming: ChatView's SSE `title` handler broadcasts
  // chatgo:title once the server generates the chat's auto-title. Without
  // this the mobile bar keeps the placeholder fetched at route change until
  // the next navigation (the sidebar refreshes itself via location.path).
  $effect(() => {
    const onTitle = (e: Event) => {
      const d = (e as CustomEvent<{ chat_id?: string; title?: string }>).detail;
      if (d?.title && route.name === 'chat' && d.chat_id === route.params.id) {
        chatTitle = d.title;
      }
    };
    window.addEventListener('chatgo:title', onTitle);
    return () => window.removeEventListener('chatgo:title', onTitle);
  });

  // ⌘B — collapse sidebar (desktop) / toggle drawer (mobile); Esc closes drawer
  $effect(() => {
    const offB = onShortcut('mod+b', () => {
      if (bare) return;
      if (isMobile) mobileOpen = !mobileOpen;
      else toggleCollapse();
    });
    const offEsc = onShortcut('escape', () => {
      if (mobileOpen) mobileOpen = false;
    });
    return () => {
      offB();
      offEsc();
    };
  });

  // SPA-ify same-origin anchor clicks (router is pushState-based)
  $effect(() => {
    const onClick = (e: MouseEvent) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      const a = (e.target as Element | null)?.closest?.('a');
      if (!a) return;
      const href = a.getAttribute('href');
      if (!href || !href.startsWith('/') || a.target || a.hasAttribute('download') || a.hasAttribute('data-no-spa')) return;
      e.preventDefault();
      navigate(href);
    };
    window.addEventListener('click', onClick);
    return () => window.removeEventListener('click', onClick);
  });
</script>

{#if bare}
  {#if route.name === 'login'}
    <Login />
  {:else if route.name === 'register'}
    <Register />
  {:else if route.name === 'welcome'}
    <FirstRun />
  {:else if route.name === 'onboarding'}
    <Onboarding />
  {:else}
    <Share token={route.params.token} />
  {/if}
{:else}
  <div class="flex h-full overflow-hidden">
    <Sidebar
      {rail}
      {mobileOpen}
      onToggleCollapse={toggleCollapse}
      onCloseMobile={() => (mobileOpen = false)}
    />

    <!-- backdrop for the off-canvas drawer -->
    {#if mobileOpen}
      <div
        class="fixed inset-0 z-30 bg-black/50 backdrop-blur-[1px] md:hidden"
        role="presentation"
        aria-hidden="true"
        onclick={() => (mobileOpen = false)}
      ></div>
    {/if}

    <div class="flex min-w-0 flex-1 flex-col">
      <!-- mobile top bar — quiet #171717 bar, fill contrast only (§3) -->
      <header
        class="flex h-12 shrink-0 items-center gap-1 px-2 md:hidden"
        style="background:var(--bg-deep); padding-top:env(safe-area-inset-top)"
      >
        <IconButton label="Open sidebar" onclick={() => (mobileOpen = true)}>
          <Menu class="size-5" />
        </IconButton>
        <h1 class="min-w-0 flex-1 truncate px-1 text-[15px] font-medium">{chatTitle || 'ChatGo'}</h1>
        <IconButton label="New chat" onclick={() => navigate('/')}>
          <Plus class="size-5" />
        </IconButton>
      </header>

      <!-- main scroll container -->
      <main class="min-h-0 flex-1 overflow-y-auto">
        {#if route.name === 'home'}
          <Home />
        {:else if route.name === 'chat'}
          <ChatView id={route.params.id} />
        {:else if route.name === 'compare'}
          <Compare />
        {:else if route.name === 'gallery'}
          <Gallery />
        {:else if route.name === 'knowledge'}
          <Knowledge />
        {:else if route.name === 'images'}
          <Images />
        {:else if route.name === 'settings'}
          <Settings />
        {:else if route.name === 'admin'}
          <Admin />
        {:else}
          <div class="flex h-full items-center justify-center text-sm text-[var(--muted)]">
            Not found — <a class="ml-1 text-[var(--acc)] hover:underline" href="/">go home</a>
          </div>
        {/if}
      </main>
    </div>
  </div>
{/if}

<ConfirmDialog />

<!-- themed toast stack + global ⌘K palette (FE-extra components, mounted here) -->
<Toasts />
<CommandPalette />
