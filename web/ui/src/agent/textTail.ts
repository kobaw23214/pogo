/**
 * The streaming tail: what a live thinking/answer window shows.
 *
 * The live preview shows a recent fragment, not a playback of every line.
 * Full thinking remains available separately, verbatim. Legacy clause helpers
 * remain here for the other rolling-line displays.
 */

// A clause ends at CJK/Latin terminal or comma punctuation, or a line break.
// Commas count: Chinese prose is comma-spliced into clauses, and the comma is
// where a reader's chunk ends anyway.
const CLAUSE_END = /[。！？；…，、,.!?;:\n]/;

/** Split stream text into clauses; each keeps its punctuation. The last piece
 * is the clause still being written (it has no boundary yet). */
export function splitClauses(raw: string): string[] {
  const text = raw.replace(/[ \t]+/g, " ").trim();
  if (text === "") return [];
  const out: string[] = [];
  let last = 0;
  for (let i = 0; i < text.length; i++) {
    if (!CLAUSE_END.test(text[i])) continue;
    const piece = text.slice(last, i + 1).trim();
    // A piece that is nothing but boundaries (doubled punctuation, stray
    // commas) carries no clause — keeping it would render empty lines.
    if (piece && !/^[。！？；…，、,.!?;:\s]+$/.test(piece)) out.push(piece);
    last = i + 1;
  }
  const tail = text.slice(last).trim();
  if (tail) out.push(tail);
  return out;
}

/**
 * Lines to reveal on one tick of the window's pace: one per tick keeps the
 * cadence readable, and a backlog (a burst, or mounting onto an already-long
 * stream) accelerates smoothly — every fifth waiting line adds one more to
 * the tick — so the window catches up without ever skipping a line.
 */
export function revealStep(backlog: number): number {
  return backlog <= 0 ? 0 : 1 + Math.floor(backlog / 5);
}

/** True when `next` is `prev` with text appended — the same line growing, as
 * a streaming sentence does between two ticker updates. A line like that
 * should extend in place; replaying the roll animation on every growth is
 * what reads as "the same line keeps jumping". */
export function continuesLine(prev: string, next: string): boolean {
  return next.startsWith(prev);
}

// Display width in half-char units: a CJK glyph renders ~2× an ASCII letter,
// so the pack budget below counts codepoints above U+00FF double.
function width(s: string): number {
  let w = 0;
  for (const ch of s) w += ch.codePointAt(0)! > 0xff ? 2 : 1;
  return w;
}

/**
 * Pack clauses into filled display lines. A comma does not start a new line —
 * it joins the current one — because one short clause per line reads as
 * sparse staccato. A line breaks only when the stream itself breaks (\n) or
 * when adding the next clause would overflow `maxUnits` (half-char units:
 * 72 ≈ 36 CJK glyphs or 72 ASCII letters), so the window shows dense,
 * terminal-like rows. The last line is the one still being written.
 */
export function packLines(raw: string, maxUnits = 72): string[] {
  const text = raw.replace(/[ \t]+/g, " ").trim();
  if (text === "") return [];
  const out: string[] = [];
  for (const para of text.split(/\n+/)) {
    const p = para.trim();
    if (!p) continue;
    let line = "";
    for (const clause of splitClauses(p)) {
      // Latin clauses lost their separating space to trimming; CJK never had
      // one. Re-join with a space only where both sides are ASCII.
      const join = line && /[.,!?;:]$/.test(line) && /^[A-Za-z0-9]/.test(clause) ? " " : "";
      if (line && width(line) + width(join + clause) > maxUnits) {
        out.push(line);
        line = clause;
      } else {
        line += join + clause;
      }
    }
    if (line) out.push(line);
  }
  return out;
}

export interface RecentTailLine {
  /** UTF-16 offset in the original text; appending keeps existing row keys. */
  key: number;
  text: string;
}

/** Literal recent rows, including the first unfinished fragment immediately. */
export function recentTailLines(raw: string, maxUnits = 72, count = 3): RecentTailLine[] {
  if (!raw) return [];
  const budget = Number.isFinite(maxUnits) && maxUnits > 0 ? Math.max(1, Math.floor(maxUnits)) : 72;
  const limit = Number.isFinite(count) && count > 0 ? Math.max(1, Math.floor(count)) : 3;
  const rows: RecentTailLine[] = [];
  const push = (start: number, end: number) => {
    rows.push({ key: start, text: raw.slice(start, end) });
    if (rows.length > limit) rows.shift();
  };
  let start = 0;
  let offset = 0;
  let units = 0;
  for (const char of raw) {
    if (char === "\n") {
      push(start, offset);
      start = offset + 1;
      units = 0;
    } else {
      const size = char === "\t" ? 4 : char.codePointAt(0)! > 0xff ? 2 : 1;
      if (units > 0 && units + size > budget) {
        push(start, offset);
        start = offset;
        units = 0;
      }
      units += size;
    }
    offset += char.length;
  }
  push(start, raw.length);
  return rows;
}
