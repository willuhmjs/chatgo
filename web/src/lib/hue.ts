// Deterministic hue helper (FE-COLOR): maps an arbitrary string (provider or
// tag name) to a stable hue so model dots / tag chips cycle colors without any
// server data or per-instance state. Pure + cheap.

/** FNV-1a-style string hash → hue 0–359. Same string ⇒ same hue forever. */
export function hueFrom(seed: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < seed.length; i++) {
    h ^= seed.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h % 360;
}

/** Ready-to-use `hsl()` fill for provider dots / chips. Mid saturation and
 *  lightness so one value reads on both the dark and light surfaces. */
export function hueColor(seed: string, saturation = 68, lightness = 58): string {
  return `hsl(${hueFrom(seed)} ${saturation}% ${lightness}%)`;
}
