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
  /* §1: filled panel select, quiet, accent-soft focus ring */
  .field {
    width: 100%;
    height: 2.25rem;
    border-radius: 0.625rem;
    border: 1px solid transparent;
    background: var(--panel);
    color: var(--text);
    padding: 0 0.5rem;
    font-size: 0.875rem;
    outline: none;
    transition: box-shadow 0.12s ease;
  }
  .field:focus-visible {
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--acc) 55%, transparent);
  }
  .field:disabled {
    opacity: 0.55;
  }
  @media (pointer: coarse) {
    .field {
      height: 2.75rem;
    }
  }
</style>
