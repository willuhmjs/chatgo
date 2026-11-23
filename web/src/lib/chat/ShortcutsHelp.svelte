<script lang="ts">
  // Keyboard-shortcuts help modal (FE-chat). Opens with ? (bare key — the
  // global shortcut registry skips it while typing in a field); mounted by
  // the Composer so chat-scoped keys and the global ⌘K/⌘B rows live in one
  // honest list. Rows mirror real behavior — see Composer.svelte /
  // MessageList.svelte "edit-message walk" and $lib/shortcuts.svelte.js.
  import Modal from './ui/Modal.svelte';

  let { open = false, onclose }: { open: boolean; onclose: () => void } = $props();

  interface Row {
    keys: string[];
    desc: string;
  }
  const sections: { name: string; rows: Row[] }[] = [
    {
      name: 'Composer',
      rows: [
        { keys: ['↵'], desc: 'Send message (⇧↵ newline; with Enter-to-send on)' },
        { keys: ['⌘↵'], desc: 'Send message (always)' },
        { keys: ['/'], desc: 'Focus the composer' },
        { keys: ['↑'], desc: 'Edit last message (empty composer)' },
        { keys: ['?'], desc: 'Open this help' },
      ],
    },
    {
      name: 'Editing a message',
      rows: [
        { keys: ['↑', '↓'], desc: 'Previous / next message to edit' },
        { keys: ['⌘↵'], desc: 'Save & resend — regenerates the rest' },
        { keys: ['Esc'], desc: 'Cancel the edit' },
      ],
    },
    {
      name: 'Global',
      rows: [
        { keys: ['⌘K'], desc: 'Command palette' },
        { keys: ['⌘B'], desc: 'Toggle sidebar' },
      ],
    },
  ];
</script>

<Modal {open} {onclose}>
  <div class="sheet">
    <h2 id="modal-title" class="text-[15px] font-medium">Keyboard shortcuts</h2>
    {#each sections as s (s.name)}
      <p class="secname">{s.name}</p>
      <div class="rows">
        {#each s.rows as r (r.desc)}
          <div class="row">
            <span class="keys">{#each r.keys as k, i (k)}{#if i}or{/if}<kbd>{k}</kbd>{/each}</span>
            <span class="desc">{r.desc}</span>
          </div>
        {/each}
      </div>
    {/each}
  </div>
</Modal>

<style>
  .sheet {
    width: min(92vw, 26rem);
    padding: 1.1rem 1.25rem 1.25rem;
    border-radius: 0.875rem;
    background: var(--panel, #2f2f2f);
    box-shadow: var(--shadow-menu, 0 8px 28px rgb(0 0 0 / 0.45));
  }
  .secname {
    margin: 0.9rem 0 0.3rem;
    font-size: 0.6875rem;
    font-weight: 500;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--faint, #6e6e6e);
  }
  .rows {
    display: flex;
    flex-direction: column;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.28rem 0;
    font-size: 0.8125rem;
  }
  .keys {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    flex: none;
    min-width: 4.75rem;
    font-size: 0.6875rem;
    color: var(--faint, #6e6e6e);
  }
  kbd {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 1.5rem;
    padding: 0.1rem 0.4rem;
    border-radius: 0.375rem;
    background: var(--panel-2, #3a3a3a);
    color: var(--text);
    font-family: inherit;
    font-size: 0.75rem;
    line-height: 1.5;
  }
  .desc {
    color: var(--muted, #a8a8a8);
  }
</style>
