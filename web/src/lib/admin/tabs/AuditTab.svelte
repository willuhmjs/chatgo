<script lang="ts">
  // Audit log tab — newest-first activity feed with cursor pagination.
  // Quiet monochrome table (§5) + hue-coded mono action chips.
  import { ScrollText } from 'lucide-svelte';
  import { actionKind, fetchAuditPage } from '$lib/admin/audit-api';
  import type { AuditEntry, AuditKind } from '$lib/admin/audit-api';
  import { errMsg, relTime } from '$lib/admin/format';
  import EmptyState from '$lib/admin/ui/EmptyState.svelte';
  import Spinner from '$lib/admin/ui/Spinner.svelte';
  import {
    btn,
    btnGhostStyle,
    errorBannerStyle,
    panel,
    panelStyle,
    row,
    rowBorder,
    td,
    th,
  } from '$lib/admin/ui/styles';

  let items = $state<AuditEntry[]>([]);
  let nextCursor = $state<string | null>(null);
  let loading = $state(true);
  let loadingMore = $state(false);
  let loadError = $state('');
  let moreError = $state('');

  // Pure CSS tints (§1: fills over borders), same color-mix recipe as Badge.
  const chipStyles: Record<AuditKind, string> = {
    auth: 'background:color-mix(in srgb, #10a37f 12%, transparent); color:#10a37f', // green — logins, sessions
    security: 'background:color-mix(in srgb, #e8a33d 14%, transparent); color:#e8a33d', // amber — 2fa, login failures
    admin: 'background:color-mix(in srgb, #8b7cf6 13%, transparent); color:#8b7cf6', // blue-purple — admin mutations
    neutral: 'background:var(--panel-2); color:var(--muted)', // everything else / unknown actions
  };
  const chipClass =
    'inline-flex max-w-full items-center truncate rounded-full px-2 py-0.5 font-mono text-[11px] font-medium';

  function fmtTime(ts: number): string {
    if (!ts) return '—';
    return new Date(ts * 1000).toLocaleString(undefined, {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    });
  }

  async function load() {
    loading = true;
    loadError = '';
    try {
      const page = await fetchAuditPage(null);
      items = page.items;
      nextCursor = page.next_cursor;
    } catch (e) {
      items = [];
      nextCursor = null;
      loadError = errMsg(e, 'Could not load the audit log');
    } finally {
      loading = false;
    }
  }
  void load();

  async function loadMore() {
    if (loadingMore || !nextCursor) return;
    loadingMore = true;
    moreError = '';
    try {
      const page = await fetchAuditPage(nextCursor);
      items = [...items, ...page.items];
      nextCursor = page.next_cursor;
    } catch (e) {
      moreError = errMsg(e, 'Could not load more activity');
    } finally {
      loadingMore = false;
    }
  }
</script>

<div class="flex flex-col gap-4">
  {#if loading}
    <div class="flex items-center gap-2 rounded-2xl px-4 py-8 text-sm text-[var(--muted)]" style={panelStyle}>
      <Spinner size={16} /> Loading activity…
    </div>
  {:else if loadError}
    <div class="flex items-center justify-between gap-3 rounded-[10px] px-4 py-3 text-sm" style={errorBannerStyle}>
      {loadError}
      <button class={btn} style={btnGhostStyle} onclick={load}>Retry</button>
    </div>
  {:else if items.length === 0}
    <EmptyState title="No activity yet" description="Sign-ins, security events and admin changes will appear here as they happen.">
      {#snippet icon()}<ScrollText size={22} />{/snippet}
    </EmptyState>
  {:else}
    <div class="{panel} overflow-x-auto" style={panelStyle}>
      <table class="w-full text-sm">
        <thead>
          <tr class="uppercase tracking-wider" style={rowBorder}>
            <th class={th}>Time</th>
            <th class={th}>Actor</th>
            <th class={th}>Action</th>
            <th class={th}>Detail</th>
            <th class={th}>IP</th>
          </tr>
        </thead>
        <tbody>
          {#each items as e, i (i)}
            <tr class={row} style={rowBorder}>
              <td class="{td} whitespace-nowrap text-xs text-[var(--muted)]" title={relTime(e.ts)}>
                {fmtTime(e.ts)}
              </td>
              <td class="{td} max-w-[240px]">
                <span class="block truncate font-mono text-xs" title={e.email}>{e.email || '—'}</span>
              </td>
              <td class="{td}">
                <span class={chipClass} style={chipStyles[actionKind(e.action)]} title={e.action}>
                  {e.action || '—'}
                </span>
              </td>
              <td class={td}>
                <span class="block max-w-72 truncate text-xs text-[var(--muted)]" title={e.detail}>
                  {e.detail || '—'}
                </span>
              </td>
              <td class="{td} whitespace-nowrap font-mono text-xs text-[var(--muted)]">{e.ip || '—'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    <div class="flex flex-col items-center gap-2">
      {#if nextCursor}
        <button class={btn} style={btnGhostStyle} disabled={loadingMore} onclick={loadMore}>
          {#if loadingMore}<Spinner size={14} />{/if} Load more
        </button>
      {:else}
        <p class="text-xs text-[var(--faint)]">End of log</p>
      {/if}
      {#if moreError}
        <div class="flex w-full items-center justify-between gap-3 rounded-[10px] px-4 py-3 text-sm" style={errorBannerStyle}>
          {moreError}
          <button class={btn} style={btnGhostStyle} onclick={loadMore}>Retry</button>
        </div>
      {/if}
    </div>
  {/if}
</div>
