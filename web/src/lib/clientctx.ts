// Browser context attached to every chat turn so the model can resolve
// "today / here" without guessing from search snippets.
//
//   * timezone — implicit, zero-prompt (Intl API); the server validates it
//     against real IANA zones before trusting it.
//   * location — strictly opt-in: only Settings → "Share location" ever
//     invokes the browser permission prompt. We store the coarse fix in
//     localStorage and send the cache with each message; the server rounds
//     it to ~1 km.
//
// chatview-api.ts deliberately avoids localStorage, so this small sibling
// module owns the geo key (documented in docs/FE-CHAT-UI.md keys table).

const GEO_KEY = 'chatgo.geo…are';

export interface GeoFix {
  lat: number;
  lon: number;
}

export function browserTimezone(): string | undefined {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || undefined;
  } catch {
    return undefined;
  }
}

/** Last opt-in fix, or undefined when the user never enabled sharing. */
export function cachedGeo(): GeoFix | undefined {
  try {
    const raw = localStorage.getItem(GEO_KEY);
    if (!raw) return undefined;
    const v = JSON.parse(raw) as GeoFix;
    if (typeof v?.lat !== 'number' || typeof v?.lon !== 'number') return undefined;
    return { lat: v.lat, lon: v.lon };
  } catch {
    return undefined;
  }
}

export function geoShareOn(): boolean {
  return cachedGeo() !== undefined;
}

/** Ask the browser once, then remember. Rejects with a human-readable reason. */
export function enableGeoShare(): Promise<GeoFix & { ts: number }> {
  return new Promise((resolve, reject) => {
    if (!navigator.geolocation) {
      reject(new Error('This browser has no location support'));
      return;
    }
    navigator.geolocation.getCurrentPosition(
      (p) => {
        const v = { lat: p.coords.latitude, lon: p.coords.longitude, ts: Date.now() };
        try {
          localStorage.setItem(GEO_KEY, JSON.stringify(v));
        } catch {
          /* private mode — share nothing rather than fail the toggle */
        }
        resolve(v);
      },
      (e) => reject(new Error(e.message || 'Location unavailable')),
      { maximumAge: 600_000, timeout: 10_000 },
    );
  });
}

export function disableGeoShare(): void {
  try {
    localStorage.removeItem(GEO_KEY);
  } catch {
    /* ignore */
  }
}

/** Refresh the stored fix when sharing is on (called on Settings mount). */
export function refreshGeo(): void {
  if (!geoShareOn()) return;
  navigator.geolocation?.getCurrentPosition(
    (p) => {
      try {
        localStorage.setItem(GEO_KEY, JSON.stringify({ lat: p.coords.latitude, lon: p.coords.longitude, ts: Date.now() }));
      } catch {
        /* ignore */
      }
    },
    () => undefined,
    { maximumAge: 600_000, timeout: 8_000 },
  );
}

/** Fill the client-context fields of a send/regenerate body in place. */
export function withClientContext<T extends object>(body: T): T & Partial<GeoFields> {
  const b = body as Partial<GeoFields>;
  if (b.timezone === undefined) b.timezone = browserTimezone();
  if (b.location === undefined) {
    const g = cachedGeo();
    if (g) b.location = g;
  }
  return body as T & Partial<GeoFields>;
}

interface GeoFields {
  timezone: string;
  location: GeoFix;
}
