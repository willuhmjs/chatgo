// Tiny global switch for the About modal (mounted once in App.svelte):
// anyone can `openAbout()` — sidebar footer, command palette, welcome page.
export const aboutState = $state({ open: false });

export function openAbout() {
  aboutState.open = true;
}
