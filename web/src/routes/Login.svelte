<script lang="ts">
  // Login — clean two-column auth per docs/DESIGN-POLISH.md §1/§7. FE-admin owned.
  // Reads /api/auth/config for the SSO provider display name and the
  // registration policy, and surfaces OIDC failures handed back as
  // /login?error=… from the callback redirect.
  import { LogIn, Mail, MessageSquare, ShieldAlert } from 'lucide-svelte';
  import * as api from '$lib/api';
  import { navigate } from '$lib/router.svelte.js';
  import { bootstrap, session } from '$lib/session.svelte.js';
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

  // --- 2FA challenge state (BE-SEC) ------------------------------------------
  // totpToken lives in memory ONLY — never localStorage, and the OIDC-leg URL
  // copy is scrubbed on mount below.
  let totpMode = $state(false);
  let totpToken = $state('');
  let code = $state('');
  let totpError = $state('');

  // The challenge shape is typed locally rather than added to $lib/api (the
  // scaffold-owned fetch wrapper): only this page consumes it, so the type and
  // the one POST stay here.
  type TotpChallenge = { requires_totp: true; totp_token: string };

  function isTotpChallenge(v: unknown): v is TotpChallenge {
    const c = v as Partial<TotpChallenge> | null;
    return c !== null && c.requires_totp === true && typeof c.totp_token === 'string';
  }

  // POST /api/auth/totp {totp_token, code} → sets the session cookie. Mirrors
  // api.ts's req() envelope (and rethrows its ApiError, so status branches
  // below read exactly like every other API call in the app).
  async function verifyTotp(totpToken: string, code: string): Promise<void> {
    const res = await fetch('/api/auth/totp', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ totp_token: totpToken, code }),
    });
    if (!res.ok) {
      let msg = res.statusText || `HTTP ${res.status}`;
      try {
        const j = (await res.json()) as { error?: string };
        if (j.error) msg = j.error;
      } catch {
        /* body wasn't JSON */
      }
      throw new api.ApiError(res.status, msg);
    }
  }

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

  // OIDC callback 2FA leg: for a TOTP-enabled user the callback 302s here as
  // /login?totp=1&t=<single-use totp_token> (see oidcTOTPChallenge, internal/
  // auth/oidc.go). Seed the challenge from the query — same scrub pattern as
  // ?error above; the token then lives only in component memory.
  $effect(() => {
    const q = new URLSearchParams(window.location.search);
    if (q.get('totp') !== '1') return;
    const t = q.get('t');
    if (!t) return;
    totpToken = t;
    totpMode = true;
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
      // api.login directly, not session.login(): a 2FA-enabled account
      // answers {requires_totp, totp_token} instead of a User, and the store
      // helper would mis-file that payload as a signed-in user before we can
      // branch. The success path mirrors session.login's two assignments.
      const res = (await api.login(email, password)) as unknown as api.User | TotpChallenge;
      if (isTotpChallenge(res)) {
        totpToken = res.totp_token; // memory only
        password = '';
        totpMode = true;
        return;
      }
      session.user = res;
      session.authenticated = true;
      navigate('/');
    } catch (err) {
      formError = err instanceof Error && err.message ? err.message : 'Login failed';
    } finally {
      busy = false;
    }
  }

  async function submitTOTP(e: SubmitEvent) {
    e.preventDefault();
    if (!totpToken) return;
    busy = true;
    totpError = '';
    try {
      await verifyTotp(totpToken, code);
      totpMode = false;
      totpToken = '';
      code = '';
      await bootstrap(); // pull the fresh session into the store…
      navigate('/', { replace: true }); // …then home (post-login pattern)
    } catch (err) {
      if (err instanceof api.ApiError && (err.status === 401 || err.status === 403)) {
        // The BE burns the challenge nonce on EVERY attempt — pass or fail
        // (C6 security law: one minted token = one code guess). A retry on
        // this token, even with the now-correct code, is a replay of a
        // spent challenge and can only 401 again — so don't dangle "try
        // again": drop the dead token, return to the password step, and
        // let the user mint a fresh challenge.
        totpMode = false;
        totpToken = '';
        code = '';
        totpError = '';
        formError = 'That code was wrong or expired — sign in again with your password.';
      } else if (err instanceof api.ApiError && err.status === 429) {
        // Throttled BEFORE the nonce burn (per-IP cap / per-user bucket),
        // so this challenge is still unspent — staying on the code step and
        // waiting is honest here. If it keeps failing, the eventual 401
        // above bounces to the password form anyway.
        totpError = 'Too many attempts — wait a minute and try again.';
      } else {
        totpError = err instanceof Error && err.message ? err.message : 'Verification failed';
      }
    } finally {
      busy = false;
    }
  }

  function cancelTOTP() {
    totpMode = false;
    totpToken = '';
    code = '';
    totpError = '';
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
    <p class="mb-5 mt-0.5 text-sm" style="color:var(--muted)">
      {#if totpMode}Two-factor authentication is enabled on this account — enter your code to finish signing in.{:else}Sign in to your ChatGo workspace.{/if}
    </p>

    {#if totpMode}
      <form onsubmit={submitTOTP} class="flex flex-col gap-3.5">
        <label class="block text-sm">
          <span class="mb-1 block text-[13px]" style="color:var(--muted)">Authentication code</span>
          <input
            type="text"
            inputmode="numeric"
            autocomplete="one-time-code"
            pattern="[0-9]{6}"
            maxlength="6"
            required
            class="inp"
            placeholder="123456"
            bind:value={code}
          />
        </label>

        {#if totpError}
          <p class="pop rounded-[10px] px-3 py-2 text-[13px]" style="background:color-mix(in srgb, var(--danger) 9%, transparent); color:var(--danger)">
            {totpError}
          </p>
        {/if}

        <button type="submit" class={`${btn} mt-1 w-full`} style="background:var(--accent); color:var(--accent-fg)" disabled={busy}>
          {#if busy}<Spinner /> Verifying…{:else}<LogIn size={15} /> Verify{/if}
        </button>
        <button type="button" class={`${btn} w-full`} style={btnGhostStyle} onclick={cancelTOTP} disabled={busy}>
          ← Back to sign-in
        </button>
      </form>
    {:else}
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
    {/if}
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
