import { mount } from 'svelte';
import App from './App.svelte';
import './app.css';

const target = document.getElementById('app');
if (target) {
  mount(App, { target });
}

// PWA: register the hand-rolled service worker (public/sw.js — offline shell
// + asset cache). Deferred past load so it never competes with first paint;
// skipped in dev (vite serves the SPA; a stale SW there is a footgun).
if ('serviceWorker' in navigator && !import.meta.env.DEV) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {
      /* no-HTTPS, private mode, blocked — the app runs fully without it */
    });
  });
}
