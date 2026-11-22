<script lang="ts">
  // Settings — non-admin sections: profile / preferences / account.
  // Admin console lives at /admin (FE-admin); the shell links there.
  // Restyled per docs/DESIGN-POLISH.md §5 using the local admin primitives in
  // $lib/admin/ui (no $lib/ui imports — that kit is owned elsewhere).
  import { session } from '$lib/session.svelte.js';
  import { navigate } from '$lib/router.svelte.js';
  import { theme, setTheme } from '$lib/theme.svelte.js';
  import { push } from '$lib/toast.svelte.js';
  import { logout } from '$lib/session.svelte.js';
  import { ApiError } from '$lib/api';
  import { changePassword, getSendOnEnter, logoutEverywhere, setMeName, setSendOnEnter } from '$lib/api-shell';
  import TokensPanel from '$lib/admin/TokensPanel.svelte';
  import Confirm from '$lib/admin/ui/Confirm.svelte';
  import Field from '$lib/admin/ui/Field.svelte';
  import Toggle from '$lib/admin/ui/Toggle.svelte';
  import { btn, btnGhostStyle, btnPrimaryStyle, input, inputStyle } from '$lib/admin/ui/styles';
  import { Moon, ShieldCheck, Sun } from 'lucide-svelte';

  let { tab = 'profile' }: { tab?: 'profile' | 'preferences' | 'account' } = $props();

  // ----- profile -----
  let name = $state(session.user?.name ?? '');
  let savingName = $state(false);
  const dirty = $derived(name.trim() !== '' && name.trim() !== (session.user?.name ?? ''));

  async function saveName() {
    const n = name.trim();
    if (!n || savingName) return;
    savingName = true;
    try {
      await setMeName(n);
      if (session.user) session.user.name = n;
      push('Profile saved', 'success');
    } catch (e) {
      push(e instanceof ApiError ? e.message : 'Could not save profile', 'error');
    } finally {
      savingName = false;
    }
  }

  // ----- preferences -----
  let sendOnEnter = $state(getSendOnEnter());

  // ----- account -----
  let curPw = $state('');
  let newPw = $state('');
  let confirmPw = $state('');
  let pwBusy = $state(false);
  const pwError = $derived(
    newPw && confirmPw && newPw !== confirmPw ? 'New passwords do not match.' : '',
  );
  const pwReady = $derived(curPw.length > 0 && newPw.length >= 8 && newPw === confirmPw);

  async function doChangePassword() {
    if (!pwReady || pwBusy) return;
    pwBusy = true;
    try {
      await changePassword(curPw, newPw);
      curPw = newPw = confirmPw = '';
      push('Password changed', 'success');
    } catch (e) {
      push(e instanceof ApiError ? e.message : 'Password change failed', 'error');
    } finally {
      pwBusy = false;
    }
  }

  let confirmSignout = $state(false);

  async function doLogoutEverywhere() {
    try {
      await logoutEverywhere();
    } catch {
      /* still drop local state */
    }
    session.user = null;
    navigate('/login');
  }

  async function doLogout() {
    await logout();
    navigate('/login');
  }

  $effect(() => {
    name = session.user?.name ?? '';
  });
</script>

<div class="w-full space-y-4">
  {#if tab === 'profile'}
    <section class="rounded-2xl p-4 sm:p-5" style="background:var(--panel); color:var(--text)">
      <h2 class="text-[15px] font-medium">Profile</h2>
      <p class="mt-0.5 mb-4 text-xs text-[var(--muted)]">How your name appears in the sidebar and chats.</p>
      {#if session.user}
        <form
          class="max-w-sm space-y-3"
          onsubmit={(e) => {
            e.preventDefault();
            void saveName();
          }}
        >
          <Field label="Display name">
            <input type="text" class={input} style={inputStyle} placeholder="Ada Lovelace" bind:value={name} />
          </Field>
          <div>
            <p class="text-xs font-medium text-[var(--muted)]">Email</p>
            <p class="mt-0.5 text-sm">{session.user.email}</p>
          </div>
          <button
            type="submit"
            class={btn}
            style={btnPrimaryStyle}
            disabled={!dirty || savingName}
          >
            {savingName ? 'Saving…' : 'Save'}
          </button>
        </form>
      {:else}
        <p class="text-sm text-[var(--muted)]">Not signed in.</p>
      {/if}
    </section>
  {:else if tab === 'preferences'}
    <section class="rounded-2xl p-4 sm:p-5" style="background:var(--panel); color:var(--text)">
      <h2 class="text-[15px] font-medium">Preferences</h2>
      <p class="mt-0.5 mb-4 text-xs text-[var(--muted)]">Stored on this device.</p>

      <div class="flex items-center justify-between gap-4 border-t py-3.5" style="border-color:var(--border)">
        <div>
          <p class="text-sm font-medium">Theme</p>
          <p class="text-xs text-[var(--muted)]">Dark is the default; choice is persisted.</p>
        </div>
        <div class="flex items-center gap-0.5 rounded-full p-0.5" style="background:var(--panel-2)">
          <button
            type="button"
            class="rounded-full p-1.5 transition-colors"
            style={theme.mode === 'dark' ? 'background:var(--accent); color:var(--accent-fg)' : 'color:var(--muted)'}
            aria-pressed={theme.mode === 'dark'}
            aria-label="Dark mode"
            onclick={() => setTheme('dark')}
          >
            <Moon class="size-4" />
          </button>
          <button
            type="button"
            class="rounded-full p-1.5 transition-colors"
            style={theme.mode === 'light' ? 'background:var(--accent); color:var(--accent-fg)' : 'color:var(--muted)'}
            aria-pressed={theme.mode === 'light'}
            aria-label="Light mode"
            onclick={() => setTheme('light')}
          >
            <Sun class="size-4" />
          </button>
        </div>
      </div>

      <div class="flex items-center justify-between gap-4 border-t py-3.5" style="border-color:var(--border)">
        <div>
          <p class="text-sm font-medium">Send message on Enter</p>
          <p class="text-xs text-[var(--muted)]">When off, use ⌘/Ctrl + Enter to send.</p>
        </div>
        <Toggle label="Send on Enter" bind:checked={sendOnEnter} onchange={() => setSendOnEnter(sendOnEnter)} />
      </div>
    </section>
  {:else if tab === 'account'}
    <section class="rounded-2xl p-4 sm:p-5" style="background:var(--panel); color:var(--text)">
      <h2 class="text-[15px] font-medium">Account</h2>
      {#if session.user}
        <p class="mt-0.5 mb-3 flex items-center gap-1.5 text-xs text-[var(--muted)]">
          {session.user.email}
          {#if session.user.role === 'admin'}
            <span class="flex items-center gap-0.5 text-[var(--text)]"><ShieldCheck class="size-3" /> admin</span>
          {/if}
        </p>
      {/if}

      {#if session.user}
        <form
          class="mt-2 max-w-sm space-y-3 border-t pt-4"
          style="border-color:var(--border)"
          onsubmit={(e) => {
            e.preventDefault();
            void doChangePassword();
          }}
        >
          <p class="text-sm font-medium">Change password</p>
          <Field label="Current password">
            <input type="password" autocomplete="current-password" class={input} style={inputStyle} bind:value={curPw} />
          </Field>
          <Field label="New password">
            <input
              type="password"
              autocomplete="new-password"
              class={input}
              style="background:var(--panel-2); border-color:{newPw && newPw.length < 8 ? 'var(--danger)' : 'var(--border)'}; color:var(--text)"
              bind:value={newPw}
            />
            {#if newPw && newPw.length < 8}
              <span class="mt-1 block text-xs" style="color:var(--danger)">At least 8 characters.</span>
            {/if}
          </Field>
          <Field label="Confirm new password">
            <input
              type="password"
              autocomplete="new-password"
              class={input}
              style="background:var(--panel-2); border-color:{pwError ? 'var(--danger)' : 'var(--border)'}; color:var(--text)"
              bind:value={confirmPw}
            />
            {#if pwError}
              <span class="mt-1 block text-xs" style="color:var(--danger)">{pwError}</span>
            {/if}
          </Field>
          <button type="submit" class={btn} style={btnPrimaryStyle} disabled={!pwReady || pwBusy}>
            {pwBusy ? 'Changing…' : 'Change password'}
          </button>
          <p class="text-[11px] leading-relaxed text-[var(--muted)]">
            Changing your password signs you out of every other device; this browser stays signed
            in. SSO accounts should use their identity provider.
          </p>
        </form>
      {/if}

      <div class="mt-4 flex flex-wrap items-center gap-2 border-t pt-4" style="border-color:var(--border)">
        <button class={btn} style={btnGhostStyle} onclick={() => void doLogout()}>Sign out</button>
        <button
          class={btn}
          style="background:transparent; color:var(--danger)"
          onclick={() => (confirmSignout = true)}
        >
          Sign out everywhere
        </button>
      </div>
    </section>

    <section class="mt-4 rounded-2xl p-4 sm:p-5" style="background:var(--panel); color:var(--text)">
      <h2 class="text-[15px] font-medium">API tokens</h2>
      <p class="mt-0.5 mb-1 text-xs text-[var(--muted)]">
        Issue <code class="font-mono">ck_…</code> keys for scripts and tools against the OpenAI-compatible
        <code class="font-mono">/v1/chat/completions</code> endpoint.
      </p>
      <TokensPanel />
    </section>
  {/if}
</div>

<Confirm
  bind:open={confirmSignout}
  title="Sign out everywhere?"
  message="Your current session will end. Other devices need to sign in again."
  confirmLabel="Sign out"
  danger
  onconfirm={doLogoutEverywhere}
/>
