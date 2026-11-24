<script lang="ts">
  // First-run admin bootstrap (docs/AUTHGATE-CONTRACT.md §5) — standalone card
  // on /welcome, sent here by session bootstrap when auth.status.first_run.
  // AG-2 owned. Deliberately imports NO lib/ui or lib/admin components (POLISH
  // agents own those concurrently) — plain markup on DESIGN-POLISH tokens.
  import { LoaderCircle, MessageSquare } from 'lucide-svelte';
  import * as api from '$lib/api';
  import { navigate } from '$lib/router.svelte.js';
  import { refresh } from '$lib/session.svelte.js';
  import { registerAdmin } from '$lib/onboarding-api';

  let name = $state('');
  let email = $state('');
  let password = $state('');
  let busy = $state(false);
  let error = $state('');
  let config = $state<api.AuthConfig | null>(null);

  $effect(() => {
    api.authConfig().then(
      (c) => (config = c),
      () => void 0, // the local-account form still works without the config
    );
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (busy) return;
    busy = true;
    error = '';
    try {
      await registerAdmin(name.trim(), email.trim(), password);
      await refresh(); // session picks up the new admin + first_run=false
      navigate('/', { replace: true });
    } catch (err) {
      error = err instanceof Error && err.message ? err.message : 'Could not create the account';
    } finally {
      busy = false;
    }
  }

  // Muted note only — the SSO button intentionally lives on /login, not here.
  const ssoNote = $derived(config?.oidc_enabled ? config.oidc_name || 'SSO' : '');
</script>

<section
  class="flex min-h-dvh flex-col items-center justify-center px-5 py-12"
  style="background:var(--bg); color:var(--text)"
>
  <div class="w-full max-w-[26rem]">
    <div class="mb-6 flex items-center justify-center gap-2">
      <span
        class="flex h-7 w-7 items-center justify-center rounded-lg"
        style="background:var(--acc-grad, var(--accent)); color:var(--accent-fg)"
      >
        <MessageSquare size={15} />
      </span>
      <span class="text-[15px] font-medium tracking-tight">{config?.title?.trim() || 'ChatGo'}</span>
    </div>

    <div class="rounded-2xl p-7" style="background:var(--panel)">
      <h1 class="text-[30px] font-semibold tracking-tight">Welcome to {config?.title?.trim() || 'ChatGo'}</h1>
      <p class="mt-1 text-sm" style="color:var(--muted)">Create your admin account</p>

      <form onsubmit={submit} class="mt-6 flex flex-col gap-3.5">
        <label class="block text-sm">
          <span class="mb-1 block text-[13px]" style="color:var(--muted)">Name</span>
          <input
            type="text"
            required
            maxlength={64}
            autocomplete="name"
            class="inp"
            placeholder="Ada Lovelace"
            bind:value={name}
          />
        </label>
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
            minlength={8}
            maxlength={72}
            autocomplete="new-password"
            class="inp"
            placeholder="At least 8 characters"
            bind:value={password}
          />
        </label>

        {#if error}
          <p
            role="alert"
            class="rounded-[10px] px-3 py-2 text-[13px]"
            style="background:color-mix(in srgb, var(--danger) 9%, transparent); color:var(--danger)"
          >
            {error}
          </p>
        {/if}

        <button type="submit" class="primary mt-1 w-full justify-center" disabled={busy}>
          {#if busy}
            <LoaderCircle size={15} class="animate-spin" /> Creating…
          {:else}
            Create admin account
          {/if}
        </button>
      </form>

      {#if ssoNote}
        <p class="mt-5 text-center text-xs" style="color:var(--muted)">
          Single sign-on will be available at {ssoNote} after setup.
        </p>
      {/if}
    </div>

    <p class="mt-5 text-center text-[11px]" style="color:var(--faint)">
      This account will own the instance — everyone else can be invited afterwards.
    </p>
  </div>
</section>

<style>
  /* inputs sit one step brighter than the --panel card: fill contrast, §1 */
  .inp {
    width: 100%;
    border-radius: 10px;
    padding: 0.5rem 0.75rem;
    font-size: 0.875rem;
    color: var(--text);
    background: var(--panel-2);
    border: 1px solid transparent;
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
  .primary {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    border: 0;
    border-radius: 999px;
    padding: 0.5625rem 1rem;
    font-size: 0.875rem;
    font-weight: 500;
    background: var(--accent);
    color: var(--accent-fg);
    cursor: pointer;
    transition: opacity 120ms ease;
  }
  .primary:hover {
    opacity: 0.9;
  }
  .primary:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .primary:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--acc) 60%, transparent);
    outline-offset: 2px;
  }
</style>
