<script lang="ts">
  // Evaluations tab — 👍/👎 feedback roll-up (GET /api/admin/ratings).
  // Quiet monochrome tables (§5) reusing the AuditTab kit; page-based
  // pagination (the endpoint answers total/page/per, unlike the audit cursor).
  import { MessagesSquare, ThumbsDown, ThumbsUp } from 'lucide-svelte';
  import * as api from '$lib/api';
  import type { EvalModel, EvalSummary } from '$lib/api';
  import { timeAgo } from '$lib/ui/chat-grouping';
  import { errMsg } from '$lib/admin/format';
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

  const PER = 25;

  let data = $state<EvalSummary | null>(null);
  let page = $state(1);
  let loading = $state(true);
  let loadError = $state('');
  let forbidden = $state(false); // 403 — the tab only renders for admins, so this means a stale/odd session

  async function load(p = page) {
    loading = true;
    loadError = '';
    forbidden = false;
    try {
      data = await api.getEvaluations(p, PER);
      page = data.page || p;
    } catch (e) {
      data = null;
      if (e instanceof api.ApiError && e.status === 403) forbidden = true;
      else loadError = errMsg(e, 'Could not load evaluations');
    } finally {
      loading = false;
    }
  }
  void load(1);

  const totals = $derived(data?.totals ?? { up: 0, down: 0, score: 0 });
  const votes = $derived(totals.up + totals.down);
  const satPct = $derived(votes === 0 ? 0 : Math.round((totals.up / votes) * 100));
  const pageCount = $derived(data ? Math.max(1, Math.ceil(data.total / data.per)) : 1);

  /** Satisfaction % for one model row, guarded divide-by-zero. */
  function modelPct(m: EvalModel): number {
    const n = m.up + m.down;
    return n === 0 ? 0 : Math.round((m.up / n) * 100);
  }
</script>

<div class="flex flex-col gap-4">
  {#if loading}
    <div class="flex items-center gap-2 rounded-2xl px-4 py-8 text-sm text-[var(--muted)]" style={panelStyle}>
      <Spinner size={16} /> Loading evaluations…
    </div>
  {:else if forbidden}
    <EmptyState title="Admins only" description="Evaluation stats are only visible to administrator accounts.">
      {#snippet icon()}<ThumbsUp size={22} />{/snippet}
    </EmptyState>
  {:else if loadError}
    <div class="flex items-center justify-between gap-3 rounded-[10px] px-4 py-3 text-sm" style={errorBannerStyle}>
      {loadError}
      <button class={btn} style={btnGhostStyle} onclick={() => load()}>Retry</button>
    </div>
  {:else if data && data.total === 0}
    <EmptyState
      title="No feedback yet"
      description="Thumbs up and down votes on assistant messages will appear here as readers use the chat."
    >
      {#snippet icon()}<MessagesSquare size={22} />{/snippet}
    </EmptyState>
  {:else if data}
    <!-- stat cards: 👍 / 👎 / satisfaction + split bar -->
    <div class="grid gap-3 sm:grid-cols-3">
      <div class="rounded-2xl px-4 py-3" style={panelStyle}>
        <p class="flex items-center gap-1.5 text-xs text-[var(--muted)]">
          <ThumbsUp size={13} style="color:var(--ok)" /> Thumbs up
        </p>
        <p class="mt-1 text-xl font-semibold tabular-nums" style="color:var(--ok)">{totals.up}</p>
      </div>
      <div class="rounded-2xl px-4 py-3" style={panelStyle}>
        <p class="flex items-center gap-1.5 text-xs text-[var(--muted)]">
          <ThumbsDown size={13} style="color:var(--danger)" /> Thumbs down
        </p>
        <p class="mt-1 text-xl font-semibold tabular-nums" style="color:var(--danger)">{totals.down}</p>
      </div>
      <div class="rounded-2xl px-4 py-3" style={panelStyle}>
        <p class="text-xs text-[var(--muted)]">Satisfaction</p>
        <p class="mt-1 text-xl font-semibold tabular-nums">
          {#if votes === 0}—{:else}{satPct}%{/if}
        </p>
        <!-- two flex divs sized by share; empty track when nobody has voted -->
        <div class="mt-2 flex h-1.5 overflow-hidden rounded-full" style="background:var(--panel-2)">
          {#if votes > 0}
            <div style="width:{(totals.up / votes) * 100}%; background:var(--ok)"></div>
            <div style="width:{(totals.down / votes) * 100}%; background:var(--danger)"></div>
          {/if}
        </div>
      </div>
    </div>

    <!-- per-model breakdown (server already sorts by volume) -->
    <section class="flex flex-col gap-3">
      <h2 class="flex items-center gap-2 text-[15px] font-medium">Models</h2>
      {#if data.models.length === 0}
        <p class="px-1 text-xs text-[var(--muted)]">No per-model feedback yet.</p>
      {:else}
        <div class="{panel} overflow-x-auto" style={panelStyle}>
          <table class="w-full text-sm">
            <thead>
              <tr class="uppercase tracking-wider" style={rowBorder}>
                <th class={th}>Model</th>
                <th class={th}>Provider</th>
                <th class="{th} text-right">👍</th>
                <th class="{th} text-right">👎</th>
                <th class={th}>Satisfaction</th>
              </tr>
            </thead>
            <tbody>
              {#each data.models as m, i (i)}
                {@const pct = modelPct(m)}
                <tr class={row} style={rowBorder}>
                  <td class="{td} max-w-[280px]">
                    <span class="block truncate font-mono text-xs" title={m.model_id}>{m.model_id || '—'}</span>
                  </td>
                  <td class="{td} max-w-[160px]">
                    <span class="block truncate text-xs text-[var(--muted)]" title={m.provider_id}>
                      {m.provider_id || '—'}
                    </span>
                  </td>
                  <td class="{td} text-right text-xs tabular-nums" style="color:var(--ok)">{m.up}</td>
                  <td class="{td} text-right text-xs tabular-nums" style="color:var(--danger)">{m.down}</td>
                  <td class="{td} w-48">
                    <div class="flex items-center gap-2">
                      <div class="flex h-1.5 w-24 overflow-hidden rounded-full" style="background:var(--panel-2)">
                        {#if m.up + m.down > 0}
                          <div style="width:{(m.up / (m.up + m.down)) * 100}%; background:var(--ok)"></div>
                          <div style="width:{(m.down / (m.up + m.down)) * 100}%; background:var(--danger)"></div>
                        {/if}
                      </div>
                      <span class="text-xs tabular-nums text-[var(--muted)]">
                        {m.up + m.down === 0 ? '—' : `${pct}%`}
                      </span>
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>

    <!-- recent feedback feed -->
    <section class="flex flex-col gap-3">
      <h2 class="flex items-center gap-2 text-[15px] font-medium">Recent feedback</h2>
      {#if data.recent.length === 0}
        <p class="px-1 text-xs text-[var(--muted)]">No feedback on this page.</p>
      {:else}
        <div class="{panel} divide-y" style={panelStyle}>
          {#each data.recent as f, i (i)}
            <div class="flex items-start gap-3 px-3 py-2.5 transition-colors hover:bg-[var(--fill-hover)]" style={rowBorder}>
              <span class="mt-0.5 shrink-0" style="color:{f.value === 1 ? 'var(--ok)' : 'var(--danger)'}">
                {#if f.value === 1}<ThumbsUp size={15} />{:else}<ThumbsDown size={15} />{/if}
              </span>
              <div class="min-w-0 flex-1">
                <p class="flex flex-wrap items-center gap-x-2 text-xs text-[var(--muted)]">
                  <span class="font-mono text-[var(--text)]">{f.model_id || '—'}</span>
                  <span>·</span>
                  <span title={f.user_name}>{f.user_name || '—'}</span>
                  <span>·</span>
                  <span class="whitespace-nowrap" title={new Date(f.created_at * 1000).toLocaleString()}>
                    {timeAgo(f.created_at)}
                  </span>
                  <a
                    href="/c/{f.chat_id}"
                    class="max-w-[280px] truncate underline-offset-2 hover:text-[var(--text)] hover:underline"
                    title={f.chat_title}
                  >{f.chat_title || 'Untitled chat'}</a>
                </p>
                <p class="mt-1 line-clamp-2 text-sm leading-snug">{f.excerpt || '—'}</p>
                {#if f.comment}
                  <p class="mt-1 line-clamp-2 text-xs italic leading-snug text-[var(--muted)]">“{f.comment}”</p>
                {/if}
              </div>
            </div>
          {/each}
        </div>

        {#if data.total > data.per}
          <div class="flex items-center justify-between gap-3">
            <button
              class={btn}
              style={btnGhostStyle}
              disabled={loading || page <= 1}
              onclick={() => load(page - 1)}
            >Previous</button>
            <p class="text-xs text-[var(--faint)]">
              Page {page} of {pageCount} · {data.total} feedback event{data.total === 1 ? '' : 's'}
            </p>
            <button
              class={btn}
              style={btnGhostStyle}
              disabled={loading || page >= pageCount}
              onclick={() => load(page + 1)}
            >Next</button>
          </div>
        {/if}
      {/if}
    </section>
  {/if}
</div>
