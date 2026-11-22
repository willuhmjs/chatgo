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
  .field {
    width: 100%;
    height: 2.25rem;
    border-radius: 0.5rem;
    border: 1px solid var(--border);
    background: var(--panel-2);
    color: var(--text);
    padding: 0 0.75rem;
    font-size: 0.8125rem;
    outline: none;
    transition:
      border-color 0.12s ease,
      box-shadow 0.12s ease;
  }
  .field::placeholder {
    color: var(--muted);
  }
  .field:focus-visible {
    border-color: var(--accent);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 22%, transparent);
  }
  .field-error {
    border-color: var(--danger);
  }
  .field:disabled {
    opacity: 0.6;
  }
  @media (pointer: coarse) {
    .field {
      height: 2.75rem;
      font-size: 1rem;
    }
  }
</style>
