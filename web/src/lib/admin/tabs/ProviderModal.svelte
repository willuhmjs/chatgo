<script lang="ts">
  import { KeyRound } from 'lucide-svelte';
  import { push } from '$lib/toast.svelte.js';
  import * as admin from '$lib/admin/api-admin';
  import type { Provider } from '$lib/admin/api-admin';
  import { errMsg } from '$lib/admin/format';
  import Field from '$lib/admin/ui/Field.svelte';
  import Modal from '$lib/admin/ui/Modal.svelte';
  import Spinner from '$lib/admin/ui/Spinner.svelte';
  import Toggle from '$lib/admin/ui/Toggle.svelte';
  import { btn, btnGhostStyle, btnPrimaryStyle, input, inputStyle } from '$lib/admin/ui/styles';

  let {
    open = $bindable(false),
    provider = null,
    onsaved,
  }: {
    open?: boolean;
    /** null → create mode; otherwise edit mode. */
    provider?: Provider | null;
    onsaved?: () => void;
  } = $props();

  let name = $state('');
  let baseUrl = $state('');
  let apiKey = $state('');
  let enabled = $state(true);
  let isDefault = $state(false);
  let busy = $state(false);
  let error = $state('');

  // Reset the form each time the dialog opens (write-only key ⇒ never echoed).
  $effect(() => {
    if (!open) return;
    name = provider?.name ?? '';
    baseUrl = provider?.base_url ?? '';
    apiKey = '';
    enabled = provider?.enabled ?? true;
    isDefault = provider?.is_default ?? false;
    busy = false;
    error = '';
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      const payload: Partial<Provider> & { api_key?: string } = {
        name: name.trim(),
        base_url: baseUrl.trim(),
        enabled,
        is_default: isDefault,
      };
      // Keep-unchanged semantics: omit api_key entirely when left blank on edit.
      const key = apiKey.trim();
      if (key) payload.api_key = key;
      if (provider) await admin.updateProvider(provider.id, payload);
      else await admin.createProvider(payload);
      push(provider ? `Updated ${name.trim()}` : `Added ${name.trim()}`, 'success');
      open = false;
      onsaved?.();
    } catch (err) {
      error = errMsg(err, 'Could not save provider');
    } finally {
      busy = false;
    }
  }
</script>

<Modal bind:open title={provider ? `Edit ${provider.name}` : 'Add provider'} wide>
  <form id="provider-form" onsubmit={submit} class="flex flex-col gap-4">
    <Field label="Name">
      <input class={input} style={inputStyle} required maxlength={64} placeholder="OpenAI" bind:value={name} />
    </Field>

    <Field
      label="Base URL"
      hint="OpenAI-compatible root, usually ending in /v1. SSRF guard: the server only dials http(s) hosts that resolve to public IPs — private/loopback ranges need TRUSTED_URL_NETS / ALLOW_LOOPBACK in the server config, otherwise saving succeeds but sync will fail."
    >
      <input
        class={`${input} font-mono text-xs`}
        style={inputStyle}
        required
        type="url"
        placeholder="https://api.openai.com/v1"
        bind:value={baseUrl}
      />
    </Field>

    <Field
      label="API key"
      hint={provider?.has_key
        ? 'Stored encrypted and never echoed back. Leave blank to keep the current key.'
        : 'Sent once, stored encrypted (AES-GCM). Leave blank to create without a key.'}
    >
      <input
        class={input}
        style={inputStyle}
        type="password"
        autocomplete="new-password"
        placeholder={provider?.has_key ? '•••• unchanged — paste a new key to replace' : 'sk-…'}
        bind:value={apiKey}
      />
    </Field>

    <div class="flex items-center justify-between gap-4 rounded-lg border px-3 py-2.5" style={inputStyle}>
      <div>
        <p class="text-sm font-medium">Enabled</p>
        <p class="text-xs text-[var(--muted)]">Disabled providers stay hidden from chat model pickers.</p>
      </div>
      <Toggle bind:checked={enabled} label="Provider enabled" />
    </div>

    <label class="flex items-center gap-2 text-sm">
      <input type="checkbox" class="h-4 w-4 accent-[var(--accent)]" bind:checked={isDefault} />
      <span>
        Default provider
        <span class="block text-xs text-[var(--muted)]">Pre-selected for new chats when a user hasn't chosen one.</span>
      </span>
    </label>

    {#if error}
      <p class="rounded-lg px-3 py-2 text-xs" style="background:color-mix(in srgb, var(--danger) 12%, transparent); color:var(--danger)">
        {error}
      </p>
    {/if}
  </form>

  {#snippet footer()}
    <button class={btn} style={btnGhostStyle} onclick={() => (open = false)}>Cancel</button>
    <button type="submit" form="provider-form" class={btn} style={btnPrimaryStyle} disabled={busy}>
      {#if busy}
        <Spinner /> Saving…
      {:else}
        <KeyRound size={14} /> {provider ? 'Save changes' : 'Create provider'}
      {/if}
    </button>
  {/snippet}
</Modal>
