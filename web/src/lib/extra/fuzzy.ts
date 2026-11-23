// Tiny fuzzy subsequence scorer (~30 lines) for the command palette.
// Returns null when `query` is not a subsequence of `target`; higher = better.

const BOUNDARY = /[\s\-_/:.,()[\]]/;

export function fuzzyScore(query: string, target: string): number | null {
  const q = query.trim().toLowerCase();
  const t = target.toLowerCase();
  if (!q) return 0;
  let score = 0;
  let ti = 0;
  let streak = 0;
  let first = -1;
  for (let qi = 0; qi < q.length; qi++) {
    const found = t.indexOf(q[qi], ti);
    if (found < 0) return null;
    if (first < 0) first = found;
    const boundary = found === 0 || BOUNDARY.test(t[found - 1]);
    streak = found === ti ? streak + 1 : 0;
    score += 10 + streak * 6 + (boundary ? 8 : 0) + (q[qi] === target[found] ? 2 : 0);
    score -= Math.min(found - ti, 8); // penalise big gaps
    ti = found + 1;
  }
  return score - first; // earlier first match ranks higher
}
