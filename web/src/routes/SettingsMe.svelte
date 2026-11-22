<script lang="ts">
  // Settings — non-admin sections (FE-shell): profile / preferences / account.
  // Admin console lives at /admin (FE-admin); the shell links there.
  import { session } from '$lib/session.svelte.js';
  import { navigate } from '$lib/router.svelte.js';
  import { theme, setTheme } from '$lib/theme.svelte.js';
  import { push } from '$lib/toast.svelte.js';
  import { logout } from '$lib/session.svelte.js';
  import { ApiError } from '$lib/api';
  import { changePassword, getSendOnEnter, logoutEverywhere, setMeName, setSendOnEnter } from '$lib/api-shell';
  import TokensPanel from '$lib/admin/TokensPanel.svelte';
  import { confirmDialog } from '$lib/ui/confirm.svelte.js';
  import Button from '$lib/ui/Button.svelte';
  import IconButton from '$lib/ui/IconButton.svelte';
  import Input from '$lib/ui/Input.svelte';
  import Toggle from '$lib/ui/Toggle.svelte';
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

  async function doLogoutEverywhere() {
    const ok = await confirmDialog({
      title: 'Sign out everywhere?',
      message: 'Your current session will end. Other devices need to sign in again.',
      confirmLabel: 'Sign out',
      danger: true,
    });
    if (!ok) return;
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
    <section class="rounded-xl border p-4 sm:p-5" style="background:var(--panel); border-color:var(--border)">
      <h2 class="text-sm font-semibold">Profile</h2>
      <p class="mt-0.5 mb-4 text-xs text-[var(--muted)]">How your name appears in the sidebar and chats.</p>
      {#if session.user}
        <form
          class="max-w-sm space-y-3"
          onsubmit={(e) => {
            e.preventDefault();
            void saveName();
          }}
        >
          <Input label="Display name" bind:value={name} placeholder="Ada Lovelace" />
          <div>
            <p class="text-xs font-medium text-[var(--muted)]">Email</p>
            <p class="mt-0.5 text-sm">{session.user.email}</p>
          </div>
          <Button type="submit" size="sm" disabled={!dirty || savingName}>
            {savingName ? 'Saving…' : 'Save'}
          </Button>
        </form>
      {:else}
        <p class="text-sm text-[var(--muted)]">Not signed in.</p>
      {/if}
    </section>
  {:else if tab === 'preferences'}
    <section class="rounded-xl border p-4 sm:p-5" style="background:var(--panel); border-color:var(--border)">
      <h2 class="text-sm font-semibold">Preferences</h2>
      <p class="mt-0.5 mb-4 text-xs text-[var(--muted)]">Stored on this device.</p>

      <div class="flex items-center justify-between gap-4 border-t py-3.5" style="border-color:var(--border)">
        <div>
          <p class="text-sm font-medium">Theme</p>
          <p class="text-xs text-[var(--muted)]">Dark is the default; choice is persisted.</p>
        </div>
        <div class="flex rounded-lg border p-0.5" style="border-color:var(--border)">
          <IconButton label="Dark mode" variant="ghost" active={theme.mode === 'dark'} onclick={() => setTheme('dark')}>
            <Moon class="size-4" />
          </IconButton>
          <IconButton label="Light mode" variant="ghost" active={theme.mode === 'light'} onclick={() => setTheme('light')}>
            <Sun class="size-4" />
          </IconButton>
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
    <section class="rounded-xl border p-4 sm:p-5" style="background:var(--panel); border-color:var(--border)">
      <h2 class="text-sm font-semibold">Account</h2>
      {#if session.user}
        <p class="mt-0.5 mb-3 flex items-center gap-1.5 text-xs text-[var(--muted)]">
          {session.user.email}
          {#if session.user.role === 'admin'}
            <span class="flex items-center gap-0.5 text-[var(--accent)]"><ShieldCheck class="size-3" /> admin</span>
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
          <Input label="Current password" type="password" autocomplete="current-password" bind:value={curPw} />
          <Input
            label="New password"
            type="password"
            autocomplete="new-password"
            bind:value={newPw}
            error={newPw && newPw.length < 8 ? 'At least 8 characters.' : ''}
          />
          <Input
            label="Confirm new password"
            type="password"
            autocomplete="new-password"
            bind:value={confirmPw}
            error={pwError}
          />
          <Button type="submit" size="sm" disabled={!pwReady || pwBusy}>
            {pwBusy ? 'Changing…' : 'Change password'}
          </Button>
          <p class="text-[11px] leading-relaxed text-[var(--muted)]">
            Changing your password signs you out of every other device; this browser stays signed
            in. SSO accounts should use their identity provider.
          </p>
        </form>
      {/if}

      <div class="mt-4 flex flex-wrap items-center gap-2 border-t pt-4" style="border-color:var(--border)">
        <Button variant="outline" size="sm" onclick={() => void doLogout()}>Sign out</Button>
        <Button variant="danger" size="sm" onclick={() => void doLogoutEverywhere()}>Sign out everywhere</Button>
      </div>
    </section>

    <section class="mt-4 rounded-xl border p-4 sm:p-5" style="background:var(--panel); border-color:var(--border)">
      <h2 class="text-sm font-semibold">API tokens</h2>
      <p class="mt-0.5 mb-1 text-xs text-[var(--muted)]">
        Issue <code>ck_…</code> keys for scripts and tools against the OpenAI-compatible
        <code>/v1/chat/completions</code> endpoint.
      </p>
      <TokensPanel />
    </section>
  {/if}
</div>
