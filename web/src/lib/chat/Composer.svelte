<script lang="ts">
  // Composer (docs/DESIGN-POLISH.md §6 — "THE ChatGPT moment", OWUI pass):
  // rounded-3xl container, transparent autosize textarea, footer row with
  // [attach] [model picker] [search] [knowledge] … [temporary] [mic] [stop]
  // [send]. The send circle is always the up-arrow primary button: while a
  // response streams, sending again ENQUEUES (ChatView owns the queue and
  // auto-fires items when the stream settles); a separate stop circle shows
  // mid-stream. Temporary-chat switch dashed-frames the pill (OWUI contract:
  // ChatView deletes the chat on leave). Enter/⌘Enter semantics keep the
  // chatgo-send-enter pref; image attach via paste/drag/clip → /api/files;
  // Web Speech mic; stop wired to the existing abort path.
  import type { Chat, SendMessageBody } from '$lib/api';
  import * as api from '$lib/api';
  import ModelPicker from './ModelPicker.svelte';
  import SlashPrompts from './SlashPrompts.svelte';
  import ShortcutsHelp from './ShortcutsHelp.svelte';
  import IconButton from './ui/IconButton.svelte';
  import { capabilities } from './state.svelte.js';
  import { prefersEnterSend, formatBytes } from '$lib/chatview-api';
  import {
    listKbCached,
    kbIdsFromChat,
    withKbIds,
    listPromptsCached,
    filterPrompts,
    type KbCollection,
    type PromptEntry,
  } from '$lib/chat-features-api';
  import { navigate } from '$lib/router.svelte.js';
  import { onShortcut } from '$lib/shortcuts.svelte.js';
  import { push as toast } from '$lib/toast.svelte.js';
  import Plus from 'lucide-svelte/icons/plus';
  import Mic from 'lucide-svelte/icons/mic';
  import MicOff from 'lucide-svelte/icons/mic-off';
  import Globe from 'lucide-svelte/icons/globe';
  import BookOpen from 'lucide-svelte/icons/book-open';
  import Check from 'lucide-svelte/icons/check';
  import ArrowUp from 'lucide-svelte/icons/arrow-up';
  import Square from 'lucide-svelte/icons/square';
  import X from 'lucide-svelte/icons/x';
  import LoaderCircle from 'lucide-svelte/icons/loader-circle';

  const MAX_IMAGES = 4;
  const MAX_BYTES = 20 * 1024 * 1024;
  const MAX_LINES = 10;

  interface Attachment {
    id: string;
    name: string;
    size: number;
    previewUrl: string;
    status: 'uploading' | 'done' | 'error';
    fileId?: string;
    error?: string;
  }

  let {
    chat,
    busy = false,
    queue = [],
    temporary = false,
    onsend,
    onstop,
    onupdated,
    onremovequeued,
    ontemporary,
  }: {
    chat: Chat | null | undefined;
    busy?: boolean;
    /** bodies waiting to fire after the current stream settles (ChatView) */
    queue?: SendMessageBody[];
    /** temporary-chat mode: dashed pill + badge; ChatView deletes on leave */
    temporary?: boolean;
    onsend: (body: SendMessageBody) => void;
    onstop: () => void;
    onupdated?: (c: Chat) => void;
    onremovequeued?: (index: number) => void;
    ontemporary?: (v: boolean) => void;
  } = $props();

  let value = $state('');
  let attachments = $state<Attachment[]>([]);
  let dragging = $state(false);
  let searchOn = $state(false);
  let sendEnter = $state(prefersEnterSend());
  let ta = $state<HTMLTextAreaElement | null>(null);
  let fileInput = $state<HTMLInputElement | null>(null);
  let shortcutsOpen = $state(false);

  const uploading = $derived(attachments.some((a) => a.status === 'uploading'));
  const canSend = $derived(!!value.trim() && !uploading);

  // ---------- auto-grow ----------
  $effect(() => {
    void value;
    const el = ta;
    if (!el) return;
    el.style.height = 'auto';
    const lineH = parseFloat(getComputedStyle(el).lineHeight) || 21;
    el.style.height = `${Math.min(el.scrollHeight, lineH * MAX_LINES + 2)}px`;
  });

  // ---------- global shortcuts (⌘Enter send, / focus) ----------
  $effect(() => {
    const offSend = onShortcut('mod+enter', () => doSend());
    const offFocus = onShortcut('slash', () => ta?.focus());
    // '?' with and without the shift flag — layouts differ on which one lands
    const offHelp = onShortcut('shift+?', () => (shortcutsOpen = !shortcutsOpen));
    const offHelp2 = onShortcut('?', () => (shortcutsOpen = !shortcutsOpen));
    const offReqFocus = () => ta?.focus();
    window.addEventListener('chatgo:focus-composer', offReqFocus);
    return () => {
      offSend();
      offFocus();
      offHelp();
      offHelp2();
      window.removeEventListener('chatgo:focus-composer', offReqFocus);
    };
  });

  // ---------- auto-focus on chat open ----------
  // ChatView re-mounts the Composer per route ({#key id}), so this fires once
  // as each chat opens. Desktop only: (min-width:768px) and not (pointer:
  // coarse) — phones/tablets must never get the soft keyboard hijacked. Also
  // skipped when focus already sits in another field (e.g. sidebar search).
  let autoFocusDone = false;
  $effect(() => {
    void ta; // re-run once the textarea binding lands
    if (autoFocusDone || !ta) return;
    autoFocusDone = true;
    if (!window.matchMedia('(min-width: 768px)').matches) return;
    if (window.matchMedia('(pointer: coarse)').matches) return;
    const ae = document.activeElement;
    if (
      ae instanceof HTMLInputElement ||
      ae instanceof HTMLTextAreaElement ||
      ae instanceof HTMLSelectElement ||
      (ae instanceof HTMLElement && ae.isContentEditable)
    ) {
      return;
    }
    ta.focus();
  });

  // ---------- Esc two-step while streaming (arm → stop + recall prompt) ----------
  // Esc in the composer during a stream is deliberately NOT a one-key stop:
  // 1st Esc arms (never stops) — shows an accent chip on the hint strip for
  // ESC_ARM_MS and hands the LAST USER MESSAGE back into the textarea (caret
  // at end, focused) so the prompt is ready to tweak and re-send later.
  // 2nd Esc inside the arm window fires the real stop (existing onstop path)
  // AND marks the stream for retract (chatgo:retract-request → ChatView
  // deletes the cancelled exchange once the stream settles).
  // Honest tradeoff: if a draft is already in the textarea we still arm/show
  // the hint but NEVER clobber unsaved input — recall only into an empty box.
  // Own guards win first: the slash popover consumes Esc in handleSlashKey
  // above; the kb/model popovers and the shortcuts modal cancel Esc in their
  // capture-phase listeners, and the inline row editor is a different
  // textarea — none of them can reach this gesture. Not streaming → no-op,
  // even if a previous arm is lingering (arm always dies with the stream).
  const ESC_ARM_MS = 2500;
  let escHint = $state(false);
  let escArmedAt = 0;
  let escTimer: ReturnType<typeof setTimeout> | undefined;

  function clearEscArm(): void {
    if (escTimer !== undefined) clearTimeout(escTimer);
    escTimer = undefined;
    escHint = false;
    escArmedAt = 0;
  }

  // hint dismissals covered by this one effect: stream end, stop, error —
  // busy dropping to false is common to all of them (send clears in doSend)
  $effect(() => {
    if (!busy) clearEscArm();
  });

  function onComposerEsc(): void {
    if (!busy) return; // not streaming → plain no-op
    if (escHint && Date.now() - escArmedAt <= ESC_ARM_MS) {
      clearEscArm();
      // Esc-Esc means RETRACT, a stop-button press does not (button = stop &
      // keep the partial, ChatGPT parity). ChatView listens, waits for the
      // stream to settle, then deletes the cancelled exchange server-side —
      // the recalled prompt stays in the textarea for the edit+resend.
      window.dispatchEvent(new CustomEvent('chatgo:retract-request'));
      onstop(); // second consecutive Esc → the real stop
      return;
    }
    escHint = true;
    escArmedAt = Date.now();
    if (escTimer !== undefined) clearTimeout(escTimer);
    escTimer = setTimeout(clearEscArm, ESC_ARM_MS);
    if (!value.trim()) {
      // MessageList answers synchronously (same bus pattern as
      // chatgo:edit-message); ChatView owns the messages array outside these
      // walls and state.svelte.js exposes no messages store to consume.
      const req = new CustomEvent('chatgo:last-user-request', {
        detail: { content: '', found: false },
      });
      window.dispatchEvent(req);
      const d = req.detail as { content: string; found: boolean };
      if (d.found) {
        value = d.content; // auto-grow $effect re-fits the height
        ta?.focus();
        ta?.setSelectionRange(value.length, value.length); // caret at end
      }
    }
  }

  function onKeydown(e: KeyboardEvent): void {
    if (slashOpen && handleSlashKey(e)) return;
    if (e.key === 'Escape') {
      onComposerEsc();
      return;
    }
    if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
      // ChatGPT-parity: with an EMPTY composer, ↑/↓ hand off to MessageList's
      // inline-edit walk (opens the last user message / moves the open editor).
      // An empty composer makes ↑ unambiguous — inside text it stays caret
      // movement. Guarded: IME composition, slash/kb popovers (the model
      // picker keeps focus out of this textarea, so it can't reach here),
      // modifiers, and mid-stream.
      if (
        !value.trim() &&
        !e.isComposing &&
        !kbOpen &&
        !busy &&
        !e.shiftKey &&
        !e.metaKey &&
        !e.ctrlKey &&
        !e.altKey
      ) {
        e.preventDefault();
        window.dispatchEvent(
          new CustomEvent('chatgo:edit-message', {
            detail: { direction: e.key === 'ArrowUp' ? -1 : 1 },
          }),
        );
      }
      return;
    }
    if (e.key !== 'Enter') return;
    if (e.metaKey || e.ctrlKey) {
      e.preventDefault();
      doSend();
      return;
    }
    if (e.shiftKey) return; // explicit newline
    if (sendEnter && !e.altKey) {
      e.preventDefault();
      doSend();
    }
  }

  function onKeyup(e: KeyboardEvent): void {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Escape') return;
    refreshSlash();
  }

  function doSend(): void {
    const text = value.trim();
    // While a response streams this is a QUEUED send — ChatView.submit routes
    // it to the visible queue instead of starting a second stream.
    if (!text) return;
    if (uploading) {
      toast('Images are still uploading…', 'info');
      return;
    }
    const images = attachments
      .filter((a) => a.status === 'done' && a.fileId)
      .map((a) => ({ file_id: a.fileId as string }));
    onsend({
      content: text,
      images: images.length ? images : undefined,
      search: capabilities.search && searchOn ? true : undefined,
    });
    value = '';
    slashOpen = false;
    slashDismissed = null;
    clearEscArm(); // any send dismisses the Esc arm/hint
    attachments.forEach((a) => URL.revokeObjectURL(a.previewUrl));
    attachments = [];
  }

  // ---------- image attachments ----------
  function addFiles(list: FileList | File[]): void {
    for (const f of Array.from(list)) {
      if (!f.type.startsWith('image/')) continue;
      if (attachments.length >= MAX_IMAGES) {
        toast(`Max ${MAX_IMAGES} images per message`, 'error');
        return;
      }
      if (f.size > MAX_BYTES) {
        toast(`${f.name} is ${formatBytes(f.size)} (20 MB limit)`, 'error');
        continue;
      }
      const att: Attachment = {
        id: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
        name: f.name || 'pasted.png',
        size: f.size,
        previewUrl: URL.createObjectURL(f),
        status: 'uploading',
      };
      attachments.push(att);
      const proxy = attachments[attachments.length - 1];
      api
        .uploadFile(f, chat?.id)
        .then((meta) => {
          proxy.status = 'done';
          proxy.fileId = meta.id;
        })
        .catch((err: unknown) => {
          proxy.status = 'error';
          proxy.error = String((err as Error)?.message ?? 'upload failed');
          toast(`Upload failed: ${proxy.error}`, 'error');
        });
    }
  }

  function removeAttachment(a: Attachment): void {
    URL.revokeObjectURL(a.previewUrl);
    const i = attachments.indexOf(a);
    if (i >= 0) attachments.splice(i, 1);
  }

  function onPaste(e: ClipboardEvent): void {
    const files = e.clipboardData?.files;
    if (files && files.length && Array.from(files).some((f) => f.type.startsWith('image/'))) {
      e.preventDefault();
      addFiles(files);
    }
  }

  function onDrop(e: DragEvent): void {
    dragging = false;
    if (e.dataTransfer?.files?.length) {
      e.preventDefault();
      addFiles(e.dataTransfer.files);
    }
  }

  $effect(
    () => () => {
      attachments.forEach((a) => URL.revokeObjectURL(a.previewUrl));
      stopMic(true);
      clearEscArm();
    },
  );

  // ---------- mic (Web Speech API; hidden when unsupported) ----------
  interface SRResultLike {
    isFinal: boolean;
    0: { transcript: string };
  }
  interface SRLike {
    lang: string;
    continuous: boolean;
    interimResults: boolean;
    start(): void;
    stop(): void;
    abort(): void;
    onresult: ((e: { resultIndex: number; results: { length: number; [i: number]: SRResultLike } }) => void) | null;
    onend: (() => void) | null;
    onerror: ((e: { error?: string }) => void) | null;
  }
  type SRCtor = new () => SRLike;
  const SR: SRCtor | undefined =
    typeof window === 'undefined'
      ? undefined
      : (
          window as unknown as {
            SpeechRecognition?: SRCtor;
            webkitSpeechRecognition?: SRCtor;
          }
        ).SpeechRecognition ??
        (window as unknown as { webkitSpeechRecognition?: SRCtor }).webkitSpeechRecognition;

  let micOn = $state(false);
  let rec: SRLike | null = null;
  let micStopRequested = false;
  let micRetry: ReturnType<typeof setTimeout> | undefined;

  function startMic(): void {
    if (!SR || micOn) return;
    const r = new SR();
    r.lang = navigator.language || 'en-US';
    r.continuous = true;
    r.interimResults = true;
    const base = value.trim();
    let finals = '';
    let interim = '';
    micStopRequested = false;
    r.onresult = (e) => {
      for (let i = e.resultIndex; i < e.results.length; i++) {
        const res = e.results[i];
        if (res.isFinal) finals += res[0].transcript + ' ';
        else interim = res[0].transcript;
      }
      const spoken = `${finals}${interim}`.replace(/\s+/g, ' ').trim();
      value = base ? `${base} ${spoken}`.replace(/\s+/g, ' ') : spoken;
    };
    r.onend = () => {
      if (!micStopRequested) {
        // browsers stop recognition on silence — resume
        micRetry = setTimeout(() => {
          try {
            if (!micStopRequested) r.start();
          } catch {
            /* already started */
          }
        }, 300);
      }
    };
    r.onerror = (e) => {
      if (e?.error === 'not-allowed' || e?.error === 'service-not-allowed') {
        micStopRequested = true;
        micOn = false;
        rec = null;
        toast('Microphone permission denied', 'error');
      }
    };
    try {
      r.start();
      rec = r;
      micOn = true;
    } catch {
      toast('Could not start microphone', 'error');
    }
  }

  function stopMic(abort = false): void {
    micStopRequested = true;
    micOn = false;
    if (micRetry !== undefined) clearTimeout(micRetry);
    if (rec) {
      if (abort) rec.abort();
      else rec.stop();
      rec.onresult = null;
      rec.onend = null;
      rec.onerror = null;
      rec = null;
    }
  }

  function toggleMic(): void {
    if (micOn) stopMic();
    else startMic();
  }

  // ---------- KB collections (📚 toggle; selection on chat.params via PATCH) ----------
  // Hidden when GET /api/kb 404s (endpoint absent on this build). An OK-but-
  // empty list still shows the toggle with the "No collections yet" hint.
  let kb = $state<KbCollection[] | null>(null);
  let kbOpen = $state(false);
  let kbRoot = $state<HTMLDivElement | null>(null);
  const kbSelected = $derived(kbIdsFromChat(chat));

  $effect(() => {
    void listKbCached().then(
      (rows) => (kb = rows),
      () => (kb = null),
    );
  });

  function toggleKbCollection(cid: string): void {
    if (!chat) return;
    const sel = kbSelected.includes(cid) ? kbSelected.filter((x) => x !== cid) : [...kbSelected, cid];
    api
      .patchChat(chat.id, { params: withKbIds(chat.params, sel) })
      .then((c) => {
        onupdated?.(c);
        window.dispatchEvent(new CustomEvent('chatgo:chat-updated', { detail: { chat: c } }));
      })
      .catch((err: unknown) =>
        toast(`Knowledge collections not saved: ${String((err as Error)?.message ?? err)}`, 'error'),
      );
  }

  function gotoKnowledge(e: MouseEvent): void {
    e.preventDefault();
    kbOpen = false;
    navigate('/knowledge');
  }

  $effect(() => {
    if (!kbOpen) return;
    const onDoc = (e: MouseEvent) => {
      if (kbRoot && !kbRoot.contains(e.target as Node)) kbOpen = false;
    };
    const onKey = (e: KeyboardEvent) => {
      // don't steal Esc while the slash menu is being driven from the textarea
      if (e.key === 'Escape' && !(e.target instanceof HTMLTextAreaElement)) {
        e.stopPropagation();
        kbOpen = false;
      }
    };
    document.addEventListener('click', onDoc, true);
    document.addEventListener('keydown', onKey, true);
    return () => {
      document.removeEventListener('click', onDoc, true);
      document.removeEventListener('keydown', onKey, true);
    };
  });

  // ---------- slash prompts ('/' popover over GET /api/prompts) ----------
  let prompts = $state<PromptEntry[]>([]);
  let promptsFetched = false;
  let promptsLoading = $state(false);
  let promptsAvailable = $state(true); // 404 → false: never show the popover
  let slashOpen = $state(false);
  let slashQuery = $state('');
  let slashIndex = $state(0);
  let slashDismissed = $state<string | null>(null); // value when Esc closed it

  const slashFiltered = $derived(filterPrompts(prompts, slashQuery));
  $effect(() => {
    void slashFiltered.length;
    slashIndex = 0;
  });

  function loadPrompts(): void {
    if (promptsFetched || promptsLoading || !promptsAvailable) return;
    promptsLoading = true;
    listPromptsCached().then(
      (rows) => {
        prompts = rows;
        promptsFetched = true;
      },
      (err: unknown) => {
        if ((err as { status?: number })?.status === 404) promptsAvailable = false;
        slashOpen = false;
      },
    ).finally(() => (promptsLoading = false));
  }

  /** Recompute popover visibility from value + caret (single-line guard). */
  function refreshSlash(): void {
    const v = value;
    if (slashDismissed !== null && !v.startsWith(slashDismissed)) slashDismissed = null;
    const caret = ta ? ta.selectionStart : v.length;
    const ok =
      promptsAvailable &&
      v.startsWith('/') &&
      !v.slice(0, caret).includes('\n') &&
      slashDismissed !== v;
    if (!ok) {
      slashOpen = false;
      return;
    }
    slashQuery = v.slice(1, caret);
    loadPrompts();
    slashOpen = true;
  }

  function pickPrompt(p: PromptEntry): void {
    value = p.content;
    slashOpen = false;
    slashDismissed = null;
    requestAnimationFrame(() => {
      ta?.focus();
      ta?.setSelectionRange(value.length, value.length);
    });
  }

  /** Returns true when the key belongs to the slash popover. */
  function handleSlashKey(e: KeyboardEvent): boolean {
    const n = slashFiltered.length;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (n) slashIndex = (slashIndex + (e.key === 'ArrowDown' ? 1 : n - 1)) % n;
      return true;
    }
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      slashDismissed = value;
      slashOpen = false;
      return true;
    }
    if (e.key === 'Tab' && n) {
      e.preventDefault();
      pickPrompt(slashFiltered[slashIndex]);
      return true;
    }
    if (e.key === 'Enter' && n && !e.shiftKey && !e.metaKey && !e.ctrlKey) {
      e.preventDefault();
      pickPrompt(slashFiltered[slashIndex]);
      return true;
    }
    return false;
  }
</script>

<div style="background:var(--bg)" aria-label="Message composer">
  <div class="mx-auto w-full max-w-[48rem] px-4 pb-2.5 pt-1">
    {#if queue.length}
      <!-- queued sends (Enter mid-stream) — auto-fired in order when the
           current stream settles; each item removable -->
      <div class="mb-1.5 flex flex-col gap-1" role="list" aria-label="Queued messages">
        <p class="px-1.5 text-[11px] font-medium uppercase tracking-wide" style="color:var(--faint, #6e6e6e)">
          Queued · {queue.length}
        </p>
        {#each queue as item, i (i)}
          <div class="qrow" role="listitem">
            <span class="min-w-0 flex-1 truncate text-[13px]" style="color:var(--text)">{item.content}</span>
            <button
              type="button"
              class="attx"
              title="Remove from queue"
              aria-label="Remove queued message"
              onclick={() => onremovequeued?.(i)}
            >
              <X size={12} />
            </button>
          </div>
        {/each}
      </div>
    {/if}
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_no_static_element_interactions -->
    <div
      class="pill"
      class:dragging
      class:temp={temporary}
      ondragover={(e) => {
        e.preventDefault();
        dragging = true;
      }}
      ondragleave={() => (dragging = false)}
      ondrop={onDrop}
    >
      {#if attachments.length}
        <!-- attachment chips inside the pill, above the textarea (§6) -->
        <div class="mb-1.5 flex flex-col items-start gap-1.5">
          {#each attachments as a (a.id)}
            <div class="att">
              <img src={a.previewUrl} alt={a.name} />
              <span class="attname">{a.name}</span>
              {#if a.status === 'uploading'}
                <LoaderCircle size={13} class="animate-spin" style="color:var(--muted); flex:none" />
              {:else if a.status === 'error'}
                <span class="text-[11px] font-bold" style="color:var(--danger); flex:none">!</span>
              {/if}
              <button type="button" class="attx" title="Remove" aria-label="Remove {a.name}" onclick={() => removeAttachment(a)}>
                <X size={12} />
              </button>
            </div>
          {/each}
        </div>
      {/if}

      {#if slashOpen}
        <SlashPrompts
          items={slashFiltered}
          active={slashIndex}
          loading={promptsLoading}
          onpick={pickPrompt}
          onhover={(i) => (slashIndex = i)}
        />
      {/if}

      <textarea
        bind:this={ta}
        bind:value={value}
        rows="1"
        placeholder={busy ? 'Queue a message…' : 'Ask anything'}
        class="w-full resize-none overflow-y-auto bg-transparent px-1 py-0.5 text-base leading-[1.6] outline-none"
        style="color:var(--text)"
        aria-label="Message"
        onkeydown={onKeydown}
        oninput={() => queueMicrotask(refreshSlash)}
        onkeyup={onKeyup}
        onblur={() => (slashOpen = false)}
        onpaste={onPaste}></textarea>

      <!-- inner bottom row: attach · search · model … mic · send/stop (§6) -->
      <div class="mt-0.5 flex items-center gap-0.5">
        <input
          bind:this={fileInput}
          type="file"
          accept="image/png,image/jpeg,image/webp,image/gif"
          multiple
          class="hidden"
          onchange={(e) => {
            const inp = e.currentTarget;
            if (inp.files?.length) addFiles(inp.files);
            inp.value = '';
          }}
        />
        <IconButton title="Attach images (max 4, 20 MB)" onclick={() => fileInput?.click()}>
          <Plus size={16} />
        </IconButton>
        <!-- model picker: compact trigger anchored at the footer's bottom-left -->
        <ModelPicker {chat} {onupdated} />
        {#if capabilities.search}
          <IconButton title="Web search (SearXNG)" active={searchOn} onclick={() => (searchOn = !searchOn)}>
            <span class="globe">
              <Globe size={15} />
              {#if searchOn}<span class="dot" aria-hidden="true"></span>{/if}
            </span>
          </IconButton>
        {/if}
        {#if kb !== null}
          <!-- 📚 KB collections — multi-select persisted on chat.params -->
          <div bind:this={kbRoot} class="relative">
            <IconButton
              title="Knowledge collections"
              active={kbSelected.length > 0}
              onclick={() => (kbOpen = !kbOpen)}
            >
              <span class="globe">
                <BookOpen size={15} />
                {#if kbSelected.length}<span class="dot" aria-hidden="true"></span>{/if}
              </span>
            </IconButton>
            {#if kbOpen}
              <div class="kbpop" role="listbox" aria-label="Knowledge collections" aria-multiselectable="true">
                <div class="max-h-64 overflow-y-auto px-1 pt-1.5 pb-1">
                  {#each kb as c (c.id)}
                    <button
                      type="button"
                      class="kbrow"
                      class:sel={kbSelected.includes(c.id)}
                      role="option"
                      aria-selected={kbSelected.includes(c.id)}
                      onclick={() => toggleKbCollection(c.id)}
                    >
                      <span class="min-w-0 flex-1 truncate">{c.name || 'Untitled'}</span>
                      {#if kbSelected.includes(c.id)}
                        <Check size={14} style="flex:none; color:var(--acc, var(--accent))" />
                      {/if}
                    </button>
                  {:else}
                    <p class="px-3 py-3 text-xs" style="color:var(--muted)">No collections yet</p>
                  {/each}
                </div>
                <div class="kbfoot">
                  <a href="/knowledge" onclick={gotoKnowledge}>Manage knowledge →</a>
                </div>
              </div>
            {/if}
          </div>
        {/if}
        <div class="ml-auto flex items-center gap-1">
          <!-- temporary-chat switch (OWUI style): dashed pill + badge while on;
               ChatView owns the state and deletes the chat on leave -->
          <button
            type="button"
            role="switch"
            aria-checked={temporary}
            class="tempbtn"
            class:on={temporary}
            title="Temporary chat — deleted when you leave"
            onclick={() => ontemporary?.(!temporary)}
          >
            <span class="temptrack" aria-hidden="true"><span class="tempthumb"></span></span>
            {#if temporary}<span class="tempbadge">Temporary</span>{/if}
          </button>
          {#if SR}
            <IconButton title={micOn ? 'Stop dictation' : 'Dictate (Web Speech)'} active={micOn} onclick={toggleMic}>
              {#if micOn}<MicOff size={15} />{:else}<Mic size={15} />{/if}
            </IconButton>
          {/if}
          <!-- stop circle appears mid-stream; the send circle keeps its
               up-arrow and queues while busy -->
          {#if busy}
            <button type="button" class="stopbtn" title="Stop generating" aria-label="Stop generating" onclick={onstop}>
              <Square size={11} fill="currentColor" />
            </button>
          {/if}
          <button
            type="button"
            class="send"
            title={busy ? 'Queue message' : 'Send'}
            aria-label={busy ? 'Queue message' : 'Send message'}
            disabled={!canSend}
            onclick={doSend}
          >
            <ArrowUp size={16} />
          </button>
        </div>
      </div>
    </div>

    <p class="mt-1.5 px-1 text-[11px]" style="color:var(--faint, #6e6e6e)">
      {#if escHint}
        <!-- armed Esc: accent chip swaps the hint strip (auto-dismiss ~2.5s) -->
        <span class="eschip" role="status">Esc again to stop generating</span>
      {:else if busy}
        {sendEnter ? '↵ queue · ⇧↵ newline · ' : '⇧↵ newline · ⌘↵ queue · '}Esc stop · / focus
      {:else}
        {sendEnter ? '↵ send · ⇧↵ newline · ' : '⇧↵ send · '}⌘↵ send · / focus · ↑ edit · ? help
      {/if}
    </p>
  </div>
</div>

<ShortcutsHelp open={shortcutsOpen} onclose={() => (shortcutsOpen = false)} />

<style>
  /* OWUI composer container: rounded-3xl, --panel fill, backdrop-blur + the
     near-invisible hairline border (light black/8%, dark white/6%). Blur needs
     a translucent fill to read, so the bg keeps --panel but lets 6% through. */
  .pill {
    position: relative; /* anchors the slash-prompt popover */
    border-radius: 1.5rem; /* rounded-3xl */
    border: 1px solid rgb(0 0 0 / 0.08);
    padding: 10px 12px 8px;
    background: color-mix(in srgb, var(--panel, #2f2f2f) 94%, transparent);
    backdrop-filter: blur(12px);
    -webkit-backdrop-filter: blur(12px);
    box-shadow: var(--shadow-composer, 0 2px 12px rgb(0 0 0 / 0.25));
    transition: background 120ms ease;
  }
  :global(html.dark) .pill {
    border-color: rgb(255 255 255 / 0.06);
  }
  .pill:focus-within {
    /* slightly brighter on focus — no ring glow (§6) */
    background: color-mix(in srgb, var(--panel, #2f2f2f) 97%, white);
  }
  .pill.dragging {
    outline: 2px dashed color-mix(in srgb, var(--accent, #ffffff) 45%, transparent);
    outline-offset: 2px;
  }
  /* temporary chat (OWUI): dashed frame while the mode is on */
  .pill.temp {
    border-style: dashed;
    border-color: color-mix(in srgb, var(--muted, #a8a8a8) 45%, transparent);
  }

  /* queued-send row: compact --panel-2 line above the composer */
  .qrow {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    border-radius: 0.625rem;
    background: var(--panel-2, #3a3a3a);
    padding: 0.3rem 0.4rem 0.3rem 0.65rem;
  }

  /* attachment chip: rounded-lg --panel-2 row, 28px thumb (§6) */
  .att {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    max-width: 100%;
    border-radius: 0.625rem;
    background: var(--panel-2, #3a3a3a);
    padding: 0.35rem 0.4rem 0.35rem 0.35rem;
  }
  .att img {
    width: 28px;
    height: 28px;
    flex: none;
    border-radius: 0.375rem;
    object-fit: cover;
    display: block;
  }
  .attname {
    min-width: 0;
    max-width: 16rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.8125rem;
    color: var(--text);
  }
  .attx {
    display: inline-flex;
    flex: none;
    padding: 0.2rem;
    border: 0;
    border-radius: 999px;
    background: transparent;
    color: var(--muted, #a8a8a8);
    cursor: pointer;
    transition:
      color 120ms ease,
      background 120ms ease;
  }
  .attx:hover {
    color: var(--text);
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
  }

  /* web-search toggle: active state gets a tiny accent dot (§6) */
  .globe {
    position: relative;
    display: inline-flex;
  }
  .dot {
    position: absolute;
    right: -2px;
    bottom: -2px;
    width: 4px;
    height: 4px;
    border-radius: 999px;
    background: var(--acc, var(--accent, #ffffff));
  }

  /* KB collections popover: ModelPicker menu recipe (--panel r14 shadow-menu) */
  .kbpop {
    position: absolute;
    bottom: calc(100% + 8px);
    left: 0;
    z-index: 30;
    width: 18rem;
    min-width: 14rem;
    border-radius: 0.875rem;
    border: 0;
    background: var(--panel, #2f2f2f);
    box-shadow: var(--shadow-menu, 0 8px 28px rgb(0 0 0 / 0.45));
    overflow: hidden;
    animation: kb-pop 90ms ease-out;
  }
  .kbrow {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    padding: 0.4rem 0.65rem;
    border: 0;
    border-radius: 0.625rem;
    background: transparent;
    color: var(--text);
    font-size: 0.8125rem;
    text-align: left;
    cursor: pointer;
    transition: background 120ms ease;
  }
  .kbrow:hover,
  .kbrow.sel {
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
  }
  .kbfoot {
    border-top: 1px solid var(--border, rgb(255 255 255 / 0.1));
    padding: 0.4rem 0.75rem;
  }
  .kbfoot a {
    font-size: 0.75rem;
    color: var(--acc, var(--accent, #ffffff));
    text-decoration: none;
  }
  .kbfoot a:hover {
    text-decoration: underline;
  }
  @keyframes kb-pop {
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
    .kbpop {
      animation: none;
    }
  }

  /* 32px send/stop circle: accent gradient, near-black glyph, 25% when
     disabled; hover brightens with an --acc-strong glow (FE-COLOR) */
  .send {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 2rem;
    height: 2rem;
    border-radius: 999px;
    border: 0;
    background: var(--acc-grad, var(--accent, #ffffff));
    color: var(--accent-fg, #0d0d0d);
    cursor: pointer;
    transition:
      opacity 120ms ease,
      filter 120ms ease,
      box-shadow 120ms ease;
  }
  .send:disabled {
    opacity: 0.25;
    cursor: not-allowed;
  }
  .send:hover:not(:disabled) {
    filter: brightness(1.08);
    box-shadow: 0 0 12px color-mix(in srgb, var(--acc-strong, var(--accent)) 55%, transparent);
  }

  /* mid-stream stop circle: quiet --panel-2 sibling left of send */
  .stopbtn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 2rem;
    height: 2rem;
    border-radius: 999px;
    border: 0;
    background: var(--panel-2, #3a3a3a);
    color: var(--text);
    cursor: pointer;
    transition:
      background 120ms ease,
      color 120ms ease;
  }
  .stopbtn:hover {
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
    color: var(--accent, #ffffff);
  }

  /* temporary-chat switch (footer): mini track + on-state accent badge */
  .tempbtn {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    height: 1.75rem;
    padding: 0 0.45rem;
    border: 0;
    border-radius: 999px;
    background: transparent;
    color: var(--muted, #a8a8a8);
    font-size: 0.75rem;
    cursor: pointer;
    transition:
      background 120ms ease,
      color 120ms ease;
  }
  .tempbtn:hover {
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
    color: var(--text);
  }
  .tempbtn:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--acc, var(--accent)) 60%, transparent);
    outline-offset: 1px;
  }
  .tempbtn.on {
    color: var(--acc, var(--accent, #ffffff));
  }
  .temptrack {
    display: inline-flex;
    align-items: center;
    width: 1.55rem;
    height: 0.9rem;
    flex: none;
    border-radius: 999px;
    background: var(--panel-2, #3a3a3a);
    padding: 0 2px;
    transition: background 150ms ease;
  }
  .tempbtn.on .temptrack {
    background: var(--acc, var(--accent, #ffffff));
  }
  .tempthumb {
    width: 0.7rem;
    height: 0.7rem;
    border-radius: 999px;
    background: #fff;
    box-shadow: 0 1px 2px rgb(0 0 0 / 0.4);
    transition: transform 150ms ease;
  }
  .tempbtn.on .tempthumb {
    transform: translateX(0.6rem);
  }
  .tempbadge {
    padding: 0.05rem 0.45rem;
    border-radius: 999px;
    background: color-mix(in srgb, var(--acc, var(--accent, #ffffff)) 16%, transparent);
    color: var(--acc, var(--accent, #ffffff));
    font-size: 0.6875rem;
    font-weight: 600;
    letter-spacing: 0.02em;
  }

  /* armed-Esc hint chip (quiet accent fill, pill, swaps in on the hint strip) */
  .eschip {
    display: inline-block;
    padding: 0.05rem 0.5rem;
    border-radius: 999px;
    background: color-mix(in srgb, var(--acc, var(--accent, #ffffff)) 16%, transparent);
    color: var(--acc, var(--accent, #ffffff));
    animation: esc-hint-in 120ms ease-out;
  }
  @keyframes esc-hint-in {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .eschip {
      animation: none;
    }
  }
</style>
