<script lang="ts">
  let {
    checked = $bindable(false),
    label,
    disabled = false,
    onchange,
  }: {
    checked?: boolean;
    /** aria-label; also rendered as a visible label beside the switch. */
    label?: string;
    disabled?: boolean;
    onchange?: (checked: boolean) => void;
  } = $props();
</script>

<button
  type="button"
  role="switch"
  aria-checked={checked}
  aria-label={label || undefined}
  {disabled}
  class="flex items-center gap-2.5 rounded-lg py-1 pr-1 text-left focus-visible:outline-2 focus-visible:outline-[color-mix(in_srgb,var(--indigo)_60%,transparent)] disabled:opacity-50"
  onclick={() => {
    checked = !checked;
    onchange?.(checked);
  }}
>
  {#if label}
    <span class="text-sm">{label}</span>
  {/if}
  <span class="track" data-on={checked}>
    <span class="thumb"></span>
  </span>
</button>

<style>
  .track {
    display: inline-flex;
    width: 2.375rem;
    height: 1.375rem;
    flex-shrink: 0;
    align-items: center;
    border-radius: 999px;
    border: 1px solid transparent;
    background: var(--panel-2);
    padding: 0 2px;
    transition: background-color 0.15s ease;
  }
  .track[data-on='true'] {
    background: var(--indigo);
    border-color: var(--indigo);
  }
  .thumb {
    width: 1rem;
    height: 1rem;
    border-radius: 999px;
    background: #fff;
    box-shadow: 0 1px 2px rgb(0 0 0 / 0.4);
    transition: transform 0.15s ease;
  }
  .track[data-on='true'] .thumb {
    transform: translateX(1rem);
  }
</style>
