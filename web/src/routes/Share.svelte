<script lang="ts">
  // /s/:token — public read-only shared chat (FE-extra owns polish).
  import * as api from '$lib/api';
  import { onMount } from 'svelte';

  let { token }: { token: string } = $props();

  let chat = $state<api.Chat | null>(null);
  let messages = $state<api.Message[]>([]);
  let error = $state('');

  onMount(async () => {
    try {
      const payload = await api.getShare(token);
      chat = payload.chat;
      messages = payload.messages;
    } catch (err) {
      error = err instanceof api.ApiError ? err.message : 'Failed to load shared chat';
    }
  });
</script>

<div class="mx-auto h-full w-full max-w-[48rem] overflow-y-auto px-6 py-8">
  {#if error}
    <p class="text-sm text-[var(--danger)]">{error}</p>
  {:else if chat}
    <h1 class="mb-6 text-xl font-semibold">{chat.title}</h1>
    <div class="flex flex-col gap-4">
      {#each messages as m (m.id)}
        <div
          class="rounded-xl border px-4 py-3 text-sm"
          style="background:var(--panel); border-color:var(--border)"
        >
          <p class="mb-1 text-xs uppercase tracking-wide text-[var(--muted)]">{m.role}</p>
          <p class="whitespace-pre-wrap">{m.content}</p>
        </div>
      {/each}
    </div>
  {:else}
    <p class="text-sm text-[var(--muted)]">Loading shared chat…</p>
  {/if}
</div>
