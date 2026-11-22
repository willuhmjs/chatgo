<script lang="ts">
  // One message row. Assistant: flat full-width + avatar + hover bar
  // (copy / retry / read-aloud), error card, sources chips, collapse.
  // User: right-aligned subtle bubble with copy + inline edit (edit →
  // regenerate path; see docs/FE-CHAT-UI.md "regenerate semantics").
  import type { UIMessage } from '$lib/chat/group';
  import { COLLAPSE_CHARS, COLLAPSE_MAX_PX } from '$lib/chat/group';
  import Markdown from './Markdown.svelte';
  import IconButton from './ui/IconButton.svelte';
  import Copy from 'lucide-svelte/icons/copy';
  import RefreshCw from 'lucide-svelte/icons/refresh-cw';
  import Pencil from 'lucide-svelte/icons/pencil';
  import Volume2 from 'lucide-svelte/icons/volume-2';
  import VolumeX from 'lucide-svelte/icons/volume-x';
  import TriangleAlert from 'lucide-svelte/icons/triangle-alert';
  import LinkIcon from 'lucide-svelte/icons/link';
  import ChevronDown from 'lucide-svelte/icons/chevron-down';
  import Sparkles from 'lucide-svelte/icons/sparkles';
  import { copyText, domainOf, fmtClock, fmtDateTime } from '$lib/chatview-api';
  import { speechSupported, speech, toggleSpeak } from '$lib/chat/state.svelte.js';
  import { push as toast } from '$lib/toast.svelte.js';

  let {
    message,
    busy = false,
    onretry,
    onedit,
  }: {
    message: UIMessage;
    busy?: boolean;
    onretry?: (m: UIMessage) => void;
    onedit?: (m: UIMessage, content: string) => void;
  } = $props();

  let editing = $state(false);
  let editText = $state('');
  let expanded = $state(false);
  let sourcesOpen = $state(false);

  const collapsible = $derived(
    !message.streaming && (message.content?.length ?? 0) > COLLAPSE_CHARS,
  );
  const collapsed = $derived(collapsible && !expanded);
  const speaking = $derived(speech.id === message.id && !!message.id);

  function doCopy(): void {
    void copyText(message.content ?? '').then((ok) =>
      toast(ok ? 'Copied to clipboard' : 'Copy failed', ok ? 'success' : 'error', 2000),
    );
  }
  function startEdit(): void {
    editText = message.content ?? '';
    editing = true;
  }
  function saveEdit(): void {
    const t = editText.trim();
    editing = false;
    if (t && t !== message.content) onedit?.(message, t);
  }
</script>

{#if message.role === 'user'}
  <div class="msg-row group relative flex flex-col items-end gap-1" role="article" aria-label="Your message">
    {#if editing}
      <div class="flex w-full max-w-[92%] flex-col gap-2">
        <textarea
          class="editbox"
          rows="3"
          bind:value={editText}
          aria-label="Edit message"
          onkeydown={(e) => {
            if (e.key === 'Escape') {
              e.stopPropagation();
              editing = false;
            } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              saveEdit();
            }
          }}></textarea>
        <div class="flex items-center gap-2 text-xs">
          <button
            class="rounded-md px-2.5 py-1 font-medium disabled:opacity-40"
            style="background:var(--accent); color:var(--accent-fg)"
            disabled={!editText.trim() || editText.trim() === message.content || busy}
            onclick={saveEdit}>Save &amp; resend</button
          >
          <button class="rounded-md px-2 py-1 disabled:opacity-40" style="color:var(--muted)" onclick={() => (editing = false)}>
            Cancel
          </button>
          <span class="ml-auto opacity-70">Resends this turn and regenerates the rest</span>
        </div>
      </div>
    {:else}
      <div class="bubble" class:pending={message.pending}>
        <div class="whitespace-pre-wrap">{message.content}</div>
        {#if message.pending}
          <span class="ml-1 inline-block h-2 w-2 rounded-full align-middle" style="background:var(--accent)"></span>
        {/if}
      </div>
    {/if}
    {#if !editing}
      <div class="flex items-center gap-1 pr-1">
        <time class="timechip" title={fmtDateTime(message.created_at)}>{fmtClock(message.created_at)}</time>
        <div class="hoverbar">
          <IconButton title="Copy" onclick={doCopy}><Copy size={13} /></IconButton>
          {#if !busy}
            <IconButton title="Edit &amp; resend" onclick={startEdit}><Pencil size={13} /></IconButton>
          {/if}
        </div>
      </div>
    {/if}
  </div>
{:else if message.role === 'system'}
  <div class="msg-row mx-auto max-w-[85%] rounded-lg border px-3 py-1.5 text-center text-xs" style="border-color:var(--border); color:var(--muted); background:var(--panel-2)">
    {message.content}
  </div>
{:else}
  <div class="msg-row group relative flex gap-3" role="article" aria-label="Assistant message">
    <div class="avatar" aria-hidden="true"><Sparkles size={13} /></div>
    <div class="min-w-0 flex-1">
      <div class="relative" class:clamped={collapsed} style={collapsed ? `max-height:${COLLAPSE_MAX_PX}px` : ''}>
        <Markdown src={message.content ?? ''} streaming={!!message.streaming} />
        {#if collapsed}
          <div class="clampfade"></div>
        {/if}
      </div>
      {#if collapsible}
        <button
          class="mt-1 text-xs font-medium"
          style="color:var(--accent)"
          onclick={() => (expanded = !expanded)}
        >
          {expanded ? 'Collapse' : 'Expand'}
        </button>
      {/if}

      {#if message.stopped}
        <p class="mt-1 text-xs italic" style="color:var(--muted)">Generation stopped.</p>
      {/if}

      {#if message.error}
        <div class="errcard" role="alert">
          <TriangleAlert size={15} style="color:var(--danger); flex:none; margin-top:2px" />
          <div class="min-w-0 flex-1">
            <p class="text-sm font-medium">Generation failed</p>
            <p class="text-xs" style="color:var(--muted)">{message.error}</p>
          </div>
          <button
            class="retrybtn"
            disabled={busy}
            onclick={() => onretry?.(message)}
          >
            <RefreshCw size={12} /> Retry
          </button>
        </div>
      {/if}

      {#if message.sources?.length}
        <div class="mt-2">
          <button class="sourcespill" onclick={() => (sourcesOpen = !sourcesOpen)} aria-expanded={sourcesOpen}>
            <LinkIcon size={12} />
            Sources · {message.sources.length}
            <ChevronDown size={12} style={sourcesOpen ? 'transform: rotate(180deg)' : ''} class="transition-transform" />
          </button>
          {#if sourcesOpen}
            <div class="mt-1.5 flex flex-wrap gap-1.5">
              {#each message.sources as s, i (s.url + i)}
                <a
                  class="chip"
                  href={s.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  title={s.title || s.url}
                >
                  <span class="chipidx">[{i + 1}]</span>
                  <span class="truncate">{domainOf(s.url)}</span>
                </a>
              {/each}
            </div>
          {/if}
        </div>
      {/if}

      {#if !message.streaming}
        <div class="mt-0.5 flex items-center gap-1">
          <time class="timechip" title={fmtDateTime(message.created_at)}>{fmtClock(message.created_at)}</time>
          <div class="hoverbar">
            <IconButton title="Copy" onclick={doCopy}><Copy size={13} /></IconButton>
            {#if !busy}
              <IconButton title="Retry (regenerate)" onclick={() => onretry?.(message)}>
                <RefreshCw size={13} />
              </IconButton>
            {/if}
            {#if speechSupported}
              <IconButton title={speaking ? 'Stop reading' : 'Read aloud'} active={speaking} onclick={() => toggleSpeak(message)}>
                {#if speaking}<VolumeX size={13} />{:else}<Volume2 size={13} />{/if}
              </IconButton>
            {/if}
            {#if message.tokens_out}
              <span class="ml-1 text-[10px] tabular-nums opacity-0 transition-opacity group-hover:opacity-100" style="color:var(--muted)">
                {message.tokens_in ?? 0}→{message.tokens_out} tok
              </span>
            {/if}
          </div>
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  /* render offscreen rows cheaply without a virtualizer dependency */
  .msg-row {
    content-visibility: auto;
    contain-intrinsic-size: auto 140px;
  }

  .bubble {
    max-width: 85%;
    border-radius: 1.1rem;
    border-bottom-right-radius: 0.4rem;
    padding: 0.55rem 0.9rem;
    font-size: 0.9375rem;
    line-height: 1.6;
    background: var(--panel-2);
    border: 1px solid var(--border);
    white-space: normal;
  }
  .bubble.pending {
    opacity: 0.7;
  }

  .avatar {
    flex: none;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    margin-top: 3px;
    border-radius: 0.5rem;
    color: #fff;
    background: linear-gradient(135deg, var(--accent), #8b5cf6);
    box-shadow: 0 0 0 1px color-mix(in oklab, var(--accent) 35%, transparent);
  }

  .timechip {
    font-size: 10px;
    color: var(--muted);
    opacity: 0;
    transition: opacity 120ms ease;
  }
  .group:hover .timechip {
    opacity: 0.8;
  }

  .hoverbar {
    display: flex;
    align-items: center;
    gap: 1px;
    opacity: 0;
    transition: opacity 120ms ease;
  }
  .group:hover .hoverbar,
  .hoverbar:focus-within {
    opacity: 1;
  }

  .editbox {
    width: 100%;
    resize: vertical;
    border-radius: 0.75rem;
    border: 1px solid color-mix(in oklab, var(--accent) 45%, var(--border));
    background: var(--panel);
    color: var(--text);
    padding: 0.6rem 0.8rem;
    font-size: 0.9375rem;
    line-height: 1.6;
    outline: none;
    font-family: inherit;
  }

  .clamped {
    overflow: hidden;
  }
  .clampfade {
    position: absolute;
    inset-inline: 0;
    bottom: 0;
    height: 4.5rem;
    pointer-events: none;
    background: linear-gradient(to bottom, transparent, var(--bg));
  }

  .errcard {
    display: flex;
    align-items: flex-start;
    gap: 0.6rem;
    margin-top: 0.6rem;
    padding: 0.6rem 0.75rem;
    border-radius: 0.625rem;
    border: 1px solid color-mix(in oklab, var(--danger) 40%, var(--border));
    background: color-mix(in oklab, var(--danger) 8%, var(--panel));
  }
  .retrybtn {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    flex: none;
    border: 1px solid var(--border);
    background: var(--panel-2);
    color: var(--text);
    border-radius: 0.5rem;
    font-size: 0.75rem;
    padding: 0.25rem 0.6rem;
    cursor: pointer;
  }
  .retrybtn:hover:not(:disabled) {
    border-color: var(--accent);
  }
  .retrybtn:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  .sourcespill {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    border: 1px solid var(--border);
    background: var(--panel-2);
    color: var(--muted);
    border-radius: 999px;
    font-size: 0.72rem;
    padding: 0.2rem 0.65rem;
    cursor: pointer;
  }
  .sourcespill:hover {
    color: var(--text);
    border-color: color-mix(in oklab, var(--accent) 40%, var(--border));
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    max-width: 16rem;
    border: 1px solid var(--border);
    border-radius: 0.5rem;
    background: var(--panel);
    color: var(--text);
    font-size: 0.72rem;
    padding: 0.22rem 0.55rem;
    text-decoration: none;
  }
  .chip:hover {
    border-color: var(--accent);
  }
  .chipidx {
    color: var(--accent);
    font-family: ui-monospace, monospace;
    font-size: 0.68rem;
  }
</style>
