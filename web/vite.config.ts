import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';

// Build lands in the Go embed dir (internal/web/dist) — docs/CONTRACTS.md §Build.
export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  resolve: {
    // $lib is a SvelteKit convention; standalone projects alias it themselves
    // (keep in sync with tsconfig paths).
    alias: { $lib: fileURLToPath(new URL('./src/lib', import.meta.url)) },
  },
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/files': 'http://localhost:8080',
      '/v1': 'http://localhost:8080',
    },
  },
});
