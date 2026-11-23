<script lang="ts">
  // Empty-chat state (OWUI Placeholder paradigm — see their ChatPlaceholder /
  // Suggestions): centered greeting + a quiet 2-column grid of suggestion
  // cards. Clicking a card inserts its prompt and sends immediately (ChatView
  // owns submit). Theme tokens only; nothing here redefines CSS vars.
  import Sparkles from 'lucide-svelte/icons/sparkles';
  import PenLine from 'lucide-svelte/icons/pen-line';
  import Bug from 'lucide-svelte/icons/bug';
  import Languages from 'lucide-svelte/icons/languages';

  let {
    greeting = 'What can I help you with today?',
    model = '',
    onselect,
  }: {
    greeting?: string;
    /** selected model display name, shown as a quiet subline (OWUI style) */
    model?: string;
    onselect: (prompt: string) => void;
  } = $props();

  const cards = [
    {
      icon: Sparkles,
      title: 'Explain a concept',
      hint: 'Break something down simply',
      prompt:
        'Explain how HTTPS and TLS certificates work, as if I know basic networking but no crypto.',
    },
    {
      icon: PenLine,
      title: 'Draft an email',
      hint: 'Tone and length adjustable',
      prompt:
        'Draft a friendly email asking my project team for a two-day deadline extension, with a short reason and a new proposed date.',
    },
    {
      icon: Bug,
      title: 'Debug code',
      hint: 'Paste it in and go',
      prompt:
        'This function is not behaving as expected — walk me through what could be wrong and how to fix it:\n\n',
    },
    {
      icon: Languages,
      title: 'Translate & polish',
      hint: 'Rewrite in any register',
      prompt:
        'Rewrite the text below in professional, concise English, keeping the original meaning:\n\n',
    },
  ];
</script>

<div class="flex flex-1 flex-col items-center justify-center px-6 py-8 text-center">
  <div class="w-full max-w-[38rem]">
    <h1 class="text-[28px] font-semibold tracking-tight">{greeting}</h1>
    {#if model}
      <p class="mt-1 truncate text-sm" style="color:var(--muted)">{model}</p>
    {/if}

    <div class="mt-6 grid grid-cols-1 gap-2.5 text-left sm:grid-cols-2">
      {#each cards as c (c.title)}
        <button type="button" class="card" onclick={() => onselect(c.prompt)}>
          <span class="cardicon" aria-hidden="true"><c.icon size={15} /></span>
          <span class="min-w-0 flex-1">
            <span class="block truncate text-sm font-medium">{c.title}</span>
            <span class="mt-0.5 block truncate text-xs" style="color:var(--faint, var(--muted))"
              >{c.hint}</span
            >
          </span>
        </button>
      {/each}
    </div>
  </div>
</div>

<style>
  /* quiet bordered rounded-2xl cards (OWUI suggestion-card recipe): no fill
     until hover, hairline border, tiny lift on focus-visible */
  .card {
    display: flex;
    align-items: center;
    gap: 0.625rem;
    border: 1px solid var(--border, rgb(255 255 255 / 0.1));
    border-radius: 1rem;
    background: transparent;
    padding: 0.8rem 0.9rem;
    color: var(--text);
    cursor: pointer;
    transition:
      background 120ms ease,
      border-color 120ms ease;
  }
  .card:hover {
    background: var(--fill-hover, rgb(255 255 255 / 0.065));
    border-color: color-mix(in srgb, var(--muted, #a8a8a8) 40%, transparent);
  }
  .card:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--acc, var(--accent)) 60%, transparent);
    outline-offset: 2px;
  }
  .cardicon {
    display: inline-flex;
    flex: none;
    color: var(--muted, #a8a8a8);
  }
</style>
