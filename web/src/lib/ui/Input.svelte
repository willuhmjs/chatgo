<script lang="ts">
  let {
    label = '',
    type = 'text',
    placeholder = '',
    value = $bindable(''),
    error = '',
    disabled = false,
    autocomplete = 'off',
    id = 'fld-' + Math.random().toString(36).slice(2, 8),
  }: {
    label?: string;
    type?: 'text' | 'email' | 'password' | 'search' | 'number';
    placeholder?: string;
    value?: string;
    error?: string;
    disabled?: boolean;
    autocomplete?: 'off' | 'on' | 'name' | 'email' | 'username' | 'current-password' | 'new-password';
    id?: string;
  } = $props();
</script>

<div class="flex flex-col gap-1">
  {#if label}
    <label class="text-xs font-medium text-[var(--muted)]" for={id}>{label}</label>
  {/if}
  <input
    {id}
    {type}
    {placeholder}
    {disabled}
    {autocomplete}
    aria-invalid={error ? true : undefined}
    aria-describedby={error ? `${id}-err` : undefined}
    class="field {error ? 'field-error' : ''}"
    bind:value
  />
  {#if error}
    <p id="{id}-err" class="text-xs" style="color:var(--danger)">{error}</p>
  {/if}
</div>

<style>
  /* §1: filled #2f2f2f (--panel) fields, no border, accent-soft focus ring */
  .field {
    width: 100%;
    height: 2.25rem;
    border-radius: 0.625rem;
    border: 1px solid transparent;
    background: var(--panel);
    color: var(--text);
    padding: 0 0.75rem;
    font-size: 0.875rem;
    outline: none;
    transition: box-shadow 0.12s ease;
  }
  .field::placeholder {
    color: var(--faint);
  }
  .field:focus-visible {
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--acc) 55%, transparent);
  }
  .field-error {
    box-shadow: 0 0 0 1.5px var(--danger);
  }
  .field:disabled {
    opacity: 0.55;
  }
  @media (pointer: coarse) {
    .field {
      height: 2.75rem;
      font-size: 1rem;
    }
  }
</style>
