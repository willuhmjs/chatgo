/* ChatGo service worker — offline app shell + asset cache.
 *
 * Deliberately hand-rolled (no Workbox/build plugins — repo law: no new deps,
 * keep the bundle lean). RAM discipline mirrors the server's: one named cache
 * per boot generation, bounded entry count, nothing cached for /api, /v1,
 * /files or share pages (all dynamic, auth-scoped or SSE).
 *
 * Strategies:
 *  - Navigations: network-first, falling back to the last cached shell when
 *    offline. Redirected responses are never cached (the shell guard 302s
 *    anonymous users to /login — caching that would poison the fallback).
 *  - /assets/* (content-hashed by Vite): cache-first, immutable forever.
 *  - Other same-origin GETs (icons, manifest, uploaded-file thumbnails):
 *    stale-while-revalidate.
 */
'use strict';

const VERSION = 'chatgo-v1';
const SHELL_CACHE = VERSION + ':shell';
const ASSET_CACHE = VERSION + ':assets';
const MISC_CACHE = VERSION + ':misc';
const MISC_MAX = 120; // bounded LRU-ish cap on the SWR cache

const PRECACHE = ['/index.html', '/manifest.webmanifest', '/icon.svg', '/icon-maskable.svg'];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches
      .open(SHELL_CACHE)
      .then((c) => c.addAll(PRECACHE))
      .catch(() => {}) // offline first install: the runtime paths still work later
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => !k.startsWith(VERSION + ':')).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

/** Bounded put: keep the misc cache from growing forever (delete oldest). */
async function putBounded(cache, request, response) {
  await cache.put(request, response);
  const keys = await cache.keys();
  for (let i = 0; i < keys.length - MISC_MAX; i++) await cache.delete(keys[i]);
}

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/v1/') || url.pathname.startsWith('/files/')) return;

  // Navigations (document requests): network-first with cached-shell fallback.
  if (req.mode === 'navigate') {
    event.respondWith(
      fetch(req)
        .then((res) => {
          if (res.ok && !res.redirected) {
            const copy = res.clone();
            caches.open(SHELL_CACHE).then((c) => c.put('/index.html', copy));
          }
          return res;
        })
        .catch(async () => {
          const cached = await caches.match('/index.html');
          return cached || new Response('Offline — reconnection required', { status: 503, headers: { 'Content-Type': 'text/plain' } });
        }),
    );
    return;
  }

  // Vite-hashed chunks: immutable → cache-first.
  if (url.pathname.startsWith('/assets/')) {
    event.respondWith(
      caches.match(req).then(
        (hit) =>
          hit ||
          fetch(req).then((res) => {
            if (res.ok) {
              const copy = res.clone();
              caches.open(ASSET_CACHE).then((c) => c.put(req, copy));
            }
            return res;
          }),
      ),
    );
    return;
  }

  // Everything else same-origin: stale-while-revalidate (bounded).
  if (req.destination === 'image' || url.pathname === '/manifest.webmanifest' || url.pathname.endsWith('.svg')) {
    event.respondWith(
      caches.open(MISC_CACHE).then(async (cache) => {
        const hit = await cache.match(req);
        const refresh = fetch(req)
          .then((res) => {
            if (res.ok && !res.redirected) putBounded(cache, req, res.clone());
            return res;
          })
          .catch(() => null);
        return hit || (await refresh) || new Response('', { status: 504 });
      }),
    );
  }
});
