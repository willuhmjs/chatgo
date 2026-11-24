<script lang="ts">
  import { RotateCcw, Save, Globe } from 'lucide-svelte';
  import { push } from '$lib/toast.svelte.js';
  import * as admin from '$lib/admin/api-admin';
  import type { ModelEntry, Registration, SystemSettings } from '$lib/admin/api-admin';
  import { errMsg } from '$lib/admin/format';
  import Field from '$lib/admin/ui/Field.svelte';
  import TokensPanel from '$lib/admin/TokensPanel.svelte';
  import Spinner from '$lib/admin/ui/Spinner.svelte';
  import { btn, btnGhostStyle, btnPrimaryStyle, errorBannerStyle, input, inputStyle, panelStyle } from '$lib/admin/ui/styles';

  // The four keys api-admin's SystemSettings types plus the two other writable
  // globals the server allowlist accepts (title, kb_embed_model). api-admin's
  // typed get/put is frozen at the four keys, so this tab goes through the raw
  // getSettings/setSettings that same module re-exports instead.
  type SystemForm = SystemSettings & { title: string; kb_embed_model: string; search_max_results: string };

  let saved = $state<SystemForm | null>(null);
  let form = $state<SystemForm>({ title: '', task_model: '', kb_embed_model: '', system_prompt: '', search_url: '', search_max_results: '', registration: 'approve' });
  let models = $state<ModelEntry[]>([]);
  let loading = $state(true);
  let loadError = $state('');
  let saving = $state(false);

  const dirty = $derived(
    saved !== null &&
      (form.title !== saved.title ||
        form.task_model !== saved.task_model ||
        form.kb_embed_model !== saved.kb_embed_model ||
        form.system_prompt !== saved.system_prompt ||
        form.search_url !== saved.search_url ||
        form.search_max_results !== saved.search_max_results ||
        form.registration !== saved.registration),
  );

  async function load() {
    loading = saved === null;
    loadError = '';
    try {
      // Raw getSettings instead of admin.getSystemSettings: the typed wrapper
      // is frozen at four keys and drops title/kb_embed_model, which the
      // server allowlist also accepts. One GET, all six keys mapped here.
      const [all, ms] = await Promise.all([admin.getSettings(), admin.listModels().catch(() => [] as ModelEntry[])]);
      saved = {
        title: all['title'] ?? '',
        task_model: all['task_model'] ?? '',
        kb_embed_model: all['kb_embed_model'] ?? '',
        system_prompt: all['system_prompt'] ?? '',
        search_url: all['search_url'] ?? '',
        search_max_results: all['search_max_results'] ?? '',
        registration: all['registration'] === 'open' || all['registration'] === 'closed' ? all['registration'] : 'approve',
      };
      form = { ...saved };
      models = ms;
    } catch (e) {
      loadError = errMsg(e, 'Could not load system settings');
    } finally {
      loading = false;
    }
  }
  void load();

  // ----- web-search live test (POST /api/settings/search/test) -----
  // Dials the EFFECTIVE target: SEARXNG_URL env wins server-side, else the
  // saved search_url setting. Unsaved edits can't be tested (the server has
  // them), hence the save-first hint while the field is dirty.
  let testing = $state(false);
  let testResult = $state<admin.SearchTestResult | null>(null);
  async function runSearchTest() {
    testing = true;
    testResult = null;
    try {
      testResult = await admin.testWebSearch();
    } catch (e) {
      testResult = { ok: false, hits: 0, error: errMsg(e, 'Test request failed') };
    } finally {
      testing = false;
    }
  }

  function reset() {
    if (saved) form = { ...saved };
  }

  async function save() {
    if (saving || !dirty) return;
    saving = true;
    try {
      // Single raw setSettings PUT (not admin.putSystemSettings, which would
      // silently drop the two extra keys and need a second request): the
      // server validates the whole batch before writing anything.
      await admin.setSettings({ ...form });
      saved = { ...form };
      push('System settings saved', 'success');
      // Live brand refresh (lib/brand): an admin renaming the instance sees
      // the sidebar/login/tab title update without a reload.
      window.dispatchEvent(new Event('chatgo:brand-changed'));
    } catch (e) {
      push(errMsg(e, 'Could not save settings'), 'error');
    } finally {
      saving = false;
    }
  }

  const policies: { value: Registration; label: string; hint: string }[] = [
    { value: 'open', label: 'Open', hint: 'Anyone can create an account instantly.' },
    { value: 'approve', label: 'Require approval', hint: 'New signups wait in the admin queue until approved.' },
    { value: 'closed', label: 'Closed', hint: 'Registration is disabled; admins add users via SSO or invites only.' },
  ];

  function modelOptionLabel(m: ModelEntry): string {
    return `${m.provider ?? m.provider_id} · ${m.display || m.model_id}`;
  }
  function modelOptionValue(m: ModelEntry): string {
    // TaskModel format is "providerID:modelID"; prefer the human-readable provider
    // name the models payload carries (server matches id or name).
    return `${m.provider || m.provider_id}:${m.model_id}`;
  }
</script>

<div class="flex flex-col gap-8">
  {#if loading}
    <div class="flex items-center gap-2 rounded-2xl px-4 py-8 text-sm text-[var(--muted)]" style={panelStyle}>
      <Spinner size={16} /> Loading settings…
    </div>
  {:else if loadError}
    <div class="flex items-center justify-between gap-3 rounded-[10px] px-4 py-3 text-sm" style={errorBannerStyle}>
      {loadError}
      <button class={btn} style={btnGhostStyle} onclick={load}>Retry</button>
    </div>
  {:else}
    <!-- instance settings -->
    <form
      class="flex flex-col gap-6"
      onsubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <section class="flex flex-col gap-5 rounded-2xl p-5" style="background:var(--panel)">
        <div>
          <h2 class="text-[15px] font-medium">Instance</h2>
          <p class="mt-0.5 text-xs text-[var(--muted)]">How this deployment introduces itself.</p>
        </div>

        <Field label="Instance title" hint="Display name for this ChatGo instance. Optional — blank uses the built-in default.">
          <input class={input} style={inputStyle} placeholder="ChatGo" autocomplete="off" bind:value={form.title} />
        </Field>
      </section>

      <section class="flex flex-col gap-5 rounded-2xl p-5" style="background:var(--panel)">
        <div>
          <h2 class="text-[15px] font-medium">Background tasks</h2>
          <p class="mt-0.5 text-xs text-[var(--muted)]">Used for auto chat titles and context summaries. Optional — leave blank to disable those features.</p>
        </div>

        <Field label="Task model" hint="Format: provider:model — e.g. openai:gpt-4o-mini. Type or pick a synced model.">
          <input
            class={`${input} font-mono text-xs`}
            style={inputStyle}
            placeholder="providerID:modelID"
            list="task-model-options"
            autocomplete="off"
            bind:value={form.task_model}
          />
          <datalist id="task-model-options">
            {#each models as m (`${m.provider_id}/${m.model_id}`)}
              <option value={modelOptionValue(m)}>{modelOptionLabel(m)}</option>
            {/each}
          </datalist>
        </Field>

        <Field label="Embedding model" hint="Format: provider:model — e.g. openai:text-embedding-3-small. Used for knowledge-base embeddings; blank auto-detects from synced models.">
          <input
            class={`${input} font-mono text-xs`}
            style={inputStyle}
            placeholder="providerID:modelID"
            list="kb-embed-model-options"
            autocomplete="off"
            bind:value={form.kb_embed_model}
          />
          <datalist id="kb-embed-model-options">
            {#each models as m (`${m.provider_id}/${m.model_id}`)}
              <option value={modelOptionValue(m)}>{modelOptionLabel(m)}</option>
            {/each}
          </datalist>
        </Field>

        <Field label="System prompt" hint="Prepended to every conversation as the assistant's base instructions. Empty = none.">
          <textarea
            class={`${input} min-h-32 resize-y font-mono text-xs leading-relaxed`}
            style={inputStyle}
            rows={6}
            placeholder="You are a helpful and knowledgeable assistant…"
            bind:value={form.system_prompt}
          ></textarea>
        </Field>
      </section>

      <section class="flex flex-col gap-5 rounded-2xl p-5" style="background:var(--panel)">
        <div>
          <h2 class="text-[15px] font-medium">Web search</h2>
          <p class="mt-0.5 text-xs text-[var(--muted)]">Enables the per-message search toggle in the composer.</p>
        </div>
        <Field label="SearXNG URL" hint="Self-hosted SearXNG base URL, e.g. http://searxng:8080. Blank disables web search. Private hosts need the server's TrustedURLNets/AllowLoopback config.">
          <input class={`${input} font-mono text-xs`} style={inputStyle} placeholder="http://searxng:8080" autocomplete="off" bind:value={form.search_url} />
        </Field>
        <Field label="Results per message" hint="SearXNG hits fed to the model per search (1–20). Blank uses the server default (SEARCH_MAX_RESULTS env or 5). More hits = better grounding but a bigger prompt.">
          <input class={`${input} font-mono text-xs`} style={inputStyle} inputmode="numeric" autocomplete="off" placeholder="5" bind:value={form.search_max_results} />
        </Field>
        <!-- Live probe: surfaces the real failure (json format disabled, SSRF-
             blocked network, unreachable host) instead of silent degradation -->
        <div class="flex items-start gap-3">
          <button
            type="button"
            class={btn}
            style={btnGhostStyle}
            disabled={testing || form.search_url !== saved?.search_url}
            title={form.search_url !== saved?.search_url ? 'Save the URL first — the test dials the saved config' : 'Run one search against the configured SearXNG'}
            onclick={() => void runSearchTest()}
          >
            {#if testing}<Spinner size={14} />{:else}<Globe size={14} />{/if}
            {testing ? 'Testing…' : 'Test web search'}
          </button>
          {#if testResult}
            <div class="min-w-0 flex-1 text-xs leading-relaxed" role="status">
              {#if testResult.ok}
                <span style="color:var(--ok, #10a37f)">✓ Working — {testResult.hits} result{testResult.hits === 1 ? '' : 's'} returned.</span>
                {#if testResult.note}<span class="text-[var(--muted)]"> {testResult.note}</span>{/if}
              {:else}
                <span style="color:var(--danger)">✗ {testResult.error}</span>
                {#if testResult.hint}
                  <span class="mt-0.5 block text-[var(--muted)]">Fix: {testResult.hint}</span>
                {/if}
              {/if}
            </div>
          {/if}
        </div>
        {#if form.search_url !== saved?.search_url && (form.search_url || saved?.search_url)}
          <p class="-mt-3 text-[11px] text-[var(--faint)]">Save the URL first — the test dials the saved config (or SEARXNG_URL when set).</p>
        {/if}
      </section>

      <section class="flex flex-col gap-4 rounded-2xl p-5" style="background:var(--panel)">
        <div>
          <h2 class="text-[15px] font-medium">Registration</h2>
          <p class="mt-0.5 text-xs text-[var(--muted)]">Who can create accounts. The instance env var is the startup default; this overrides at runtime.</p>
        </div>
        <div class="grid gap-2 sm:grid-cols-3">
          {#each policies as p (p.value)}
            <label
              class="flex cursor-pointer items-start gap-2.5 rounded-xl px-3 py-2.5 text-sm transition-colors hover:bg-[var(--fill-hover)]"
              style={form.registration === p.value ? 'background:var(--panel-2)' : 'background:transparent'}
            >
              <input type="radio" name="registration" class="mt-0.5 h-4 w-4 accent-[var(--accent)]" value={p.value} bind:group={form.registration} />
              <span>
                <span class="block font-medium">{p.label}</span>
                <span class="mt-0.5 block text-xs leading-relaxed text-[var(--muted)]">{p.hint}</span>
              </span>
            </label>
          {/each}
        </div>
      </section>

      <!-- dirty-state save bar -->
      {#if dirty}
        <div class="sticky bottom-4 z-10 flex items-center justify-between gap-3 rounded-2xl px-4 py-3" style="background:var(--panel-2); box-shadow:var(--shadow-menu)">
          <span class="text-xs font-medium text-[var(--muted)]">Unsaved system settings</span>
          <div class="flex items-center gap-2">
            <button type="button" class={btn} style={btnGhostStyle} onclick={reset} disabled={saving}>
              <RotateCcw size={14} /> Reset
            </button>
            <button type="submit" class={btn} style={btnPrimaryStyle} disabled={saving}>
              {#if saving}<Spinner /> Saving…{:else}<Save size={14} /> Save changes{/if}
            </button>
          </div>
        </div>
      {/if}
    </form>

    <!-- personal API tokens, admin edition -->
    <section class="flex flex-col gap-3">
      <div>
        <h2 class="text-[15px] font-medium">API tokens</h2>
        <p class="mt-0.5 text-xs text-[var(--muted)]">Your own <code class="font-mono">ck_…</code> keys for the OpenAI-compatible endpoint. Every user manages theirs under Settings.</p>
      </div>
      <TokensPanel />
    </section>
  {/if}
</div>
