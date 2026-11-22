<script lang="ts">
  import { ChevronDown, Inbox, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-svelte';
  import { push } from '$lib/toast.svelte.js';
  import * as admin from '$lib/admin/api-admin';
  import type { ModelToggle, ProviderAdmin, ProviderModel } from '$lib/admin/api-admin';
  import { ctxLabel, errMsg } from '$lib/admin/format';
  import Badge from '$lib/admin/ui/Badge.svelte';
  import Confirm from '$lib/admin/ui/Confirm.svelte';
  import EmptyState from '$lib/admin/ui/EmptyState.svelte';
  import ProviderModal from './ProviderModal.svelte';
  import Spinner from '$lib/admin/ui/Spinner.svelte';
  import Toggle from '$lib/admin/ui/Toggle.svelte';
  import { btn, btnGhostStyle, btnPrimaryStyle, errorBannerStyle, panel, panel2Style, panelStyle, row, rowBorder, td, th } from '$lib/admin/ui/styles';

  let providers = $state<ProviderAdmin[]>([]);
  let loading = $state(true);
  let loadError = $state('');
  let syncing = $state<Record<string, boolean>>({});
  let syncingAll = $state(false);
  let expanded = $state.raw(new Set<string>());
  // NOTE: $state does NOT deep-proxy Set instances (Svelte 5 class-instance semantics) —
  // mutations would never trigger a rerender; .raw + reassign-on-change is the correct runes idiom.

  // Per-provider model editor: lazy-loaded on first expand (includes
  // admin-disabled rows, which never appear in GET /api/models).
  let models = $state<Record<string, ProviderModel[]>>({});
  let modelsBusy = $state<Record<string, boolean>>({});
  let modelsError = $state<Record<string, string>>({});

  let modalOpen = $state(false);
  let editing = $state<ProviderAdmin | null>(null);
  let deleteTarget = $state<ProviderAdmin | null>(null);
  let confirmDelete = $state(false);

  const totalModels = $derived(providers.reduce((n, p) => n + p.model_count, 0));

  /** Chat pickers (chat/state.svelte.js listens app-wide) refresh their cached
   *  GET /api/models list on this event — same tab, no page reload. Call only
   *  AFTER the server write succeeded and local optimistic state settled. */
  function broadcastModelsChanged() {
    window.dispatchEvent(new Event('chatgo:models-changed'));
  }

  const deleteMessage = $derived.by(() => {
    const t = deleteTarget;
    if (!t) return '';
    const n = t.model_count;
    return `Delete ${t.name} and its ${n} cached model${n === 1 ? '' : 's'}? Chats pinned to it will stop working. This cannot be undone.`;
  });

  async function load() {
    loading = providers.length === 0;
    loadError = '';
    try {
      providers = await admin.listProvidersAdmin();
    } catch (e) {
      loadError = errMsg(e, 'Could not load providers');
    } finally {
      loading = false;
    }
  }
  void load();

  async function loadModels(p: ProviderAdmin) {
    modelsBusy[p.id] = true;
    modelsError[p.id] = '';
    try {
      models[p.id] = await admin.listProviderModels(p.id);
    } catch (e) {
      modelsError[p.id] = errMsg(e, 'Could not load models');
    } finally {
      modelsBusy[p.id] = false;
    }
  }

  function toggleExpand(p: ProviderAdmin) {
    // $state.raw Set: mutate-in-place would not notify — rebuild + reassign.
    const next = new Set(expanded);
    if (next.has(p.id)) {
      next.delete(p.id);
    } else {
      next.add(p.id);
      if (!models[p.id]) void loadModels(p);
    }
    expanded = next;
  }

  /** Bulk write with optimistic rows + revert-on-error; the PUT answers the
   *  server-truth list so counts and flags re-sync in one step. */
  async function applyToggles(p: ProviderAdmin, toggles: ModelToggle[]) {
    const prev = models[p.id];
    const next = new Map(toggles.map((t) => [t.model_id, t.enabled]));
    models[p.id] = (prev ?? []).map((m) => (next.has(m.model_id) ? { ...m, enabled: next.get(m.model_id)! } : m));
    modelsBusy[p.id] = true;
    try {
      models[p.id] = await admin.setProviderModels(p.id, toggles);
      p.models_enabled = models[p.id].filter((m) => m.enabled).length;
      broadcastModelsChanged(); // server truth settled → pickers re-GET
    } catch (e) {
      models[p.id] = prev; // reverted — nothing changed, no broadcast
      push(errMsg(e, 'Could not update models'), 'error');
    } finally {
      modelsBusy[p.id] = false;
    }
  }

  async function syncOne(p: ProviderAdmin) {
    syncing[p.id] = true;
    try {
      const r = await admin.syncProviderAdmin(p.id);
      push(
        r.added > 0
          ? `+${r.added} new model${r.added === 1 ? '' : 's'} from ${p.name} · ${r.count} cached`
          : `No new models from ${p.name} (${r.count} cached)`,
        'success',
      );
      if (expanded.has(p.id)) void loadModels(p);
      providers = await admin.listProvidersAdmin(); // refresh cached-model tallies
      broadcastModelsChanged(); // sync can add rows to GET /api/models
    } catch (e) {
      push(`Sync failed for ${p.name}: ${errMsg(e)}`, 'error');
    } finally {
      syncing[p.id] = false;
    }
  }

  async function syncAll() {
    if (syncingAll) return;
    syncingAll = true;
    let added = 0;
    let total = 0;
    const failures: string[] = [];
    for (const p of providers) {
      syncing[p.id] = true;
      try {
        const r = await admin.syncProviderAdmin(p.id);
        added += r.added;
        total += r.count;
      } catch {
        failures.push(p.name);
      } finally {
        syncing[p.id] = false;
      }
    }
    syncingAll = false;
    providers = await admin.listProvidersAdmin().catch(() => providers);
    for (const p of providers) if (expanded.has(p.id)) void loadModels(p);
    broadcastModelsChanged(); // partial failures are fine — pickers just re-GET
    const tail = failures.length ? ` (${failures.length} failed: ${failures.join(', ')})` : '';
    push(`${added > 0 ? `+${added} new model${added === 1 ? '' : 's'}` : 'No new models'} · ${total} cached total${tail}`, failures.length ? 'error' : 'success');
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
    expanded = new Set([...expanded].filter((x) => x !== p.id)); // raw Set: reassign, don't mutate
    broadcastModelsChanged(); // every row of this provider leaves GET /api/models
    push(`Deleted ${p.name}`, 'success');
    deleteTarget = null;
  }

  /** ProviderModal create/edit success — reload the table, then tell pickers
   *  (name/base_url/enabled changes all move rows or labels in /api/models). */
  async function onProviderSaved() {
    await load();
    broadcastModelsChanged();
  }
</script>

<div class="flex flex-col gap-4">
  <div class="flex items-center justify-between gap-3">
    <p class="text-sm text-[var(--muted)]">
      {providers.length} provider{providers.length === 1 ? '' : 's'} · {totalModels} cached model{totalModels === 1 ? '' : 's'}
    </p>
    <div class="flex items-center gap-2">
      <button class={btn} style={btnGhostStyle} onclick={syncAll} disabled={syncingAll || loading || providers.length === 0}>
        {#if syncingAll}<Spinner />{:else}<RefreshCw size={14} />{/if}
        Sync all
      </button>
      <button class={btn} style={btnPrimaryStyle} onclick={() => { editing = null; modalOpen = true; }}>
        <Plus size={14} /> Add provider
      </button>
    </div>
  </div>

  {#if loading}
    <div class="flex items-center gap-2 rounded-2xl px-4 py-8 text-sm text-[var(--muted)]" style={panelStyle}>
      <Spinner size={16} /> Loading providers…
    </div>
  {:else if loadError}
    <div class="flex items-center justify-between gap-3 rounded-[10px] px-4 py-3 text-sm" style={errorBannerStyle}>
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
        <button class={btn} style={btnPrimaryStyle} onclick={() => { editing = null; modalOpen = true; }}><Plus size={14} /> Add your first provider</button>
      {/snippet}
    </EmptyState>
  {:else}
    <div class="{panel} overflow-x-auto" style={panelStyle}>
      <table class="w-full text-sm">
        <thead>
          <tr class="uppercase tracking-wider" style={rowBorder}>
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
            <tr class={row} style={rowBorder}>
              <td class={td}>
                <span class="flex items-center gap-1.5 text-left">
                  <span class="font-medium">{p.name}</span>
                  {#if p.is_default}<Badge kind="accent">default</Badge>{/if}
                </span>
              </td>
              <td class="{td} max-w-[16rem] truncate font-mono text-xs text-[var(--muted)]" title={p.base_url}>{p.base_url}</td>
              <td class={td}>
                <span class="flex items-center gap-1.5 text-xs {p.has_key ? '' : 'text-[var(--muted)]'}" title={p.has_key ? 'Key stored encrypted' : 'No key configured'}>
                  <span class="h-1.5 w-1.5 shrink-0 rounded-full" style="background:{p.has_key ? '#10a37f' : 'var(--faint)'}"></span>
                  {p.has_key ? 'configured' : 'unset'}
                </span>
              </td>
              <td class={td}>
                <Toggle
                  checked={p.enabled}
                  onchange={async (next) => {
                    const prev = p.enabled;
                    p.enabled = next; // optimistic; rolled back on failure
                    try {
                      await admin.updateProvider(p.id, { enabled: next });
                      broadcastModelsChanged(); // whole provider appears/disappears
                    } catch (e) {
                      p.enabled = prev;
                      push(errMsg(e, 'Could not update provider'), 'error');
                    }
                  }}
                  label="Toggle {p.name}"
                />
              </td>
              <td class={td}>
                <button
                  class="flex items-center gap-1.5 rounded-lg px-2 py-1 text-xs text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)]"
                  aria-expanded={expanded.has(p.id)}
                  title="Enable/disable this provider's models"
                  onclick={() => toggleExpand(p)}
                >
                  <span class="tabular-nums">{p.models_enabled}/{p.model_count}</span>
                  <span>models</span>
                  <ChevronDown
                    size={13}
                    class="transition-transform {expanded.has(p.id) ? 'text-[var(--acc)]' : 'text-[var(--faint)]'}"
                    style={expanded.has(p.id) ? 'transform:rotate(180deg)' : ''}
                  />
                </button>
              </td>
              <td class="{td}">
                <div class="flex items-center justify-end gap-1">
                  <button
                    class="rounded-md p-1.5 text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)] disabled:opacity-40"
                    title="Sync models"
                    disabled={syncing[p.id] || syncingAll}
                    onclick={() => syncOne(p)}
                  >
                    {#if syncing[p.id]}<Spinner />{:else}<RefreshCw size={14} />{/if}
                  </button>
                  <button
                    class="rounded-md p-1.5 text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)]"
                    title="Edit provider"
                    onclick={() => { editing = p; modalOpen = true; }}
                  >
                    <Pencil size={14} />
                  </button>
                  <button
                    class="rounded-md p-1.5 text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--danger)]"
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
              {@const rows = models[p.id] ?? []}
              <tr>
                <td colspan={6} class="px-3 py-3" style={panel2Style}>
                  {#if syncing[p.id] || modelsBusy[p.id]}
                    <p class="flex items-center gap-2 py-2 text-xs text-[var(--muted)]">
                      <Spinner /> {syncing[p.id] ? `Fetching models from ${p.name}…` : 'Updating models…'}
                    </p>
                  {:else if modelsError[p.id]}
                    <p class="flex items-center justify-between gap-3 py-1 text-xs" style={errorBannerStyle}>
                      <span class="px-3">{modelsError[p.id]}</span>
                      <button class="px-3 underline underline-offset-2" onclick={() => loadModels(p)}>Retry</button>
                    </p>
                  {:else if rows.length === 0}
                    <p class="py-2 text-xs text-[var(--muted)]">
                      No cached models — <button class="underline underline-offset-2 hover:text-[var(--text)]" onclick={() => syncOne(p)}>sync now</button>.
                    </p>
                  {:else}
                    <div class="mb-1 flex items-center justify-between">
                      <p class="text-xs uppercase tracking-wider text-[var(--muted)]">
                        Models · {rows.filter((m) => m.enabled).length}/{rows.length} enabled
                      </p>
                      <div class="flex items-center gap-2 text-[11px]">
                        <button class="text-[var(--muted)] underline underline-offset-2 hover:text-[var(--text)]" onclick={() => applyToggles(p, rows.map((m) => ({ model_id: m.model_id, enabled: true })))}>
                          Select all
                        </button>
                        <button class="text-[var(--muted)] underline underline-offset-2 hover:text-[var(--text)]" onclick={() => applyToggles(p, rows.map((m) => ({ model_id: m.model_id, enabled: false })))}>
                          Select none
                        </button>
                      </div>
                    </div>
                    <table class="w-full text-xs">
                      <tbody>
                        {#each rows as m (m.model_id)}
                          <tr class="transition-colors hover:bg-[var(--fill-hover)] {m.enabled ? '' : 'opacity-45'}" style={rowBorder}>
                            <td class="{td} w-10">
                              <Toggle
                                checked={m.enabled}
                                onchange={(next) => applyToggles(p, [{ model_id: m.model_id, enabled: next }])}
                                label={m.enabled ? `Disable ${m.model_id}` : `Enable ${m.model_id}`}
                              />
                            </td>
                            <td class={td}>
                              <span class="font-medium">{m.display || m.model_id}</span>
                              {#if m.display && m.display !== m.model_id}
                                <span class="ml-2 font-mono text-[var(--muted)]">{m.model_id}</span>
                              {/if}
                            </td>
                            <td class="{td} w-24 text-right">
                              {#if m.context_len > 0}<Badge>{ctxLabel(m.context_len)}</Badge>{/if}
                            </td>
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

<ProviderModal bind:open={modalOpen} provider={editing} onsaved={onProviderSaved} />

<Confirm
  bind:open={confirmDelete}
  title="Delete provider"
  message={deleteMessage}
  confirmLabel="Delete"
  danger
  onconfirm={doDelete}
/>
