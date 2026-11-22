<script lang="ts">
  import { CheckCheck, Copy, Inbox, ShieldOff, UserCheck, Users } from 'lucide-svelte';
  import { session } from '$lib/session.svelte.js';
  import { push } from '$lib/toast.svelte.js';
  import * as admin from '$lib/admin/api-admin';
  import type { User } from '$lib/admin/api-admin';
  import { errMsg, relTime } from '$lib/admin/format';
  import Badge from '$lib/admin/ui/Badge.svelte';
  import Confirm from '$lib/admin/ui/Confirm.svelte';
  import EmptyState from '$lib/admin/ui/EmptyState.svelte';
  import Modal from '$lib/admin/ui/Modal.svelte';
  import Spinner from '$lib/admin/ui/Spinner.svelte';
  import { btn, btnGhostStyle, btnPrimaryStyle, errorBannerStyle, input, inputStyle, mono, panel, panelStyle, row, rowBorder, td, th } from '$lib/admin/ui/styles';

  let users = $state<User[]>([]);
  let loading = $state(true);
  let loadError = $state('');
  let selected = $state.raw(new Set<string>());
  let approving = $state(false);

  // 2FA lockout recovery (POST /api/users/{id}/reset-2fa)
  interface Reset2FAResult {
    ok: boolean;
    was_enabled: boolean;
    /** Present ONLY for local accounts; shown once, never recoverable. */
    new_password?: string;
    sessions_revoked: number;
  }
  let resetTarget = $state<User | null>(null);
  let confirmReset = $state(false);
  /** Set after a successful reset — the password exists ONLY in this state. */
  let resetResult = $state<{ email: string; res: Reset2FAResult } | null>(null);
  let resetReveal = $state(false);

  const pending = $derived(users.filter((u) => u.role === 'pending'));
  const active = $derived(users.filter((u) => u.role !== 'pending'));
  const me = $derived(session.user);

  async function load() {
    loading = users.length === 0;
    loadError = '';
    try {
      users = await admin.listUsers();
      selected = new Set();
    } catch (e) {
      loadError = errMsg(e, 'Could not load users');
    } finally {
      loading = false;
    }
  }
  void load();

  function toggleSelect(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    selected = next;
  }
  function selectAllPending() {
    if (selected.size === pending.length) selected = new Set();
    else selected = new Set(pending.map((u) => u.id));
  }

  async function approve(ids: string[], label: string) {
    if (approving || ids.length === 0) return;
    approving = true;
    let ok = 0;
    const failed: string[] = [];
    for (const id of ids) {
      try {
        await admin.approveUser(id);
        ok++;
      } catch {
        const u = users.find((x) => x.id === id);
        failed.push(u?.email ?? id);
      }
    }
    approving = false;
    try {
      users = await admin.listUsers();
      selected = new Set();
    } catch (e) {
      push(errMsg(e, 'Approved, but the list may be stale'), 'info');
    }
    if (failed.length === 0) push(`Approved ${ok} user${ok === 1 ? '' : 's'}${label ? ` ${label}` : ''}`, 'success');
    else push(`Approved ${ok}, failed ${failed.length}: ${failed.join(', ')}`, 'error');
  }

  async function changeRole(u: User, e: Event) {
    const role = (e.target as HTMLSelectElement).value;
    const prev = u.role;
    if (role === prev) return;
    u.role = role as User['role']; // optimistic
    try {
      await admin.setUserRole(u.id, role);
      push(`${u.email} is now ${role}`, 'success');
      // role edits move rows between queue/table views
      users = await admin.listUsers();
    } catch (err) {
      u.role = prev;
      push(errMsg(err, 'Could not change role'), 'error');
    }
  }

  // One-off fetch for the out-of-contract /reset-2fa route: api.ts is the
  // frozen scaffold fetch wrapper and cannot grow a method here, so this
  // follows the accepted audit-api.ts deviation pattern (typed local fetch).
  async function resetUser2FA(id: string): Promise<Reset2FAResult> {
    const res = await fetch(`/api/users/${id}/reset-2fa`, { method: 'POST', credentials: 'include' });
    if (!res.ok) {
      let msg = res.statusText || `HTTP ${res.status}`;
      try {
        const j = (await res.json()) as { error?: string };
        if (typeof j?.error === 'string' && j.error) msg = j.error;
      } catch {
        /* body wasn't JSON */
      }
      throw new admin.ApiError(res.status, msg);
    }
    return (await res.json()) as Reset2FAResult;
  }

  async function doReset2FA() {
    const u = resetTarget;
    if (!u) return;
    try {
      const res = await resetUser2FA(u.id);
      resetResult = { email: u.email, res };
      resetReveal = true;
      resetTarget = null;
    } catch (err) {
      push(errMsg(err, 'Could not reset 2FA'), 'error');
      throw err; // keep the confirm dialog open (Confirm.svelte contract)
    }
  }

  async function copyNewPassword() {
    const pw = resetResult?.res.new_password;
    if (!pw) return;
    try {
      await navigator.clipboard.writeText(pw);
      push('Password copied to clipboard', 'success');
    } catch {
      push('Clipboard blocked — select the text and copy manually', 'error');
    }
  }
</script>

<div class="flex flex-col gap-6">
  {#if loading}
    <div class="flex items-center gap-2 rounded-2xl px-4 py-8 text-sm text-[var(--muted)]" style={panelStyle}>
      <Spinner size={16} /> Loading users…
    </div>
  {:else if loadError}
    <div class="flex items-center justify-between gap-3 rounded-[10px] px-4 py-3 text-sm" style={errorBannerStyle}>
      {loadError}
      <button class={btn} style={btnGhostStyle} onclick={load}>Retry</button>
    </div>
  {:else}
    <!-- pending approval queue -->
    <section class="flex flex-col gap-3">
      <div class="flex items-center justify-between gap-3">
        <h2 class="flex items-center gap-2 text-[15px] font-medium">
          Pending approval
          {#if pending.length > 0}<Badge kind="accent">{pending.length}</Badge>{/if}
        </h2>
        {#if pending.length > 0}
          <div class="flex items-center gap-2">
            <button class="text-xs text-[var(--muted)] underline-offset-2 hover:text-[var(--text)] hover:underline" onclick={selectAllPending}>
              {selected.size === pending.length ? 'Clear selection' : `Select all (${pending.length})`}
            </button>
            <button
              class={btn}
              style={btnPrimaryStyle}
              disabled={approving || selected.size === 0}
              onclick={() => approve([...selected], '')}
            >
              {#if approving}<Spinner />{:else}<CheckCheck size={14} />{/if}
              Approve selected{selected.size > 0 ? ` (${selected.size})` : ''}
            </button>
          </div>
        {/if}
      </div>

      {#if pending.length === 0}
        <EmptyState title="Approval queue is empty" description="New signups land here when the registration policy is “require approval”." >
          {#snippet icon()}<Inbox size={22} />{/snippet}
        </EmptyState>
      {:else}
        <div class="grid gap-2 sm:grid-cols-2">
          {#each pending as u (u.id)}
            <div class="flex items-center gap-3 rounded-2xl px-3 py-2.5 transition-colors hover:bg-[var(--fill-hover)]" style="background:var(--panel)">
              <input
                type="checkbox"
                class="h-4 w-4 shrink-0 accent-[var(--accent)]"
                checked={selected.has(u.id)}
                onchange={() => toggleSelect(u.id)}
                aria-label="Select {u.email}"
              />
              <div class="min-w-0 flex-1">
                <p class="truncate text-sm font-medium">{u.name || u.email}</p>
                <p class="truncate text-xs text-[var(--muted)]">
                  {u.email} · requested {relTime(u.created_at)}
                </p>
              </div>
              <button
                class={btn}
                style={btnGhostStyle}
                disabled={approving}
                onclick={() => approve([u.id], '')}
                title="Approve {u.email}"
              >
                <UserCheck size={14} /> Approve
              </button>
            </div>
          {/each}
        </div>
      {/if}
    </section>

    <!-- all users -->
    <section class="flex flex-col gap-3">
      <h2 class="flex items-center gap-2 text-[15px] font-medium">
        All users
        <Badge>{active.length}</Badge>
      </h2>

      {#if active.length === 0}
        <EmptyState title="No active users yet" description="Approved users will show up here.">
          {#snippet icon()}<Users size={22} />{/snippet}
        </EmptyState>
      {:else}
        <div class="{panel} overflow-x-auto" style={panelStyle}>
          <table class="w-full text-sm">
            <thead>
              <tr class="uppercase tracking-wider" style={rowBorder}>
                <th class={th}>Email</th>
                <th class={th}>Name</th>
                <th class={th}>Role</th>
                <th class={th}>Created</th>
                <th class={th}>Last active</th>
                <th class="{th} text-right">2FA</th>
              </tr>
            </thead>
            <tbody>
              {#each active as u (u.id)}
                {@const self = me !== null && u.id === me.id}
                <tr class={row} style={rowBorder}>
                  <td class="{td} flex items-center gap-2">
                    <span class="font-mono text-xs">{u.email}</span>
                    {#if self}<Badge kind="accent">you</Badge>{/if}
                  </td>
                  <td class={td}>{u.name || '—'}</td>
                  <td class={td}>
                    <select
                      class={`${input} w-28 cursor-pointer py-1 pr-7 text-xs`}
                      style={inputStyle}
                      value={u.role}
                      disabled={self || approving}
                      onchange={(e) => changeRole(u, e)}
                      aria-label="Role for {u.email}"
                    >
                      <option value="admin">admin</option>
                      <option value="user">user</option>
                      <option value="pending">pending</option>
                    </select>
                  </td>
                  <td class="{td} text-xs text-[var(--muted)]">{relTime(u.created_at)}</td>
                  <td class="{td} text-xs text-[var(--muted)]">{relTime(u.last_active_at)}</td>
                  <td class="{td} text-right">
                    <button
                      class="inline-flex items-center gap-1 rounded-[10px] px-2 py-1 text-xs transition-colors hover:bg-[var(--fill-hover)]"
                      style="color:var(--danger)"
                      title="Clear two-factor authentication for {u.email} and sign all their sessions out"
                      onclick={() => {
                        resetTarget = u;
                        confirmReset = true;
                      }}
                    >
                      <ShieldOff size={13} /> Reset 2FA
                    </button>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        <p class="text-xs leading-relaxed text-[var(--muted)]">
          Your own row is locked: ChatGo disables self role changes so you can't lock yourself out of the admin
          console. Demoting the last remaining admin is rejected server-side — promote another admin first. Setting a
          user back to “pending” deactivates them until the next approval. “Reset 2FA” is the lockout recovery path:
          it clears their authenticator setup, signs out every one of their sessions, and for password accounts sets
          a one-time password shown exactly once here — share it over a secure channel and have them change it
          immediately.
        </p>
      {/if}
    </section>
  {/if}
</div>

<Confirm
  bind:open={confirmReset}
  title="Reset two-factor authentication"
  message={resetTarget
    ? `Recover ${resetTarget.email} from a lost authenticator? Their 2FA setup is cleared and every active session is signed out immediately. If the account uses a password, a new one-time password is generated and shown once — it cannot be retrieved afterwards.`
    : ''}
  confirmLabel="Reset 2FA"
  danger
  onconfirm={doReset2FA}
/>

<Modal bind:open={resetReveal} title={resetResult ? `2FA reset — ${resetResult.email}` : '2FA reset'}>
  {#if resetResult}
    <div class="flex flex-col gap-3">
      <p class="text-sm text-[var(--muted)]">
        {resetResult.res.was_enabled ? 'Two-factor authentication was cleared.' : 'That account had no 2FA enabled.'}
        {resetResult.res.sessions_revoked === 1
          ? '1 active session was signed out.'
          : `${resetResult.res.sessions_revoked} active sessions were signed out.`}
      </p>
      {#if resetResult.res.new_password}
        <p class="text-xs font-medium" style="color:var(--danger)">
          ⚠ One-time password — shown once and never recoverable. Share it via a secure channel; the user must change
          it at their next sign-in.
        </p>
        <div class="flex items-center gap-2">
          <code class="min-w-0 flex-1 select-all break-all rounded-xl px-3 py-2 {mono}" style="background:var(--bg-deep); color:var(--text)"
            >{resetResult.res.new_password}</code
          >
          <button class={`${btn} uppercase tracking-wide`} style={btnPrimaryStyle} onclick={copyNewPassword}>
            <Copy size={13} /> Copy
          </button>
        </div>
      {:else}
        <p class="text-sm text-[var(--muted)]">
          This account signs in through single sign-on, so no password was set — they can sign in again through the
          identity provider without a verification code.
        </p>
      {/if}
    </div>
    {#snippet footer()}
      <button
        class={btn}
        style={btnGhostStyle}
        onclick={() => {
          resetReveal = false;
          resetResult = null;
        }}>Done — I saved it</button
      >
    {/snippet}
  {/if}
</Modal>
