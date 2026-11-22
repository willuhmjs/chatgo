<script lang="ts">
  // Login (FE-admin owns the polished form; scaffold keeps it functional).
  import { login } from '$lib/session.svelte.js';
  import { navigate } from '$lib/router.svelte.js';
  import { push } from '$lib/toast.svelte.js';

  let email = $state('');
  let password = $state('');
  let busy = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    try {
      await login(email, password);
      navigate('/');
    } catch (err) {
      push(err instanceof Error ? err.message : 'Login failed', 'error');
    } finally {
      busy = false;
    }
  }
</script>

<div class="flex h-full items-center justify-center px-4">
  <form
    onsubmit={submit}
    class="w-full max-w-sm rounded-2xl border p-6"
    style="background:var(--panel); border-color:var(--border)"
  >
    <h1 class="mb-4 text-lg font-semibold">Sign in to ChatGo</h1>
    <label class="mb-3 block text-sm">
      <span class="mb-1 block text-[var(--muted)]">Email</span>
      <input
        type="email"
        required
        autocomplete="email"
        class="w-full rounded-lg border px-3 py-2 outline-none"
        style="background:var(--panel-2); border-color:var(--border)"
        bind:value={email}
      />
    </label>
    <label class="mb-4 block text-sm">
      <span class="mb-1 block text-[var(--muted)]">Password</span>
      <input
        type="password"
        required
        autocomplete="current-password"
        class="w-full rounded-lg border px-3 py-2 outline-none"
        style="background:var(--panel-2); border-color:var(--border)"
        bind:value={password}
      />
    </label>
    <button
      type="submit"
      disabled={busy}
      class="w-full rounded-lg py-2 text-sm font-medium disabled:opacity-50"
      style="background:var(--accent); color:var(--accent-fg)"
    >
      {busy ? 'Signing in…' : 'Sign in'}
    </button>
    <p class="mt-3 text-center text-xs text-[var(--muted)]">
      Need an account? <a href="/register" class="text-[var(--accent)]">Register</a>
    </p>
  </form>
</div>
