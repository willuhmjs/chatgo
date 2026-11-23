// Small formatting helpers shared by the admin pages.

/** "3m ago"-style relative time from unix seconds; em-dash when missing. */
export function relTime(ts?: number | null): string {
  if (!ts) return '—';
  const delta = Date.now() / 1000 - ts;
  if (delta < 60) return 'just now';
  const units: [number, string][] = [
    [60, 'm'],
    [24, 'h'],
    [7, 'd'],
  ];
  let v = delta / 60; // minutes
  let unit = 'm';
  if (v >= 60) {
    v = v / 24;
    unit = 'h';
    if (v >= 24) {
      v = v / 7;
      unit = 'd';
      if (v >= 7) return new Date(ts * 1000).toLocaleDateString();
    }
  }
  return `${Math.floor(v)}${unit} ago`;
}

/** 128000 → "128k", 1048576-ish → "1.0m". */
export function ctxLabel(n: number): string {
  if (!n || n <= 0) return '—';
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}m`;
  if (n >= 1000) return `${Math.round(n / 1000)}k`;
  return String(n);
}

/** Error message from ApiError/unknown throwables. */
export function errMsg(e: unknown, fallback = 'Request failed'): string {
  return e instanceof Error && e.message ? e.message : fallback;
}
