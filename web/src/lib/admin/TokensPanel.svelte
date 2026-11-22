<script lang="ts">
  // Personal API tokens (/api/tokens) — usable by ANY signed-in user.
  //
  // //INTEGRATION: embeddable anywhere a logged-in user should manage tokens:
  // //   import TokensPanel from '$lib/admin/TokensPanel.svelte';
  // //   <TokensPanel />
  // // Suggested home: the regular-user Settings page (FE-shell) and the
  // // Admin → System tab (already wired here). No props; self-loads.
  import { Copy, KeyRound, Plus, Trash2 } from 'lucide-svelte';
  import { push } from '$lib/toast.svelte.js';
  import * as admin from '$lib/admin/api-admin';
  import type { ApiToken } from '$lib/admin/api-admin';
  import { errMsg, relTime } from '$lib/admin/format';
  import Badge from '$lib/admin/ui/Badge.svelte';
  import Confirm from '$lib/admin/ui/Confirm.svelte';
  import EmptyState from '$lib/admin/ui/EmptyState.svelte';
  import Field from '$lib/admin/ui/Field.svelte';
  import Modal from '$lib/admin/ui/Modal.svelte';
  import Spinner from '$lib/admin/ui/Spinner.svelte';
  import { btn, btnGhostStyle, btnPrimaryStyle, errorBannerStyle, input, inputStyle, mono, panel, panelStyle, row, rowBorder, td, th } from '$lib/admin/ui/styles';

  let tokens = $state<ApiToken[]>([]);
  let loading = $state(true);
  let createOpen = $state(false);
  let newName = $state('');
  let creating = $state(false);
  let createError = $state('');
  /** Set after a successful create — the full key exists ONLY in this state. */
  let revealed = $state<ApiToken | null>(null);
  let deleteTarget = $state<ApiToken | null>(null);
  let confirmDelete = $state(false);

  async function load() {
    loading = tokens.length === 0;
    try {
      tokens = await admin.listTokens();
    } catch (e) {
      push(errMsg(e, 'Could not load API tokens'), 'error');
    } finally {
      loading = false;
    }
  }
  void load();

  $effect(() => {
    if (!createOpen) {
      newName = '';
      creating = false;
      createError = '';
    }
  });

  async function create() {
    const name = newName.trim();
    if (!name || creating) return;
    creating = true;
    createError = '';
    try {
      const t = await admin.createToken(name);
      createOpen = false;
      revealed = t;
      void load();
    } catch (e) {
      createError = errMsg(e, 'Could not create token');
      creating = false;
    }
  }

  async function copyKey() {
    const key = revealed?.key;
    if (!key) return;
    try {
      await navigator.clipboard.writeText(key);
      push('Token copied to clipboard', 'success');
    } catch {
      push('Clipboard blocked — select the text and copy manually', 'error');
    }
  }

  async function doDelete() {
    const t = deleteTarget;
    if (!t) return;
    try {
      await admin.deleteToken(t.id);
    } catch (e) {
      push(errMsg(e, 'Could not revoke token'), 'error');
      throw e;
    }
    tokens = tokens.filter((x) => x.id !== t.id);
    push(`Revoked “${t.name}”`, 'success');
    deleteTarget = null;
  }
</script>

<div class="flex flex-col gap-3">
  <div class="flex items-center justify-between gap-3">
    <p class="text-sm text-[var(--muted)]">
      <code class={mono}>ck_…</code> tokens authenticate <code class={mono}>POST /v1/chat/completions</code> as your
      account. {tokens.length} active.
    </p>
    <button class={btn} style={btnPrimaryStyle} onclick={() => (createOpen = true)}>
      <Plus size={14} /> New token
    </button>
  </div>

  {#if revealed}
    <!-- one-time reveal card: quiet panel, mono line + COPY -->
    <div class="flex flex-col gap-3 rounded-2xl p-4" style="background:var(--panel-2)">
      <p class="flex items-center gap-2 text-sm font-medium">
        <KeyRound size={15} /> Token “{revealed.name}” created
      </p>
      <p class="text-xs font-medium" style="color:var(--danger)">
        ⚠ Shown once — ChatGo stores only a hash, so this key can never be recovered. Copy it now.
      </p>
      <div class="flex items-center gap-2">
        <code
          class="min-w-0 flex-1 select-all break-all rounded-xl px-3 py-2 font-mono text-xs"
          style="background:var(--bg-deep); color:var(--text)"
          id="token-reveal-value"
        >{revealed.key}</code>
        <button class={`${btn} uppercase tracking-wide`} style={btnPrimaryStyle} onclick={copyKey}><Copy size={13} /> Copy</button>
      </div>
      <div class="flex justify-end">
        <button class={btn} style={btnGhostStyle} onclick={() => (revealed = null)}>Done — I saved it</button>
      </div>
    </div>
  {/if}

  {#if loading}
    <div class="flex items-center gap-2 rounded-2xl px-4 py-6 text-sm text-[var(--muted)]" style={panelStyle}>
      <Spinner size={16} /> Loading tokens…
    </div>
  {:else if tokens.length === 0}
    <EmptyState title="No API tokens" description="Create a token to call ChatGo from scripts, SDKs or anything that speaks the OpenAI API.">
      {#snippet icon()}<KeyRound size={22} />{/snippet}
    </EmptyState>
  {:else}
    <div class="{panel} overflow-x-auto" style={panelStyle}>
      <table class="w-full text-sm">
        <thead>
          <tr class="uppercase tracking-wider" style={rowBorder}>
            <th class={th}>Name</th>
            <th class={th}>Token</th>
            <th class={th}>Created</th>
            <th class={th}>Last used</th>
            <th class="{th} text-right">Revoke</th>
          </tr>
        </thead>
        <tbody>
          {#each tokens as t (t.id)}
            <tr class={row} style={rowBorder}>
              <td class="{td} font-medium">{t.name}</td>
              <td class={td}><Badge>ck_••••••••••••</Badge></td>
              <td class="{td} text-xs text-[var(--muted)]">{relTime(t.created_at)}</td>
              <td class="{td} text-xs text-[var(--muted)]">{relTime(t.last_used_at)}</td>
              <td class="{td} text-right">
                <button
                  class="rounded-md p-1.5 text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--danger)]"
                  title="Revoke {t.name}"
                  onclick={() => {
                    deleteTarget = t;
                    confirmDelete = true;
                  }}
                >
                  <Trash2 size={14} />
                </button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<Modal bind:open={createOpen} title="New API token">
  <form id="token-form" onsubmit={(e) => { e.preventDefault(); void create(); }} class="flex flex-col gap-4">
    <Field label="Name" hint="A label only — e.g. “laptop scripts”. You can have multiple tokens.">
      <input class={input} style={inputStyle} required maxlength={64} placeholder="My script" bind:value={newName} />
    </Field>
    {#if createError}
      <p class="rounded-[10px] px-3 py-2 text-[13px]" style={errorBannerStyle}>
        {createError}
      </p>
    {/if}
  </form>
  {#snippet footer()}
    <button class={btn} style={btnGhostStyle} onclick={() => (createOpen = false)}>Cancel</button>
    <button type="submit" form="token-form" class={btn} style={btnPrimaryStyle} disabled={creating || !newName.trim()}>
      {#if creating}<Spinner /> Creating…{:else}<Plus size={14} /> Create token{/if}
    </button>
  {/snippet}
</Modal>

<Confirm
  bind:open={confirmDelete}
  title="Revoke token"
  message={deleteTarget ? `Revoke “${deleteTarget.name}”? Anything using it starts failing with 401 immediately, and it cannot be restored.` : ''}
  confirmLabel="Revoke"
  danger
  onconfirm={doDelete}
/>
