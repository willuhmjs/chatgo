// Scheme allow-list for hrefs built from untrusted strings (search-result
// URLs on citation chips, shared-chat sources). The server stores whatever a
// provider returned and cannot guarantee it is free of javascript:/data:
// vectors, so gate every user/search-origin href through this.

/**
 * Return `u` unchanged only when it parses as an absolute http(s) URL;
 * anything else (other schemes, relative URLs, malformed input) becomes '#'.
 */
export function safeHref(u: string): string {
  if (!u || typeof u !== 'string') return '#';
  // Reject leading/trailing whitespace and embedded control chars up front:
  // the WHATWG URL parser silently strips them, so ' java\nscript:…' would
  // otherwise normalize to javascript: (browsers apply the same trimming to
  // href attributes, so ' javascript:…' is a real click vector).
  if (u !== u.trim() || /[\u0000-\u0020\u007f]/.test(u)) return '#';
  try {
    const { protocol } = new URL(u);
    return protocol === 'http:' || protocol === 'https:' ? u : '#';
  } catch {
    return '#'; // relative or malformed — never a script vector, but not a citation either
  }
}
