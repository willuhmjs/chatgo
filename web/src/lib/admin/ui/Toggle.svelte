<script lang="ts">
  let {
    checked = $bindable(false),
    disabled = false,
    label = 'Toggle',
    onchange,
  }: {
    checked?: boolean;
    disabled?: boolean;
    label?: string;
    onchange?: (next: boolean) => void;
  } = $props();

  function click() {
    if (disabled) return;
    checked = !checked;
    onchange?.(checked);
  }
</script>

<button
  type="button"
  role="switch"
  aria-checked={checked}
  aria-label={label}
  {disabled}
  onclick={click}
  class="relative h-5 w-9 shrink-0 rounded-full transition-colors disabled:cursor-not-allowed disabled:opacity-40"
  style="background:{checked ? 'var(--accent)' : 'var(--border)'}"
>
  <span
    class="absolute top-0.5 h-4 w-4 rounded-full bg-white shadow transition-all"
    style="left:{checked ? '1.125rem' : '0.125rem'}"
  ></span>
</button>
