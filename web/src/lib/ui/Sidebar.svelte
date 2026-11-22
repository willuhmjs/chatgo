<script lang="ts">
  // App sidebar (FE-shell): chat list w/ search + folders + tags + pinned +
  // day grouping, row hover menu (rename/pin/move/delete), user block.
  import * as api from '$lib/api';
  import type { Chat, Folder, Tag } from '$lib/api';
  import { currentRoute, location, navigate } from '$lib/router.svelte.js';
  import { session, logout } from '$lib/session.svelte.js';
  import { theme, toggleTheme } from '$lib/theme.svelte.js';
  import { push } from '$lib/toast.svelte.js';
  import { ApiError } from '$lib/api';
  import { confirmDialog } from '$lib/ui/confirm.svelte.js';
  import { groupByDay, timeAgo } from '$lib/ui/chat-grouping';
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
    ChevronDown,
    ChevronRight,
    Clock,
    Folder as FolderIcon,
    FolderInput,
    FolderOpen,
    FolderPlus,
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
</script>

<!-- row snippet (shared by pinned / folder / day sections) -->
{#snippet chatRow(c: Chat)}
    {#if renamingId === c.id}
      <form
        class="px-1 py-0.5"
        onsubmit={(e) => {
          e.preventDefault();
          void commitRename(c);
        }}
      >
        <input
          bind:this={renameEl}
          bind:value={renameValue}
          class="w-full rounded-md border px-2 py-1 text-sm outline-none"
          style="background:var(--panel-2); border-color:var(--accent); color:var(--text)"
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
        class="chat-row group flex items-center rounded-lg {activeId === c.id
          ? 'bg-[color-mix(in_srgb,var(--accent)_14%,transparent)] text-[var(--text)]'
          : 'text-[var(--muted)] hover:bg-[color-mix(in_srgb,var(--text)_7%,transparent)] hover:text-[var(--text)]'}"
      >
        <a
          data-row
          href="/c/{c.id}"
          onclick={(e) => {
            e.preventDefault();
            go(c.id);
          }}
          class="flex h-8 min-w-0 flex-1 items-center gap-1.5 rounded-lg px-2 text-[13px] focus-visible:outline-2 focus-visible:outline-[color-mix(in_srgb,var(--accent)_70%,transparent)]"
        >
          {#if c.pinned}<Pin class="size-3 shrink-0 opacity-60" />{/if}
          {#if c.share_token}<span class="size-1.5 shrink-0 rounded-full bg-[var(--accent)]" title="Shared"></span>{/if}
          <span class="truncate">{c.title || 'Untitled'}</span>
        </a>
        <span class="time-ago shrink-0 pr-1.5 text-[10px] tabular-nums opacity-50 group-hover:hidden group-focus-within:hidden">
          {timeAgo(c.updated_at)}
        </span>
        <span class="pr-1 invisible opacity-0 group-hover:visible group-hover:opacity-100 group-focus-within:visible group-focus-within:opacity-100">
          <Dropdown label="Chat actions">
            <MoreHorizontal class="size-4" />
            {#snippet items({ close })}
              <MenuItem onclick={() => startRename(c)} {close}>
                {#snippet icon()}<Pencil class="size-3.5" />{/snippet}
                Rename
              </MenuItem>
              <MenuItem onclick={() => void togglePin(c)} {close}>
                {#snippet icon()}{#if c.pinned}<PinOff class="size-3.5" />{:else}<Pin class="size-3.5" />{/if}{/snippet}
                {c.pinned ? 'Unpin' : 'Pin to top'}
              </MenuItem>
              <MenuItem onclick={() => startMove(c)} {close}>
                {#snippet icon()}<FolderInput class="size-3.5" />{/snippet}
                Move to folder…
              </MenuItem>
              <div class="my-1 border-t" style="border-color:var(--border)"></div>
              <MenuItem danger onclick={() => void removeChat(c)} {close}>
                {#snippet icon()}<Trash2 class="size-3.5" />{/snippet}
                Delete
              </MenuItem>
            {/snippet}
          </Dropdown>
        </span>
      </div>
    {/if}
  {/snippet}

  {#snippet sectionLabel(text: string)}
    <p class="px-2 pt-3 pb-1 text-[11px] font-semibold tracking-wide text-[var(--muted)] uppercase">{text}</p>
  {/snippet}

<aside
  class="chat-sidebar {mobileOpen ? 'is-open' : ''}"
  style="background:var(--panel); border-color:var(--border); width:{rail ? 72 : 260}px"
  aria-label="Sidebar"
>
  <!-- header -->
  <div class="flex h-14 shrink-0 items-center gap-2 px-3">
    {#if rail}
      <Tooltip text="Expand sidebar" side="right">
        <IconButton label="Expand sidebar" onclick={() => onToggleCollapse?.()}>
          <PanelLeftOpen class="size-4" />
        </IconButton>
      </Tooltip>
    {:else}
      <a
        href="/"
        onclick={(e) => {
          e.preventDefault();
          navigate('/');
          onCloseMobile?.();
        }}
        class="flex min-w-0 flex-1 items-center gap-2 rounded-lg focus-visible:outline-2 focus-visible:outline-[color-mix(in_srgb,var(--accent)_70%,transparent)]"
      >
        <span
          class="grid size-7 shrink-0 place-items-center rounded-lg text-[13px] font-bold text-white"
          style="background:linear-gradient(135deg,var(--accent),#8b5cf6)"
        >C</span>
        <span class="truncate text-sm font-semibold">ChatGo</span>
      </a>
      <IconButton label="Collapse sidebar" onclick={() => onToggleCollapse?.()} class="hidden md:inline-flex">
        <PanelLeftClose class="size-4" />
      </IconButton>
      <IconButton label="Close sidebar" onclick={() => onCloseMobile?.()} class="md:hidden">
        <PanelLeftClose class="size-4" />
      </IconButton>
    {/if}
  </div>

  <!-- new chat -->
  <div class="shrink-0 px-3 pb-2">
    {#if rail}
      <Tooltip text="New chat" side="right">
        <IconButton label="New chat" onclick={() => { navigate('/'); onCloseMobile?.(); }}>
          <Plus class="size-5" />
        </IconButton>
      </Tooltip>
    {:else}
      <Button variant="gradient" full onclick={() => { navigate('/'); onCloseMobile?.(); }}>
        <Plus class="size-4" /> New chat
      </Button>
    {/if}
  </div>

  <!-- search -->
  <div class="shrink-0 px-3 pb-2">
    {#if rail}
      <Tooltip text="Search chats" side="right">
        <IconButton label="Search chats" onclick={() => onToggleCollapse?.()}>
          <Search class="size-4" />
        </IconButton>
      </Tooltip>
    {:else}
      <div class="relative">
        <Search class="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-[var(--muted)]" />
        <input
          type="search"
          role="searchbox"
          aria-label="Search chats"
          placeholder="Search chats…"
          class="h-8 w-full rounded-lg border pr-2 pl-8 text-[13px] outline-none focus-visible:border-[var(--accent)] focus-visible:shadow-[0_0_0_3px_color-mix(in_srgb,var(--accent)_22%,transparent)]"
          style="background:var(--panel-2); border-color:var(--border); color:var(--text)"
          bind:value={query}
        />
      </div>
    {/if}
  </div>

  <!-- tag chip filters -->
  {#if !rail && tags.length > 0 && !pending}
    <div class="flex shrink-0 flex-wrap gap-1.5 px-3 pb-2" aria-label="Filter by tag">
      {#each tags as t (t.id)}
        <button
          type="button"
          class="rounded-full border px-2 py-0.5 text-[11px] transition-colors focus-visible:outline-2 focus-visible:outline-[color-mix(in_srgb,var(--accent)_70%,transparent)] {activeTag === t.id
            ? 'border-[var(--accent)] text-[var(--accent)]'
            : 'text-[var(--muted)] hover:text-[var(--text)]'}"
          style="border-color:{activeTag === t.id ? 'var(--accent)' : 'var(--border)'}; background:{activeTag === t.id
            ? 'color-mix(in srgb, var(--accent) 12%, transparent)'
            : 'transparent'}"
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
    <div class="mx-3 mb-2 rounded-lg border px-3 py-2.5 text-xs leading-relaxed" style="background:var(--panel-2); border-color:var(--border); color:var(--muted)">
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
        <div class="space-y-2.5 px-1 pt-3">
          {#each Array(6) as _i}
            <Skeleton w="90%" />
          {/each}
        </div>
      {:else if !hasAny && !filtering}
        <EmptyState title="No chats yet" description="Start a new conversation — it will show up here.">
          <Button variant="secondary" size="sm" onclick={() => navigate('/')}>
            <Plus class="size-3.5" /> New chat
          </Button>
        </EmptyState>
      {:else if !hasAny}
        <p class="px-2 py-8 text-center text-xs text-[var(--muted)]">No chats match.</p>
      {:else}
        {#if filtering}
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
            <div class="mt-1 flex items-center justify-between px-2 pt-2">
              <p class="text-[11px] font-semibold tracking-wide text-[var(--muted)] uppercase">Folders</p>
              <IconButton label="New folder" onclick={() => (folderModalOpen = true)}>
                <FolderPlus class="size-3.5" />
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
          <Button variant="ghost" size="sm" full class="mt-2" onclick={() => void loadChats(page + 1)}>
            Show more ({total - chats.length})
          </Button>
        {/if}
      {/if}
    </nav>
  {:else}
    <div class="min-h-0 flex-1"></div>
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
            class="flex size-9 items-center justify-center rounded-lg text-[var(--muted)] hover:bg-[color-mix(in_srgb,var(--text)_8%,transparent)] hover:text-[var(--text)] focus-visible:outline-2 focus-visible:outline-[color-mix(in_srgb,var(--accent)_70%,transparent)] {activeId === c.id
              ? 'bg-[color-mix(in_srgb,var(--accent)_16%,transparent)] text-[var(--accent)]'
              : ''}"
          >
            <MessageSquare class="size-4" />
          </a>
        </Tooltip>
      {/each}
    </nav>
  {/if}

  <!-- user block -->
  <div
    class="shrink-0 border-t p-3"
    style="border-color:var(--border); padding-bottom:max(0.75rem, env(safe-area-inset-bottom))"
  >
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
        <div class="flex items-center gap-2">
          <Avatar name={displayName} src={user.avatar_url ?? ''} size={30} />
          <div class="min-w-0 flex-1">
            <p class="flex items-center gap-1 truncate text-[13px] font-medium">
              <span class="truncate">{displayName}</span>
              {#if user.role === 'admin'}
                <span
                  class="flex shrink-0 items-center gap-0.5 rounded-full px-1.5 py-px text-[9px] font-semibold tracking-wide uppercase"
                  style="background:color-mix(in srgb, var(--accent) 18%, transparent); color:var(--accent)"
                >
                  <ShieldCheck class="size-2.5" /> Admin
                </span>
              {/if}
            </p>
            {#if user.name}<p class="truncate text-[11px] text-[var(--muted)]">{user.email}</p>{/if}
          </div>
          <IconButton label={theme.mode === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'} onclick={toggleTheme}>
            {#if theme.mode === 'dark'}<Sun class="size-4" />{:else}<Moon class="size-4" />{/if}
          </IconButton>
          <IconButton label="Settings" onclick={() => navigate('/settings')}>
            <Settings class="size-4" />
          </IconButton>
          <IconButton label="Sign out" variant="danger" onclick={() => void doLogout()}>
            <LogOut class="size-4" />
          </IconButton>
        </div>
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
        class="px-1 py-0.5"
        onsubmit={(e) => {
          e.preventDefault();
          void commitFolderRename(f);
        }}
      >
        <input
          bind:value={folderRenameValue}
          autofocus
          class="w-full rounded-md border px-2 py-1 text-sm outline-none"
          style="background:var(--panel-2); border-color:var(--accent); color:var(--text)"
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
      <div class="group flex items-center rounded-lg hover:bg-[color-mix(in_srgb,var(--text)_7%,transparent)]">
        <button
          data-row
          type="button"
          class="flex h-8 min-w-0 flex-1 items-center gap-1.5 rounded-lg px-2 text-[13px] font-medium text-[var(--text)] focus-visible:outline-2 focus-visible:outline-[color-mix(in_srgb,var(--accent)_70%,transparent)]"
          aria-expanded={!collapsedFolders.has(f.id)}
          onclick={() => toggleFolder(f.id)}
        >
          {#if collapsedFolders.has(f.id)}
            <ChevronRight class="size-3.5 shrink-0 text-[var(--muted)]" />
            <FolderIcon class="size-3.5 shrink-0 text-[var(--muted)]" />
          {:else}
            <ChevronDown class="size-3.5 shrink-0 text-[var(--muted)]" />
            <FolderOpen class="size-3.5 shrink-0 text-[var(--muted)]" />
          {/if}
          <span class="truncate">{f.name}</span>
          {#if fc.length > 0}
            <span class="ml-auto shrink-0 pr-1 text-[10px] text-[var(--muted)]">{fc.length}</span>
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
              {#snippet icon()}<Pencil class="size-3.5" />{/snippet}
              Rename folder
            </MenuItem>
            <MenuItem danger onclick={() => void removeFolder(f)} {close}>
              {#snippet icon()}<Trash2 class="size-3.5" />{/snippet}
              Delete folder
            </MenuItem>
          {/snippet}
        </Dropdown>
      </div>
      {#if !collapsedFolders.has(f.id)}
        <div class="ml-3 border-l pl-1.5" style="border-color:var(--border)">
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
      class="flex cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm hover:bg-[color-mix(in_srgb,var(--text)_7%,transparent)]"
    >
      <input type="radio" name="move-folder" value="" bind:group={moveChoice} class="accent-[var(--accent)]" />
      <MessageSquare class="size-4 text-[var(--muted)]" /> No folder
    </label>
    {#each folders as f (f.id)}
      <label
        class="flex cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm hover:bg-[color-mix(in_srgb,var(--text)_7%,transparent)]"
      >
        <input type="radio" name="move-folder" value={f.id} bind:group={moveChoice} class="accent-[var(--accent)]" />
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
  /* off-canvas below md, static column at md+ (scoped so it beats utilities) */
  .chat-sidebar {
    position: fixed;
    inset-block: 0;
    left: 0;
    z-index: 40;
    display: flex;
    flex-direction: column;
    flex-shrink: 0;
    border-right: 1px solid var(--border);
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
