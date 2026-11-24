<script lang="ts">
  // "Install ChatGo" nudge (Open WebUI PWA parity). Chrome/Edge/Android fire
  // beforeinstallprompt — we stash the deferred prompt and offer one-click
  // install. iOS Safari never fires it, so it gets the one-line recipe
  // instead. A single dismissal is remembered forever; an installed app
  // (standalone display-mode) never sees any of this.
  import { brand, brandName } from '$lib/brand.svelte.js';
  import { Download, X } from 'lucide-svelte';

  const KEY = 'chatgo:install-dismissed';

  interface BeforeInstallPromptEvent extends Event {
    prompt: () => Promise<void>;
    userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>;
  }

  let deferred = $state<BeforeInstallPromptEvent | null>(null);
  let iosHint = $state(false);
  let dismissed = $state(
    (() => {
      try {
        return localStorage.getItem(KEY) === '1';
      } catch {
        return false;
      }
    })(),
  );
  let installed = $state(false);

  const standalone = () =>
    (typeof window.matchMedia === 'function' && window.matchMedia('(display-mode: standalone)').matches) ||
    (navigator as unknown as { standalone?: boolean }).standalone === true;

  $effect(() => {
    if (standalone()) {
      installed = true;
      return;
    }
    const onBip = (e: Event) => {
      e.preventDefault();
      deferred = e as BeforeInstallPromptEvent;
    };
    window.addEventListener('beforeinstallprompt', onBip);
    // iOS heuristic: WebKit touch device, not standalone, and the install
    // event never arrives (it never fires on iOS — the 2.5s timer is cheap
    // insurance against desktop-Safari-like iPads claiming to be touchy).
    const ios = /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
    const t = ios && !/CriOS|FxiOS/.test(navigator.userAgent) ? setTimeout(() => (iosHint = true), 2500) : 0;
    return () => {
      window.removeEventListener('beforeinstallprompt', onBip);
      if (t) clearTimeout(t);
    };
  });

  function dismiss() {
    dismissed = true;
    try {
      localStorage.setItem(KEY, '1');
    } catch {
      /* private mode — stays visible this session only */
    }
  }

  async function install() {
    if (!deferred) return;
    const ev = deferred;
    deferred = null;
    await ev.prompt();
    const choice = await ev.userChoice;
    if (choice.outcome === 'accepted') dismissed = true;
  }

  const show = $derived(!dismissed && !installed && (!!deferred || iosHint));
</script>

{#if show}
  <div class="card" role="dialog" aria-label="Install ChatGo">
    <img src="/icon.svg" alt="" class="size-8 rounded-lg" />
    <div class="min-w-0 flex-1">
      <p class="truncate text-[13px] font-medium">Install {brand.title || brandName()}</p>
      <p class="text-[12px]" style="color:var(--muted)">
        {#if deferred}Add it to your dock — opens offline-ready.{:else}Share → “Add to Home Screen” to install.{/if}
      </p>
    </div>
    {#if deferred}
      <button
        type="button"
        class="install"
        onclick={() => void install()}><Download size={13} /> Install</button
    >
    {/if}
    <button type="button" class="x" aria-label="Dismiss" onclick={dismiss}><X size={14} /></button>
  </div>
{/if}

<style>
  .card {
    position: fixed;
    right: 1rem;
    bottom: max(1rem, env(safe-area-inset-bottom));
    z-index: 50;
    display: flex;
    max-width: min(22rem, calc(100vw - 2rem));
    align-items: center;
    gap: 0.65rem;
    border-radius: 14px;
    border: 1px solid var(--border);
    background: var(--panel);
    padding: 0.7rem 0.8rem;
    box-shadow: 0 12px 36px rgb(0 0 0 / 0.35);
    animation: rise 260ms ease-out;
  }
  .install {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    border: 0;
    border-radius: 999px;
    background: var(--accent);
    color: var(--accent-fg);
    font-size: 12px;
    font-weight: 500;
    padding: 0.35rem 0.7rem;
    cursor: pointer;
    white-space: nowrap;
  }
  .install:hover {
    filter: brightness(1.08);
  }
  .x {
    border: 0;
    background: transparent;
    color: var(--faint);
    cursor: pointer;
    padding: 0.2rem;
    border-radius: 999px;
  }
  .x:hover {
    color: var(--text);
  }
  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(8px);
    }
  }
</style>
