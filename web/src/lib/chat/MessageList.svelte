<script lang="ts">
  // Message scroll area. Sticky-bottom auto-follows the stream; any real
  // user scroll-up (wheel / touch / PageUp) releases it and shows a floating
  // circular "jump to latest" button with an unseen-count dot. Rows use
  // content-visibility:auto instead of a virtualizer (no dependency per
  // contract stack). Layout per docs/DESIGN-POLISH.md §5/§9: 24px rhythm,
  // "ChatGo" wordmark above the first message of each assistant turn,
  // fade+rise 200ms for appended rows only (never on initial load).
  import type { UIMessage } from '$lib/chat/group';
  import { groupMessages } from '$lib/chat/group';
  import type { RatingsMap } from '$lib/chat-features-api';
  import MessageRow from './MessageRow.svelte';
  import ArrowDown from 'lucide-svelte/icons/arrow-down';

  let {
    messages,
    busy = false,
    ratings,
    onretry,
    onedit,
    onrate,
    onratecomment,
    onfork,
  }: {
    messages: UIMessage[];
    busy?: boolean;
    ratings?: RatingsMap;
    onretry?: (m: UIMessage) => void;
    onedit?: (m: UIMessage, content: string) => void;
    onrate?: (m: UIMessage, value: 1 | -1) => void;
    onratecomment?: (m: UIMessage, comment: string) => void;
    onfork?: (m: UIMessage) => void;
  } = $props();

  const groups = $derived(groupMessages(messages));

  // Ids present when this list mounts (initial load) — those rows must NOT
  // animate; everything appended afterwards gets the rise-in. Capturing the
  // initial value is the whole point, so the reactive ref is one-shot.
  // svelte-ignore state_referenced_locally
  const baseline = new Set(messages.map((m) => m.id));
  const riseClass = (id: string): string => (baseline.has(id) ? '' : 'rise');

  let el = $state<HTMLElement | null>(null);
  let stick = $state(true);
  let unseen = $state(0);

  function nearBottom(): boolean {
    return !!el && el.scrollHeight - el.scrollTop - el.clientHeight < 64;
  }

  function onScroll(): void {
    // only ever re-attach at the bottom; user-scroll-away is detected from
    // input events below, so programmatic autoscroll can never un-stick us
    if (nearBottom()) {
      stick = true;
      unseen = 0;
    }
  }
  function releaseFromWheel(e: WheelEvent): void {
    // wheel up = user intent, even while still within the bottom threshold
    if (e.deltaY < 0) stick = false;
  }
  function releaseFromTouch(): void {
    stick = false; // onScroll re-attaches as soon as they hit bottom again
  }
  function releaseFromKey(e: KeyboardEvent): void {
    if (e.key === 'PageUp' || e.key === 'Home') stick = false;
  }

  // ---------- autoscroll (rAF + settle so content-visibility rows that
  // resolve their real heights right after we jump still keep us pinned) ----
  let settleLeft = 0;
  let scheduled = false;
  function scheduleScroll(iterations = 1): void {
    settleLeft = Math.max(settleLeft, iterations);
    if (scheduled) return;
    scheduled = true;
    requestAnimationFrame(() => {
      scheduled = false;
      if (!stick || !el) {
        settleLeft = 0;
        return;
      }
      el.scrollTop = el.scrollHeight;
      if (settleLeft > 1) {
        settleLeft -= 1;
        scheduleScroll(1);
      } else {
        settleLeft = 0;
      }
    });
  }

  // new messages while scrolled away → count them on the button dot
  let prevLen = 0;
  $effect(() => {
    const n = messages.length;
    if (n > prevLen && !stick) unseen += n - prevLen;
    if (n > prevLen) scheduleScroll(10);
    prevLen = n;
  });

  // token flushes grow the tail content → follow along
  $effect(() => {
    void messages[messages.length - 1]?.content?.length;
    scheduleScroll(1);
  });

  function scrollToLatest(): void {
    stick = true;
    unseen = 0;
    el?.scrollTo({ top: el.scrollHeight, behavior: 'smooth' });
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div
  bind:this={el}
  class="relative min-h-0 flex-1 overflow-y-auto overscroll-contain"
  role="log"
  aria-label="Messages"
  aria-live="polite"
  onscroll={onScroll}
  onwheel={releaseFromWheel}
  ontouchmove={releaseFromTouch}
  onkeydown={releaseFromKey}
>
  <div class="mx-auto flex w-full max-w-[48rem] flex-col gap-6 px-4 pb-8 pt-7">
    {#each groups as g (g.key)}
      {#if g.role === 'user'}
        <div class="flex min-w-0 flex-col items-end gap-2.5">
          {#each g.items as m (m.id)}
            <div class="min-w-0 max-w-full {riseClass(m.id)}">
              <MessageRow
                message={m}
                {busy}
                rating={ratings?.[m.id] ?? null}
                {onretry}
                {onedit}
                {onrate}
                {onratecomment}
                {onfork}
              />
            </div>
          {/each}
        </div>
      {:else}
        <div class="flex min-w-0 flex-col gap-5">
          {#each g.items as m, i (m.id)}
            <div class="flex min-w-0 flex-col {riseClass(m.id)}">
              {#if i === 0}<span class="wordmark">ChatGo</span>{/if}
              <MessageRow
                message={m}
                {busy}
                rating={ratings?.[m.id] ?? null}
                {onretry}
                {onedit}
                {onrate}
                {onratecomment}
                {onfork}
              />
            </div>
          {/each}
        </div>
      {/if}
    {/each}
  </div>

  {#if !stick}
    <div class="sticky bottom-4 z-10 flex justify-center" aria-live="polite">
      <button
        type="button"
        class="jump"
        title="Jump to latest"
        aria-label={unseen > 0 ? `Jump to latest (${unseen} new)` : 'Jump to latest'}
        onclick={scrollToLatest}
      >
        <ArrowDown size={16} />
        {#if unseen > 0}
          <span class="count" aria-hidden="true">{unseen > 9 ? '9+' : unseen}</span>
        {/if}
      </button>
    </div>
  {/if}
</div>

<style>
  /* tiny wordmark above the first message of each assistant turn (§5) */
  .wordmark {
    display: block;
    margin-bottom: 6px;
    font-size: 13px;
    font-weight: 500;
    color: var(--muted, #a8a8a8);
    letter-spacing: 0.01em;
  }

  /* appended rows fade + rise 200ms; initial load never animates (§9) */
  .rise {
    animation: row-rise 200ms ease-out both;
  }
  @keyframes row-rise {
    from {
      opacity: 0;
      transform: translateY(6px);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }

  /* floating white circular ↓ with unseen-count dot (§5) */
  .jump {
    position: relative;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 2.125rem;
    height: 2.125rem;
    border-radius: 999px;
    border: 0;
    background: var(--accent, #ffffff);
    color: var(--accent-fg, #0d0d0d);
    box-shadow: var(--shadow-menu, 0 8px 28px rgb(0 0 0 / 0.45));
    cursor: pointer;
    animation: jump-in 150ms ease-out both;
  }
  .count {
    position: absolute;
    top: -3px;
    right: -3px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 15px;
    height: 15px;
    padding: 0 3px;
    border-radius: 999px;
    background: rgb(0 0 0 / 0.85);
    color: var(--accent, #ffffff);
    font-size: 10px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    line-height: 1;
  }
  @keyframes jump-in {
    from {
      opacity: 0;
      transform: translateY(4px);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .rise,
    .jump {
      animation: none;
    }
  }
</style>
