<script lang="ts">
  // Sticky-bottom composer: auto-grow textarea, Enter/⌘Enter semantics with
  // chatgo-send-enter pref, model pill, search toggle (capability-gated),
  // image attach (paste/drag/clip → /api/files), Web Speech mic, stop.
  import type { Chat, SendMessageBody } from '$lib/api';
  import * as api from '$lib/api';
  import ModelPicker from './ModelPicker.svelte';
  import IconButton from './ui/IconButton.svelte';
  import { capabilities } from './state.svelte.js';
  import { prefersEnterSend, formatBytes } from '$lib/chatview-api';
  import { onShortcut } from '$lib/shortcuts.svelte.js';
  import { push as toast } from '$lib/toast.svelte.js';
  import Paperclip from 'lucide-svelte/icons/paperclip';
  import Mic from 'lucide-svelte/icons/mic';
  import MicOff from 'lucide-svelte/icons/mic-off';
  import Globe from 'lucide-svelte/icons/globe';
  import Send from 'lucide-svelte/icons/send';
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

<div
  class="border-t"
  style="background:var(--bg); border-color:var(--border)"
  aria-label="Message composer"
>
  <div class="mx-auto w-full max-w-[48rem] px-4 pb-3 pt-2.5">
    {#if attachments.length}
      <div class="mb-2 flex flex-wrap gap-2">
        {#each attachments as a (a.id)}
          <div class="thumbgroup">
            <img src={a.previewUrl} alt={a.name} />
            {#if a.status === 'uploading'}
              <div class="thumbstatus"><LoaderCircle size={16} class="animate-spin" /></div>
            {:else if a.status === 'error'}
              <div class="thumbstatus error"><span class="text-xs font-bold">!</span></div>
            {/if}
            <button type="button" class="thumbx" title="Remove" aria-label="Remove {a.name}" onclick={() => removeAttachment(a)}>
              <X size={11} />
            </button>
          </div>
        {/each}
      </div>
    {/if}

    <!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_no_static_element_interactions -->
    <div
      class="box"
      class:dragging
      ondragover={(e) => {
        e.preventDefault();
        dragging = true;
      }}
      ondragleave={() => (dragging = false)}
      ondrop={onDrop}
    >
      <div class="flex items-end gap-1 px-2 py-1.5">
        <textarea
          bind:this={ta}
          bind:value={value}
          rows="1"
          placeholder={busy ? 'Generating…' : 'Message ChatGo…'}
          class="max-h-[13.75rem] flex-1 resize-none overflow-y-auto bg-transparent px-1.5 py-1.5 text-[0.9375rem] leading-[1.5] outline-none"
          style="color:var(--text)"
          aria-label="Message"
          onkeydown={onKeydown}
          onpaste={onPaste}></textarea>

        <div class="flex items-center gap-0.5 pb-0.5">
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
            <Paperclip size={15} />
          </IconButton>
          {#if SR}
            <IconButton title={micOn ? 'Stop dictation' : 'Dictate (Web Speech)'} active={micOn} onclick={toggleMic}>
              {#if micOn}<MicOff size={15} />{:else}<Mic size={15} />{/if}
            </IconButton>
          {/if}
          {#if capabilities.search}
            <IconButton title="Web search (SearXNG)" active={searchOn} onclick={() => (searchOn = !searchOn)}>
              <Globe size={15} />
            </IconButton>
          {/if}
          {#if busy}
            <button type="button" class="send stop" title="Stop generating" aria-label="Stop generating" onclick={onstop}>
              <Square size={13} />
            </button>
          {:else}
            <button type="button" class="send" title="Send" aria-label="Send message" disabled={!canSend} onclick={doSend}>
              <Send size={14} />
            </button>
          {/if}
        </div>
      </div>
    </div>

    <div class="mt-2 flex items-center gap-3">
      <ModelPicker {chat} {onupdated} />
      <span class="ml-auto hidden text-[11px] sm:block" style="color:var(--muted)">
        {sendEnter ? '↵ send · ⇧↵ newline · ' : '⇧↵ send · '}⌘↵ send · / focus
      </span>
    </div>
  </div>
</div>

<style>
  .box {
    border-radius: 1.1rem;
    border: 1px solid var(--border);
    background: var(--panel);
    transition:
      border-color 120ms ease,
      box-shadow 120ms ease;
  }
  .box:focus-within {
    border-color: color-mix(in oklab, var(--accent) 55%, var(--border));
    box-shadow: 0 0 0 3px color-mix(in oklab, var(--accent) 12%, transparent);
  }
  .box.dragging {
    border-style: dashed;
    border-color: var(--accent);
  }
  .send {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 2rem;
    height: 2rem;
    border-radius: 0.75rem;
    border: 0;
    background: var(--accent);
    color: var(--accent-fg);
    cursor: pointer;
    transition:
      opacity 120ms ease,
      transform 120ms ease;
  }
  .send:disabled {
    opacity: 0.35;
    cursor: not-allowed;
  }
  .send:hover:not(:disabled) {
    transform: translateY(-1px);
  }
  .send.stop {
    background: var(--panel-2);
    color: var(--danger);
    border: 1px solid color-mix(in oklab, var(--danger) 45%, var(--border));
  }
  .thumbgroup {
    position: relative;
    width: 60px;
    height: 60px;
    border-radius: 0.625rem;
    overflow: hidden;
    border: 1px solid var(--border);
    background: var(--panel-2);
  }
  .thumbgroup img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    display: block;
  }
  .thumbstatus {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgb(0 0 0 / 0.45);
    color: #fff;
  }
  .thumbstatus.error {
    background: color-mix(in oklab, var(--danger) 65%, black);
  }
  .thumbx {
    position: absolute;
    top: 2px;
    right: 2px;
    display: flex;
    padding: 2px;
    border-radius: 0.375rem;
    border: 0;
    background: rgb(0 0 0 / 0.6);
    color: #fff;
    cursor: pointer;
  }
</style>
