import type { CiteSource } from "@/api/types";

export interface SourcePreview {
  /** A contiguous, unmodified substring of the recorded source text. */
  text: string;
  /** Lexical overlap with the nearby answer only; never a support/validity check. */
  matched: boolean;
  kind: "excerpt" | "output";
  /** Known browser envelopes distinguish recorded page text from protocol output. */
  browserContent?: "page" | "output";
  /** An explicit framed page marker in the output, not a native PDF location. */
  page?: number;
  location?: string;
}

const MAX_CONTENT = 200_000;
const MAX_PREVIEW = 420;
const WINDOW_STRIDE = 280;
const MAX_TERMS = 48;

const STOP_WORDS = new Set([
  "the", "and", "for", "are", "was", "were", "this", "that", "with", "from",
  "has", "have", "had", "not", "but", "you", "your", "its", "into", "can",
  "will", "would", "could", "should", "than", "then", "also", "their", "they",
  "which", "when", "where", "been", "about", "some", "such", "there", "these",
  "source", "sources", "report", "page", "cite", "output",
]);
const STOP_BIGRAMS = new Set([
  "这个", "这些", "一个", "一种", "以及", "其中", "因此", "所以", "可以",
  "进行", "通过", "相关", "根据", "报告", "文档", "来源", "引用", "显示",
]);

interface Term {
  value: string;
  weight: number;
  kind: "specific" | "word" | "cjk";
}

function termsFor(context: string): Term[] {
  // Context is untrusted answer text. Citation IDs, markdown destinations and
  // page labels must not cause an unrelated output to look like a good match.
  const text = Array.from(context.slice(-1800)).slice(-900).join("")
    .replace(/\[[^\]]*\]\(#cite:[^)]+\)/gi, " ")
    .replace(/第\s*\d+\s*页|\bpage\s+\d+\b/gi, " ")
    .toLowerCase();
  const terms = new Map<string, Term>();
  const add = (value: string, kind: Term["kind"], weight: number) => {
    if (value.length > 96 || terms.has(value)) return;
    terms.set(value, { value, kind, weight });
  };

  // Paths, hashes, addresses and version strings are more discriminating than
  // shared prose. They get first claim on the bounded term budget.
  for (const match of text.matchAll(/(?:[a-z]:[\\/]|\.{0,2}\/)[\w./\\-]+|\b[\da-f]{12,}\b|\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b|\b[a-z0-9_]+(?:[._-][a-z0-9_]+)+\b|\b\d{3,}\b/g)) {
    add(match[0], "specific", 12);
  }
  for (const match of text.matchAll(/[a-z][a-z0-9_]{1,}/g)) {
    if ((match[0].length >= 3 || /\d/.test(match[0])) && !STOP_WORDS.has(match[0])) add(match[0], "word", 2);
  }
  for (const match of text.matchAll(/[\u3400-\u9fff]{2,}/g)) {
    const chars = Array.from(match[0]);
    for (let i = 0; i < chars.length - 1; i++) {
      const value = chars[i] + chars[i + 1];
      if (!STOP_BIGRAMS.has(value)) add(value, "cjk", 0.6);
    }
  }
  return [...terms.values()].slice(0, MAX_TERMS);
}

function pageHintFor(context: string): number | undefined {
  const hints = new Set<number>();
  const bounded = Array.from(context.slice(-1800)).slice(-900).join("");
  for (const match of bounded.matchAll(/第\s*([1-9]\d{0,4})\s*页|\bpage\s+([1-9]\d{0,4})\b/gi)) {
    hints.add(Number(match[1] ?? match[2]));
  }
  // Conflicting page references in the same claim are not a useful hint.
  return hints.size === 1 ? [...hints][0] : undefined;
}

function termPosition(text: string, term: Term): number {
  let at = text.indexOf(term.value);
  while (at !== -1) {
    if (term.kind === "cjk") return at;
    // Do not count "api" inside "capital" or an IP inside another number.
    const before = at > 0 ? text[at - 1] : "";
    const after = text[at + term.value.length] ?? "";
    if (!/[a-z0-9_]/.test(before) && !/[a-z0-9_]/.test(after)) return at;
    at = text.indexOf(term.value, at + 1);
  }
  return -1;
}

function relevance(text: string, terms: Term[]): number {
  const lower = text.toLowerCase();
  let score = 0;
  let lexical = false;
  let cjk = 0;
  for (const term of terms) {
    if (termPosition(lower, term) < 0) continue;
    score += term.weight;
    if (term.kind === "cjk") cjk++;
    else lexical = true;
  }
  // A single common Chinese bigram is weak evidence of relevance. Two
  // distinct bigrams also accommodate a three-character subject.
  return lexical || cjk >= 2 ? score : 0;
}

/** Code-point offset that brings a late, distinctive hit into a short preview. */
function alignedStart(text: string, terms: Term[]): number {
  // Match positions must remain offsets in the original string. Some Unicode
  // case mappings expand (for example İ → i + combining dot), so fold only
  // the ASCII letters that our word/identifier tokenizer recognizes.
  const lower = text.replace(/[A-Z]/g, (letter) => letter.toLowerCase());
  const hits = terms.map((term) => ({ term, at: termPosition(lower, term) }))
    .filter((hit) => hit.at >= 0)
    .sort((a, b) => b.term.weight - a.term.weight || b.term.value.length - a.term.value.length || a.at - b.at);
  if (!hits.length) return 0;
  const at = hits[0].at;
  const before = text.slice(0, at);
  if (Array.from(before).length <= 110 && (before.match(/\n/g)?.length ?? 0) < 3) return 0;

  let start = text.lastIndexOf("\n", at - 1) + 1;
  if (Array.from(text.slice(start, at)).length > 48) {
    const chars = Array.from(before);
    start = chars.slice(0, Math.max(0, chars.length - 48)).join("").length;
    // Prefer a recent sentence boundary within the small context window.
    const context = text.slice(start, at);
    const boundaries = [...context.matchAll(/[。！？!?；;]\s*|\.\s+/g)];
    const last = boundaries[boundaries.length - 1];
    if (last) start += last.index! + last[0].length;
    // The code-point boundary above protects surrogate pairs. If it falls
    // inside an ASCII identifier/word, advance to that word's end.
    while (start < at && /[a-z0-9_]/i.test(text[start - 1] ?? "") && /[a-z0-9_]/i.test(text[start])) start++;
    while (start < at && /\s/.test(text[start])) start++;
  } else if (start > 0) {
    const previousStart = text.lastIndexOf("\n", start - 2) + 1;
    const previous = text.slice(previousStart, start).trim();
    // A short preceding label is useful context; preceding hash/log rows are
    // not. Keeping at most 64 code points leaves the match near the top.
    if (/^[^.!?。！？]{1,40}[:：]$/.test(previous)
      && Array.from(text.slice(previousStart, at)).length <= 64) start = previousStart;
  }
  return Array.from(text.slice(0, start)).length;
}

interface Section {
  text: string;
  page?: number;
}

function sectionsFor(text: string): Section[] {
  const sections: Section[] = [];
  const marker = /^={3,}[ \t]*Page[ \t]+([1-9]\d{0,4})[ \t]*={3,}[ \t]*\r?$/gim;
  let start = 0;
  let page: number | undefined;
  for (const match of text.matchAll(marker)) {
    const at = match.index!;
    const body = text.slice(start, at).trim();
    if (body) sections.push({ text: body, page });
    start = at + match[0].length;
    page = Number(match[1]);
  }
  const body = text.slice(start).trim();
  if (body) sections.push({ text: body, page });
  return sections;
}

function previewResult(text: string, matched: boolean, kind: SourcePreview["kind"], page?: number, browserContent?: SourcePreview["browserContent"]): SourcePreview {
  return {
    text,
    matched,
    kind,
    ...(browserContent ? { browserContent } : {}),
    ...(page === undefined ? {} : { page, location: `第 ${page} 页（输出标记）` }),
  };
}

/** Find the end of one complete JSON object without parsing trailing page
 * text as JSON. Input is already bounded; JSON.parse still validates syntax. */
function jsonObjectEnd(text: string, start: number): number | undefined {
  if (text[start] !== "{") return undefined;
  let depth = 0;
  let quoted = false;
  let escaped = false;
  for (let i = start; i < text.length; i++) {
    const char = text[i];
    if (quoted) {
      if (escaped) escaped = false;
      else if (char === "\\") escaped = true;
      else if (char === '"') quoted = false;
    } else if (char === '"') quoted = true;
    else if (char === "{" || char === "[") depth++;
    else if (char === "}" || char === "]") {
      depth--;
      if (depth === 0) return i + 1;
    }
  }
  return undefined;
}

function browserPreviewText(src: CiteSource, bounded: string): { text: string; browserContent?: SourcePreview["browserContent"] } {
  if (src.type !== "web_page") return { text: bounded };
  const runtime = /^(?:\[cite:[^\r\n]*\]\r?\n)?runtime: native; tab: [^;\r\n]*; version: [^\r\n]*\r?\n/.exec(bounded);
  if (!runtime) return { text: bounded };
  const output = { text: bounded, browserContent: "output" as const };
  if (src.error || !safeSourceURL(src)) return output;
  const pageEnd = bounded.indexOf("\n", runtime[0].length);
  if (pageEnd < 0) return output;
  const page = bounded.slice(runtime[0].length, pageEnd).replace(/\r$/, "");
  const prefix = `page: ${src.locator.trim()}`;
  if (page !== prefix && !page.startsWith(prefix + " · ")) return output;
  const evidence = /^visible DOM evidence \/ as-observed calculation:\r?\n[ \t\r\n]*/.exec(bounded.slice(pageEnd + 1));
  if (!evidence) return output;
  const start = pageEnd + 1 + evidence[0].length;
  const end = jsonObjectEnd(bounded, start);
  if (end === undefined || !/^\r?\n/.test(bounded.slice(end))) return output;
  try {
    const value: unknown = JSON.parse(bounded.slice(start, end));
    if (!value || typeof value !== "object" || Array.isArray(value)
      || (value as { schema?: unknown }).schema !== "dom-evidence-v1") return output;
  } catch {
    return output;
  }
  const text = bounded.slice(end).trim();
  if (!text || /^(?:data:|viewport screenshot:)/.test(text)) return output;
  // The browser already appends its original page text after the structured
  // observation. Prefer that contiguous recorded text; never fabricate prose
  // by joining JSON fields, table cells, or different frame observations.
  return { text, browserContent: "page" };
}

/**
 * Pick a readable excerpt from the recorded tool output using nearby answer
 * words. This is intentionally a local lexical selector: it neither retrieves
 * new evidence nor claims the excerpt supports the answer. Work and displayed
 * size are bounded even when a transcript contains a very large output.
 */
export function sourcePreview(src: CiteSource & { content?: string }, context: string): SourcePreview {
  const hasOutput = Boolean(src.content?.slice(0, MAX_CONTENT).trim());
  const kind = hasOutput ? "output" : "excerpt";
  const raw = hasOutput ? src.content! : src.excerpt;
  // Do not leave half a surrogate at the inspection boundary.
  let bounded = raw.slice(0, MAX_CONTENT);
  if (/[\ud800-\udbff]$/.test(bounded)) bounded = bounded.slice(0, -1);
  if (src.type === "information") {
    // Only decode the host's known read envelope. Saved material can contain
    // arbitrary JSON but cannot replace the source's stamped identity/version.
    try {
      const value: unknown = JSON.parse(bounded);
      const entry = value as { entry?: { id?: unknown }; version?: unknown; snapshot_sha256?: unknown; content?: unknown };
      bounded = src.information && entry.entry?.id === src.information.entry_id
        && entry.version === src.information?.version
        && entry.snapshot_sha256 === src.information?.snapshot_sha256
        && typeof entry.content === "string" ? entry.content : src.excerpt;
    } catch {
      bounded = src.excerpt;
    }
  }
  const browser = browserPreviewText(src, bounded);
  const sections = sectionsFor(browser.text);
  if (!sections.length) return previewResult("", false, kind, undefined, browser.browserContent);

  const terms = termsFor(context);
  const pageHint = pageHintFor(context);
  let bestText = "";
  let bestChars: string[] = [];
  let bestStart = 0;
  let bestPage: number | undefined;
  let bestScore = 0;
  for (const section of sections) {
    // Keep internal single newlines, including table rows and command logs.
    // Paragraph boundaries prevent previewing unrelated neighboring passages.
    for (const paragraph of section.text.split(/\r?\n[ \t]*\r?\n/)) {
      const chars = Array.from(paragraph.trim());
      for (let start = 0; start < chars.length; start += WINDOW_STRIDE) {
        const candidate = chars.slice(start, start + MAX_PREVIEW).join("");
        const score = relevance(candidate, terms);
        const preferredPageTie = score > 0 && score === bestScore
          && section.page !== undefined && section.page === pageHint && bestPage !== pageHint;
        if (score > bestScore || preferredPageTie) {
          bestText = candidate;
          bestChars = chars;
          bestStart = start;
          bestPage = section.page;
          bestScore = score;
        }
        if (start + MAX_PREVIEW >= chars.length) break;
      }
    }
  }
  if (bestScore > 0) {
    const start = bestStart + alignedStart(bestText, terms);
    return previewResult(bestChars.slice(start, start + MAX_PREVIEW).join(""), true, kind, bestPage, browser.browserContent);
  }
  const first = sections[0];
  return previewResult(Array.from(first.text).slice(0, MAX_PREVIEW).join(""), false, kind, first.page, browser.browserContent);
}

/** Only directly recorded, absolute HTTP(S) locators can open an external tab. */
export function safeSourceURL(src: Pick<CiteSource, "locator">): string | undefined {
  const locator = src.locator.trim();
  if (!/^https?:\/\//i.test(locator) || /[\u0000-\u001f\u007f]/.test(locator)) return undefined;
  try {
    const url = new URL(locator);
    if (!url.hostname || url.username || url.password) return undefined;
    return url.href;
  } catch {
    return undefined;
  }
}

export interface SourceIdentity {
  title: string;
  detail: string;
  kind: "web" | "file" | "tool" | "library";
  url?: string;
}

function pdfOutputTitle(src: CiteSource & { content?: string }): string | undefined {
  if (src.type !== "command_exec") return undefined;
  const output = (src.content?.slice(0, MAX_CONTENT) || src.excerpt).trimStart();
  // Recognize the actual pdf-text envelope, not a filename mentioned in prose.
  // Both the complete header and its first framed page are required. This is
  // display metadata only: neither the filename nor the page creates a target.
  const header = /^# ([^\r\n/\\\u0000-\u001f]{1,240}\.pdf) · 第 ([1-9]\d{0,4})-([1-9]\d{0,4}) 页 \/ 共 ([1-9]\d{0,4}) 页[ \t]*\r?\n[ \t\r\n]*===== Page ([1-9]\d{0,4}) =====[ \t]*(?:\r?\n|$)/i.exec(output);
  if (!header) return undefined;
  const [, filename, first, last, total, page] = header;
  const start = Number(first);
  const end = Number(last);
  const markedPage = Number(page);
  if (start > end || end > Number(total) || markedPage < start || markedPage > end) return undefined;
  return filename;
}

/** Source identity stays separate from the excerpt and any acquisition tool. */
export function sourceIdentity(src: CiteSource & { content?: string }): SourceIdentity {
  if (src.type === "information") {
    return { title: src.title.trim() || "信息库", detail: src.information ? `v${src.information.version}` : "information", kind: "library" };
  }
  const url = safeSourceURL(src);
  if (url) {
    const host = new URL(url).hostname.replace(/^www\./i, "");
    return { title: src.title.trim() || host, detail: host, kind: "web", url };
  }
  const pdfTitle = pdfOutputTitle(src);
  if (pdfTitle) return { title: pdfTitle, detail: "PDF", kind: "file" };
  if (src.type === "file_read" || src.type === "file_write" || src.locator.startsWith("file://")) {
    const path = src.locator.replace(/^file:\/\//, "");
    return { title: src.title.trim() || path || "文件", detail: path, kind: "file" };
  }
  return { title: src.title.trim() || "工具输出", detail: src.type, kind: "tool" };
}
