<script lang="ts">
  // Login — clean two-column auth per docs/DESIGN-POLISH.md §1/§7. FE-admin owned.
  // Reads /api/auth/config for the SSO provider display name and the
  // registration policy, and surfaces OIDC failures handed back as
  // /login?error=… from the callback redirect.
  import { LogIn, Mail, MessageSquare, ShieldAlert } from 'lucide-svelte';
  import * as api from '$lib/api';
  import { navigate } from '$lib/router.svelte.js';
  import { login, session } from '$lib/session.svelte.js';
  import { push } from '$lib/toast.svelte.js';
  import AuthShell from '$lib/admin/AuthShell.svelte';
  import Spinner from '$lib/admin/ui/Spinner.svelte';
  import { btn, btnGhostStyle } from '$lib/admin/ui/styles';

  let email = $state('');
  let password = $state('');
  let busy = $state(false);
  let formError = $state('');
  let banner = $state('');
  let config = $state<api.AuthConfig | null>(null);

  // OIDC callback errors arrive as ?error=<code|message>; toast + banner, then
  // scrub the URL (the router ignores queries, so replaceState is safe here).
  $effect(() => {
    const q = new URLSearchParams(window.location.search);
    const err = q.get('error_description') ?? q.get('error');
    if (!err) return;
    banner = humanizeAuthError(err);
    push(banner, 'error');
    history.replaceState({}, '', '/login');
  });

  $effect(() => {
    api.authConfig().then(
      (c) => (config = c),
      () => void 0, // form login still works without the config
    );
  });

  // Already signed in? Go home.
  $effect(() => {
    if (session.ready && session.user) navigate('/', { replace: true });
  });

  function humanizeAuthError(err: string): string {
    const known: Record<string, string> = {
      access_denied: 'Single sign-on was cancelled or access was denied at the identity provider.',
      invalid_credentials: 'Invalid email or password.',
      oidc_failed: 'Single sign-on failed. Try again or sign in with email.',
      server_error: 'The server hit an error during sign-in. Try again shortly.',
    };
    return known[err] ?? `Sign-in problem: ${err.replace(/_/g, ' ')}.`;
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    formError = '';
    try {
      await login(email, password);
      navigate('/');
    } catch (err) {
      formError = err instanceof Error && err.message ? err.message : 'Login failed';
    } finally {
      busy = false;
    }
  }

  function sso() {
    window.location.assign(api.oidcLoginURL); // full-page redirect → 302 to IdP
  }

  // Derived (not narrowed in-template) so TS stays happy and label is stable.
  const ssoLabel = $derived(config?.oidc_enabled ? config.oidc_name || 'SSO' : '');
</script>

<AuthShell>
  <div class="mb-5 flex items-center gap-2 lg:hidden">
    <span class="flex h-7 w-7 items-center justify-center rounded-lg" style="background:var(--accent); color:var(--accent-fg)">
      <MessageSquare size={15} />
    </span>
    <span class="text-[15px] font-medium tracking-tight">ChatGo</span>
  </div>

  {#if banner}
    <div role="alert" class="pop mb-4 flex items-start gap-2 rounded-[10px] px-3.5 py-2.5 text-[13px] leading-relaxed" style="background:color-mix(in srgb, var(--danger) 9%, transparent); color:var(--danger)">
      <ShieldAlert size={15} class="mt-0.5 shrink-0" />
      {banner}
    </div>
  {/if}

  <div class="rounded-2xl p-6" style="background:var(--panel); color:var(--text)">
    <h1 class="text-[17px] font-semibold tracking-tight">Welcome back</h1>
    <p class="mb-5 mt-0.5 text-sm" style="color:var(--muted)">Sign in to your ChatGo workspace.</p>

    <form onsubmit={submit} class="flex flex-col gap-3.5">
      <label class="block text-sm">
        <span class="mb-1 block text-[13px]" style="color:var(--muted)">Email</span>
        <input
          type="email"
          required
          autocomplete="email"
          class="inp"
          placeholder="you@example.com"
          bind:value={email}
        />
      </label>
      <label class="block text-sm">
        <span class="mb-1 block text-[13px]" style="color:var(--muted)">Password</span>
        <input
          type="password"
          required
          autocomplete="current-password"
          class="inp"
          placeholder="••••••••"
          bind:value={password}
        />
      </label>

      {#if formError}
        <p class="pop rounded-[10px] px-3 py-2 text-[13px]" style="background:color-mix(in srgb, var(--danger) 9%, transparent); color:var(--danger)">
          {formError}
        </p>
      {/if}

      <button type="submit" class={`${btn} mt-1 w-full`} style="background:var(--accent); color:var(--accent-fg)" disabled={busy}>
        {#if busy}<Spinner /> Signing in…{:else}<LogIn size={15} /> Sign in{/if}
      </button>
    </form>

    {#if ssoLabel}
      <div class="my-4 flex items-center gap-3 text-[11px] uppercase tracking-wider" style="color:var(--faint)">
        <span class="h-px flex-1" style="background:var(--border)"></span> or <span class="h-px flex-1" style="background:var(--border)"></span>
      </div>
      <button type="button" class={`${btn} w-full`} style={btnGhostStyle} onclick={sso}>
        <Mail size={15} /> Continue with {ssoLabel}
      </button>
    {/if}

    <p class="mt-5 text-center text-xs" style="color:var(--muted)">
      {#if config === null || config.registration !== 'closed'}
        Need an account? <a href="/register" class="font-medium text-[var(--text)] underline-offset-2 hover:underline">Register</a>
      {:else}
        Registration is closed — ask your administrator for an invite.
      {/if}
    </p>
  </div>
</AuthShell>

<style>
  /* #2f2f2f fill per spec — hairline separates it from the same-tone card. */
  .inp {
    width: 100%;
    border-radius: 10px;
    padding: 0.5rem 0.75rem;
    font-size: 0.875rem;
    color: var(--text);
    background: var(--panel);
    border: 1px solid var(--border);
    outline: none;
    transition:
      border-color 150ms ease,
      box-shadow 150ms ease;
  }
  .inp::placeholder {
    color: var(--faint);
  }
  .inp:focus-visible {
    border-color: rgba(255, 255, 255, 0.22);
    box-shadow: 0 0 0 3px var(--accent-soft);
  }
  .pop {
    animation: pop-in 150ms ease-out;
  }
  @keyframes pop-in {
    from {
      opacity: 0;
      transform: translateY(2px);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .pop {
      animation: none;
    }
  }
</style>
