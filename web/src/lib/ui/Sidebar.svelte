<script lang="ts">
  // App sidebar (FE-shell): chat list w/ search + folders + tags + pinned +
  // day grouping, row hover menu (rename/pin/move/delete), user menu.
  // Styling follows docs/DESIGN-POLISH.md §3 — quiet rows on --bg-deep.
  import * as api from '$lib/api';
  import type { Chat, Folder, Tag } from '$lib/api';
  import { currentRoute, location, navigate } from '$lib/router.svelte.js';
  import { session, logout } from '$lib/session.svelte.js';
  import { theme, toggleTheme } from '$lib/theme.svelte.js';
  import { push } from '$lib/toast.svelte.js';
  import { ApiError } from '$lib/api';
  import { confirmDialog } from '$lib/ui/confirm.svelte.js';
  import { groupByDay, timeAgo } from '$lib/ui/chat-grouping';
  import { hueFrom } from '$lib/hue';
  import { openAbout } from '$lib/ui/about.svelte.js';
  import Button from '$lib/ui/Button.svelte';
  import IconButton from '$lib/ui/IconButton.svelte';
  import Input from '$lib/ui/Input.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Dropdown from '$lib/ui/Dropdown.svelte';
  import MenuItem from '$lib/ui/MenuItem.svelte';
  import Tooltip from '$lib/ui/Tooltip.svelte';
  import Avatar from '$lib/ui/Avatar.svelte';
  import Skeleton from '$lib/ui/Skeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import {
    Archive,
    ArchiveRestore,
    ArrowLeft,
    BookOpen,
    ChevronDown,
    ChevronRight,
    ChevronUp,
    Clock,
    Folder as FolderIcon,
    FolderInput,
    FolderOpen,
    FolderPlus,
    Image as ImageIcon,
    Info,
    LogOut,
    MessageSquare,
    Moon,
    MoreHorizontal,
    PanelLeftClose,
    PanelLeftOpen,
    Pencil,
    Pin,
    PinOff,
    Plus,
    Search,
    Settings,
    ShieldCheck,
    SquarePen,
    StickyNote,
    Sun,
    Trash2,
  } from 'lucide-svelte';

  let {
    rail = false,
    mobileOpen = false,
    onToggleCollapse,
    onCloseMobile,
  }: {
    /** true → 72px icons-only (desktop collapsed). */
    rail?: boolean;
    mobileOpen?: boolean;
    onToggleCollapse?: () => void;
    onCloseMobile?: () => void;
  } = $props();

  const route = $derived(currentRoute());
  const activeId = $derived(route.name === 'chat' ? route.params.id : '');
  const user = $derived(session.user);
  const pending = $derived(user?.role === 'pending');

  // ----- state -----
  let chats = $state<Chat[]>([]);
  let folders = $state<Folder[]>([]);
  let tags = $state<Tag[]>([]);
  let loading = $state(true);
  let total = $state(0);
  let page = 1;
  let seq = 0;

  let query = $state('');
  let debounced = $state('');
  let activeTag = $state<string | null>(null);
  // Archive lane (OWUI parity): true = the list below shows ARCHIVED chats
  // only; pinned/folder/day grouping stands down for a flat, restorable list.
  let archivedView = $state(false);

  let collapsedFolders = $state<Set<string>>(loadCollapsedFolders());
  let renamingId = $state<string | null>(null);
  let renameValue = $state('');
  let renameEl = $state<HTMLInputElement | undefined>();

  let movingChat = $state<Chat | null>(null);
  let moveChoice = $state('');
  let folderModalOpen = $state(false);
  let newFolderName = $state('');

  let navEl = $state<HTMLElement | undefined>();

  function loadCollapsedFolders(): Set<string> {
    try {
      return new Set(JSON.parse(localStorage.getItem('chatgo:folders-collapsed') ?? '[]') as string[]);
    } catch {
      return new Set();
    }
  }
  function toggleFolder(id: string) {
    const next = new Set(collapsedFolders);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    collapsedFolders = next;
    try {
      localStorage.setItem('chatgo:folders-collapsed', JSON.stringify([...next]));
    } catch {
      /* ignore */
    }
  }

  // ----- loading -----
  async function loadChats(p: number) {
    const my = ++seq;
    loading = true;
    try {
      const res = await api.listChats({
        q: debounced || undefined,
        tag: activeTag ?? undefined,
        archived: archivedView || undefined,
        page: p,
        per: 50,
      });
      if (my !== seq) return;
      chats = p === 1 ? res.chats : [...chats, ...res.chats];
      total = res.total;
      page = p;
    } catch (e) {
      if (my !== seq) return;
      chats = p === 1 ? [] : chats;
      total = 0;
      push(e instanceof ApiError ? e.message : 'Failed to load chats', 'error');
    } finally {
      if (my === seq) loading = false;
    }
  }

  async function loadMeta() {
    try {
      const [f, t] = await Promise.all([api.listFolders(), api.listTags()]);
      folders = f;
      tags = t;
    } catch {
      folders = [];
      tags = [];
    }
  }

  // debounce search → api list q= (300ms per brief)
  $effect(() => {
    const t = setTimeout(() => (debounced = query), 300);
    return () => clearTimeout(t);
  });

  $effect(() => {
    if (!user || pending) {
      loading = false;
      return;
    }
    void location.path; // refresh list after navigation (new chats, titles)
    void debounced;
    void activeTag;
    void archivedView;
    void loadChats(1);
  });

  $effect(() => {
    if (user && !pending) void loadMeta();
  });

  $effect(() => {
    if (renamingId && renameEl) {
      renameEl.focus();
      renameEl.select();
    }
  });

  // ----- derived list structure -----
  const filtering = $derived(Boolean(debounced) || Boolean(activeTag));
  // Archived lane: server ignores ?q for archived lists, so title-match here.
  const archivedShown = $derived(
    debounced ? chats.filter((c) => c.title.toLowerCase().includes(debounced.toLowerCase())) : chats,
  );
  const pinned = $derived(chats.filter((c) => c.pinned));
  const rest = $derived(chats.filter((c) => !c.pinned));
  const folderSections = $derived(
    filtering
      ? []
      : folders
          .map((f) => ({ folder: f, chats: rest.filter((c) => c.folder_id === f.id) }))
          .filter((s) => s.chats.length > 0),
  );
  const folderChatIds = $derived(new Set(folderSections.flatMap((s) => s.chats.map((c) => c.id))));
  const looseChats = $derived(rest.filter((c) => !folderChatIds.has(c.id)));
  const groups = $derived(groupByDay(looseChats));
  const hasAny = $derived(chats.length > 0);

  // ----- row actions -----
  function go(id: string) {
    navigate(`/c/${id}`);
    onCloseMobile?.();
  }

  function newChat() {
    navigate('/');
    onCloseMobile?.();
  }

  function startRename(c: Chat) {
    renamingId = c.id;
    renameValue = c.title;
  }
  async function commitRename(c: Chat) {
    const t = renameValue.trim();
    renamingId = null;
    if (!t || t === c.title) return;
    try {
      const updated = await api.patchChat(c.id, { title: t });
      chats = chats.map((x) => (x.id === c.id ? updated : x));
    } catch (e) {
      push(e instanceof ApiError ? e.message : 'Rename failed', 'error');
    }
  }

  async function togglePin(c: Chat) {
    try {
      const updated = await api.patchChat(c.id, { pinned: !c.pinned });
      chats = chats.map((x) => (x.id === c.id ? updated : x));
    } catch (e) {
      push(e instanceof ApiError ? e.message : 'Update failed', 'error');
    }
  }

  // Archive/restore: the row leaves the current lane immediately (server
  // truth: archived chats drop out of the active list and vice versa).
  async function toggleArchive(c: Chat) {
    try {
      if (archivedView) await api.unarchiveChat(c.id);
      else await api.archiveChat(c.id);
      chats = chats.filter((x) => x.id !== c.id);
      total = Math.max(0, total - 1);
      if (!archivedView && activeId === c.id) navigate('/');
      push(archivedView ? 'Chat restored' : 'Chat archived', 'success');
    } catch (e) {
      push(e instanceof ApiError ? e.message : 'Update failed', 'error');
    }
  }

  async function removeChat(c: Chat) {
    const ok = await confirmDialog({
      title: 'Delete chat?',
      message: `“${c.title || 'Untitled'}” will be permanently deleted. This cannot be undone.`,
      confirmLabel: 'Delete',
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteChat(c.id);
      chats = chats.filter((x) => x.id !== c.id);
      total = Math.max(0, total - 1);
      if (activeId === c.id) navigate('/');
      push('Chat deleted', 'success');
    } catch (e) {
      push(e instanceof ApiError ? e.message : 'Delete failed', 'error');
    }
  }

  function startMove(c: Chat) {
    movingChat = c;
    moveChoice = c.folder_id ?? '';
  }
  async function commitMove() {
    if (!movingChat) return;
    try {
      await api.setChatFolder(movingChat.id, moveChoice || null);
      chats = chats.map((x) => (x.id === movingChat!.id ? { ...x, folder_id: moveChoice || null } : x));
      push('Moved', 'success');
    } catch (e) {
      push(e instanceof ApiError ? e.message : 'Move failed', 'error');
    }
    movingChat = null;
  }

  async function createFolder() {
    const name = newFolderName.trim();
    if (!name) return;
    try {
      await api.createFolder(name);
      await loadMeta();
      push('Folder created', 'success');
    } catch (e) {
      push(e instanceof ApiError ? e.message : 'Could not create folder', 'error');
    }
    newFolderName = '';
    folderModalOpen = false;
  }

  let renamingFolder = $state<string | null>(null);
  let folderRenameValue = $state('');
  async function commitFolderRename(f: Folder) {
    const name = folderRenameValue.trim();
    renamingFolder = null;
    if (!name || name === f.name) return;
    try {
      await api.updateFolder(f.id, { name });
      await loadMeta();
    } catch (e) {
      push(e instanceof ApiError ? e.message : 'Rename failed', 'error');
    }
  }
  async function removeFolder(f: Folder) {
    const ok = await confirmDialog({
      title: 'Delete folder?',
      message: `“${f.name}” is deleted. Its chats stay and move back to the main list.`,
      confirmLabel: 'Delete folder',
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteFolder(f.id);
      await loadMeta();
      chats = chats.map((c) => (c.folder_id === f.id ? { ...c, folder_id: null } : c));
    } catch (e) {
      push(e instanceof ApiError ? e.message : 'Delete failed', 'error');
    }
  }

  function onTagClick(id: string) {
    activeTag = activeTag === id ? null : id;
  }

  // arrow-key navigation across chat rows / folder headers
  function onNavKeys(e: KeyboardEvent) {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    const items = navEl ? [...navEl.querySelectorAll<HTMLElement>('[data-row]')] : [];
    if (items.length === 0) return;
    e.preventDefault();
    const i = items.indexOf(document.activeElement as HTMLElement);
    const next =
      e.key === 'ArrowDown' ? (i < 0 ? 0 : Math.min(items.length - 1, i + 1)) : i <= 0 ? items.length - 1 : Math.max(0, i - 1);
    items[next]?.focus();
  }

  async function doLogout() {
    await logout();
    navigate('/login');
  }

  const displayName = $derived(user?.name || user?.email || 'Signed out');

  // Live re-title: ChatView broadcasts chatgo:title when the server
  // auto-names a chat. The sidebar holds its own fetched list, so patch the
  // matching row in place — otherwise 'New chat' lingers until a reload.
  $effect(() => {
    const onTitle = (e: Event) => {
      const d = (e as CustomEvent<{ chat_id?: string; title?: string }>).detail;
      if (d?.chat_id && d.title) {
        chats = chats.map((c) => (c.id === d.chat_id ? { ...c, title: d.title! } : c));
      }
    };
    window.addEventListener('chatgo:title', onTitle);
    return () => window.removeEventListener('chatgo:title', onTitle);
  });

  const rowFocus =
    'focus-visible:outline-2 focus-visible:-outline-offset-1 focus-visible:outline-[color-mix(in_srgb,var(--acc)_60%,transparent)]';
</script>

<!-- row snippet (shared by pinned / folder / day sections) -->
{#snippet chatRow(c: Chat)}
    {#if renamingId === c.id}
      <form
        class="px-1.5 py-0.5"
        onsubmit={(e) => {
          e.preventDefault();
          void commitRename(c);
        }}
      >
        <input
          bind:this={renameEl}
          bind:value={renameValue}
          class="h-9 w-full rounded-lg border-0 px-2.5 text-sm text-[var(--text)] outline-none"
          style="background:var(--panel-2); box-shadow:0 0 0 2px color-mix(in srgb, var(--acc) 55%, transparent)"
          onkeydown={(e) => {
            if (e.key === 'Escape') {
              e.stopPropagation();
              renamingId = null;
            }
          }}
          onblur={() => void commitRename(c)}
        />
      </form>
    {:else}
      <div
        class="chat-row group flex h-9 items-center rounded-lg {activeId === c.id
          ? 'bg-[var(--acc-tint)] text-[var(--acc)]'
          : 'text-[var(--muted)] hover:bg-[var(--fill-hover)] hover:text-[var(--text)]'}"
      >
        <a
          data-row
          href="/c/{c.id}"
          onclick={(e) => {
            e.preventDefault();
            go(c.id);
          }}
          class="flex h-full min-w-0 flex-1 items-center gap-1.5 rounded-lg px-2.5 text-sm {rowFocus}"
        >
          {#if c.pinned}<Pin class="size-3 shrink-0 opacity-60" />{/if}
          {#if c.share_token}<span class="size-1.5 shrink-0 rounded-full bg-[var(--acc)]" title="Shared"></span>{/if}
          <span class="truncate">{c.title || 'Untitled'}</span>
        </a>
        <span class="time-ago shrink-0 pr-1.5 text-[11px] tabular-nums text-[var(--faint)] group-hover:hidden group-focus-within:hidden">
          {timeAgo(c.updated_at)}
        </span>
        <span class="shrink-0 pr-1 opacity-0 transition-opacity duration-150 group-hover:opacity-100 group-focus-within:opacity-100 focus-within:opacity-100">
          <Dropdown label="Chat actions">
            <MoreHorizontal class="size-4" />
            {#snippet items({ close })}
              <MenuItem onclick={() => startRename(c)} {close}>
                {#snippet icon()}<Pencil class="size-4" />{/snippet}
                Rename
              </MenuItem>
              <MenuItem onclick={() => void togglePin(c)} {close}>
                {#snippet icon()}{#if c.pinned}<PinOff class="size-4" />{:else}<Pin class="size-4" />{/if}{/snippet}
                {c.pinned ? 'Unpin' : 'Pin to top'}
              </MenuItem>
              {#if archivedView}
                <MenuItem onclick={() => void toggleArchive(c)} {close}>
                  {#snippet icon()}<ArchiveRestore class="size-4" />{/snippet}
                  Restore to sidebar
                </MenuItem>
              {:else}
                <MenuItem onclick={() => startMove(c)} {close}>
                  {#snippet icon()}<FolderInput class="size-4" />{/snippet}
                  Move to folder…
                </MenuItem>
                <MenuItem onclick={() => void toggleArchive(c)} {close}>
                  {#snippet icon()}<Archive class="size-4" />{/snippet}
                  Archive
                </MenuItem>
              {/if}
              <div class="my-1 border-t" style="border-color:var(--border)"></div>
              <MenuItem danger onclick={() => void removeChat(c)} {close}>
                {#snippet icon()}<Trash2 class="size-4" />{/snippet}
                Delete
              </MenuItem>
            {/snippet}
          </Dropdown>
        </span>
      </div>
    {/if}
  {/snippet}

  {#snippet sectionLabel(text: string)}
    <p class="px-2.5 pt-3 pb-0.5 text-xs font-medium text-[var(--muted)]">{text}</p>
  {/snippet}

<!-- OWUI parity: sidebar shares the canvas background with a hairline
     divider. The old --bg-deep two-tone read as a misaligned floating
     panel at near-black dark values (user-reported "ghost strip"). -->
<aside
  class="chat-sidebar {mobileOpen ? 'is-open' : ''}"
  style="background:var(--bg); border-right:1px solid var(--border); width:{rail ? 72 : 260}px"
  aria-label="Sidebar"
>
  <!-- top row: toggle + new chat + brand glyph (§3) -->
  {#if rail}
    <div class="flex shrink-0 flex-col items-center gap-1 px-2 pt-2">
      <Tooltip text="Expand sidebar" side="right">
        <IconButton label="Expand sidebar" onclick={() => onToggleCollapse?.()}>
          <PanelLeftOpen class="size-[18px]" />
        </IconButton>
      </Tooltip>
      <Tooltip text="New chat" side="right">
        <IconButton label="New chat" onclick={newChat}>
          <SquarePen class="size-[18px]" />
        </IconButton>
      </Tooltip>
      <Tooltip text="Search chats" side="right">
        <IconButton label="Search chats" onclick={() => onToggleCollapse?.()}>
          <Search class="size-[18px]" />
        </IconButton>
      </Tooltip>
      <Tooltip text="Knowledge" side="right">
        <a
          href="/knowledge"
          aria-label="Knowledge"
          onclick={(e) => { e.preventDefault(); navigate('/knowledge'); }}
          class="grid size-9 place-items-center rounded-lg text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)] {rowFocus}"
        ><BookOpen class="size-[18px]" /></a>
      </Tooltip>
      <Tooltip text="Images" side="right">
        <a
          href="/images"
          aria-label="Images"
          onclick={(e) => { e.preventDefault(); navigate('/images'); }}
          class="grid size-9 place-items-center rounded-lg text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)] {rowFocus}"
        ><ImageIcon class="size-[18px]" /></a>
      </Tooltip>
      <Tooltip text="Notes" side="right">
        <a
          href="/notes"
          aria-label="Notes"
          onclick={(e) => { e.preventDefault(); navigate('/notes'); }}
          class="grid size-9 place-items-center rounded-lg text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)] {rowFocus}"
        ><StickyNote class="size-[18px]" /></a>
      </Tooltip>
    </div>
  {:else}
    <div class="flex h-12 shrink-0 items-center gap-0.5 px-2">
      <!-- ! important beats the base `inline-flex` utility IconButton
           applies (Tailwind v4 sorts .inline-flex after .hidden) -->
      <IconButton label="Collapse sidebar" onclick={() => onToggleCollapse?.()} class="!hidden md:!inline-flex">
        <PanelLeftClose class="size-[18px]" />
      </IconButton>
      <IconButton label="Close sidebar" onclick={() => onCloseMobile?.()} class="md:!hidden">
        <PanelLeftClose class="size-[18px]" />
      </IconButton>
      <IconButton label="New chat" onclick={newChat}>
        <SquarePen class="size-[18px]" />
      </IconButton>
      <span class="min-w-0 flex-1"></span>
      <a
        href="/"
        aria-label="ChatGo home"
        onclick={(e) => {
          e.preventDefault();
          navigate('/');
          onCloseMobile?.();
        }}
        class="grid size-7 shrink-0 place-items-center rounded-lg text-[12px] font-semibold text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)] {rowFocus}"
        >C</a
      >
    </div>
  {/if}

  <!-- search: ghost row with magnifier -->
  {#if !rail}
    <div class="shrink-0 px-2 pb-1">
      <div class="relative">
        <Search class="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-[var(--muted)]" />
        <input
          type="search"
          role="searchbox"
          aria-label="Search chats"
          placeholder="Search chats"
          class="h-9 w-full appearance-none rounded-lg border-0 bg-transparent pr-2.5 pl-9 text-sm text-[var(--text)] outline-none transition-colors placeholder:text-[var(--faint)] hover:bg-[var(--fill-hover)] focus-visible:bg-[var(--fill-hover)] focus-visible:outline-2 focus-visible:-outline-offset-1 focus-visible:outline-[color-mix(in_srgb,var(--acc)_60%,transparent)]"
          bind:value={query}
        />
      </div>
    </div>
    <!-- FE-PAGES: quiet nav rows (same styling as the rows above) -->
    <div class="shrink-0 px-2 pb-1">
      {#each [['/knowledge', 'Knowledge'], ['/images', 'Images'], ['/notes', 'Notes']] as [href, label] (href)}
        <a
          href={href}
          onclick={(e) => {
            e.preventDefault();
            navigate(href);
            onCloseMobile?.();
          }}
          class="flex h-9 items-center gap-1.5 rounded-lg px-2.5 text-sm transition-colors {rowFocus} {location.path.startsWith(href)
            ? 'bg-[var(--acc-tint)] text-[var(--acc)]'
            : 'text-[var(--muted)] hover:bg-[var(--fill-hover)] hover:text-[var(--text)]'}"
        >
          {#if href === '/knowledge'}<BookOpen class="size-4 shrink-0" />{:else if href === '/images'}<ImageIcon class="size-4 shrink-0" />{:else}<StickyNote class="size-4 shrink-0" />{/if}
          <span>{label}</span>
        </a>
      {/each}
    </div>
  {/if}

  <!-- tag chip filters: quiet chips, no border -->
  {#if !rail && tags.length > 0 && !pending && !archivedView}
    <div class="flex shrink-0 flex-wrap gap-1.5 px-2.5 pb-2" aria-label="Filter by tag">
      {#each tags as t (t.id)}
        <button
          type="button"
          class="tagchip rounded-full px-2.5 py-0.5 text-[11px] transition-colors focus-visible:outline-2 focus-visible:outline-[color-mix(in_srgb,var(--acc)_60%,transparent)]"
          style="--chip-h:{hueFrom(t.name)}"
          aria-pressed={activeTag === t.id}
          onclick={() => onTagClick(t.id)}
        >
          {t.name}
        </button>
      {/each}
    </div>
  {/if}

  <!-- pending banner -->
  {#if pending}
    <div class="mx-2.5 mb-2 rounded-xl px-3 py-2.5 text-xs leading-relaxed" style="background:var(--panel); color:var(--muted)">
      <span class="mb-1 flex items-center gap-1.5 font-medium text-[var(--text)]">
        <Clock class="size-3.5" /> Awaiting approval
      </span>
      An administrator needs to approve your account before chats unlock.
    </div>
    <div class="min-h-0 flex-1"></div>
  {:else if !rail}
    <!-- nav -->
    <nav
      bind:this={navEl}
      class="min-h-0 flex-1 overflow-y-auto px-2 pb-2"
      aria-label="Chats"
      onkeydown={onNavKeys}
    >
      {#if loading && !hasAny}
        <div class="space-y-2.5 px-1.5 pt-3">
          {#each Array(6) as _i}
            <Skeleton w="90%" />
          {/each}
        </div>
      {:else if !hasAny && !filtering}
        {#if archivedView}
          <EmptyState title="No archived chats" description="Archive a chat from its ⋯ menu — it leaves the sidebar but keeps everything, restorable any time.">
            <Button variant="secondary" size="sm" onclick={() => (archivedView = false)}>
              <ArrowLeft class="size-3.5" /> Back to chats
            </Button>
          </EmptyState>
        {:else}
          <EmptyState title="No chats yet" description="Start a new conversation — it will show up here.">
            <Button variant="secondary" size="sm" onclick={() => navigate('/')}>
              <Plus class="size-3.5" /> New chat
            </Button>
          </EmptyState>
        {/if}
      {:else if !hasAny}
        <p class="px-2 py-8 text-center text-xs text-[var(--muted)]">No chats match.</p>
      {:else}
        {#if archivedView}
          <!-- archive lane: flat, restorable; grouping/pinned/folders stand down -->
          {@render sectionLabel('Archived')}
          {#each archivedShown as c (c.id)}{@render chatRow(c)}{/each}
        {:else if filtering}
          <!-- flat search/tag results -->
          {@render sectionLabel(activeTag ? `Tag: ${tags.find((t) => t.id === activeTag)?.name ?? ''}` : 'Results')}
          {#each chats as c (c.id)}{@render chatRow(c)}{/each}
        {:else}
          {#if pinned.length > 0}
            {@render sectionLabel('Pinned')}
            {#each pinned as c (c.id)}{@render chatRow(c)}{/each}
          {/if}

          <!-- folders -->
          {#if folders.length > 0 || !rail}
            {#each folderSections as { folder: f, chats: fc } (f.id)}
              {@render folderBlock(f, fc)}
            {/each}
            <div class="mt-1 flex items-center justify-between pr-0.5 pl-2.5 pt-2">
              <p class="text-xs font-medium text-[var(--muted)]">Folders</p>
              <IconButton label="New folder" onclick={() => (folderModalOpen = true)}>
                <FolderPlus class="size-4" />
              </IconButton>
            </div>
            {#each folders as f (f.id)}
              {#if !folderSections.some((s) => s.folder.id === f.id)}
                {@render folderBlock(f, [])}
              {/if}
            {/each}
          {/if}

          <!-- day groups -->
          {#each groups as g (g.label)}
            {@render sectionLabel(g.label)}
            {#each g.chats as c (c.id)}{@render chatRow(c)}{/each}
          {/each}
        {/if}

        {#if chats.length < total}
          <Button variant="ghost" size="sm" full class="mt-2 text-[var(--muted)]" onclick={() => void loadChats(page + 1)}>
            Show more ({total - chats.length})
          </Button>
        {/if}
      {/if}
    </nav>
    <!-- archive lane toggle (OWUI: the archive lives quietly at the bottom) -->
    <button
      type="button"
      class="mx-2 mb-1 flex h-8 items-center gap-1.5 rounded-lg px-2.5 text-[13px] transition-colors {rowFocus} {archivedView
        ? 'text-[var(--acc)] hover:bg-[var(--acc-tint)]'
        : 'text-[var(--faint)] hover:bg-[var(--fill-hover)] hover:text-[var(--muted)]'}"
      onclick={() => (archivedView = !archivedView)}
    >
      {#if archivedView}<ArrowLeft class="size-4 shrink-0" />{:else}<Archive class="size-4 shrink-0" />{/if}
      <span>{archivedView ? 'Back to chats' : 'Archived'}</span>
    </button>
  {/if}

  <!-- rail: recent chats as icons -->
  {#if rail && !pending}
    <nav class="min-h-0 flex-1 space-y-1 overflow-y-auto px-2 pb-2" aria-label="Chats (icons)">
      {#each chats.slice(0, 30) as c (c.id)}
        <Tooltip text={c.title || 'Untitled'} side="right">
          <a
            href="/c/{c.id}"
            aria-label={c.title || 'Untitled chat'}
            onclick={(e) => {
              e.preventDefault();
              go(c.id);
            }}
            class="flex size-9 items-center justify-center rounded-lg text-[var(--muted)] transition-colors hover:bg-[var(--fill-hover)] hover:text-[var(--text)] focus-visible:outline-2 focus-visible:-outline-offset-1 focus-visible:outline-[color-mix(in_srgb,var(--acc)_60%,transparent)] {activeId === c.id
              ? 'bg-[var(--acc-tint)] text-[var(--acc)]'
              : ''}"
          >
            <MessageSquare class="size-4" />
          </a>
        </Tooltip>
      {/each}
    </nav>
  {/if}

  <!-- user block: quiet row that opens a floating account menu (§3) -->
  <div class="shrink-0 p-2" style="padding-bottom:max(0.5rem, env(safe-area-inset-bottom))">
    {#if user}
      {#if rail}
        <div class="flex flex-col items-center gap-1.5">
          <Tooltip text={displayName} side="right">
            <Avatar name={displayName} src={user.avatar_url ?? ''} size={30} />
          </Tooltip>
          <Tooltip text={theme.mode === 'dark' ? 'Light mode' : 'Dark mode'} side="right">
            <IconButton label="Toggle theme" onclick={toggleTheme}>
              {#if theme.mode === 'dark'}<Sun class="size-4" />{:else}<Moon class="size-4" />{/if}
            </IconButton>
          </Tooltip>
          <Tooltip text="Settings" side="right">
            <IconButton label="Settings" onclick={() => navigate('/settings')}>
              <Settings class="size-4" />
            </IconButton>
          </Tooltip>
          <Tooltip text="Sign out" side="right">
            <IconButton label="Sign out" onclick={() => void doLogout()}>
              <LogOut class="size-4" />
            </IconButton>
          </Tooltip>
        </div>
      {:else}
        <Dropdown
          label="Account menu"
          align="start"
          full
          menuClass="w-64"
          buttonClass="flex h-11 w-full min-w-0 items-center gap-2 rounded-lg px-2 text-left transition-colors hover:bg-[var(--fill-hover)] focus-visible:outline-2 focus-visible:-outline-offset-1 focus-visible:outline-[color-mix(in_srgb,var(--acc)_60%,transparent)]"
        >
          <Avatar name={displayName} src={user.avatar_url ?? ''} size={28} />
          <span class="min-w-0 flex-1">
            <span class="flex items-center gap-1.5 truncate text-sm font-medium">
              <span class="truncate">{displayName}</span>
              {#if user.role === 'admin'}
                <span
                  class="flex shrink-0 items-center gap-0.5 rounded-full px-1.5 py-px text-[9px] font-semibold tracking-wide uppercase"
                  style="background:var(--panel-2); color:var(--muted)"
                >
                  <ShieldCheck class="size-2.5" /> Admin
                </span>
              {/if}
            </span>
            {#if user.name}<span class="block truncate text-[11px] font-normal text-[var(--muted)]">{user.email}</span>{/if}
          </span>
          <ChevronUp class="size-4 shrink-0 text-[var(--muted)]" />
          {#snippet items({ close })}
            <MenuItem onclick={() => navigate('/settings')} {close}>
              {#snippet icon()}<Settings class="size-4" />{/snippet}
              Settings
            </MenuItem>
            <MenuItem onclick={toggleTheme} {close}>
              {#snippet icon()}{#if theme.mode === 'dark'}<Sun class="size-4" />{:else}<Moon class="size-4" />{/if}{/snippet}
              {theme.mode === 'dark' ? 'Light mode' : 'Dark mode'}
            </MenuItem>
            {#if user.role === 'admin'}
              <MenuItem onclick={() => navigate('/admin')} {close}>
                {#snippet icon()}<ShieldCheck class="size-4" />{/snippet}
                Admin
              </MenuItem>
            {/if}
            <MenuItem onclick={openAbout} {close}>
              {#snippet icon()}<Info class="size-4" />{/snippet}
              About
            </MenuItem>
            <div class="my-1 border-t" style="border-color:var(--border)"></div>
            <MenuItem danger onclick={() => void doLogout()} {close}>
              {#snippet icon()}<LogOut class="size-4" />{/snippet}
              Log out
            </MenuItem>
          {/snippet}
        </Dropdown>
      {/if}
    {:else}
      <Button variant="outline" size="sm" full onclick={() => navigate('/login')}>Sign in</Button>
    {/if}
  </div>
</aside>

<!-- folder collapse / expand block -->
{#snippet folderBlock(f: Folder, fc: Chat[])}
  <div class="mt-0.5">
    {#if renamingFolder === f.id}
      <form
        class="px-1.5 py-0.5"
        onsubmit={(e) => {
          e.preventDefault();
          void commitFolderRename(f);
        }}
      >
        <input
          bind:value={folderRenameValue}
          autofocus
          class="h-9 w-full rounded-lg border-0 px-2.5 text-sm text-[var(--text)] outline-none"
          style="background:var(--panel-2); box-shadow:0 0 0 2px color-mix(in srgb, var(--acc) 55%, transparent)"
          onkeydown={(e) => {
            if (e.key === 'Escape') {
              e.stopPropagation();
              renamingFolder = null;
            }
          }}
          onblur={() => void commitFolderRename(f)}
        />
      </form>
    {:else}
      <div class="group flex h-9 items-center rounded-lg transition-colors hover:bg-[var(--fill-hover)]">
        <button
          data-row
          type="button"
          class="flex h-full min-w-0 flex-1 items-center gap-1.5 rounded-lg px-2.5 text-sm font-medium text-[var(--text)] {rowFocus}"
          aria-expanded={!collapsedFolders.has(f.id)}
          onclick={() => toggleFolder(f.id)}
        >
          {#if collapsedFolders.has(f.id)}
            <ChevronRight class="size-4 shrink-0 text-[var(--muted)]" />
            <FolderIcon class="size-4 shrink-0 text-[var(--muted)]" />
          {:else}
            <ChevronDown class="size-4 shrink-0 text-[var(--muted)]" />
            <FolderOpen class="size-4 shrink-0 text-[var(--acc)]" />
          {/if}
          <span class="truncate">{f.name}</span>
          {#if fc.length > 0}
            <span class="ml-auto shrink-0 pr-1 text-[11px] text-[var(--faint)]">{fc.length}</span>
          {/if}
        </button>
        <Dropdown label={`Folder actions for ${f.name}`}>
          <MoreHorizontal class="size-4" />
          {#snippet items({ close })}
            <MenuItem
              onclick={() => {
                renamingFolder = f.id;
                folderRenameValue = f.name;
              }}
              {close}
            >
              {#snippet icon()}<Pencil class="size-4" />{/snippet}
              Rename folder
            </MenuItem>
            <MenuItem danger onclick={() => void removeFolder(f)} {close}>
              {#snippet icon()}<Trash2 class="size-4" />{/snippet}
              Delete folder
            </MenuItem>
          {/snippet}
        </Dropdown>
      </div>
      {#if !collapsedFolders.has(f.id)}
        <div class="ml-3.5 border-l pl-2" style="border-color:var(--border)">
          {#each fc as c (c.id)}{@render chatRow(c)}{/each}
        </div>
      {/if}
    {/if}
  </div>
{/snippet}

<!-- move-to-folder modal -->
<Modal open={movingChat !== null} title="Move to folder" width="max-w-sm" onclose={() => (movingChat = null)}>
  <div class="space-y-1" role="radiogroup" aria-label="Choose folder">
    <label
      class="flex cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm transition-colors hover:bg-[var(--fill-hover)]"
    >
      <input type="radio" name="move-folder" value="" bind:group={moveChoice} class="accent-[var(--acc)]" />
      <MessageSquare class="size-4 text-[var(--muted)]" /> No folder
    </label>
    {#each folders as f (f.id)}
      <label
        class="flex cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm transition-colors hover:bg-[var(--fill-hover)]"
      >
        <input type="radio" name="move-folder" value={f.id} bind:group={moveChoice} class="accent-[var(--acc)]" />
        <FolderIcon class="size-4 text-[var(--muted)]" /> {f.name}
      </label>
    {/each}
  </div>
  {#snippet footer()}
    <Button variant="secondary" size="sm" onclick={() => (movingChat = null)}>Cancel</Button>
    <Button variant="primary" size="sm" onclick={() => void commitMove()}>Move</Button>
  {/snippet}
</Modal>

<!-- new folder modal -->
<Modal bind:open={folderModalOpen} title="New folder" width="max-w-sm">
  <form
    onsubmit={(e) => {
      e.preventDefault();
      void createFolder();
    }}
    class="space-y-3"
  >
    <Input label="Name" placeholder="e.g. Work" bind:value={newFolderName} autocomplete="off" />
    <div class="flex justify-end gap-2 pt-1">
      <Button variant="secondary" size="sm" onclick={() => (folderModalOpen = false)}>Cancel</Button>
      <Button variant="primary" size="sm" type="submit" disabled={!newFolderName.trim()}>Create</Button>
    </div>
  </form>
</Modal>

<style>
  /* tag chips (FE-COLOR): quiet pills that cycle hues — deterministic hue per
     tag name via the --chip-h custom property (lib/hue). Active = stronger
     tint of the same hue; sizes/spacing unchanged. */
  .tagchip {
    border: 0;
    cursor: pointer;
    background: hsl(var(--chip-h, 260) 45% 42% / 0.12);
    color: hsl(var(--chip-h, 260) 45% 32%);
  }
  .tagchip:hover {
    background: hsl(var(--chip-h, 260) 50% 40% / 0.2);
    color: hsl(var(--chip-h, 260) 50% 24%);
  }
  .tagchip[aria-pressed='true'] {
    background: hsl(var(--chip-h, 260) 52% 42% / 0.24);
    color: hsl(var(--chip-h, 260) 55% 23%);
  }
  :global(html.dark) .tagchip {
    background: hsl(var(--chip-h, 260) 65% 65% / 0.14);
    color: hsl(var(--chip-h, 260) 60% 72%);
  }
  :global(html.dark) .tagchip:hover {
    background: hsl(var(--chip-h, 260) 65% 65% / 0.22);
    color: hsl(var(--chip-h, 260) 65% 80%);
  }
  :global(html.dark) .tagchip[aria-pressed='true'] {
    background: hsl(var(--chip-h, 260) 68% 68% / 0.28);
    color: hsl(var(--chip-h, 260) 75% 84%);
  }

  /* off-canvas below md, static column at md+ (scoped so it beats utilities) */
  .chat-sidebar {
    position: fixed;
    inset-block: 0;
    left: 0;
    z-index: 40;
    display: flex;
    flex-direction: column;
    flex-shrink: 0;
    transform: translateX(-100%);
    transition:
      transform 0.2s ease,
      width 0.15s ease;
  }
  .chat-sidebar.is-open {
    transform: none;
    box-shadow: 0 0 40px rgb(0 0 0 / 0.5);
  }
  @media (min-width: 768px) {
    .chat-sidebar {
      position: relative;
      transform: none;
      box-shadow: none;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .chat-sidebar {
      transition: none;
    }
  }
</style>
