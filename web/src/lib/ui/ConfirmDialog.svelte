<script lang="ts">
  import Modal from '$lib/ui/Modal.svelte';
  import Button from '$lib/ui/Button.svelte';
  import { confirmState, settleConfirm } from '$lib/ui/confirm.svelte.js';
</script>

<Modal
  bind:open={confirmState.open}
  title={confirmState.title}
  width="max-w-sm"
  onclose={() => settleConfirm(false)}
>
  <p class="text-sm leading-relaxed text-[var(--muted)]">{confirmState.message}</p>
  {#snippet footer()}
    <Button variant="secondary" size="sm" onclick={() => settleConfirm(false)}>Cancel</Button>
    <Button variant={confirmState.danger ? 'danger' : 'primary'} size="sm" onclick={() => settleConfirm(true)}>
      {confirmState.confirmLabel}
    </Button>
  {/snippet}
</Modal>
