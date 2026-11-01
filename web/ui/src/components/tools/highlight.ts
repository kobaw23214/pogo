// Match highlighting for the skills panel's filter. A pure function so the
// segmentation is unit-testable without a component: the panel and the tool
// card both render its segments, and the one rule worth testing — never
// corrupt the text — is exactly what the length-change fallback protects.

export interface Segment {
  text: string;
  hit: boolean;
}

// highlightSegments splits text around every case-insensitive occurrence of
// query, marking the occurrences. Empty query returns the text untouched.
//
// Substring, not the fuzzy matcher: the point of highlighting is that the
// marked run is literally what the user typed (fuzzy subsequence hits have no
// contiguous run to draw a box around). Filtering itself stays fuzzier than
// highlighting — a fuzzy-only match shows the card unhighlighted, which is the
// honest fallback rather than a wrong box.
//
// The guard: matching runs on lowercased text but slices the original, and
// toLowerCase can change length for some code points (İ, ẞ…). If it did, the
// indices no longer line up and the function returns the text unhighlighted —
// no match is better than a highlight shifted onto the wrong characters.
export function highlightSegments(text: string, query: string): Segment[] {
  const q = query.trim();
  if (!q) return [{ text, hit: false }];
  const lower = text.toLowerCase();
  const needle = q.toLowerCase();
  if (lower.length !== text.length) return [{ text, hit: false }];

  const out: Segment[] = [];
  let i = 0;
  for (;;) {
    const idx = lower.indexOf(needle, i);
    if (idx < 0) break;
    if (idx > i) out.push({ text: text.slice(i, idx), hit: false });
    out.push({ text: text.slice(idx, idx + q.length), hit: true });
    i = idx + q.length;
  }
  if (i < text.length) out.push({ text: text.slice(i), hit: false });
  return out;
}
