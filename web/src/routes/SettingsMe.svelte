<script lang="ts">
  // Settings — non-admin sections: profile / preferences / account.
  // Admin console lives at /admin (FE-admin); the shell links there.
  // Restyled per docs/DESIGN-POLISH.md §5 using the local admin primitives in
  // $lib/admin/ui (no $lib/ui imports — that kit is owned elsewhere).
  // FE-SEC (wave 2): + two-factor authentication + memory manager sections in
  // the account tab, backed by $lib/security-api.
  import { session } from '$lib/session.svelte.js';
  import { navigate } from '$lib/router.svelte.js';
  import { theme, setTheme } from '$lib/theme.svelte.js';
  import { ACCENTS, accent, setAccent } from '$lib/accent.svelte.js';
  import { push } from '$lib/toast.svelte.js';
  import { logout } from '$lib/session.svelte.js';
  import { ApiError, listChats } from '$lib/api';
  import type { Chat } from '$lib/api';
  import { changePassword, getSendOnEnter, logoutEverywhere, setMeName, setSendOnEnter } from '$lib/api-shell';
  import {
    addMemory,
    deleteMemory,
    disableTwoFactor,
    enableTwoFactor,
    extractMemories,
    getTwoFactorStatus,
    listMemories,
    setupTwoFactor,
    updateMemory,
    type Memory,
    type TwoFactorStatus,
  } from '$lib/security-api';
  import { copyText } from '$lib/chatview-api';
  import { modelPrefs, load as loadModelPrefs, setDefault, unhide, unhideAll } from '$lib/chat/model-prefs.svelte.js';
  import { loadModels, findModel } from '$lib/chat/state.svelte.js';
  import TokensPanel from '$lib/admin/TokensPanel.svelte';
  import Badge from '$lib/admin/ui/Badge.svelte';
  import Confirm from '$lib/admin/ui/Confirm.svelte';
  import Field from '$lib/admin/ui/Field.svelte';
  import Modal from '$lib/admin/ui/Modal.svelte';
  import Spinner from '$lib/admin/ui/Spinner.svelte';
  import Toggle from '$lib/admin/ui/Toggle.svelte';
  import {
    btn,
    btnDangerStyle,
    btnGhostStyle,
    btnPrimaryStyle,
    errorBannerStyle,
    input,
    inputStyle,
  } from '$lib/admin/ui/styles';
  import { Check, Copy, Eye, Moon, Pencil, ShieldCheck, Sun, Trash2 } from 'lucide-svelte';

  let { tab = 'profile' }: { tab?: 'profile' | 'preferences' | 'account' } = $props();

  /** Shared quiet-red inline error text for ApiError-shaped failures. */
  function errText(e: unknown, fallback: string): string {
    return e instanceof ApiError && e.message ? e.message : fallback;
  }

  // Picker display hues (FE-COLOR): the dark-mode token values — they read on
  // both card surfaces; the live tokens live in app.css per html[data-accent].
  const SWATCH: Record<string, string> = {
    violet: '#a78bfa',
    blue: '#60a5fa',
    teal: '#2dd4bf',
    green: '#4ade80',
    orange: '#fbbf24',
    pink: '#f472b6',
  };

  /** Picker label for an accent id ("violet" → "Violet"). */
  function accentLabel(a: string): string {
    return a.charAt(0).toUpperCase() + a.slice(1);
  }

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

  // ----- account: password -----
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

  // ----- account: two-factor -----
  // 'probing' until the GET /api/me/2fa/status read resolves; 'unavailable'
  // (route missing / unreachable) hides the whole section. The status read
  // fires when the account tab first mounts — not at app boot. (It replaces
  // the old bodiless POST /2fa/setup probe, which lied "Off" for enrolled
  // users — the BE checks the password before the enrollment 409 — and
  // burned the setup rate bucket on every mount.)
  type TwofaUi = TwoFactorStatus | 'probing';
  let twofa = $state<TwofaUi>('probing');
  let twofaProbed = false;
  let tfStage = $state<'idle' | 'setup' | 'verify' | 'disable'>('idle');
  let tfPw = $state('');
  let tfCode = $state('');
  let tfSecret = $state('');
  let tfUri = $state('');
  let tfBusy = $state(false);
  let tfError = $state('');

  function pwErr(e: unknown): string {
    return e instanceof ApiError && e.status === 401 ? 'Incorrect password.' : errText(e, 'Something went wrong. Try again.');
  }

  function resetTwoFa() {
    tfStage = 'idle';
    tfPw = '';
    tfCode = '';
    tfSecret = ''; // secrets never outlive the step that needs them
    tfUri = '';
    tfError = '';
  }

  async function submitTwoFaPassword() {
    if (!tfPw || tfBusy) return;
    tfBusy = true;
    tfError = '';
    try {
      if (tfStage === 'setup') {
        const r = await setupTwoFactor(tfPw);
        tfPw = '';
        tfSecret = r.secret;
        tfUri = r.otpauth_uri;
        tfStage = 'verify';
      } else {
        await disableTwoFactor(tfPw);
        twofa = 'off';
        resetTwoFa();
        push('Two-factor authentication disabled', 'success');
      }
    } catch (e) {
      tfError = pwErr(e);
    } finally {
      tfBusy = false;
    }
  }

  async function submitTwoFaCode() {
    const code = tfCode.replace(/\D/g, '');
    if (code.length !== 6 || tfBusy) return;
    tfBusy = true;
    tfError = '';
    try {
      await enableTwoFactor(code);
      twofa = 'enabled';
      resetTwoFa();
      push('Two-factor authentication enabled', 'success');
    } catch (e) {
      tfError = e instanceof ApiError && e.status === 401 ? 'Incorrect code, try again' : errText(e, 'Could not enable two-factor');
    } finally {
      tfBusy = false;
    }
  }

  async function copySecret(value: string) {
    const ok = await copyText(value);
    push(ok ? 'Copied to clipboard' : 'Copy failed', ok ? 'success' : 'error', 2000);
  }

  // ----- account: memories -----
  const MEM_LIMIT = 200;
  let memories = $state<Memory[]>([]);
  let memLoading = $state(false);
  let memLoaded = false;
  let memAvailable = $state(true); // false once GET 404s (BE not wired yet)
  let memError = $state('');
  let memNew = $state('');
  let memBusy = $state(false);
  let editId = $state('');
  let editDraft = $state('');

  async function refreshMemories() {
    memLoading = true;
    try {
      memories = await listMemories();
      memError = '';
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) memAvailable = false;
      else memError = errText(e, 'Could not load memories');
    } finally {
      memLoading = false;
    }
  }

  async function addMemoryRow() {
    const c = memNew.trim();
    if (!c || memBusy) return;
    memBusy = true;
    try {
      const m = await addMemory(c);
      if (m) memories = [m, ...memories];
      else await refreshMemories();
      memNew = '';
      memError = '';
    } catch (e) {
      memError =
        e instanceof ApiError && e.status === 409
          ? 'Memory limit reached — prune some first'
          : errText(e, 'Could not add memory');
    } finally {
      memBusy = false;
    }
  }

  async function setMemoryEnabled(m: Memory, next: boolean) {
    try {
      await updateMemory(m.id, { enabled: next });
      memError = '';
    } catch (e) {
      m.enabled = !next; // Toggle wrote through the bind; roll it back
      memError = errText(e, 'Could not update memory');
    }
  }

  function startEditMemory(m: Memory) {
    editId = m.id;
    editDraft = m.content;
  }

  function cancelEditMemory() {
    editId = '';
    editDraft = '';
  }

  async function saveEditMemory(m: Memory) {
    const c = editDraft.trim();
    if (!c || memBusy) return;
    memBusy = true;
    try {
      const res = await updateMemory(m.id, { content: c });
      m.content = res?.content ?? c;
      cancelEditMemory();
      memError = '';
    } catch (e) {
      memError = errText(e, 'Could not save memory');
    } finally {
      memBusy = false;
    }
  }

  async function removeMemory(m: Memory) {
    const snapshot = memories;
    memories = memories.filter((x) => x.id !== m.id);
    if (editId === m.id) cancelEditMemory();
    try {
      await deleteMemory(m.id);
    } catch (e) {
      memories = snapshot;
      memError = errText(e, 'Could not delete memory');
    }
  }

  // ----- account: extract-from-chat picker -----
  let chatsOpen = $state(false);
  let chats = $state<Chat[]>([]);
  let chatsLoading = $state(false);
  let extractingId = $state('');

  async function openChats() {
    chatsOpen = true;
    if (chats.length) return;
    chatsLoading = true;
    try {
      chats = (await listChats({ per: 30 })).chats ?? [];
    } catch (e) {
      push(errText(e, 'Could not load chats'), 'error');
      chatsOpen = false;
    } finally {
      chatsLoading = false;
    }
  }

  async function extractFrom(chatId: string) {
    if (extractingId) return;
    extractingId = chatId;
    try {
      const r = await extractMemories(chatId);
      chatsOpen = false;
      // Surface the first few extracted facts when the response carries
      // them ({created:[Memory…]}) — proof the run did something before
      // the list refresh lands (finding #4: the old parser always said 0).
      const shown = r.samples.map((s) => (s.length > 34 ? `${s.slice(0, 33)}…` : s));
      const detail = shown.length
        ? `: ${shown.map((s) => `"${s}"`).join(', ')}${r.count > shown.length ? ' …' : ''}`
        : '';
      push(`${r.count} ${r.count === 1 ? 'memory' : 'memories'} extracted${detail}`, 'success');
      await refreshMemories();
    } catch (e) {
      push(errText(e, 'Extraction failed'), 'error');
    } finally {
      extractingId = '';
    }
  }

  // ----- account: sessions -----
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

  // ----- preferences: model prefs (server-synced, $lib/chat/model-prefs) -----
  /** Friendly label for a "providerID:modelID" key; falls back to the key. */
  function prefModelLabel(key: string): string {
    const i = key.indexOf(':');
    if (i < 0) return key;
    const m = findModel(key.slice(0, i), key.slice(i + 1));
    return m ? m.display || m.model_id : key;
  }

  // Lazy boot for the Models card: prefs fetch is idempotent/shared, model
  // cache only needed here for friendly labels.
  $effect(() => {
    if (tab !== 'preferences') return;
    void loadModelPrefs();
    void loadModels();
  });

  // Lazy section boot: 2FA status read + memory list fire the first time the
  // account tab renders, never at app boot (docs/WAVE2-PLAN.md §FE-SEC-FE).
  $effect(() => {
    if (tab !== 'account') return;
    if (!twofaProbed) {
      twofaProbed = true;
      void getTwoFactorStatus().then((s) => (twofa = s));
    }
    if (!memLoaded) {
      memLoaded = true;
      void refreshMemories();
    }
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
          <p class="text-sm font-medium">Accent color</p>
          <p class="text-xs text-[var(--muted)]">
            Tints bubbles, highlights and the send button. Accent color follows your account across
            devices.
          </p>
        </div>
        <div class="flex flex-wrap items-start justify-end gap-1" role="radiogroup" aria-label="Accent color">
          {#each ACCENTS as a (a)}
            <div class="flex w-11 flex-col items-center gap-1 pt-0.5">
              <button
                type="button"
                class="swatch"
                style="background:{SWATCH[a]};{accent.name === a ? ' box-shadow:0 0 0 2px var(--panel), 0 0 0 4px var(--acc)' : ''}"
                role="radio"
                aria-checked={accent.name === a}
                aria-label={accentLabel(a)}
                title={accentLabel(a)}
                onclick={() => setAccent(a)}
              >
                {#if accent.name === a}
                  <Check class="size-3" style="color:#0d0d0d" />
                {/if}
              </button>
              <span class="text-[10px] leading-none text-[var(--muted)]">{accentLabel(a)}</span>
            </div>
          {/each}
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

    <section class="rounded-2xl p-4 sm:p-5" style="background:var(--panel); color:var(--text)">
      <h2 class="text-[15px] font-medium">Models</h2>
      <p class="mt-0.5 mb-1 text-xs text-[var(--muted)]">
        Default model and models hidden from the pickers. Saved to your account.
      </p>

      <div class="flex items-center justify-between gap-4 border-t py-3.5" style="border-color:var(--border)">
        <div class="min-w-0">
          <p class="text-sm font-medium">Default model</p>
          <p class="truncate text-xs text-[var(--muted)]">
            {#if modelPrefs.defaultModel}
              {prefModelLabel(modelPrefs.defaultModel)}
            {:else}
              No default — new chats start on the first chat model.
            {/if}
          </p>
        </div>
        {#if modelPrefs.defaultModel}
          <button
            type="button"
            class={btn}
            style={btnGhostStyle}
            onclick={() => {
              void setDefault('');
              push('Default model cleared', 'success');
            }}
          >
            Clear default
          </button>
        {/if}
      </div>

      <div class="border-t py-3.5" style="border-color:var(--border)">
        <div class="flex items-center justify-between gap-3">
          <p class="text-sm font-medium">Hidden models</p>
          {#if modelPrefs.hiddenModels.length > 0}
            <button
              type="button"
              class={btn}
              style={btnGhostStyle}
              onclick={() => {
                void unhideAll();
                push('All models unhidden', 'success');
              }}
            >
              Unhide all
            </button>
          {/if}
        </div>
        {#if !modelPrefs.loaded}
          <p class="mt-1 text-xs text-[var(--muted)]">Loading…</p>
        {:else if !modelPrefs.hiddenModels.length}
          <p class="mt-1 text-xs text-[var(--muted)]">
            Nothing hidden — use the eye button in any model picker to tuck models away.
          </p>
        {:else}
          <ul class="mt-1">
            {#each modelPrefs.hiddenModels as key (key)}
              <li class="flex items-center gap-2.5 py-1.5">
                <span class="min-w-0 flex-1 truncate text-sm text-[var(--muted)]">{prefModelLabel(key)}</span>
                <span class="hidden max-w-40 shrink-0 truncate font-mono text-[11px] text-[var(--faint)] sm:block">{key}</span>
                <button
                  type="button"
                  class="iconbtn"
                  aria-label="Unhide {prefModelLabel(key)}"
                  title="Unhide — back in the pickers"
                  onclick={() => void unhide(key)}
                >
                  <Eye class="size-3.5" />
                </button>
              </li>
            {/each}
          </ul>
        {/if}
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

    {#if twofa !== 'probing' && twofa !== 'unavailable'}
      <section class="rounded-2xl p-4 sm:p-5" style="background:var(--panel); color:var(--text)">
        <h2 class="text-[15px] font-medium">Two-factor authentication</h2>
        <p class="mt-0.5 mb-1 text-xs text-[var(--muted)]">
          Adds a 6-digit code from your authenticator app at sign-in. Local password accounts only.
        </p>

        <div class="flex items-center justify-between gap-4 border-t py-3.5" style="border-color:var(--border)">
          <div class="flex min-w-0 items-center gap-2">
            {#if twofa === 'enabled'}
              <Badge kind="success">Enabled</Badge>
              <span class="truncate text-xs text-[var(--muted)]">Authenticator codes are required when you sign in.</span>
            {:else}
              <Badge kind="muted">Off</Badge>
              <span class="truncate text-xs text-[var(--muted)]">Your password is the only sign-in factor.</span>
            {/if}
          </div>
          {#if tfStage === 'idle' && twofa === 'enabled'}
            <button
              type="button"
              class={btn}
              style={btnGhostStyle}
              onclick={() => {
                tfError = '';
                tfStage = 'disable';
              }}
            >
              Disable
            </button>
          {:else if tfStage === 'idle'}
            <button
              type="button"
              class={btn}
              style={btnPrimaryStyle}
              onclick={() => {
                tfError = '';
                tfStage = 'setup';
              }}
            >
              Set up 2FA
            </button>
          {/if}
        </div>

        {#if tfError}
          <p class="mb-3 rounded-xl px-3 py-2 text-xs" style={errorBannerStyle}>{tfError}</p>
        {/if}

        {#if tfStage === 'setup' || tfStage === 'disable'}
          <form
            class="mt-1 max-w-sm space-y-3"
            onsubmit={(e) => {
              e.preventDefault();
              void submitTwoFaPassword();
            }}
          >
            <Field
              label="Confirm your password"
              hint={tfStage === 'setup'
                ? 'Re-enter your password to generate a setup key.'
                : 'Turning two-factor off requires your password.'}
            >
              <input
                type="password"
                autocomplete="current-password"
                class={input}
                style={inputStyle}
                bind:value={tfPw}
              />
            </Field>
            <div class="flex items-center gap-2">
              <button type="submit" class={btn} style={tfStage === 'disable' ? btnDangerStyle : btnPrimaryStyle} disabled={!tfPw || tfBusy}>
                {tfBusy ? 'Working…' : tfStage === 'disable' ? 'Disable 2FA' : 'Start setup'}
              </button>
              <button type="button" class={btn} style={btnGhostStyle} onclick={resetTwoFa}>Cancel</button>
            </div>
          </form>
        {:else if tfStage === 'verify'}
          <div class="mt-1 max-w-lg space-y-3">
            <div class="space-y-3 rounded-xl p-3.5" style="background:var(--panel-2)">
              <div class="space-y-1">
                <p class="text-[11px] font-medium text-[var(--muted)]">Secret key</p>
                <div class="flex items-start gap-2">
                  <code class="min-w-0 flex-1 break-all font-mono text-xs leading-relaxed">{tfSecret}</code>
                  <button type="button" class="iconbtn" aria-label="Copy secret key" onclick={() => void copySecret(tfSecret)}>
                    <Copy class="size-3.5" />
                  </button>
                </div>
              </div>
              <div class="space-y-1">
                <p class="text-[11px] font-medium text-[var(--muted)]">Setup URI</p>
                <div class="flex items-start gap-2">
                  <code class="min-w-0 flex-1 break-all font-mono text-xs leading-relaxed text-[var(--muted)]">{tfUri}</code>
                  <button type="button" class="iconbtn" aria-label="Copy setup URI" onclick={() => void copySecret(tfUri)}>
                    <Copy class="size-3.5" />
                  </button>
                </div>
              </div>
            </div>
            <p class="text-xs leading-relaxed text-[var(--muted)]">
              Scan or type this key into your authenticator app, then enter the 6-digit code it
              shows to confirm the setup.
            </p>
            <form
              class="max-w-xs space-y-3"
              onsubmit={(e) => {
                e.preventDefault();
                void submitTwoFaCode();
              }}
            >
              <Field label="Verification code">
                <input
                  type="text"
                  inputmode="numeric"
                  pattern="[0-9]*"
                  maxlength="6"
                  autocomplete="one-time-code"
                  class={input}
                  style={inputStyle}
                  bind:value={tfCode}
                />
              </Field>
              <div class="flex items-center gap-2">
                <button
                  type="submit"
                  class={btn}
                  style={btnPrimaryStyle}
                  disabled={tfCode.replace(/\D/g, '').length !== 6 || tfBusy}
                >
                  {tfBusy ? 'Verifying…' : 'Enable 2FA'}
                </button>
                <button type="button" class={btn} style={btnGhostStyle} onclick={resetTwoFa}>Cancel</button>
              </div>
            </form>
          </div>
        {/if}
      </section>
    {/if}

    <section class="rounded-2xl p-4 sm:p-5" style="background:var(--panel); color:var(--text)">
      <h2 class="text-[15px] font-medium">API tokens</h2>
      <p class="mt-0.5 mb-1 text-xs text-[var(--muted)]">
        Issue <code class="font-mono">ck_…</code> keys for scripts and tools against the OpenAI-compatible
        <code class="font-mono">/v1/chat/completions</code> endpoint.
      </p>
      <TokensPanel />
    </section>

    <section class="rounded-2xl p-4 sm:p-5" style="background:var(--panel); color:var(--text)">
      <div class="flex items-center justify-between gap-3">
        <h2 class="text-[15px] font-medium">Memory</h2>
        {#if memAvailable}
          <button
            type="button"
            class={btn}
            style={btnGhostStyle}
            disabled={!!extractingId}
            onclick={() => void openChats()}
          >
            {#if extractingId}
              <Spinner /> Extracting…
            {:else}
              Extract from chat
            {/if}
          </button>
        {/if}
      </div>
      <p class="mt-0.5 mb-1 text-xs text-[var(--muted)]">
        ChatGo remembers durable facts you add here and gives them to the model as context.
      </p>

      {#if !memAvailable}
        <p class="border-t py-3 text-xs text-[var(--muted)]" style="border-color:var(--border)">
          Memories are not available on this server yet.
        </p>
      {:else}
        {#if memError}
          <p class="my-2 rounded-xl px-3 py-2 text-xs" style={errorBannerStyle}>{memError}</p>
        {/if}

        {#if memLoading && !memories.length}
          <p class="flex items-center gap-2 border-t py-3 text-xs text-[var(--muted)]" style="border-color:var(--border)">
            <Spinner /> Loading memories…
          </p>
        {:else if !memories.length}
          <p class="border-t py-3 text-xs text-[var(--muted)]" style="border-color:var(--border)">
            No memories yet — add one below or extract some from a chat.
          </p>
        {:else}
          <ul class="border-t" style="border-color:var(--border)">
            {#each memories as m (m.id)}
              <li class="border-b py-2.5" style="border-color:var(--border)">
                {#if editId === m.id}
                  <div class="space-y-2">
                    <textarea rows="3" maxlength="500" class={input} style={inputStyle} bind:value={editDraft}></textarea>
                    <div class="flex items-center justify-between gap-2">
                      <span class="text-[11px] text-[var(--muted)]">{editDraft.length} / 500</span>
                      <div class="flex items-center gap-2">
                        <button type="button" class={btn} style={btnGhostStyle} onclick={cancelEditMemory}>Cancel</button>
                        <button
                          type="button"
                          class={btn}
                          style={btnPrimaryStyle}
                          disabled={!editDraft.trim() || memBusy}
                          onclick={() => void saveEditMemory(m)}
                        >
                          {memBusy ? 'Saving…' : 'Save'}
                        </button>
                      </div>
                    </div>
                  </div>
                {:else}
                  <div class="flex items-center gap-2.5">
                    <p class="min-w-0 flex-1 text-sm leading-snug {m.enabled ? '' : 'text-[var(--muted)]'}">{m.content}</p>
                    <Badge kind="muted">{m.source}</Badge>
                    <Toggle label="Memory enabled" bind:checked={m.enabled} onchange={(v) => void setMemoryEnabled(m, v)} />
                    <button type="button" class="iconbtn" aria-label="Edit memory" onclick={() => startEditMemory(m)}>
                      <Pencil class="size-3.5" />
                    </button>
                    <button type="button" class="iconbtn iconbtn-danger" aria-label="Delete memory" onclick={() => void removeMemory(m)}>
                      <Trash2 class="size-3.5" />
                    </button>
                  </div>
                {/if}
              </li>
            {/each}
          </ul>
          <p class="mt-2 text-[11px] text-[var(--muted)]">{memories.length} / {MEM_LIMIT}</p>
        {/if}

        <form
          class="mt-3 flex items-center gap-2"
          onsubmit={(e) => {
            e.preventDefault();
            void addMemoryRow();
          }}
        >
          <input
            type="text"
            class={input}
            style={inputStyle}
            maxlength="500"
            placeholder="e.g. I prefer concise answers with TypeScript examples"
            bind:value={memNew}
          />
          <button type="submit" class={btn} style={btnPrimaryStyle} disabled={!memNew.trim() || memBusy}>
            {memBusy ? 'Adding…' : 'Add'}
          </button>
        </form>
      {/if}
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

<Modal bind:open={chatsOpen} title="Extract memories from a chat">
  {#if chatsLoading}
    <p class="flex items-center gap-2 py-4 text-sm text-[var(--muted)]"><Spinner /> Loading chats…</p>
  {:else if !chats.length}
    <p class="py-4 text-sm text-[var(--muted)]">No chats yet.</p>
  {:else}
    <p class="pb-2 text-xs leading-relaxed text-[var(--muted)]">
      Picks durable facts out of the conversation. This runs a model call and can take ~15 seconds.
    </p>
    <ul class="-my-1">
      {#each chats as c (c.id)}
        <li>
          <button
            type="button"
            class="flex w-full items-center gap-2 rounded-xl px-2.5 py-2 text-left text-sm transition-colors hover:bg-[var(--fill-hover)] disabled:cursor-not-allowed disabled:opacity-50"
            disabled={!!extractingId}
            onclick={() => void extractFrom(c.id)}
          >
            <span class="min-w-0 flex-1 truncate">{c.title || 'Untitled'}</span>
            {#if extractingId === c.id}
              <Spinner />
            {/if}
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</Modal>

<style>
  /* accent picker swatches (FE-COLOR): 20px circles; the current choice gets
     a --acc ring (double box-shadow) + check — paint only, no sizing changes */
  .swatch {
    display: inline-flex;
    width: 20px;
    height: 20px;
    align-items: center;
    justify-content: center;
    flex: none;
    padding: 0;
    border: 0;
    border-radius: 999px;
    cursor: pointer;
    transition: box-shadow 0.12s ease;
  }
  .swatch:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--acc) 60%, transparent);
    outline-offset: 2px;
  }
  /* quiet ghost icon button (FE-SEC): copy/edit/delete affordances */
  .iconbtn {
    display: inline-flex;
    flex: none;
    padding: 5px;
    border: 0;
    border-radius: 8px;
    color: var(--muted);
    cursor: pointer;
    transition: background 0.12s ease, color 0.12s ease;
  }
  .iconbtn:hover {
    background: var(--fill-hover);
    color: var(--text);
  }
  .iconbtn:focus-visible {
    outline: 2px solid var(--accent-soft);
    outline-offset: 2px;
  }
  .iconbtn-danger:hover {
    color: var(--danger);
  }
</style>
