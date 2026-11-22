<script lang="ts">
  // Login — split hero + card. FE-admin owned. Reads /api/auth/config for the
  // SSO provider display name and the registration policy, and surfaces OIDC
  // failures handed back as /login?error=… from the callback redirect.
  import { LogIn, Mail, MessageSquare, ShieldAlert } from 'lucide-svelte';
  import * as api from '$lib/api';
  import { navigate } from '$lib/router.svelte.js';
  import { login, session } from '$lib/session.svelte.js';
  import { push } from '$lib/toast.svelte.js';
  import AuthShell from '$lib/admin/AuthShell.svelte';
  import Spinner from '$lib/admin/ui/Spinner.svelte';
  import { btn, btnGhostStyle, input, inputStyle } from '$lib/admin/ui/styles';

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
  <div class="flex flex-col gap-4 lg:hidden">
    <div class="flex items-center gap-2">
      <span class="flex h-8 w-8 items-center justify-center rounded-lg" style="background:var(--accent); color:var(--accent-fg)">
        <MessageSquare size={16} />
      </span>
      <span class="font-semibold">ChatGo</span>
    </div>
  </div>

  {#if banner}
    <div role="alert" class="mb-4 flex items-start gap-2 rounded-xl border px-3.5 py-2.5 text-xs leading-relaxed" style="border-color:var(--danger); background:color-mix(in srgb, var(--danger) 10%, transparent); color:var(--danger)">
      <ShieldAlert size={15} class="mt-0.5 shrink-0" />
      {banner}
    </div>
  {/if}

  <div class="rounded-2xl border p-6 shadow-xl" style="background:var(--panel); border-color:var(--border)">
    <h1 class="text-lg font-semibold">Welcome back</h1>
    <p class="mb-5 mt-0.5 text-sm text-[var(--muted)]">Sign in to your ChatGo workspace.</p>

    <form onsubmit={submit} class="flex flex-col gap-3.5">
      <label class="block text-sm">
        <span class="mb-1 block text-[var(--muted)]">Email</span>
        <input
          type="email"
          required
          autocomplete="email"
          class={input}
          style={inputStyle}
          placeholder="you@example.com"
          bind:value={email}
        />
      </label>
      <label class="block text-sm">
        <span class="mb-1 block text-[var(--muted)]">Password</span>
        <input
          type="password"
          required
          autocomplete="current-password"
          class={input}
          style={inputStyle}
          placeholder="••••••••"
          bind:value={password}
        />
      </label>

      {#if formError}
        <p class="rounded-lg px-3 py-2 text-xs" style="background:color-mix(in srgb, var(--danger) 12%, transparent); color:var(--danger)">
          {formError}
        </p>
      {/if}

      <button type="submit" class={btn} style="background:var(--accent); color:var(--accent-fg)" disabled={busy}>
        {#if busy}<Spinner /> Signing in…{:else}<LogIn size={15} /> Sign in{/if}
      </button>
    </form>

    {#if ssoLabel}
      <div class="my-4 flex items-center gap-3 text-[11px] uppercase tracking-wider text-[var(--muted)]">
        <span class="h-px flex-1" style="background:var(--border)"></span> or <span class="h-px flex-1" style="background:var(--border)"></span>
      </div>
      <button type="button" class={`${btn} w-full`} style={btnGhostStyle} onclick={sso}>
        <Mail size={15} /> Continue with {ssoLabel}
      </button>
    {/if}

    <p class="mt-5 text-center text-xs text-[var(--muted)]">
      {#if config === null || config.registration !== 'closed'}
        Need an account? <a href="/register" class="font-medium text-[var(--accent)] hover:underline">Register</a>
      {:else}
        Registration is closed — ask your administrator for an invite.
      {/if}
    </p>
  </div>
</AuthShell>
