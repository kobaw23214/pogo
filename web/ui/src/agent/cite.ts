import type { CiteSource } from "@/api/types";
import type { SourcePreview } from "./citePreview";
import { browserFavicon } from "./browserFavicon";

// Citation helpers, pure and Vue-free for the same reason timeline.ts is: the
// numbering an answer's chips show, the grouping those chips fold into, the
// numbering the sources panel shows and the numbering a copied answer keeps
// must come from one rule, and that rule has to be testable without a
// component or a DOM.
//
// The wire format is `[1](#cite:S3)` — the bracketed number is always 1 and
// carries no meaning; the label in the href is the identity. Display numbers
// are reassigned here in first-appearance order, so a text that cites S7 first
// shows it as [1].

// The anchor pattern. Applied to text with fenced and inline code stripped,
// so an anchor the model showed AS code is never numbered — markdown-it
// agrees: no link token is ever produced inside a code span.
const CITE_ANCHOR = /\[\s*(\d+)\s*\]\(#cite:([^\s()\[\]]+)\)/g;

/** Match the registry's case-insensitive S-label contract without guessing
 * arbitrary IDs or changing source URLs and document locators. */
export function canonicalCiteLabel(label: string): string {
  return /^s\d{1,4}$/i.test(label) ? label.toUpperCase() : label;
}

/**
 * stripCode removes fenced code blocks and inline code spans. Fences go by
 * line (``` or ~~~, an opener closed by a fence of the same marker); inline
 * spans by backtick run. What remains is the only text in which markdown-it
 * can produce a link token, which is the point: numbering here and rendering
 * in MarkdownView count the same anchors.
 */
export function stripCode(text: string): string {
  const lines = text.split("\n");
  const out: string[] = [];
  let fence: string | null = null;
  for (const line of lines) {
    const m = /^ {0,3}(`{3,}|~{3,})/.exec(line);
    if (m) {
      if (fence === null) fence = m[1][0];
      else if (m[1][0] === fence) fence = null;
      // Fence markers themselves stay out of the text either way.
      continue;
    }
    if (fence !== null) continue;
    out.push(line);
  }
  return out.join("\n").replace(/`+[^`\n]*`+/g, "");
}

export interface CiteNumbering {
  /** label → display number, first appearance first, from 1. */
  nums: Map<string, number>;
  /**
   * Anchors whose label the sources list did not cover, in appearance order.
   * Only possible when the known set was provided; a null known set (the share
   * page, a work-log row — contexts that never received sources) numbers
   * everything rather than damning every chip as unknown.
   */
  unknown: string[];
}

/**
 * One cite anchor's place in the render plan. The plan — not the regex, not
 * the renderer — is the single source of truth for numbering AND folding: the
 * chips, the sources panel and the copy path all read it.
 */
export interface CitePlanItem {
  /** The anchor's identity, from its href. */
  label: string;
  /** Display number, first appearance first, from 1; undefined when unknown. */
  num?: number;
  /** True when a known set was provided and did not cover this label. */
  unknown: boolean;
  /** Fold-group identity: anchors sharing it collapse into one pill. */
  group: number;
  /** 0-based position inside the group; the head is 0. */
  index: number;
  /** Distinct sources in the group; 1 renders a lone chip, more renders "+M". */
  size: number;
  /** True for the member that renders the pill; the rest render nothing. */
  head: boolean;
}

/**
 * planCitations walks an answer's cite anchors in appearance order and
 * decides, for each, its display number and whether it folds into the pill of
 * the anchor before it.
 *
 * Two anchors fold together when they are adjacent — only spaces and tabs
 * between them, never a newline — and both are known. An unknown anchor never
 * folds and breaks any run it sits in: the [?] chip stands alone, so it can
 * never smuggle an unverifiable claim into a real source's pill.
 *
 * Numbering matches the old numberCitations rule exactly: every label earns
 * its number at first appearance, including members a fold hides behind "+M",
 * so a card page or a panel row can always name the same number a lone chip
 * would have shown.
 */
export function planCitations(text: string, known: Set<string> | null): CitePlanItem[] {
  const stripped = stripCode(text);
  const raw: { label: string; start: number; end: number }[] = [];
  CITE_ANCHOR.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = CITE_ANCHOR.exec(stripped)) !== null) {
    raw.push({ label: canonicalCiteLabel(m[2]), start: m.index, end: m.index + m[0].length });
  }

  // Reserve an occurrence number even for a source whose metadata is pending.
  // Known citations must not renumber when an earlier source arrives later.
  const positions = new Map<string, number>();
  const nums = new Map<string, number>();
  const items: CitePlanItem[] = [];
  let group = -1;
  let groupStart = 0;
  for (let i = 0; i < raw.length; i++) {
    const { label, start } = raw[i];
    if (!positions.has(label)) positions.set(label, positions.size + 1);
    const unknown = known !== null && !known.has(label);
    let num = nums.get(label);
    if (num === undefined && !unknown) {
      num = positions.get(label)!;
      nums.set(label, num);
    }
    const prev = raw[i - 1];
    const adjacent = i > 0 && !/[^\t ]/.test(stripped.slice(prev.end, start));
    const prevItem = items[i - 1];
    const joins = adjacent && !unknown && prevItem !== undefined && !prevItem.unknown;
    if (!joins) {
      group += 1;
      groupStart = i;
    }
    items.push({
      label,
      num,
      unknown,
      group,
      index: i - groupStart,
      size: 0, // backfilled just below, as the group's reach grows
      head: i === groupStart,
    });
    // Repeated copies of the same adjacent citation are one source, not +1.
    // Keep every plan entry so the Markdown renderer still consumes in order.
    const size = new Set(items.slice(groupStart).map((p) => p.label)).size;
    for (let k = groupStart; k <= i; k++) items[k].size = size;
  }
  return items;
}

/** One entry of a CiteCard's page list: the stamp plus its display number. */
export interface CiteCardItem {
  label: string;
  src?: CiteSource;
  num?: number;
  state: "ready" | "pending" | "unavailable";
  preview?: SourcePreview;
}

/**
 * numberCitations assigns display numbers to an answer's cite anchors.
 *
 * `known` is the set of labels this turn's tool calls actually produced, or
 * null when the context never carried sources. An anchor to an unknown label
 * gets no number: the renderer shows it as a dead [?] chip instead of a link
 * that promises evidence it cannot show.
 */
export function numberCitations(text: string, known: Set<string> | null): CiteNumbering {
  const nums = new Map<string, number>();
  const unknown: string[] = [];
  for (const item of planCitations(text, known)) {
    if (item.unknown) {
      if (!unknown.includes(item.label)) unknown.push(item.label);
    } else if (!nums.has(item.label)) {
      nums.set(item.label, item.num!);
    }
  }
  return { nums, unknown };
}

/**
 * rewriteCitations replaces every numbered anchor with its plain display
 * number — the copy/export degradation, where the link has nowhere to go.
 * Unknown labels keep their original bracketed number, matching the [?] the
 * renderer shows for them.
 */
export function rewriteCitations(text: string, nums: Map<string, number>): string {
  return text.replace(CITE_ANCHOR, (whole, _n, label: string) => {
    const n = nums.get(canonicalCiteLabel(label));
    return n === undefined ? whole.replace(/\]\([^)]*\)$/, "]") : `[${n}]`;
  });
}

/** compareSourceLabels orders S2 before S10; a non-numeric tail sorts last. */
export function compareSourceLabels(a: string, b: string): number {
  const na = /(\d+)\s*$/.exec(a);
  const nb = /(\d+)\s*$/.exec(b);
  if (na && nb) return Number(na[1]) - Number(nb[1]) || a.localeCompare(b);
  if (na) return -1;
  if (nb) return 1;
  return a.localeCompare(b);
}

/**
 * collectSources gathers a turn's citation stamps, deduped by label and in
 * label order — the sources panel's "this turn" list. First sighting wins:
 * two stamps on one label describe the same evidence.
 */
function browserStatusOnly(src: CiteSource, result: { text?: string; details?: unknown; is_error?: boolean }): boolean {
  if (src.type !== "web_page" || src.error || src.full_path || result.is_error || typeof result.text !== "string") return false;
  const details = result.details as Record<string, unknown> | undefined;
  if (!details || details.url !== src.locator || details.ok !== true || details.truncated || details.full_output_path) return false;
  // Actions often include a real snapshot, so their names alone never mean
  // "not material". Only the exact empty browser-reply envelope qualifies.
  if (!["navigate", "click", "scroll", "type", "press", "select", "hover", "tab"].includes(String(details.action))) return false;
  const lines = result.text.trim().split(/\r?\n/);
  if (/^\[cite:[^\r\n]*\]$/.test(lines[0] ?? "")) lines.shift();
  if (/^runtime: [^;\r\n]+; tab: [^;\r\n]*; version: [^\r\n]*$/.test(lines[0] ?? "")) lines.shift();
  const page = lines.shift() ?? "";
  const prefix = `page: ${src.locator}`;
  if (page !== prefix && !page.startsWith(prefix + " · ")) return false;
  return lines.every((line) => !line.trim());
}

export function collectSources(calls: { result?: { source?: CiteSource; text?: string; details?: unknown; is_error?: boolean } }[]): CiteSource[] {
  const byLabel = new Map<string, CiteSource>();
  for (const c of calls) {
    const s = c.result?.source;
    if (s && !byLabel.has(s.label)) {
      const result = c.result!;
      const details = result.details as { exit_code?: unknown; favicon?: unknown; url?: unknown } | undefined;
      // Only the original tool result may supply these bytes. The page URL
      // must belong to this stamp, never another active tab in a later state.
      const favicon = details?.url === s.locator ? browserFavicon(details?.favicon) : undefined;
      const recordedSource = { ...s };
      delete recordedSource.browser_status_only;
      byLabel.set(s.label, {
        ...recordedSource,
        ...(result.text !== undefined ? { content: result.text } : {}),
        ...(typeof details?.exit_code === "number" && s.type === "command_exec" ? { exit_code: details.exit_code } : {}),
        ...(result.is_error ? { error: true } : {}),
        ...(favicon ? { favicon } : {}),
        // This remains presentation metadata. The registry and work log keep
        // the original result, and an explicit citation still displays it.
        ...(browserStatusOnly(s, result) ? { browser_status_only: true } : {}),
      });
    }
  }
  return [...byLabel.values()].sort((a, b) => compareSourceLabels(a.label, b.label));
}

/** The display name a source type wears. These are tool names, not copy, so
 * they stay untranslated — the timeline shows the same words. */
export function sourceTypeName(type: string): string {
  switch (type) {
    case "information":
      return "信息库";
    case "command_exec":
      return "bash";
    case "file_read":
      return "read";
    case "file_write":
      return "write";
    case "search_result":
      return "search";
    case "web_page":
      return "web";
    case "mcp_tool":
      return "mcp";
    case "subagent":
      return "subagent";
    default:
      return "tool";
  }
}

/**
 * describeSource is the one-line summary chips, cards and the copied
 * reference list share: `bash · exit 0 · npm test`, `read · agent/loop.go`.
 * The title comes from the stamp's own header verbatim, so a command stamp
 * that already carries its exit code keeps it — this only prepends the type.
 */
export function describeSource(src: CiteSource): string {
  const head = sourceTypeName(src.type);
  const title = src.title.trim();
  return title ? `${head} · ${title}` : head;
}

/**
 * shortHost reduces a locator to the bit that identifies it: a URL's host, a
 * file locator's path with its line range, anything else unchanged. Bounded
 * in length so a panel row can show it without truncation doing the work.
 */
export function shortHost(locator: string): string {
  const m = /^([a-z]+):\/\//i.exec(locator);
  if (m) {
    if (m[1].toLowerCase() === "file") {
      // file://agent/loop.go#L611-680 → agent/loop.go#L611-680
      return locator.slice(m[0].length);
    }
    try {
      const url = new URL(locator);
      return url.host;
    } catch {
      return locator;
    }
  }
  return locator;
}

export interface SourceGroup {
  /** The stamp, and its display number when the answer cited it. */
  src: CiteSource;
  num?: number;
}

/**
 * groupSources splits a turn's stamps into the panel's two sections: cited
 * first, numbered and in citation order; the rest below, one notch dimmer, in
 * label order (they arrive that way from collectSources).
 */
export function groupSources(
  sources: CiteSource[],
  nums: Map<string, number>,
): { cited: SourceGroup[]; others: SourceGroup[] } {
  const cited: SourceGroup[] = [];
  const others: SourceGroup[] = [];
  for (const src of sources) {
    const num = nums.get(src.label);
    if (num === undefined) others.push({ src });
    else cited.push({ src, num });
  }
  cited.sort((a, b) => (a.num ?? 0) - (b.num ?? 0));
  return { cited, others };
}
