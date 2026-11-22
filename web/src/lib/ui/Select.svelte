<script lang="ts">
  let {
    label = '',
    value = $bindable(''),
    options = [],
    disabled = false,
    id = 'sel-' + Math.random().toString(36).slice(2, 8),
  }: {
    label?: string;
    value?: string;
    options: { value: string; label: string }[];
    disabled?: boolean;
    id?: string;
  } = $props();
</script>

<div class="flex flex-col gap-1">
  {#if label}
    <label class="text-xs font-medium text-[var(--muted)]" for={id}>{label}</label>
  {/if}
  <select id={id} {disabled} class="field" bind:value>
    {#each options as o (o.value)}
      <option value={o.value}>{o.label}</option>
    {/each}
  </select>
</div>

<style>
  .field {
    width: 100%;
    height: 2.25rem;
    border-radius: 0.5rem;
    border: 1px solid var(--border);
    background: var(--panel-2);
    color: var(--text);
    padding: 0 0.5rem;
    font-size: 0.8125rem;
    outline: none;
  }
  .field:focus-visible {
    border-color: var(--accent);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 22%, transparent);
  }
  @media (pointer: coarse) {
    .field {
      height: 2.75rem;
    }
  }
</style>
