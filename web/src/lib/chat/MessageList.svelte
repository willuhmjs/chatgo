<script lang="ts">
  // Message scroll area. Sticky-bottom auto-follows the stream; any real
  // user scroll-up (wheel / touch / PageUp) releases it and shows a floating
  // "Jump to latest (N)" pill. Rows use content-visibility:auto instead of a
  // virtualizer (no dependency per contract stack).
  import type { UIMessage } from '$lib/chat/group';
  import { groupMessages } from '$lib/chat/group';
  import MessageRow from './MessageRow.svelte';
  import ArrowDown from 'lucide-svelte/icons/arrow-down';

  let {
    messages,
    busy = false,
    onretry,
    onedit,
  }: {
    messages: UIMessage[];
    busy?: boolean;
    onretry?: (m: UIMessage) => void;
    onedit?: (m: UIMessage, content: string) => void;
  } = $props();

  const groups = $derived(groupMessages(messages));

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

  // new messages while scrolled away → count them on the pill
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
  <div class="mx-auto flex w-full max-w-[48rem] flex-col gap-5 px-4 pb-8 pt-6">
    {#each groups as g (g.key)}
      {#if g.role === 'user'}
        <div class="flex flex-col items-end gap-2">
          {#each g.items as m (m.id)}
            <MessageRow message={m} {busy} {onretry} {onedit} />
          {/each}
        </div>
      {:else}
        <div class="flex flex-col gap-4">
          {#each g.items as m (m.id)}
            <MessageRow message={m} {busy} {onretry} {onedit} />
          {/each}
        </div>
      {/if}
    {/each}
  </div>

  {#if !stick}
    <div class="sticky bottom-3 z-10 flex justify-center" aria-live="polite">
      <button class="jump" onclick={scrollToLatest}>
        <ArrowDown size={14} />
        {unseen > 0 ? `Jump to latest (${unseen})` : 'Jump to latest'}
      </button>
    </div>
  {/if}
</div>

<style>
  .jump {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    border-radius: 999px;
    border: 1px solid var(--border);
    background: var(--panel);
    color: var(--text);
    box-shadow: 0 6px 20px rgb(0 0 0 / 0.35);
    font-size: 0.75rem;
    padding: 0.35rem 0.8rem;
    cursor: pointer;
    transition: border-color 120ms ease;
  }
  .jump:hover {
    border-color: var(--accent);
  }
</style>
