<script lang="ts">
  import Modal from './Modal.svelte';
  import Spinner from './Spinner.svelte';
  import { btn, btnDangerStyle, btnGhostStyle, btnPrimaryStyle } from './styles';

  let {
    open = $bindable(false),
    title = 'Are you sure?',
    message = '',
    confirmLabel = 'Confirm',
    danger = false,
    onconfirm,
  }: {
    open?: boolean;
    title?: string;
    message?: string;
    confirmLabel?: string;
    danger?: boolean;
    /** Resolve to close; throw to keep the dialog open (caller toasts). */
    onconfirm?: () => void | Promise<void>;
  } = $props();

  let busy = $state(false);

  async function run() {
    busy = true;
    try {
      await onconfirm?.();
      open = false;
    } catch {
      /* caller surfaces the toast; keep dialog open for retry */
    } finally {
      busy = false;
    }
  }
</script>

<Modal bind:open {title}>
  <p class="text-sm leading-relaxed text-[var(--muted)]">{message}</p>
  {#snippet footer()}
    <button class={btn} style={btnGhostStyle} onclick={() => (open = false)}>Cancel</button>
    <button
      class={btn}
      style={danger ? btnDangerStyle : btnPrimaryStyle}
      disabled={busy}
      onclick={run}
    >
      {#if busy}
        <Spinner /> Working…
      {:else}
        {confirmLabel}
      {/if}
    </button>
  {/snippet}
</Modal>
