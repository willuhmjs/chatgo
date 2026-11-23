<script lang="ts">
  // One message row (docs/DESIGN-POLISH.md §5). User: right-aligned quiet
  // pill (#2f2f2f, radius 24 per DESIGN-REF §3, max 85%) with copy + inline
  // edit (edit → regenerate path; see docs/FE-CHAT-UI.md "regenerate semantics").
  // Assistant: plain text on canvas — no bubble, no avatar — quiet sources
  // chips, error line + Retry pill, and a hover action bar BELOW the text.
  import { tick } from 'svelte';
  import type { UIMessage } from '$lib/chat/group';
  import { COLLAPSE_CHARS, COLLAPSE_MAX_PX, isLocalId } from '$lib/chat/group';
  import type { Rating } from '$lib/chat-features-api';
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
  import ThumbsUp from 'lucide-svelte/icons/thumbs-up';
  import ThumbsDown from 'lucide-svelte/icons/thumbs-down';
  import GitBranch from 'lucide-svelte/icons/git-branch';
  import { copyText, domainOf, fmtClock, fmtDateTime } from '$lib/chatview-api';
  import { safeHref } from '$lib/safe-url';
  import { speechSupported, speech, toggleSpeak } from '$lib/chat/state.svelte.js';
  import { push as toast } from '$lib/toast.svelte.js';

  let {
    message,
    busy = false,
    rating = null,
    editing = false,
    onretry,
    onedit,
    oneditopen,
    oneditclose,
    onrate,
    onratecomment,
    onfork,
  }: {
    message: UIMessage;
    busy?: boolean;
    rating?: Rating | null;
    /** Controlled by MessageList so at most one row editor is open at a
        time and the ↑/↓ "edit last message" walk can move it between rows. */
    editing?: boolean;
    onretry?: (m: UIMessage) => void;
    onedit?: (m: UIMessage, content: string) => void;
    oneditopen?: (m: UIMessage) => void;
    oneditclose?: () => void;
    onrate?: (m: UIMessage, value: 1 | -1) => void;
    onratecomment?: (m: UIMessage, comment: string) => void;
    onfork?: (m: UIMessage) => void;
  } = $props();

  let editText = $state('');
  let editTa = $state<HTMLTextAreaElement | null>(null);
  let expanded = $state(false);
  let sourcesOpen = $state(false);

  // Editor is parent-controlled: (re)load the message into the draft each
  // time this row's editor opens, then focus with the caret at the end.
  $effect(() => {
    if (editing) editText = message.content ?? '';
  });
  $effect(() => {
    if (!editing) return;
    void tick().then(() => {
      editTa?.focus();
      editTa?.setSelectionRange(editText.length, editText.length);
    });
  });

  // 👍/👎 + comment popover (FE-CHATTER wave 2); server ids only
  const rateable = $derived(!!onrate && !isLocalId(message.id));
  const forkable = $derived(!!onfork && !isLocalId(message.id));
  const isKbSource = (url: string): boolean => url.startsWith('kb://');
  let commentOpen = $state(false);
  let commentText = $state('');
  let commentTa = $state<HTMLTextAreaElement | null>(null);

  $effect(() => {
    if (commentOpen) void tick().then(() => commentTa?.focus());
  });

  function rateUp(): void {
    commentOpen = false;
    onrate?.(message, 1);
  }
  function rateDown(): void {
    const firstClick = rating?.value !== -1;
    onrate?.(message, -1);
    if (firstClick) {
      commentText = rating?.comment ?? '';
      commentOpen = true;
    } else {
      commentOpen = false; // second click cleared the rating — close too
    }
  }
  function saveComment(): void {
    const t = commentText.trim().slice(0, 300);
    commentOpen = false;
    if (t) onratecomment?.(message, t);
  }

  const collapsible = $derived(
    !message.streaming && (message.content?.length ?? 0) > COLLAPSE_CHARS,
  );
  const collapsed = $derived(collapsible && !expanded);
  const speaking = $derived(speech.id === message.id && !!message.id);
  // pre-first-token "thinking" beat: placeholder row with no content yet
  const thinking = $derived(!!message.streaming && !message.content && !message.error);

  function doCopy(): void {
    void copyText(message.content ?? '').then((ok) =>
      toast(ok ? 'Copied to clipboard' : 'Copy failed', ok ? 'success' : 'error', 2000),
    );
  }
  function startEdit(): void {
    if (oneditopen) oneditopen(message);
  }
  function cancelEdit(): void {
    oneditclose?.();
  }
  function saveEdit(): void {
    const t = editText.trim();
    oneditclose?.();
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
          bind:this={editTa}
          bind:value={editText}
          aria-label="Edit message"
          onkeydown={(e) => {
            if (e.key === 'Escape') {
              e.stopPropagation();
              cancelEdit();
            } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              saveEdit();
            } else if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
              // Hand the ↑/↓ "edit previous/next message" walk to MessageList
              // when the caret sits on the first/last line — inside a taller
              // draft the arrows still move the caret.
              const up = e.key === 'ArrowUp';
              const t = e.currentTarget;
              const edge = up
                ? !t.value.slice(0, t.selectionStart).includes('\n')
                : !t.value.slice(t.selectionEnd).includes('\n');
              if (edge && !e.isComposing && !e.metaKey && !e.ctrlKey && !e.altKey) {
                e.preventDefault();
                window.dispatchEvent(
                  new CustomEvent('chatgo:edit-message', { detail: { direction: up ? -1 : 1 } }),
                );
              }
            }
          }}></textarea>
        <div class="flex items-center gap-2 text-xs">
          <button
            class="rounded-full px-3.5 py-1.5 font-medium disabled:opacity-40"
            style="background:var(--accent); color:var(--accent-fg, #0d0d0d)"
            disabled={!editText.trim() || busy}
            onclick={saveEdit}>Save &amp; resend</button
          >
          <button class="rounded-full px-2.5 py-1.5 disabled:opacity-40" style="color:var(--muted)" onclick={() => (editing = false)}>
            Cancel
          </button>
          <span class="ml-auto opacity-70">Resends this turn and regenerates the rest</span>
        </div>
      </div>
    {:else}
      <div class="bubble" class:pending={message.pending}>
        <div class="whitespace-pre-wrap">{message.content}</div>
      </div>
    {/if}
    {#if !editing}
      <div class="flex items-center gap-1 pr-1">
        <time class="timechip" title={fmtDateTime(message.created_at)}>{fmtClock(message.created_at)}</time>
        <div class="hoverbar">
          <IconButton title="Copy" onclick={doCopy}><Copy size={15} /></IconButton>
          {#if !busy}
            <IconButton title="Edit &amp; resend" onclick={startEdit}><Pencil size={15} /></IconButton>
          {/if}
        </div>
      </div>
    {/if}
  </div>
{:else if message.role === 'system'}
  <div class="msg-row mx-auto max-w-[85%] rounded-lg px-3 py-1.5 text-center text-xs" style="color:var(--muted); background:var(--panel)">
    {message.content}
  </div>
{:else}
  <div class="msg-row group relative" role="article" aria-label="Assistant message">
    {#if thinking}
      <div class="thinking" role="status" aria-label="Waiting for the model">
        <span></span><span></span><span></span>
      </div>
    {:else}
      <div class="relative" class:clamped={collapsed} style={collapsed ? `max-height:${COLLAPSE_MAX_PX}px` : ''}>
        <Markdown src={message.content ?? ''} streaming={!!message.streaming} />
        {#if collapsed}
          <div class="clampfade"></div>
        {/if}
      </div>
    {/if}
    {#if collapsible}
      <button
        class="mt-1 text-xs font-medium"
        style="color:var(--text)"
        onclick={() => (expanded = !expanded)}
      >
        {expanded ? 'Collapse' : 'Expand'}
      </button>
    {/if}

    {#if message.stopped}
      <p class="mt-1 text-xs" style="color:var(--muted)">Generation stopped.</p>
    {/if}

    {#if message.error}
      <div class="errline" role="alert">
        <TriangleAlert size={14} style="color:var(--danger); flex:none" />
        <span class="min-w-0 flex-1 text-xs" style="color:var(--muted)">
          <span style="color:var(--text)">Generation failed.</span> {message.error}
        </span>
        <button type="button" class="retrypill" disabled={busy} onclick={() => onretry?.(message)}>
          <RefreshCw size={11} /> Retry
        </button>
      </div>
    {/if}

    {#if message.sources?.length}
      <div class="mt-2.5">
        <button type="button" class="sourcespill" onclick={() => (sourcesOpen = !sourcesOpen)} aria-expanded={sourcesOpen}>
          <LinkIcon size={12} />
          Sources · {message.sources.length}
          <ChevronDown size={12} style={sourcesOpen ? 'transform: rotate(180deg)' : ''} class="transition-transform" />
        </button>
        {#if sourcesOpen}
          <div class="mt-1.5 flex flex-wrap gap-1.5">
            {#each message.sources as s, i (s.url + i)}
              {#if isKbSource(s.url)}
                <!-- kb:// refs have no public URL — quiet non-clickable chip -->
                <span class="chip" title={s.title || s.url}>
                  <span class="chipidx">[{i + 1}]</span>
                  <span class="truncate">{s.title || s.url}</span>
                </span>
              {:else}
                <a
                  class="chip"
                  href={safeHref(s.url)}
                  target="_blank"
                  rel="noopener noreferrer"
                  title={s.title || s.url}
                >
                  <span class="chipidx">[{i + 1}]</span>
                  <span class="truncate">{domainOf(s.url)}</span>
                </a>
              {/if}
            {/each}
          </div>
        {/if}
      </div>
    {/if}

    {#if !message.streaming}
      <!-- hover action bar BELOW the text: copy · regenerate · read-aloud (§5) -->
      <div class="acts" class:rated={!!rating || commentOpen}>
        <IconButton title="Copy" onclick={doCopy}><Copy size={15} /></IconButton>
        {#if !busy}
          <IconButton title="Retry (regenerate)" onclick={() => onretry?.(message)}>
            <RefreshCw size={15} />
          </IconButton>
        {/if}
        {#if speechSupported}
          <IconButton title={speaking ? 'Stop reading' : 'Read aloud'} active={speaking} onclick={() => toggleSpeak(message)}>
            {#if speaking}<VolumeX size={15} />{:else}<Volume2 size={15} />{/if}
          </IconButton>
        {/if}
        {#if rateable}
          <IconButton title={rating?.value === 1 ? 'Remove rating' : 'Good response'} onclick={rateUp}>
            <ThumbsUp size={15} style={rating?.value === 1 ? 'color:var(--ok, #10a37f)' : ''} />
          </IconButton>
          <span class="rateme">
            <IconButton
              title={rating?.value === -1 ? 'Remove rating' : 'Poor response'}
              onclick={rateDown}
            >
              <ThumbsDown size={15} style={rating?.value === -1 ? 'color:var(--danger, #ef4444)' : ''} />
            </IconButton>
            {#if commentOpen}
              <div class="ratepop" role="dialog" aria-label="Tell us what went wrong">
                <textarea
                  class="ratera"
                  rows="3"
                  maxlength="300"
                  placeholder="What could be better? (optional)"
                  aria-label="Rating comment"
                  bind:value={commentText}
                  bind:this={commentTa}
                  onkeydown={(e) => {
                    if (e.key === 'Escape') {
                      e.stopPropagation();
                      commentOpen = false;
                    } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                      e.preventDefault();
                      saveComment();
                    }
                  }}></textarea>
                <div class="flex items-center justify-end gap-1.5 pt-1.5">
                  <button type="button" class="skipbtn" onclick={() => (commentOpen = false)}>Skip</button>
                  <button type="button" class="savebtn" disabled={!commentText.trim()} onclick={saveComment}>
                    Save
                  </button>
                </div>
              </div>
            {/if}
          </span>
        {/if}
        {#if forkable}
          <IconButton title="Branch from here" disabled={busy} onclick={() => onfork?.(message)}>
            <GitBranch size={15} />
          </IconButton>
        {/if}
        <time class="timechip" title={fmtDateTime(message.created_at)}>{fmtClock(message.created_at)}</time>
        {#if message.tokens_out}
          <span class="tokcount">{message.tokens_in ?? 0}→{message.tokens_out} tok</span>
        {/if}
      </div>
    {/if}
  </div>
{/if}

<style>
  /* render offscreen rows cheaply without a virtualizer dependency */
  .msg-row {
    content-visibility: auto;
    contain-intrinsic-size: auto 140px;
  }

  /* user: right-aligned quiet pill (§5 + docs/DESIGN-REF.md §3: r24) —
     accent tint over the --panel fill (FE-COLOR: quiet color = signal) */
  .bubble {
    max-width: 85%;
    border-radius: 24px;
    padding: 10px 16px;
    font-size: 1rem;
    line-height: 1.6;
    background: color-mix(in srgb, var(--acc) 16%, var(--panel, #2f2f2f));
    white-space: normal;
  }
  .bubble.pending {
    opacity: 0.7;
  }

  /* pre-first-token thinking dots — one text line tall, no layout shift (§5) */
  .thinking {
    display: flex;
    align-items: center;
    gap: 5px;
    height: 1.7rem;
  }
  .thinking span {
    width: 5px;
    height: 5px;
    border-radius: 999px;
    background: var(--muted, #a8a8a8);
    animation: dot-pulse 1.2s ease-in-out infinite;
  }
  .thinking span:nth-child(2) {
    animation-delay: 0.15s;
  }
  .thinking span:nth-child(3) {
    animation-delay: 0.3s;
  }
  @keyframes dot-pulse {
    0%,
    60%,
    100% {
      opacity: 0.25;
    }
    30% {
      opacity: 0.9;
    }
  }

  .timechip {
    font-size: 10px;
    color: var(--muted, #a8a8a8);
    opacity: 0;
    transition: opacity 120ms ease;
  }
  .tokcount {
    margin-left: 4px;
    font-size: 10px;
    color: var(--muted, #a8a8a8);
    font-variant-numeric: tabular-nums;
    opacity: 0;
    transition: opacity 120ms ease;
  }

  /* action bar below assistant text: fade in on row hover/focus (§5) */
  .acts {
    display: flex;
    align-items: center;
    gap: 2px;
    margin-top: 4px;
    opacity: 0;
    transition: opacity 120ms ease;
  }
  .hoverbar {
    display: flex;
    align-items: center;
    gap: 2px;
    opacity: 0;
    transition: opacity 120ms ease;
  }
  .group:hover .acts,
  .group:hover .timechip,
  .group:hover .tokcount,
  .acts:focus-within,
  .group:hover .hoverbar,
  .hoverbar:focus-within {
    opacity: 1;
  }
  /* a message the user already rated keeps its bar visible (quiet signal) */
  .acts.rated {
    opacity: 1;
  }

  .editbox {
    width: 100%;
    resize: vertical;
    border-radius: 16px;
    border: 1px solid transparent;
    background: var(--panel, #2f2f2f);
    color: var(--text);
    padding: 0.65rem 0.9rem;
    font-size: 1rem;
    line-height: 1.6;
    outline: none;
    font-family: inherit;
    transition: box-shadow 120ms ease;
  }
  .editbox:focus {
    box-shadow: 0 0 0 2px var(--accent-soft, rgb(99 102 241 / 0.18));
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
    background: linear-gradient(to bottom, transparent, var(--bg, #212121));
  }

  /* quiet red-tinted error line + Retry pill (§5) */
  .errline {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    margin-top: 0.6rem;
    padding: 0.5rem 0.5rem 0.5rem 0.75rem;
    border-radius: 0.75rem;
    background: color-mix(in oklab, var(--danger) 7%, transparent);
  }
  .retrypill {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    flex: none;
    border: 0;
    background: var(--panel-2, #3a3a3a);
    color: var(--text);
    border-radius: 999px;
    font-size: 0.75rem;
    padding: 0.3rem 0.7rem;
    cursor: pointer;
    transition: background 120ms ease;
  }
  .retrypill:hover:not(:disabled) {
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
  }
  .retrypill:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  /* sources: quiet #2f2f2f pills, no borders (§5) */
  .sourcespill {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    border: 0;
    background: var(--panel, #2f2f2f);
    color: var(--muted, #a8a8a8);
    border-radius: 999px;
    font-size: 0.75rem;
    padding: 0.25rem 0.7rem;
    cursor: pointer;
    transition:
      color 120ms ease,
      background 120ms ease;
  }
  .sourcespill:hover {
    color: var(--text);
    background: var(--panel-2, #3a3a3a);
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    max-width: 16rem;
    border: 0;
    border-radius: 0.5rem;
    background: var(--panel, #2f2f2f);
    color: var(--text);
    font-size: 0.75rem;
    padding: 0.25rem 0.6rem;
    text-decoration: none;
    transition: background 120ms ease;
  }
  .chip:hover {
    background: var(--panel-2, #3a3a3a);
  }
  .chipidx {
    color: var(--muted, #a8a8a8);
    font-size: 0.68rem;
    font-variant-numeric: tabular-nums;
  }

  /* 👎 inline comment popover: tiny --panel card above the thumb (wave 2) */
  .rateme {
    position: relative;
    display: inline-flex;
  }
  .ratepop {
    position: absolute;
    bottom: calc(100% + 6px);
    left: 0;
    z-index: 30;
    width: 15.5rem;
    padding: 0.5rem 0.5rem 0.55rem;
    border-radius: 0.875rem;
    background: var(--panel, #2f2f2f);
    box-shadow: var(--shadow-menu, 0 8px 28px rgb(0 0 0 / 0.45));
    animation: rate-pop 90ms ease-out;
  }
  .ratera {
    width: 100%;
    resize: none;
    border: 0;
    border-radius: 0.625rem;
    background: var(--panel-2, #3a3a3a);
    color: var(--text);
    font-family: inherit;
    font-size: 0.75rem;
    line-height: 1.5;
    padding: 0.4rem 0.5rem;
    outline: none;
  }
  .ratera::placeholder {
    color: var(--faint, #6e6e6e);
  }
  .skipbtn {
    border: 0;
    background: transparent;
    color: var(--muted, #a8a8a8);
    border-radius: 999px;
    font-size: 0.75rem;
    padding: 0.3rem 0.6rem;
    cursor: pointer;
    transition:
      color 120ms ease,
      background 120ms ease;
  }
  .skipbtn:hover {
    color: var(--text);
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
  }
  .savebtn {
    border: 0;
    background: var(--panel-2, #3a3a3a);
    color: var(--text);
    border-radius: 999px;
    font-size: 0.75rem;
    font-weight: 500;
    padding: 0.3rem 0.7rem;
    cursor: pointer;
    transition: background 120ms ease;
  }
  .savebtn:hover:not(:disabled) {
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
  }
  .savebtn:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
  @keyframes rate-pop {
    from {
      opacity: 0;
      transform: scale(0.97);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .thinking span {
      animation: none;
      opacity: 0.6;
    }
  }
</style>
