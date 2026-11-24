<script lang="ts">
  // Register — clean card auth per docs/DESIGN-POLISH.md §1 (FE-admin owned).
  // Strength meter + "awaiting approval" success screen for the `approve`
  // policy (docs/CONTRACTS.md §Config Registration).
  import { CircleCheck, LogIn, LogOut, MessageSquare, UserPlus } from 'lucide-svelte';
  import * as api from '$lib/api';
  import { navigate } from '$lib/router.svelte.js';
  import { logout } from '$lib/session.svelte.js';
  import { push } from '$lib/toast.svelte.js';
  import AuthShell from '$lib/admin/AuthShell.svelte';
  import Spinner from '$lib/admin/ui/Spinner.svelte';
  import { btn, btnGhostStyle } from '$lib/admin/ui/styles';

  let name = $state('');
  let email = $state('');
  let password = $state('');
  let busy = $state(false);
  let formError = $state('');
  let submitted = $state(false); // pending-approval success screen
  let config = $state<api.AuthConfig | null>(null);

  $effect(() => {
    api.authConfig().then(
      (c) => (config = c),
      () => void 0,
    );
  });

  // 0..4 — length, classes, mix, long. Cheap heuristic, good enough UX signal.
  const strength = $derived.by(() => {
    const p = password;
    if (!p) return 0;
    let s = 0;
    if (p.length >= 8) s++;
    if (/[a-z]/.test(p) && /[0-9]/.test(p)) s++;
    if (/[A-Z]/.test(p) || /[^A-Za-z0-9]/.test(p)) s++;
    if (p.length >= 12) s++;
    return s;
  });
  const strengthMeta = $derived(
    [
      { label: '', color: 'transparent' },
      { label: 'Too weak', color: 'var(--danger)' },
      { label: 'Weak', color: 'var(--danger)' },
      { label: 'Fair', color: '#f59e0b' },
      { label: 'Strong', color: '#10a37f' },
    ][strength],
  );

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    formError = '';
    try {
      const user = await api.register(email, password, name);
      if (user?.role === 'pending') {
        submitted = true;
        return;
      }
      push('Account created — you can sign in now.', 'success');
      navigate('/login');
    } catch (err) {
      formError = err instanceof Error && err.message ? err.message : 'Registration failed';
    } finally {
      busy = false;
    }
  }

  async function signOut() {
    try {
      await logout(); // clear any partial session cookie a pending signup may have set
    } finally {
      navigate('/login');
    }
  }
</script>

<AuthShell>
  <div class="mb-5 flex items-center gap-2 lg:hidden">
    <span class="flex h-7 w-7 items-center justify-center rounded-lg" style="background:var(--accent); color:var(--accent-fg)">
      <MessageSquare size={15} />
    </span>
    <span class="text-[15px] font-medium tracking-tight">{config?.title?.trim() || 'ChatGo'}</span>
  </div>

  <div class="rounded-2xl p-6" style="background:var(--panel); color:var(--text)">
    {#if submitted}
      <!-- pending-policy success -->
      <div class="flex flex-col items-center gap-3 py-4 text-center">
        <span class="flex h-12 w-12 items-center justify-center rounded-full" style="background:color-mix(in srgb, #10a37f 14%, transparent); color:#10a37f">
          <CircleCheck size={26} />
        </span>
        <h1 class="text-[17px] font-semibold tracking-tight">Request submitted</h1>
        <p class="text-sm leading-relaxed" style="color:var(--muted)">
          Your account <span class="font-medium text-[var(--text)]">{email}</span> is awaiting admin approval.
          You'll be able to sign in as soon as an administrator approves you.
        </p>
        <div class="mt-2 flex w-full items-center gap-2">
          <button class={`${btn} flex-1`} style={btnGhostStyle} onclick={() => navigate('/login')}>
            <LogIn size={14} /> Try signing in
          </button>
          <button class={`${btn} flex-1`} style={btnGhostStyle} onclick={signOut}>
            <LogOut size={14} /> Log out
          </button>
        </div>
      </div>
    {:else if config?.registration === 'closed'}
      <div class="flex flex-col items-center gap-3 py-4 text-center">
        <h1 class="text-[17px] font-semibold tracking-tight">Registration is closed</h1>
        <p class="text-sm leading-relaxed" style="color:var(--muted)">
          {config?.title?.trim() || 'ChatGo'} instance isn't accepting new accounts. Ask an administrator to enable registration or invite you
          through single sign-on.
        </p>
        <a href="/login" class={`${btn} mt-2 w-full`} style={btnGhostStyle}>
          <LogIn size={14} /> Back to sign in
        </a>
      </div>
    {:else}
      <h1 class="text-[17px] font-semibold tracking-tight">Create your account</h1>
      <p class="mb-5 mt-0.5 text-sm" style="color:var(--muted)">
        {#if config?.registration === 'approve'}
          An administrator will approve your request before you can sign in.
        {:else}
          Sign up and start chatting in seconds.
        {/if}
      </p>

      <form onsubmit={submit} class="flex flex-col gap-3.5">
        <label class="block text-sm">
          <span class="mb-1 block text-[13px]" style="color:var(--muted)">Name</span>
          <input type="text" required maxlength={64} autocomplete="name" class="inp" placeholder="Ada Lovelace" bind:value={name} />
        </label>
        <label class="block text-sm">
          <span class="mb-1 block text-[13px]" style="color:var(--muted)">Email</span>
          <input type="email" required autocomplete="email" class="inp" placeholder="you@example.com" bind:value={email} />
        </label>
        <label class="block text-sm">
          <span class="mb-1 block text-[13px]" style="color:var(--muted)">Password</span>
          <input type="password" required minlength={8} maxlength={72} autocomplete="new-password" class="inp" placeholder="At least 8 characters" bind:value={password} />
          <span class="mt-1 block text-[11px] leading-relaxed" style="color:var(--faint)">
            8–72 characters — the server only hashes the first 72 bytes, so very long or multi-byte passwords are truncated.
          </span>
          {#if password}
            <span class="mt-2 flex items-center gap-2">
              <span class="flex flex-1 gap-1" aria-hidden="true">
                {#each [1, 2, 3, 4] as seg (seg)}
                  <span class="h-1 flex-1 rounded-full transition-colors" style="background:{seg <= strength ? strengthMeta.color : 'var(--border)'}"></span>
                {/each}
              </span>
              <span class="w-16 text-right text-[11px] font-medium" style="color:{strengthMeta.color}">
                {strengthMeta.label}
              </span>
            </span>
          {/if}
        </label>

        {#if formError}
          <p class="pop rounded-[10px] px-3 py-2 text-[13px]" style="background:color-mix(in srgb, var(--danger) 9%, transparent); color:var(--danger)">
            {formError}
          </p>
        {/if}

        <button type="submit" class={`${btn} mt-1 w-full`} style="background:var(--accent); color:var(--accent-fg)" disabled={busy || !password}>
          {#if busy}<Spinner /> Creating…{:else}<UserPlus size={15} /> Create account{/if}
        </button>
      </form>

      <p class="mt-5 text-center text-xs" style="color:var(--muted)">
        Already registered? <a href="/login" class="font-medium text-[var(--text)] underline-offset-2 hover:underline">Sign in</a>
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
