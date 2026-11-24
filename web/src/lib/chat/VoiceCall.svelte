<script lang="ts">
  // Voice call mode (Open WebUI's signature feature, stdlib-free): a
  // hands-free loop of dictation (Web Speech API) → chat send (the same SSE
  // pipeline as typing) → read-aloud (speechSynthesis) → dictation again.
  // Everything runs in the browser — zero extra server work beyond a normal
  // chat turn, so RAM cost is nil. Tapping the orb while the model speaks
  // barges in (cancels TTS and listens). Unsupported browsers get an honest
  // message instead of a dead button.
  import * as api from '$lib/api';
  import { send as sseSend, abort as sseAbort } from '$lib/sse.svelte.js';  import { stripForTTS } from '$lib/chatview-api';
  import { models, loadModels, findModel, isLikelyChatModel } from '$lib/chat/state.svelte.js';
  import { modelPrefs } from '$lib/chat/model-prefs.svelte.js';
  import { getHomeModel } from '$lib/api-shell';
  import { push } from '$lib/toast.svelte.js';
  import { PhoneOff, Mic, MicOff, Volume2 } from 'lucide-svelte';

  let { onclose }: { onclose: () => void } = $props();

  type SRtor = new () => {
    lang: string;
    continuous: boolean;
    interimResults: boolean;
    maxAlternatives: number;
    onresult: ((e: any) => void) | null;
    onerror: ((e: any) => void) | null;
    onend: (() => void) | null;
    start: () => void;
    stop: () => void;
    abort: () => void;
  };
  const SR: SRtor | null =
    typeof window === 'undefined'
      ? null
      : ((window as unknown as { SpeechRecognition?: SRtor }).SpeechRecognition ??
          (window as unknown as { webkitSpeechRecognition?: SRtor }).webkitSpeechRecognition ??
          null);
  const ttsOn = typeof window !== 'undefined' && 'speechSynthesis' in window;

  /** 'idle' = booting, 'listening' = mic open, 'thinking' = waiting on the
   *  model, 'speaking' = TTS out, 'paused' = hands-free loop held. */
  let phase = $state<'idle' | 'listening' | 'thinking' | 'speaking' | 'paused' | 'over'>('idle');
  let caption = $state(''); // what you said / the model is saying
  let notice = $state('');
  let modelLabel = $state('');

  let chatId = '';
  let runId = 0; // bumped by end() — invalidates in-flight callbacks
  let rec: InstanceType<SRtor> | null = null;
  let paused = $state(false);

  async function start() {
    if (!SR) {
      notice = 'This browser has no speech recognition (try Chrome, Edge or Safari).';
      phase = 'over';
      return;
    }
    phase = 'idle';
    try {
      await loadModels();
      // Model priority mirrors Home's composer: server default → device
      // pick → first chat-ish model.
      const key = modelPrefs.defaultModel || getHomeModel();
      const colon = key.indexOf(':');
      let m = colon > 0 ? findModel(key.slice(0, colon), key.slice(colon + 1)) : undefined;
      if (!m) m = models.items.find(isLikelyChatModel) ?? models.items[0];
      if (!m) {
        notice = 'No models are configured — ask an admin to add a provider.';
        phase = 'over';
        return;
      }
      modelLabel = m.display || m.model_id;
      const chat = await api.createChat({ title: `Voice call · ${new Date().toLocaleString()}`, provider_id: m.provider_id, model_id: m.model_id });
      chatId = chat.id;
      listen();
    } catch {
      notice = 'Could not start the call.';
      phase = 'over';
    }
  }
  void start();

  function listen() {
    if (paused || phase === 'over') return;
    phase = 'listening';
    caption = '';
    let finalText = '';
    try {
      rec = new SR!();
    } catch {
      notice = 'The microphone could not be opened.';
      phase = 'over';
      return;
    }
    rec.lang = navigator.language || 'en-US';
    rec.continuous = false;
    rec.interimResults = true;
    rec.maxAlternatives = 1;
    rec.onresult = (e: any) => {
      let interim = '';
      for (let i = e.resultIndex; i < e.results.length; i++) {
        const r = e.results[i];
        if (r.isFinal) finalText += r[0]?.transcript ?? '';
        else interim += r[0]?.transcript ?? '';
      }
      caption = (finalText + interim).trim();
    };
    rec.onerror = (e: any) => {
      if (e?.error === 'no-speech' || e?.error === 'aborted') return; // onend re-loops
      if (e?.error === 'not-allowed' || e?.error === 'service-not-allowed') {
        notice = 'Microphone permission was denied.';
        phase = 'over';
      }
    };
    rec.onend = () => {
      rec = null;
      if (paused || phase === 'over') return;
      if (phase !== 'listening') return; // barge-in/stop already moved on
      const text = finalText.trim();
      if (text) void think(text);
      else listen(); // silence → keep the line open
    };
    try {
      rec.start();
    } catch {
      setTimeout(listen, 400); // rare transient "already started"
    }
  }

  async function think(text: string) {
    phase = 'thinking';
    const my = runId;
    let reply = '';
    await sseSend(chatId, { content: text }, {
      token: (d: unknown) => {
        if (my !== runId) return;
        reply += (d as { content?: string })?.content ?? '';
      },
      error: (d: unknown) => {
        if (my !== runId) return;
        const msg = (d as { message?: string })?.message ?? 'The model returned an error.';
        push(msg, 'error');
        caption = msg;
        phase = 'listening';
        setTimeout(listen, 900);
      },
      done: () => {
        if (my !== runId) return;
        speak(reply.trim());
      },
    });
  }

  function speak(text: string) {
    if (!text) {
      listen();
      return;
    }
    if (!ttsOn) {
      caption = text;
      setTimeout(listen, 2200);
      return;
    }
    phase = 'speaking';
    caption = text;
    const utter = new SpeechSynthesisUtterance(stripForTTS(text));
    utter.lang = navigator.language || 'en-US';
    utter.onend = () => {
      if (phase === 'speaking') listen();
    };
    utter.onerror = () => {
      if (phase === 'speaking') listen();
    };
    window.speechSynthesis.cancel();
    window.speechSynthesis.speak(utter);
  }

  /** Tap the orb: barge-in while speaking, nudge while listening does nothing. */
  function orbTap() {
    if (phase === 'speaking') {
      window.speechSynthesis?.cancel();
      if (rec) rec.abort();
      listen();
    }
  }

  function togglePause() {
    paused = !paused;
    if (paused) {
      phase = 'paused';
      window.speechSynthesis?.cancel();
      rec?.abort();
      rec = null;
    } else {
      listen();
    }
  }

  function end() {
    runId++;
    paused = false;
    phase = 'over';
    try {
      rec?.abort();
    } catch {
      /* already stopped */
    }
    rec = null;
    window.speechSynthesis?.cancel();
    sseAbort(); // safe when nothing streams (controller is optional-chained)
    onclose();
  }

  $effect(() => () => end());

  // Esc hangs up while the overlay is up.
  $effect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        end();
      }
    };
    document.addEventListener('keydown', onKey, true);
    return () => document.removeEventListener('keydown', onKey, true);
  });

  const statusText = $derived(
    notice ||
      (phase === 'idle'
        ? 'Connecting…'
        : phase === 'listening'
          ? caption || 'Listening…'
          : phase === 'thinking'
            ? 'Thinking…'
            : phase === 'speaking'
              ? 'Tap to interrupt'
              : phase === 'paused'
                ? 'Paused'
                : 'Call ended'),
  );
</script>

<div class="voice-root" role="dialog" aria-modal="true" aria-label="Voice call">
  <div class="orb-wrap">
    <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
    <div class="orb {phase}" onclick={orbTap}></div>
    <div class="ring {phase}" aria-hidden="true"></div>
  </div>

  <p class="status">{statusText}</p>
  {#if phase !== 'over' && !notice}
    <p class="model">
      <Volume2 size={12} />
      {modelLabel}
      {#if !ttsOn}· browser read-aloud unavailable{/if}
    </p>
  {/if}

  <div class="controls">
    <button
      type="button"
      class="ctl"
      class:active={paused}
      title={paused ? 'Resume' : 'Pause'}
      aria-label={paused ? 'Resume call' : 'Pause call'}
      onclick={togglePause}
    >
      {#if paused}<MicOff size={20} />{:else}<Mic size={20} />{/if}
    </button>
    <button type="button" class="ctl end" title="End call" aria-label="End call" onclick={end}>
      <PhoneOff size={22} />
    </button>
  </div>
</div>

<style>
  .voice-root {
    position: fixed;
    inset: 0;
    z-index: 60;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 1.25rem;
    padding: 2rem;
    background: color-mix(in srgb, var(--bg-deep) 88%, transparent);
    backdrop-filter: blur(14px);
    -webkit-backdrop-filter: blur(14px);
    animation: fadein 180ms ease-out;
  }
  .orb-wrap {
    position: relative;
    width: 11rem;
    height: 11rem;
    display: grid;
    place-items: center;
  }
  .orb {
    width: 8.5rem;
    height: 8.5rem;
    border-radius: 999px;
    cursor: pointer;
    background:
      radial-gradient(circle at 32% 30%, color-mix(in srgb, var(--acc) 75%, white 12%), transparent 62%),
      conic-gradient(from 210deg, var(--acc), color-mix(in srgb, var(--acc) 40%, black 8%), var(--acc));
    box-shadow: 0 0 60px color-mix(in srgb, var(--acc) 40%, transparent);
    transition: transform 240ms ease;
  }
  .orb.listening {
    animation: breathe 1.6s ease-in-out infinite;
  }
  .orb.thinking {
    animation: spin-slow 2.6s linear infinite;
  }
  .orb.speaking {
    animation: pulse 1s ease-in-out infinite;
  }
  .orb.over,
  .orb.paused {
    filter: saturate(0.35);
    animation: none;
  }
  .ring {
    position: absolute;
    inset: 0;
    border-radius: 999px;
    border: 1.5px solid color-mix(in srgb, var(--acc) 35%, transparent);
    opacity: 0;
  }
  .ring.speaking {
    animation: ripple 1.4s ease-out infinite;
  }
  .status {
    max-width: 34rem;
    min-height: 3rem;
    text-align: center;
    font-size: 1rem;
    line-height: 1.5;
    color: var(--text);
    white-space: pre-line;
  }
  .model {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    font-size: 0.75rem;
    color: var(--muted);
  }
  .controls {
    display: flex;
    align-items: center;
    gap: 1.5rem;
    margin-top: 0.5rem;
  }
  .ctl {
    display: grid;
    place-items: center;
    width: 3.25rem;
    height: 3.25rem;
    border-radius: 999px;
    border: 1px solid var(--border);
    background: var(--panel);
    color: var(--text);
    cursor: pointer;
    transition:
      background 140ms ease,
      transform 140ms ease;
  }
  .ctl:hover {
    background: var(--fill-hover);
  }
  .ctl:active {
    transform: scale(0.94);
  }
  .ctl.active {
    color: var(--danger, #ef4444);
  }
  .ctl.end {
    background: var(--danger, #ef4444);
    border-color: transparent;
    color: #fff;
  }
  .ctl.end:hover {
    filter: brightness(1.08);
  }

  @keyframes fadein {
    from {
      opacity: 0;
    }
  }
  @keyframes breathe {
    0%,
    100% {
      transform: scale(1);
    }
    50% {
      transform: scale(1.045);
    }
  }
  @keyframes pulse {
    0%,
    100% {
      transform: scale(1);
      box-shadow: 0 0 46px color-mix(in srgb, var(--acc) 34%, transparent);
    }
    50% {
      transform: scale(1.07);
      box-shadow: 0 0 74px color-mix(in srgb, var(--acc) 52%, transparent);
    }
  }
  @keyframes spin-slow {
    to {
      transform: rotate(360deg);
    }
  }
  @keyframes ripple {
    from {
      opacity: 0.7;
      transform: scale(0.92);
    }
    to {
      opacity: 0;
      transform: scale(1.12);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .orb,
    .ring {
      animation: none;
    }
  }
</style>
