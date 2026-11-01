import { highlightSegments, type Segment } from "./tools/highlight";
import type { McpCall, McpMonitorFilter } from "./mcpMonitor";

export function mcpCallFailed(call: McpCall): boolean {
  return call.status === "error" || call.status === "timeout";
}

export function mcpHighlightQueries(filter: Pick<McpMonitorFilter, "q" | "server" | "tool">, field = "text"): string[] {
  return [filter.q, field === "server" ? filter.server : field === "tool" ? filter.tool : ""]
    .map(value => value.trim()).filter((value, index, all) => value && all.findIndex(v => v.toLowerCase() === value.toLowerCase()) === index);
}

/** Reuse literal Skills highlighting, merging overlapping query/filter hits.
 * Only text segments leave this helper; no HTML or regular expression input. */
export function mcpHighlightSegments(text: string, queries: readonly string[]): Segment[] {
  const ranges: { from: number; to: number }[] = [];
  for (const query of queries) {
    if (query.toLowerCase().length !== query.length) continue;
    let position = 0;
    for (const part of highlightSegments(text, query)) {
      if (part.hit) ranges.push({ from: position, to: position + part.text.length });
      position += part.text.length;
    }
  }
  ranges.sort((a, b) => a.from - b.from || a.to - b.to);
  const merged: typeof ranges = [];
  for (const range of ranges) {
    const last = merged[merged.length - 1];
    if (last && range.from <= last.to) last.to = Math.max(last.to, range.to);
    else merged.push({ ...range });
  }
  const result: Segment[] = [];
  let position = 0;
  for (const range of merged) {
    if (range.from > position) result.push({ text: text.slice(position, range.from), hit: false });
    result.push({ text: text.slice(range.from, range.to), hit: true });
    position = range.to;
  }
  if (position < text.length) result.push({ text: text.slice(position), hit: false });
  return result.length ? result : [{ text, hit: false }];
}
