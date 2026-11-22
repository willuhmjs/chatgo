<script lang="ts">
  // Register (FE-admin owns polish; scaffold keeps the flow callable).
  import * as api from '$lib/api';
  import { navigate } from '$lib/router.svelte.js';
  import { push } from '$lib/toast.svelte.js';

  let name = $state('');
  let email = $state('');
  let password = $state('');
  let busy = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    try {
      await api.register(email, password, name);
      push('Account created.', 'success');
      navigate('/login');
    } catch (err) {
      push(err instanceof Error ? err.message : 'Registration failed', 'error');
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
    <h1 class="mb-4 text-lg font-semibold">Create account</h1>
    <label class="mb-3 block text-sm">
      <span class="mb-1 block text-[var(--muted)]">Name</span>
      <input
        type="text"
        required
        class="w-full rounded-lg border px-3 py-2 outline-none"
        style="background:var(--panel-2); border-color:var(--border)"
        bind:value={name}
      />
    </label>
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
        autocomplete="new-password"
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
      {busy ? 'Creating…' : 'Register'}
    </button>
    <p class="mt-3 text-center text-xs text-[var(--muted)]">
      Have an account? <a href="/login" class="text-[var(--accent)]">Sign in</a>
    </p>
  </form>
</div>
