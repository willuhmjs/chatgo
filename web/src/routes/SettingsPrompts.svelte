<script lang="ts">
  // Settings → Library tab (FE-PAGES): personal + admin-curated prompts.
  // /api/prompts lands in parallel (docs/FEATURES-V2.md · BE-SOCIAL) — a 404
  // degrades quietly to the empty state. Global (user_id == null) prompts are
  // read-only unless the caller is admin. Shapes: $lib/pages-api.ts.
  import { onMount } from 'svelte';
  import * as pages from '$lib/pages-api';
  import { session, isAdmin } from '$lib/session.svelte.js';
  import { push } from '$lib/toast.svelte.js';
  import { confirmDialog } from '$lib/ui/confirm.svelte.js';
  import Button from '$lib/ui/Button.svelte';
  import IconButton from '$lib/ui/IconButton.svelte';
  import Input from '$lib/ui/Input.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import { BookOpen, Pencil, Plus, Trash2 } from 'lucide-svelte';

  const MAX = pages.PROMPT_MAX_LEN;

  let prompts = $state<pages.Prompt[]>([]);
  let loading = $state(true);

  let modalOpen = $state(false);
  let editId = $state<string | null>(null);
  let title = $state('');
  let content = $state('');
  let saving = $state(false);

  /** Global = admin-curated; own = user_id matches me (or set and I'm admin). */
  const isGlobal = (p: pages.Prompt) => p.user_id === null;
  const editable = (p: pages.Prompt) => isAdmin() || (p.user_id !== null && p.user_id === session.user?.id);

  function fmtDate(ts: number): string {
    if (!ts) return '—';
    return new Date(ts > 1e12 ? ts : ts * 1000).toLocaleDateString(undefined, { dateStyle: 'medium' });
  }

  async function load() {
    loading = true;
    try {
      prompts = await pages.listPrompts();
    } catch (err) {
      prompts = [];
      if (!pages.isMissing(err)) push(pages.errMessage(err, 'Failed to load prompts'), 'error');
    } finally {
      loading = false;
    }
  }
  onMount(load);

  function openNew() {
    editId = null;
    title = '';
    content = '';
    modalOpen = true;
  }
  function openEdit(p: pages.Prompt) {
    editId = p.id;
    title = p.title;
    content = p.content;
    modalOpen = true;
  }

  async function save() {
    const t = title.trim();
    const c = content.trim();
    if (!t || !c || saving || c.length > MAX) return;
    saving = true;
    try {
      if (editId) {
        const updated = await pages.updatePrompt(editId, t, c);
        prompts = prompts.map((p) => (p.id === editId ? (updated.id ? updated : { ...p, title: t, content: c }) : p));
        push('Prompt saved', 'success');
      } else {
        const created = await pages.createPrompt(t, c);
        prompts = created.id ? [created, ...prompts] : prompts;
        if (!created.id) await load();
        push('Prompt created', 'success');
      }
      modalOpen = false;
    } catch (err) {
      push(pages.errMessage(err, 'Could not save prompt'), 'error');
    } finally {
      saving = false;
    }
  }

  async function remove(p: pages.Prompt) {
    const ok = await confirmDialog({
      title: 'Delete prompt?',
      message: `“${p.title}” will be permanently deleted.`,
      confirmLabel: 'Delete',
      danger: true,
    });
    if (!ok) return;
    try {
      await pages.deletePrompt(p.id);
      prompts = prompts.filter((x) => x.id !== p.id);
      push('Prompt deleted', 'success');
    } catch (err) {
      push(pages.errMessage(err, 'Delete failed'), 'error');
    }
  }
</script>

<div class="flex items-center justify-between gap-3">
  <p class="text-sm text-[var(--muted)]">
    Reusable prompts — yours everywhere, plus admin-curated ones. Use <code class="font-mono text-xs">/</code> in a
    chat to insert one.
  </p>
  <Button variant="primary" size="sm" onclick={openNew}>
    <Plus size={14} /> New prompt
  </Button>
</div>

<div class="mt-4 overflow-hidden rounded-2xl" style="background:var(--panel)">
  {#if loading}
    <div class="space-y-3 p-4">
      {#each Array(3) as _i}<Skeleton w="60%" h="0.9rem" />{/each}
    </div>
  {:else if prompts.length === 0}
    <EmptyState title="No prompts yet" description="Create your first prompt, or check back once the prompt library is live.">
      <icon><BookOpen size={18} /></icon>
      <Button variant="secondary" size="sm" onclick={openNew}>
        <Plus size={14} /> New prompt
      </Button>
    </EmptyState>
  {:else}
    <ul class="divide-y" style="border-color:var(--border)">
      {#each prompts as p (p.id)}
        <li class="group flex items-center gap-3 px-4 py-3">
          <div class="min-w-0 flex-1">
            <p class="truncate text-sm font-medium">{p.title}</p>
            <p class="mt-0.5 truncate text-xs text-[var(--muted)]">{p.content.slice(0, 100) || '—'}</p>
          </div>
          <span
            class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-medium"
            style={isGlobal(p)
              ? 'background:var(--acc-tint); color:var(--acc)'
              : 'background:var(--panel-2); color:var(--muted)'}
            title={isGlobal(p) ? 'Admin-curated — visible to everyone' : 'Personal'}
          >
            {isGlobal(p) ? 'Global' : 'Personal'}
          </span>
          <span class="hidden w-24 shrink-0 text-right text-xs text-[var(--faint)] sm:block">{fmtDate(p.updated_at)}</span>
          <span class="flex shrink-0 items-center">
            {#if editable(p)}
              <IconButton label={`Edit ${p.title}`} onclick={() => openEdit(p)}>
                <Pencil class="size-4" />
              </IconButton>
              <IconButton label={`Delete ${p.title}`} onclick={() => void remove(p)}>
                <Trash2 class="size-4" />
              </IconButton>
            {:else}
              <span class="px-1.5 text-[11px] text-[var(--faint)]" title="Only admins can edit curated prompts">read-only</span>
            {/if}
          </span>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<Modal bind:open={modalOpen} title={editId ? 'Edit prompt' : 'New prompt'} width="max-w-lg">
  <form
    class="space-y-3"
    onsubmit={(e) => {
      e.preventDefault();
      void save();
    }}
  >
    <Input label="Title" placeholder="e.g. Code reviewer" bind:value={title} autocomplete="off" />
    <div class="flex flex-col gap-1">
      <label class="text-xs font-medium text-[var(--muted)]" for="prompt-content">Content</label>
      <textarea
        id="prompt-content"
        rows={8}
        maxlength={MAX}
        class="w-full resize-y rounded-xl border-0 p-3 text-sm leading-relaxed outline-none placeholder:text-[var(--faint)] focus-visible:outline-2 focus-visible:-outline-offset-1 focus-visible:outline-[var(--accent-soft)]"
        style="background:var(--panel-2); color:var(--text)"
        placeholder="The text inserted into the composer…"
        bind:value={content}
      ></textarea>
      <p class="text-right text-[11px] tabular-nums" style="color:{content.length >= MAX ? 'var(--danger)' : 'var(--faint)'}">
        {content.length}/{MAX}
      </p>
    </div>
    <div class="flex justify-end gap-2 pt-1">
      <Button variant="secondary" size="sm" onclick={() => (modalOpen = false)}>Cancel</Button>
      <Button variant="primary" size="sm" type="submit" disabled={!title.trim() || !content.trim() || saving}>
        {editId ? 'Save' : 'Create'}
      </Button>
    </div>
  </form>
</Modal>
