<script lang="ts">
  import { ChevronDown, ChevronRight, Inbox, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-svelte';
  import { push } from '$lib/toast.svelte.js';
  import * as admin from '$lib/admin/api-admin';
  import type { ModelEntry, Provider } from '$lib/admin/api-admin';
  import { ctxLabel, errMsg } from '$lib/admin/format';
  import Badge from '$lib/admin/ui/Badge.svelte';
  import Confirm from '$lib/admin/ui/Confirm.svelte';
  import EmptyState from '$lib/admin/ui/EmptyState.svelte';
  import ProviderModal from './ProviderModal.svelte';
  import Spinner from '$lib/admin/ui/Spinner.svelte';
  import Toggle from '$lib/admin/ui/Toggle.svelte';
  import { btn, btnGhostStyle, btnPrimaryStyle, panel, panel2Style, panelStyle, rowBorder, td, th } from '$lib/admin/ui/styles';

  type ModelRow = ModelEntry & { enabled?: boolean };

  let providers = $state<Provider[]>([]);
  let models = $state<ModelRow[]>([]);
  let loading = $state(true);
  let loadError = $state('');
  let syncing = $state<Record<string, boolean>>({});
  let syncingAll = $state(false);
  let expanded = $state(new Set<string>());

  let modalOpen = $state(false);
  let editing = $state<Provider | null>(null);
  let deleteTarget = $state<Provider | null>(null);
  let confirmDelete = $state(false);

  const deleteMessage = $derived.by(() => {
    const t = deleteTarget;
    if (!t) return '';
    const n = models.filter((m) => m.provider_id === t.id).length;
    return `Delete ${t.name} and its ${n} cached model${n === 1 ? '' : 's'}? Chats pinned to it will stop working. This cannot be undone.`;
  });

  async function load() {
    loading = providers.length === 0;
    loadError = '';
    try {
      const [ps, ms] = await Promise.all([admin.listProviders(), admin.listModels()]);
      providers = ps;
      models = ms as ModelRow[];
    } catch (e) {
      loadError = errMsg(e, 'Could not load providers');
    } finally {
      loading = false;
    }
  }
  void load();

  function openCreate() {
    editing = null;
    modalOpen = true;
  }
  function openEdit(p: Provider) {
    editing = p;
    modalOpen = true;
  }

  function toggleExpand(id: string) {
    if (expanded.has(id)) expanded.delete(id);
    else expanded.add(id);
  }

  async function toggleEnabled(p: Provider) {
    const prev = p.enabled;
    p.enabled = !prev; // optimistic; rolled back on failure
    try {
      await admin.updateProvider(p.id, { enabled: p.enabled });
    } catch (e) {
      p.enabled = prev;
      push(errMsg(e, 'Could not update provider'), 'error');
    }
  }

  async function syncOne(p: Provider) {
    syncing[p.id] = true;
    try {
      const r = await admin.syncProvider(p.id);
      push(`Synced ${r.count} model${r.count === 1 ? '' : 's'} from ${p.name}`, 'success');
      models = (await admin.listModels()) as ModelRow[];
    } catch (e) {
      push(`Sync failed for ${p.name}: ${errMsg(e)}`, 'error');
    } finally {
      syncing[p.id] = false;
    }
  }

  async function syncAll() {
    if (syncingAll) return;
    syncingAll = true;
    let total = 0;
    const failures: string[] = [];
    for (const p of providers) {
      syncing[p.id] = true;
      try {
        total += (await admin.syncProvider(p.id)).count;
      } catch {
        failures.push(p.name);
      } finally {
        syncing[p.id] = false;
      }
    }
    syncingAll = false;
    models = (await admin.listModels()) as ModelRow[];
    const tail = failures.length ? ` (${failures.length} failed: ${failures.join(', ')})` : '';
    push(`Synced ${total} model${total === 1 ? '' : 's'} total${tail}`, failures.length ? 'error' : 'success');
  }

  async function doDelete() {
    const p = deleteTarget;
    if (!p) return;
    try {
      await admin.deleteProvider(p.id);
    } catch (e) {
      push(errMsg(e, 'Could not delete provider'), 'error');
      throw e; // keep dialog open for retry
    }
    providers = providers.filter((x) => x.id !== p.id);
    expanded.delete(p.id);
    push(`Deleted ${p.name}`, 'success');
    deleteTarget = null;
  }
</script>

<div class="flex flex-col gap-4">
  <div class="flex items-center justify-between gap-3">
    <p class="text-sm text-[var(--muted)]">
      {providers.length} provider{providers.length === 1 ? '' : 's'} · {models.length} cached model{models.length === 1 ? '' : 's'}
    </p>
    <div class="flex items-center gap-2">
      <button class={btn} style={btnGhostStyle} onclick={syncAll} disabled={syncingAll || loading || providers.length === 0}>
        {#if syncingAll}<Spinner />{:else}<RefreshCw size={14} />{/if}
        Sync all
      </button>
      <button class={btn} style={btnPrimaryStyle} onclick={openCreate}>
        <Plus size={14} /> Add provider
      </button>
    </div>
  </div>

  {#if loading}
    <div class="flex items-center gap-2 rounded-xl border px-4 py-8 text-sm text-[var(--muted)]" style={panelStyle}>
      <Spinner size={16} /> Loading providers…
    </div>
  {:else if loadError}
    <div class="flex items-center justify-between gap-3 rounded-xl border px-4 py-3 text-sm" style="background:color-mix(in srgb, var(--danger) 10%, transparent); border-color:var(--danger); color:var(--danger)">
      {loadError}
      <button class={btn} style={btnGhostStyle} onclick={load}>Retry</button>
    </div>
  {:else if providers.length === 0}
    <EmptyState
      title="No providers yet"
      description="Add an OpenAI-compatible endpoint (OpenAI, Anthropic-via-proxy, Ollama, vLLM…) and sync it to start chatting."
    >
      {#snippet icon()}<Inbox size={22} />{/snippet}
      {#snippet actions()}
        <button class={btn} style={btnPrimaryStyle} onclick={openCreate}><Plus size={14} /> Add your first provider</button>
      {/snippet}
    </EmptyState>
  {:else}
    <div class="{panel} overflow-x-auto" style={panelStyle}>
      <table class="w-full text-sm">
        <thead>
          <tr class="text-[11px] uppercase tracking-wider" style="color:var(--muted); {rowBorder}">
            <th class="{th} w-8"></th>
            <th class={th}>Provider</th>
            <th class={th}>Base URL</th>
            <th class={th}>API key</th>
            <th class={th}>Enabled</th>
            <th class={th}>Models</th>
            <th class="{th} text-right">Actions</th>
          </tr>
        </thead>
        <tbody>
          {#each providers as p (p.id)}
            {@const count = models.filter((m) => m.provider_id === p.id).length}
            <tr style={rowBorder}>
              <td class={td}>
                <button
                  class="rounded p-1 text-[var(--muted)] hover:bg-[var(--panel-2)] hover:text-[var(--text)]"
                  aria-label={expanded.has(p.id) ? 'Collapse models' : 'Expand models'}
                  onclick={() => toggleExpand(p.id)}
                >
                  {#if expanded.has(p.id)}<ChevronDown size={15} />{:else}<ChevronRight size={15} />{/if}
                </button>
              </td>
              <td class={td}>
                <span class="font-medium">{p.name}</span>
                {#if p.is_default}<span class="ml-1.5 inline-flex"><Badge kind="accent">default</Badge></span>{/if}
              </td>
              <td class="{td} max-w-[16rem] truncate font-mono text-xs text-[var(--muted)]" title={p.base_url}>{p.base_url}</td>
              <td class={td}>
                {#if p.has_key}
                  <span class="font-mono text-xs tracking-widest text-[var(--muted)]" title="Key stored encrypted">••••••••</span>
                {:else}
                  <span class="text-xs text-[var(--muted)]">none</span>
                {/if}
              </td>
              <td class={td}>
                <Toggle checked={p.enabled} onchange={() => toggleEnabled(p)} label="Toggle {p.name}" />
              </td>
              <td class={td}>
                <button class="text-xs text-[var(--accent)] hover:underline" onclick={() => toggleExpand(p.id)}>
                  {count} model{count === 1 ? '' : 's'}
                </button>
              </td>
              <td class="{td}">
                <div class="flex items-center justify-end gap-1">
                  <button
                    class="rounded-md p-1.5 text-[var(--muted)] transition-colors hover:bg-[var(--panel-2)] hover:text-[var(--text)] disabled:opacity-40"
                    title="Sync models"
                    disabled={syncing[p.id] || syncingAll}
                    onclick={() => syncOne(p)}
                  >
                    {#if syncing[p.id]}<Spinner />{:else}<RefreshCw size={14} />{/if}
                  </button>
                  <button
                    class="rounded-md p-1.5 text-[var(--muted)] transition-colors hover:bg-[var(--panel-2)] hover:text-[var(--text)]"
                    title="Edit provider"
                    onclick={() => openEdit(p)}
                  >
                    <Pencil size={14} />
                  </button>
                  <button
                    class="rounded-md p-1.5 text-[var(--muted)] transition-colors hover:bg-[var(--panel-2)] hover:text-[var(--danger)]"
                    title="Delete provider"
                    onclick={() => {
                      deleteTarget = p;
                      confirmDelete = true;
                    }}
                  >
                    <Trash2 size={14} />
                  </button>
                </div>
              </td>
            </tr>

            {#if expanded.has(p.id)}
              <tr style={rowBorder}>
                <td colspan={7} class="px-3 py-3" style={panel2Style}>
                  {#if syncing[p.id]}
                    <p class="flex items-center gap-2 py-2 text-xs text-[var(--muted)]"><Spinner /> Fetching models from {p.name}…</p>
                  {:else if count === 0}
                    <p class="py-2 text-xs text-[var(--muted)]">
                      No cached models — <button class="text-[var(--accent)] hover:underline" onclick={() => syncOne(p)}>sync now</button>.
                    </p>
                  {:else}
                    <div class="mb-1 flex items-center justify-between">
                      <p class="text-[11px] uppercase tracking-wider text-[var(--muted)]">Models · {p.name}</p>
                      {#if !admin.CAN_EDIT_MODELS}
                        <p class="text-[11px] text-[var(--muted)]">rename / enable need a model-edit API — shown read-only</p>
                      {/if}
                    </div>
                    <table class="w-full text-xs">
                      <thead>
                        <tr class="text-[10px] uppercase tracking-wider" style="color:var(--muted)">
                          <th class="{th} pl-0">Model ID</th>
                          <th class={th}>Display name</th>
                          <th class={th}>Status</th>
                          <th class={th}>Context</th>
                        </tr>
                      </thead>
                      <tbody>
                        {#each models.filter((m) => m.provider_id === p.id) as m (m.model_id)}
                          <tr style={rowBorder}>
                            <td class="{td} pl-0 font-mono text-[var(--muted)]">{m.model_id}</td>
                            <td class={td}>{m.display || m.model_id}</td>
                            <td class={td}>
                              {#if m.enabled === false}
                                <Badge kind="danger">disabled</Badge>
                              {:else}
                                <Badge kind="success">enabled</Badge>
                              {/if}
                            </td>
                            <td class={td}><Badge>{ctxLabel(m.context_len)}</Badge></td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  {/if}
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<ProviderModal bind:open={modalOpen} provider={editing} onsaved={load} />

<Confirm
  bind:open={confirmDelete}
  title="Delete provider"
  message={deleteMessage}
  confirmLabel="Delete"
  danger
  onconfirm={doDelete}
/>
