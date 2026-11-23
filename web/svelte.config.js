import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

export default {
  preprocess: vitePreprocess(),
  compilerOptions: {
    // Auto-detect per file (default). Our sources are pure runes; forcing
    // runes:true globally would break legacy-mode deps like lucide-svelte.
  },
};
