<script lang="ts">
  // ChatActionsMenu — per-chat "…" actions for the ChatView header:
  //   SHARE   shareChat/unshareChat + clipboard copy of /s/{token}; shared
  //           state mirrors the sidebar dot (bg-[var(--acc)] dot on trigger).
  //   EXPORT  plain cookie-GET anchors to /api/chats/{id}/export?format=md|json
  //           and /api/me/export (zip). BE only accepts md|json per chat —
  //           the zip lives on /api/me/export (internal/library/export.go).
  //   TAGS    removable chips + create-then-attach input over
  //           listTags/createTag/setChatTags (full id list every write).
  // Mutations broadcast `chatgo:chat-updated` with the fresh chat (same event
  // ModelPicker/Composer/ParamDrawer use) so the sidebar can refresh its dot.
  import type { Chat, Tag } from '$lib/api';
  import * as api from '$lib/api';
  import { push as toast } from '$lib/toast.svelte.js';
  import { confirmDialog } from '$lib/ui/confirm.svelte.js';
  import Dropdown from '$lib/ui/Dropdown.svelte';
  import MenuItem from '$lib/ui/MenuItem.svelte';
  import { hueFrom } from '$lib/hue';
  import MoreHorizontal from 'lucide-svelte/icons/more-horizontal';
  import Share2 from 'lucide-svelte/icons/share-2';
  import Copy from 'lucide-svelte/icons/copy';
  import Unlink from 'lucide-svelte/icons/unlink';
  import Download from 'lucide-svelte/icons/download';
  import Archive from 'lucide-svelte/icons/archive';
  import Plus from 'lucide-svelte/icons/plus';
  import X from 'lucide-svelte/icons/x';

  /** GET /api/chats/{id} carries `tags` (omitempty) even though api.ts's
   *  Chat DTO predates it — widen locally rather than touch api.ts. */
  type ChatWithTags = Chat & { tags?: Tag[] };

  let {
    chat,
    onupdated,
  }: {
    chat: Chat | null;
    onupdated?: (c: Chat) => void;
  } = $props();

  let busy = $state(false);
  let tagInput = $state('');
  let allTags = $state<Tag[]>([]);
  let tagsLoaded = $state(false);
  // survives as a fallback when the parent doesn't echo the patched chat back
  let localTags = $state<Tag[] | null>(null);

  $effect(() => {
    void chat?.id; // reset per-chat tag mirror on navigation
    localTags = null;
  });

  const shared = $derived(!!chat?.share_token);
  const currentTags = $derived<Tag[]>(localTags ?? (chat as ChatWithTags | null)?.tags ?? []);

  const msg = (err: unknown): string => String((err as Error)?.message ?? err);

  /** Lazily hydrate the tag universe (for chips/datalist) on first menu use. */
  function loadTags(): void {
    if (tagsLoaded || busy) return;
    api
      .listTags()
      .then((t) => {
        allTags = t;
        tagsLoaded = true;
      })
      .catch(() => {
        // degrade quietly: chips from the chat payload still render
      });
  }

  /** Action on the menu body: fires every time the Dropdown opens. */
  function hydrateTags(_node: HTMLElement): void {
    loadTags();
  }

  function announce(updated: Chat): void {
    onupdated?.(updated);
    window.dispatchEvent(new CustomEvent('chatgo:chat-updated', { detail: { chat: updated } }));
  }

  // ---------------- share ----------------

  const shareUrl = (token: string): string => `${location.origin}/s/${token}`;

  async function copyLink(token: string | null | undefined): Promise<void> {
    if (!token) return;
    const url = shareUrl(token);
    try {
      await navigator.clipboard.writeText(url);
      toast('Link copied', 'success', 2000);
    } catch {
      // non-secure context / permission denied: reveal the URL for manual copy
      toast(url, 'info', 8000);
    }
  }

  function doShare(): void {
    if (!chat || busy) return;
    busy = true;
    api
      .shareChat(chat.id)
      .then(({ token }) => {
        announce({ ...chat, share_token: token } as Chat);
        return copyLink(token);
      })
      .catch((err: unknown) => toast(`Could not share: ${msg(err)}`, 'error'))
      .finally(() => (busy = false));
  }

  async function doUnshare(): Promise<void> {
    if (!chat || busy) return;
    const ok = await confirmDialog({
      title: 'Remove share link?',
      message: 'Anyone with the current link will lose access. You can share this chat again later.',
      confirmLabel: 'Unshare',
      danger: true,
    });
    if (!ok || !chat) return;
    busy = true;
    api
      .unshareChat(chat.id)
      .then(() => {
        announce({ ...chat, share_token: null } as Chat);
        toast('Share link removed', 'success', 2000);
      })
      .catch((err: unknown) => toast(`Could not unshare: ${msg(err)}`, 'error'))
      .finally(() => (busy = false));
  }

  // ---------------- export ----------------

  function download(href: string): void {
    // cookie GETs; server sets Content-Disposition, download attr is a hint
    const a = document.createElement('a');
    a.href = href;
    a.download = '';
    document.body.appendChild(a);
    a.click();
    a.remove();
  }

  function exportChat(format: 'md' | 'json'): void {
    if (!chat) return;
    download(`/api/chats/${encodeURIComponent(chat.id)}/export?format=${format}`);
  }

  function exportAll(): void {
    download('/api/me/export');
  }

  // ---------------- tags ----------------

  /** PUT the FULL id list (endpoint replaces the assignment), then repaint. */
  async function commitTags(next: Tag[]): Promise<void> {
    if (!chat || busy) return;
    busy = true;
    try {
      await api.setChatTags(chat.id, next.map((t) => t.id));
      localTags = next;
      announce({ ...chat, tags: next } as unknown as Chat);
    } catch (err) {
      toast(`Could not update tags: ${msg(err)}`, 'error');
    } finally {
      busy = false;
    }
  }

  function attachTag(t: Tag): void {
    if (currentTags.some((x) => x.id === t.id)) {
      toast(`“${t.name}” is already on this chat`, 'info', 2000);
      return;
    }
    void commitTags([...currentTags, t]);
  }

  function detachTag(t: Tag): void {
    void commitTags(currentTags.filter((x) => x.id !== t.id));
  }

  /** Enter in the input: reuse by name (case-insensitive) or create-then-attach. */
  function addTagByName(): void {
    const name = tagInput.trim();
    if (!name || busy) return;
    const known = allTags.find((t) => t.name.toLowerCase() === name.toLowerCase());
    if (known) {
      tagInput = '';
      attachTag(known);
      return;
    }
    busy = true;
    api
      .createTag(name)
      .then((t) => {
        allTags = [...allTags, t];
        tagInput = '';
        return commitTags([...currentTags, t]);
      })
      .catch((err: unknown) => {
        if ((err as api.ApiError)?.status === 409) {
          // lost a race / list was stale: refresh and attach the existing row
          api
            .listTags()
            .then((fresh) => {
              allTags = fresh;
              const t = fresh.find((x) => x.name.toLowerCase() === name.toLowerCase());
              if (t) {
                tagInput = '';
                return commitTags(
                  currentTags.some((x) => x.id === t.id) ? currentTags : [...currentTags, t],
                );
              }
              toast('Tag already exists', 'error');
            })
            .catch(() => toast('Could not save tag', 'error'));
          return;
        }
        toast(`Could not add tag: ${msg(err)}`, 'error');
      })
      .finally(() => (busy = false));
  }
</script>

<Dropdown
  label="Chat actions"
  align="end"
  buttonClass="relative inline-flex size-7 items-center justify-center rounded-full text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)] focus-visible:outline-2 focus-visible:outline-[color-mix(in_srgb,var(--indigo)_60%,transparent)]"
>
  <MoreHorizontal size={15} />
  {#if shared}
    <!-- mirrors the sidebar's "Shared" dot (lib/ui/Sidebar.svelte) -->
    <span class="absolute right-0.5 top-0.5 size-1.5 rounded-full bg-[var(--acc)]" title="Shared"></span>
  {/if}

  {#snippet items({ close })}
    <div class="w-60" use:hydrateTags>
      {#if shared}
        <MenuItem selected onclick={() => void copyLink(chat?.share_token)} {close}>
          {#snippet icon()}<Copy size={14} />{/snippet}
          Copy link
        </MenuItem>
        <MenuItem danger onclick={() => void doUnshare()} {close}>
          {#snippet icon()}<Unlink size={14} />{/snippet}
          Unshare
        </MenuItem>
      {:else}
        <MenuItem onclick={() => void doShare()} {close}>
          {#snippet icon()}<Share2 size={14} />{/snippet}
          Share link
        </MenuItem>
      {/if}

      <div class="my-1 border-t" style="border-color:var(--border)"></div>

      <MenuItem onclick={() => exportChat('md')} {close}>
        {#snippet icon()}<Download size={14} />{/snippet}
        Export as Markdown (.md)
      </MenuItem>
      <MenuItem onclick={() => exportChat('json')} {close}>
        {#snippet icon()}<Download size={14} />{/snippet}
        Export as JSON (.json)
      </MenuItem>
      <MenuItem onclick={() => exportAll()} {close}>
        {#snippet icon()}<Archive size={14} />{/snippet}
        Export all data (.zip)
      </MenuItem>

      <div class="my-1 border-t" style="border-color:var(--border)"></div>

      <div class="px-2 pb-1.5 pt-0.5">
        <p class="mb-1 text-[11px] font-medium uppercase tracking-wide text-[var(--faint)]">Tags</p>
        {#if currentTags.length}
          <div class="mb-1.5 flex flex-wrap gap-1">
            {#each currentTags as t (t.id)}
              <button
                type="button"
                class="tagchip inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] transition-colors"
                style="--chip-h:{hueFrom(t.name)}"
                title="Remove “{t.name}”"
                disabled={busy}
                onclick={() => detachTag(t)}
              >
                {t.name}
                <X size={10} class="opacity-70" />
              </button>
            {/each}
          </div>
        {:else}
          <p class="mb-1.5 text-[11px] text-[var(--muted)]">No tags yet.</p>
        {/if}
        <div class="flex items-center gap-1">
          <input
            type="text"
            list="chat-actions-tag-options"
            bind:value={tagInput}
            autocomplete="off"
            placeholder="Add tag…"
            aria-label="Add tag"
            class="h-7 min-w-0 flex-1 rounded-lg bg-transparent px-2 text-[13px] text-[var(--text)] outline-none placeholder:text-[var(--faint)] focus-visible:bg-[var(--fill-hover)]"
            style="box-shadow:inset 0 0 0 1px var(--border)"
            onkeydown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                addTagByName();
              }
            }}
          />
          <button
            type="button"
            class="inline-flex size-7 shrink-0 items-center justify-center rounded-lg text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)] disabled:pointer-events-none disabled:opacity-40"
            title="Add tag"
            aria-label="Add tag"
            disabled={busy || !tagInput.trim()}
            onclick={addTagByName}
          >
            <Plus size={14} />
          </button>
        </div>
        <datalist id="chat-actions-tag-options">
          {#each allTags as t (t.id)}
            <option value={t.name}></option>
          {/each}
        </datalist>
      </div>
    </div>
  {/snippet}
</Dropdown>

<style>
  /* same hue-from-name chips as the sidebar tag filter (lib/hue) */
  .tagchip {
    border: 0;
    cursor: pointer;
    background: hsl(var(--chip-h, 260) 45% 42% / 0.12);
    color: hsl(var(--chip-h, 260) 45% 32%);
  }
  .tagchip:hover:not(:disabled) {
    background: hsl(var(--chip-h, 260) 50% 40% / 0.2);
    color: hsl(var(--chip-h, 260) 50% 24%);
  }
  :global(html.dark) .tagchip {
    background: hsl(var(--chip-h, 260) 65% 65% / 0.14);
    color: hsl(var(--chip-h, 260) 65% 78%);
  }
  :global(html.dark) .tagchip:hover:not(:disabled) {
    background: hsl(var(--chip-h, 260) 65% 65% / 0.24);
    color: hsl(var(--chip-h, 260) 70% 88%);
  }
</style>
