/** Display hints only: these are the scanners already supported by CodeBlock. */
export const shortcutLanguages = ["text", "json", "sql", "yaml", "yml", "shell", "bash", "properties", "python", "ts", "go", "java", "c", "rust", "yara", "css", "xml", "markdown"] as const;
export type ShortcutLanguage = typeof shortcutLanguages[number];
export function validShortcutLanguage(value: unknown): boolean {
  return value === undefined || typeof value === "string" && (value === "" || (shortcutLanguages as readonly string[]).includes(value));
}
export function shortcutLanguage(value: string | undefined): ShortcutLanguage {
  return (shortcutLanguages as readonly string[]).includes(value ?? "") ? value as ShortcutLanguage : "text";
}

export interface SearchSegment { text: string; match: boolean }
function normalized(value: string): string { return value.replace(/\s+/gu, " ").trim(); }
function searchPattern(query: string): RegExp | null {
  const needle = normalized(query);
  if (!needle) return null;
  // Literal matching: user queries never become a regular expression or HTML.
  return new RegExp(needle.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "giu");
}
export function shortcutTextMatches(value: string, query: string): boolean {
  const pattern = searchPattern(query);
  return !pattern || pattern.test(normalized(value));
}
export function shortcutSearchSegments(value: string, query: string): SearchSegment[] {
  const result: SearchSegment[] = [];
  let offset = 0;
  const pattern = searchPattern(query);
  for (const match of pattern ? value.matchAll(pattern) : []) {
    if (match.index! > offset) result.push({ text: value.slice(offset, match.index), match: false });
    result.push({ text: match[0], match: true });
    offset = match.index! + match[0].length;
  }
  if (offset < value.length) result.push({ text: value.slice(offset), match: false });
  return result;
}
/** Show a late content hit instead of always showing the first 150 characters. */
export function shortcutSearchPreview(content: string, query: string, limit = 150): string {
  const text = normalized(content), chars = [...text];
  if (chars.length <= limit) return text;
  // Large saved materials only need their first hit for filtering/excerpts;
  // do not allocate every match across a 64 KiB body on each keystroke.
  const hit = searchPattern(query)?.exec(text);
  const hitStart = hit ? [...text.slice(0, hit.index)].length : 0;
  const hitLength = hit ? [...hit[0]].length : 0;
  const windowLength = Math.max(limit, hitLength);
  // Keep the hit near the beginning so a two-line card cannot clamp it away.
  // Do not backfill the window from earlier text when the hit is near the end.
  const start = hit ? Math.max(0, hitStart - 12) : 0;
  const end = Math.min(chars.length, start + windowLength);
  return `${start > 0 ? "…" : ""}${chars.slice(start, end).join("")}${end < chars.length ? "…" : ""}`;
}
