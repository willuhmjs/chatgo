<script lang="ts">
  // Composer (docs/DESIGN-POLISH.md §6 — "THE ChatGPT moment"): --panel pill,
  // radius 26, soft shadow, transparent autosize textarea, inner bottom row
  // with [attach] [search] [model picker] … [mic] [send/stop]. Send morphs
  // in place into the stop circle while streaming. Enter/⌘Enter semantics
  // keep the chatgo-send-enter pref; image attach via paste/drag/clip →
  // /api/files; Web Speech mic; stop wired to the existing abort path.
  import type { Chat, SendMessageBody } from '$lib/api';
  import * as api from '$lib/api';
  import ModelPicker from './ModelPicker.svelte';
  import IconButton from './ui/IconButton.svelte';
  import { capabilities } from './state.svelte.js';
  import { prefersEnterSend, formatBytes } from '$lib/chatview-api';
  import { onShortcut } from '$lib/shortcuts.svelte.js';
  import { push as toast } from '$lib/toast.svelte.js';
  import Plus from 'lucide-svelte/icons/plus';
  import Mic from 'lucide-svelte/icons/mic';
  import MicOff from 'lucide-svelte/icons/mic-off';
  import Globe from 'lucide-svelte/icons/globe';
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
    onsend,
    onstop,
    onupdated,
  }: {
    chat: Chat | null | undefined;
    busy?: boolean;
    onsend: (body: SendMessageBody) => void;
    onstop: () => void;
    onupdated?: (c: Chat) => void;
  } = $props();

  let value = $state('');
  let attachments = $state<Attachment[]>([]);
  let dragging = $state(false);
  let searchOn = $state(false);
  let sendEnter = $state(prefersEnterSend());
  let ta = $state<HTMLTextAreaElement | null>(null);
  let fileInput = $state<HTMLInputElement | null>(null);

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
    return () => {
      offSend();
      offFocus();
    };
  });

  function onKeydown(e: KeyboardEvent): void {
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

  function doSend(): void {
    const text = value.trim();
    if (!text || busy) return;
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
</script>

<div style="background:var(--bg)" aria-label="Message composer">
  <div class="mx-auto w-full max-w-[48rem] px-4 pb-2.5 pt-1">
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_no_static_element_interactions -->
    <div
      class="pill"
      class:dragging
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

      <textarea
        bind:this={ta}
        bind:value={value}
        rows="1"
        placeholder="Ask anything"
        class="w-full resize-none overflow-y-auto bg-transparent px-1 py-0.5 text-base leading-[1.6] outline-none"
        style="color:var(--text)"
        aria-label="Message"
        onkeydown={onKeydown}
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
        {#if capabilities.search}
          <IconButton title="Web search (SearXNG)" active={searchOn} onclick={() => (searchOn = !searchOn)}>
            <span class="globe">
              <Globe size={15} />
              {#if searchOn}<span class="dot" aria-hidden="true"></span>{/if}
            </span>
          </IconButton>
        {/if}
        <ModelPicker {chat} {onupdated} />

        <div class="ml-auto flex items-center gap-1">
          {#if SR}
            <IconButton title={micOn ? 'Stop dictation' : 'Dictate (Web Speech)'} active={micOn} onclick={toggleMic}>
              {#if micOn}<MicOff size={15} />{:else}<Mic size={15} />{/if}
            </IconButton>
          {/if}
          <!-- send morphs IN PLACE into the stop circle while streaming -->
          {#if busy}
            <button type="button" class="send" title="Stop generating" aria-label="Stop generating" onclick={onstop}>
              <Square size={11} fill="currentColor" />
            </button>
          {:else}
            <button type="button" class="send" title="Send" aria-label="Send message" disabled={!canSend} onclick={doSend}>
              <ArrowUp size={16} />
            </button>
          {/if}
        </div>
      </div>
    </div>

    <p class="mt-1.5 px-1 text-[11px]" style="color:var(--faint, #6e6e6e)">
      {sendEnter ? '↵ send · ⇧↵ newline · ' : '⇧↵ send · '}⌘↵ send · / focus
    </p>
  </div>
</div>

<style>
  /* the ChatGPT pill: --panel, radius 26, soft shadow (§1/§6), plus the OWUI
     integration pass (docs/DESIGN-REF.md §2): backdrop-blur-md + near-invisible
     hairline border (light black/8%, dark white/6%). Blur needs a translucent
     fill to read, so the bg keeps --panel but lets 6% through. */
  .pill {
    border-radius: 26px;
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

  /* web-search toggle: active state gets a tiny white dot (§6) */
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
    background: var(--accent, #ffffff);
  }

  /* 32px send/stop circle: white fill, near-black glyph, 25% when disabled */
  .send {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 2rem;
    height: 2rem;
    border-radius: 999px;
    border: 0;
    background: var(--accent, #ffffff);
    color: var(--accent-fg, #0d0d0d);
    cursor: pointer;
    transition: opacity 120ms ease;
  }
  .send:disabled {
    opacity: 0.25;
    cursor: not-allowed;
  }
  .send:hover:not(:disabled) {
    opacity: 0.85;
  }
</style>
