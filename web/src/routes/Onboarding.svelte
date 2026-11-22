<script lang="ts">
  // /onboarding — 3-step admin setup wizard (docs/AUTHGATE-CONTRACT.md §4):
  // 1) connect a provider (+sync) · 2) pick a task model (+system prompt) ·
  // 3) invite your humans (SSO approval queue or token hint). Quiet 1·2·3 dot
  // header, centered card on --bg, white primary, ghost skip — per
  // docs/DESIGN-POLISH.md. AG-2 owned; imports only scaffold libs (api,
  // router, session, toast) — no lib/ui or lib/admin components.
  import { ArrowLeft, ArrowRight, CircleCheck, Users } from 'lucide-svelte';
  import * as api from '$lib/api';
  import type { AuthConfig, ModelEntry, User } from '$lib/api';
  import { navigate } from '$lib/router.svelte.js';
  import { dismissOnboard, isAdmin, session } from '$lib/session.svelte.js';
  import { push } from '$lib/toast.svelte.js';

  const STEPS = ['Provider', 'Task model', 'Invite'] as const;

  // Keep the wizard's position across remounts: if anything bounces us home
  // and back (e.g. the old setup_done nudge loop), we resume where we left
  // off instead of landing on step 1.
  const STEP_KEY = 'chatgo.onb.step';
  function restoreStep(): 0 | 1 | 2 {
    try {
      const n = Number.parseInt(sessionStorage.getItem(STEP_KEY) ?? '', 10);
      if (n === 1 || n === 2) return n;
    } catch {
      /* storage unavailable (private mode etc.) — start over */
    }
    return 0;
  }
  let step = $state<0 | 1 | 2>(restoreStep());
  $effect(() => {
    try {
      if (step === 0) sessionStorage.removeItem(STEP_KEY);
      else sessionStorage.setItem(STEP_KEY, String(step));
    } catch {
      /* ignore */
    }
  });

  // ----- step 1: provider -----
  let pName = $state('');
  let pBaseUrl = $state('https://api.openai.com/v1');
  let pApiKey = $state('');
  let pDefault = $state(true);
  let pBusy = $state(false);
  let pError = $state('');
  let connectedName = $state('');
  let syncedCount = $state<number | null>(null); // null = not synced (yet/failed)

  // ----- step 2: task model -----
  let models = $state<ModelEntry[]>([]);
  let modelsLoaded = $state(false);
  let taskModel = $state(''); // "providerID:modelID"
  let systemPrompt = $state('');

  // ----- step 3: invites -----
  let config = $state<AuthConfig | null>(null);
  let users = $state<User[]>([]);
  let approving = $state<Record<string, boolean>>({});

  const pending = $derived(users.filter((u) => u.role === 'pending'));
  const grouped = $derived.by(() => {
    const byProvider = new Map<string, { label: string; models: ModelEntry[] }>();
    for (const m of models) {
      const g = byProvider.get(m.provider_id) ?? { label: m.provider || m.provider_id, models: [] };
      g.models.push(m);
      byProvider.set(m.provider_id, g);
    }
    return [...byProvider.values()];
  });

  // Route guard — wizard is admin-only.
  $effect(() => {
    if (session.ready && !isAdmin()) navigate('/', { replace: true });
  });

  // Load synced models once we reach step 2.
  $effect(() => {
    if (step !== 1 || modelsLoaded) return;
    let cancelled = false;
    api.listModels().then(
      (ms) => {
        if (cancelled) return;
        models = ms;
        modelsLoaded = true;
        if (!taskModel && ms[0]) taskModel = `${ms[0].provider_id}:${ms[0].model_id}`;
      },
      () => {
        if (!cancelled) modelsLoaded = true;
      },
    );
    return () => {
      cancelled = true;
    };
  });

  // Step 3: OIDC config + live-ish pending approval queue (poll while shown).
  $effect(() => {
    if (step !== 2) return;
    let cancelled = false;
    void api.authConfig().then(
      (c) => {
        if (!cancelled) config = c;
      },
      () => void 0,
    );
    const load = () =>
      api.listUsers().then(
        (us) => {
          if (!cancelled) users = us;
        },
        () => void 0, // queue just stays empty; token hint still useful
      );
    void load();
    const t = setInterval(() => void load(), 8000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  });

  async function connectProvider() {
    if (pBusy) return;
    pBusy = true;
    pError = '';
    try {
      const payload: Partial<api.Provider> & { api_key?: string } = {
        name: pName.trim(),
        base_url: pBaseUrl.trim(),
        enabled: true,
        is_default: pDefault,
      };
      const key = pApiKey.trim();
      if (key) payload.api_key = key;
      const p = await api.createProvider(payload);
      connectedName = p.name;
      try {
        const r = await api.syncProvider(p.id);
        syncedCount = r.count;
        push(`${r.count} model${r.count === 1 ? '' : 's'} synced from ${p.name}`, 'success');
      } catch {
        syncedCount = null;
        push(`${p.name} saved, but the model sync failed — retry it in Admin later.`, 'error');
      }
    } catch (err) {
      pError = err instanceof Error && err.message ? err.message : 'Could not save the provider';
    } finally {
      pBusy = false;
    }
  }

  async function approve(u: User) {
    if (approving[u.id]) return;
    approving = { ...approving, [u.id]: true };
    try {
      const updated = await api.approveUser(u.id);
      users = users.map((x) => (x.id === u.id ? updated : x));
      push(`Approved ${updated.name || updated.email}`, 'success');
    } catch (err) {
      push(err instanceof Error && err.message ? err.message : 'Approval failed', 'error');
    } finally {
      const next = { ...approving };
      delete next[u.id];
      approving = next;
    }
  }

  /**
   * Finish the wizard: persist setup_done (me-setting — App.svelte's nudge
   * reads it back), best-effort the admin-allowlisted keys (contract §4b),
   * then head home. A failed setup_done save surfaces a toast but NEVER
   * traps the user here: dismissOnboard() makes the nudge effect stop even
   * though the server never recorded the flag.
   */
  let finishing = $state(false);
  async function finish(saveWizard: boolean) {
    if (finishing) return;
    finishing = true;
    const settings: Record<string, string> = { instance_setup_done: 'true' };
    if (saveWizard) {
      if (taskModel) settings.task_model = taskModel;
      const sp = systemPrompt.trim();
      if (sp) settings.system_prompt = sp;
    }
    const [meRes] = await Promise.allSettled([
      api.setMeSettings({ setup_done: 'true' }),
      api.setSettings(settings), // ignore failures per contract (best-effort)
    ]);
    if (meRes.status === 'rejected') {
      push('Could not save setup state', 'error');
    }
    try {
      sessionStorage.removeItem(STEP_KEY);
    } catch {
      /* ignore */
    }
    dismissOnboard();
    navigate('/');
  }

  function next() {
    if (step === 0) {
      if (connectedName) step = 1;
      else void connectProvider();
      return;
    }
    if (step === 1) step = 2;
  }
</script>

<section
  class="flex min-h-dvh flex-col items-center justify-center px-5 py-12"
  style="background:var(--bg); color:var(--text)"
>
  <div class="w-full max-w-[48rem]">
    <!-- quiet 1·2·3 dot header (contract §4) -->
    <ol class="mb-6 flex items-center justify-center gap-3" aria-label="Setup progress">
      {#each STEPS as label, i (label)}
        {#if i > 0}
          <li aria-hidden="true">
            <span class="block h-px w-8" style="background:var(--border)"></span>
          </li>
        {/if}
        <li class="flex items-center gap-2" aria-current={step === i ? 'step' : undefined}>
          <span
            class="flex h-6 w-6 items-center justify-center rounded-full text-[12px] font-medium"
            style="background:{step > i
              ? 'var(--accent)'
              : step === i
                ? 'transparent'
                : 'var(--panel)'}; color:{step > i
              ? 'var(--accent-fg)'
              : step === i
                ? 'var(--text)'
                : 'var(--faint)'}; border:{step === i ? '1px solid var(--border)' : '1px solid transparent'}"
          >
            {#if step > i}<CircleCheck size={13} />{:else}{i + 1}{/if}
          </span>
          <span
            class="text-[13px] {i === 0 || i === 2 ? 'hidden sm:inline' : ''}"
            style="color:{step === i ? 'var(--text)' : 'var(--muted)'}"
          >
            {label}
          </span>
        </li>
      {/each}
    </ol>

    <div class="rounded-2xl p-7" style="background:var(--panel)">
      {#if step === 0}
        <h1 class="text-[22px] font-semibold tracking-tight">Connect a provider</h1>
        <p class="mt-1 text-sm" style="color:var(--muted)">
          Any OpenAI-compatible endpoint. We'll sync its models right away.
        </p>

        {#if connectedName}
          <div
            class="mt-5 flex items-center gap-2 rounded-xl px-3.5 py-3 text-sm"
            style="background:var(--panel-2)"
          >
            <CircleCheck size={16} style="color:var(--ok)" />
            {#if syncedCount !== null}
              <span><span class="font-medium">{syncedCount}</span> models synced from {connectedName}.</span>
            {:else}
              <span>{connectedName} added — model sync failed; retry it in Admin later.</span>
            {/if}
          </div>
          <p class="mt-4 text-xs" style="color:var(--faint)">
            Need another provider? Add it later in Admin → Providers.
          </p>
        {:else}
          <form
            id="onboarding-provider-form"
            onsubmit={(e) => {
              e.preventDefault();
              void connectProvider();
            }}
            class="mt-5 flex flex-col gap-3.5"
          >
            <label class="block text-sm">
              <span class="mb-1 block text-[13px]" style="color:var(--muted)">Name</span>
              <input type="text" required maxlength={64} class="inp" placeholder="OpenAI" bind:value={pName} />
            </label>
            <label class="block text-sm">
              <span class="mb-1 block text-[13px]" style="color:var(--muted)">Base URL</span>
              <input
                type="url"
                required
                class="inp font-mono text-xs"
                placeholder="https://api.openai.com/v1"
                bind:value={pBaseUrl}
              />
            </label>
            <label class="block text-sm">
              <span class="mb-1 block text-[13px]" style="color:var(--muted)">API key</span>
              <input
                type="password"
                autocomplete="new-password"
                class="inp"
                placeholder="sk-…"
                bind:value={pApiKey}
              />
            </label>
            <label
              class="flex cursor-pointer items-center justify-between gap-4 rounded-xl px-3.5 py-2.5 text-sm"
              style="background:var(--panel-2)"
            >
              <span>
                Make this the default provider
                <span class="block text-[11px]" style="color:var(--faint)">New chats pick its models first.</span>
              </span>
              <input type="checkbox" class="chk" bind:checked={pDefault} />
            </label>

            {#if pError}
              <p
                role="alert"
                class="rounded-[10px] px-3 py-2 text-[13px]"
                style="background:color-mix(in srgb, var(--danger) 9%, transparent); color:var(--danger)"
              >
                {pError}
              </p>
            {/if}
          </form>
        {/if}
      {:else if step === 1}
        <h1 class="text-[22px] font-semibold tracking-tight">Pick a task model</h1>
        <p class="mt-1 text-sm" style="color:var(--muted)">
          Used for background jobs like chat titles and summaries — keep it cheap.
        </p>

        <div class="mt-5 flex flex-col gap-3.5">
          <label class="block text-sm">
            <span class="mb-1 block text-[13px]" style="color:var(--muted)">Task model</span>
            {#if modelsLoaded && models.length === 0}
              <p class="rounded-xl px-3.5 py-3 text-[13px]" style="background:var(--panel-2); color:var(--muted)">
                No models were synced — you can pick one later in Admin → System settings.
              </p>
            {:else}
              <select class="inp" bind:value={taskModel}>
                {#if !taskModel}<option value="">Choose a model…</option>{/if}
                {#each grouped as g (g.label)}
                  <optgroup label={g.label}>
                    {#each g.models as m (`${m.provider_id}:${m.model_id}`)}
                      <option value="{m.provider_id}:{m.model_id}">{m.display || m.model_id}</option>
                    {/each}
                  </optgroup>
                {/each}
              </select>
            {/if}
          </label>
          <label class="block text-sm">
            <span class="mb-1 block text-[13px]" style="color:var(--muted)">
              System prompt <span style="color:var(--faint)">(optional)</span>
            </span>
            <textarea
              rows={4}
              class="inp resize-y"
              placeholder="You are a helpful assistant for our team…"
              bind:value={systemPrompt}></textarea>
          </label>
        </div>
      {:else}
        <h1 class="text-[22px] font-semibold tracking-tight">Invite your humans</h1>

        {#if config?.oidc_enabled}
          <p class="mt-1 text-sm" style="color:var(--muted)">
            Single sign-on is on: people sign in through {config.oidc_name || 'SSO'} and land in the approval
            queue below — approve them here and they're in, no passwords to hand out.
          </p>

          <div class="mt-5 rounded-xl px-4 py-3.5" style="background:var(--panel-2)">
            <div class="flex items-center gap-2 text-sm font-medium">
              <Users size={15} style="color:var(--muted)" />
              Waiting for approval
              {#if pending.length > 0}
                <span
                  class="flex h-5 min-w-5 items-center justify-center rounded-full px-1.5 text-[11px] font-semibold"
                  style="background:var(--accent); color:var(--accent-fg)"
                >
                  {pending.length}
                </span>
              {/if}
            </div>
            {#if pending.length === 0}
              <p class="mt-2 text-[13px]" style="color:var(--muted)">
                Nobody yet. Point your Authelia client at this ChatGo instance and the first sign-in
                will show up here.
              </p>
            {:else}
              <ul class="mt-2 flex flex-col">
                {#each pending as u (u.id)}
                  <li class="flex items-center gap-3 py-2">
                    <span class="min-w-0 flex-1">
                      <span class="block truncate text-[13px] font-medium">{u.name || 'Unnamed'}</span>
                      <span class="block truncate text-[12px]" style="color:var(--muted)">{u.email}</span>
                    </span>
                    <button
                      type="button"
                      class="ghost !px-3 !py-1.5 !text-[13px]"
                      disabled={approving[u.id]}
                      onclick={() => void approve(u)}
                    >
                      {approving[u.id] ? 'Approving…' : 'Approve'}
                    </button>
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        {:else}
          <p class="mt-1 text-sm" style="color:var(--muted)">
            SSO isn't configured yet — accounts go through Admin → Users and the registration policy.
          </p>
          <div class="mt-5 rounded-xl px-4 py-3.5 text-sm" style="background:var(--panel-2)">
            Automating something? Mint a one-time <code class="font-mono text-[12.5px]">ck_…</code> API token in
            <a href="/settings" class="font-medium underline underline-offset-2" style="color:var(--indigo)">Settings</a>
            — it talks to the OpenAI-compatible <span class="font-mono text-[12.5px]">/v1/chat/completions</span> endpoint.
          </div>
        {/if}
      {/if}

      <!-- footer actions: ghost Back / white Next, skip = ghost text (contract §4) -->
      <div class="mt-8 flex items-center gap-2">
        <button type="button" class="ghost" onclick={() => void finish(false)} disabled={finishing}>
          Skip setup
        </button>
        <div class="flex-1"></div>
        {#if step > 0}
          <button type="button" class="ghost" onclick={() => (step = step === 2 ? 1 : 0)} disabled={finishing}>
            <ArrowLeft size={14} /> Back
          </button>
        {/if}
        {#if step < 2}
          {#if step === 0 && !connectedName}
            <button
              type="submit"
              form="onboarding-provider-form"
              class="primary"
              disabled={pBusy}
            >
              {#if pBusy}Connecting…{:else}Connect provider <ArrowRight size={14} />{/if}
            </button>
          {:else}
            <button type="button" class="primary" onclick={next}>Next <ArrowRight size={14} /></button>
          {/if}
        {:else}
          <button type="button" class="ghost" onclick={() => navigate('/admin')} disabled={finishing}>
            Open admin console
          </button>
          <button type="button" class="primary" onclick={() => void finish(true)} disabled={finishing}>
            Start chatting
          </button>
        {/if}
      </div>
    </div>
  </div>
</section>

<style>
  .inp {
    width: 100%;
    border-radius: 10px;
    padding: 0.5rem 0.75rem;
    font-size: 0.875rem;
    color: var(--text);
    background: var(--panel-2);
    border: 1px solid transparent;
    outline: none;
    transition:
      border-color 150ms ease,
      box-shadow 150ms ease;
  }
  .inp::placeholder {
    color: var(--faint);
  }
  .inp:focus-visible {
    border-color: rgba(255, 255, 255, 0.22);
    box-shadow: 0 0 0 3px var(--accent-soft);
  }
  select.inp {
    appearance: none;
    background-image: linear-gradient(45deg, transparent 50%, var(--muted) 50%),
      linear-gradient(135deg, var(--muted) 50%, transparent 50%);
    background-position:
      calc(100% - 18px) 55%,
      calc(100% - 13px) 55%;
    background-size:
      5px 5px,
      5px 5px;
    background-repeat: no-repeat;
  }
  select.inp option,
  select.inp optgroup {
    background: var(--panel);
    color: var(--text);
  }
  .chk {
    width: 1rem;
    height: 1rem;
    accent-color: var(--accent);
    flex-shrink: 0;
  }
  .primary {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    border: 0;
    border-radius: 999px;
    padding: 0.5rem 1rem;
    font-size: 0.875rem;
    font-weight: 500;
    background: var(--accent);
    color: var(--accent-fg);
    cursor: pointer;
    transition: opacity 120ms ease;
  }
  .primary:hover {
    opacity: 0.9;
  }
  .primary:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .ghost {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0.5rem 1rem;
    font-size: 0.875rem;
    background: transparent;
    color: var(--text);
    cursor: pointer;
    transition: background-color 120ms ease;
  }
  .ghost:hover {
    background: var(--fill-hover);
  }
  .ghost:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .primary:focus-visible,
  .ghost:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--indigo) 60%, transparent);
    outline-offset: 2px;
  }
</style>
