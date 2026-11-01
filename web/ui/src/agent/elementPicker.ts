// Shared read-only element references and historical message display.
// Native observations are not selectors and cannot execute browser actions.
function esc(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

export interface ElementPick {
  /** Stable identity for toggle/dedupe; native picks use tab/version/ref. */
  key: string;
  /** Chip label: `button "检索"` or a clipped selector. */
  label: string;
  /** The formatted <page-element> block. */
  block: string;
  /** The picked page's document.title — the chip leads with it, because
   * "which page did this come from" is the question a chip without it cannot
   * answer. */
  pageTitle: string;
  /** Exact observed document URL for session-owned display metadata. */
  pageURL?: string;
  /** Bounded favicon image bytes frozen at pick time; absent → globe glyph. */
  icon?: string;
  /** The pick carries page-volunteered data (the data: field) — the chip shows
   * a data badge so "this one brings real numbers" is visible before sending. */
  hasData?: boolean;
  /** The hover-preview text: what the model will actually receive, clipped. */
  preview?: string;
}

/** chipPreview is the hover/audit text for a picked chip: identity first, then
 * the data hook's JSON when there is one (the numbers are the point), else the
 * element's visible text. */
export function chipPreview(f: ElementFacts): string {
  const head = `${f.role}${f.name ? ` "${clipText(f.name, 80)}"` : ""} · ${f.selector}`;
  if (f.data) return `${head}\n${clipText(f.data, 400)}`;
  return f.text ? `${head}\n${clipText(f.text, 300)}` : head;
}

export interface ElementFacts {
  tag: string;
  selector: string;
  role: string;
  name: string;
  text: string;
  /** "top% left% W×H" — normalized so it reads the same at any zoom. */
  position: string;
  html: string;
  /** Zero-loss data the page volunteered for this element via the pick-data
   * hook (PICK_PROTOCOL.md): single-line JSON, absent when the page has none.
   * Carries what the DOM cannot — a canvas chart's series and its current
   * zoom window live in JS memory, not markup. */
  data?: string;
}

/** clipText shortens for one-line fields; whole runes, not bytes — the
 * mojibake lesson from clipBg, applied before any string leaves the pane. */
export function clipText(s: string, max: number): string {
  const t = s.replace(/\s+/g, " ").trim();
  return t.length <= max ? t : t.slice(0, max - 1).trimEnd() + "…";
}

/** describeElement builds the chip label from facts. */
export function chipLabel(f: { role: string; name: string; tag: string; selector: string }): string {
  if (f.name) return clipText(`${f.role} "${f.name}"`, 48);
  return clipText(f.selector || f.tag, 48);
}

/**
 * formatElementBlock is the exhibit itself. The wrapper tag says what it is
 * and where it came from; the fields are key: value lines; the page's own text
 * and markup sit inside, after the user's instructions will have already been
 * stated — the labelled-data placement Anthropic recommends against injection.
 */
export function formatElementBlock(
  f: ElementFacts,
  page: { title: string; url: string },
): string {
  const lines = [
    `<page-element source="${esc(page.title)}" url="${esc(page.url)}">`,
    `selector: ${f.selector}`,
    `element: ${f.role}${f.name ? ` "${clipText(f.name, 120)}"` : ""}`,
  ];
  if (f.text) lines.push(`text: ${clipText(f.text, 800)}`);
  lines.push(`position: ${f.position}`);
  // Single-line by protocol (JSON.stringify without indent), so it parses as a
  // plain `key: value` field — html below stays the only multi-line one, which
  // htmlField's whole-remainder match depends on.
  if (f.data) lines.push(`data: ${f.data}`);
  if (f.html) lines.push(`html:\n${f.html}`);
  lines.push(`</page-element>`);
  return lines.join("\n");
}

/**
 * composePickedMessage assembles what actually goes out: the user's own words
 * first, then the exhibits. That order is the injection defense — instructions
 * lead, data follows in labelled blocks, so the model reads the page content as
 * evidence attached to a question rather than as the question.
 */
export function composePickedMessage(picks: ElementPick[], text: string): string {
  const blocks = picks.map((p) => p.block).join("\n");
  return text ? `${text}\n\n${blocks}` : blocks;
}

// ── Reading the blocks back out for display ────────────────────────────────

/** ElementCardData is the render-side read of a <page-element> block: what the
 * ElementCard component draws. The message keeps its raw block for the model;
 * the bubble shows display text plus these cards — the task-notification
 * envelope→card pattern, applied to user messages. */
export interface ElementCardData {
  title: string;
  url: string;
  /** Local display snapshot, absent on legacy observations. */
  favicon?: string;
  selector: string;
  element: string;
  text: string;
  position: string;
  /** The page-volunteered data, verbatim single-line JSON from the block. */
  data: string;
  /** The serialized pruned markup, verbatim from the block. It is safe HTML by
   * construction (whitelisted attrs, escaped text), which is exactly why the
   * preview can embed it without any unescaping — unescaping would be the bug,
   * restoring markup-looking page text as real markup. */
  html: string;
}

export interface PickedDisplay {
  /** The message minus its blocks: what the bubble shows as prose. */
  display: string;
  cards: ElementCardData[];
}

const BLOCK_RE = /<page-element source="([^"]*)" url="([^"]*)">\n([\s\S]*?)\n<\/page-element>/g;

// Only decode wrapper attributes. Exhibit markup stays verbatim; decoded
// URLs are validated again by display/open helpers before use.
function attributeText(value: string): string {
  return value.replace(/&(amp|quot|lt|gt);/g, (_all, entity: string) => ({ amp: "&", quot: '"', lt: "<", gt: ">" })[entity]!);
}

/** field pulls a `key: value` line out of a block body; the html field is the
 * remainder after `html:` (multi-line, verbatim). */
function field(body: string, key: string): string {
  const m = body.match(new RegExp(`^${key}: (.*)$`, "m"));
  return m ? m[1] : "";
}

function htmlField(body: string): string {
  const m = body.match(/^html:\n([\s\S]*)$/m);
  return m ? m[1] : "";
}

/** Newly authored references keep the complete original quotation in JSON.
 * Return it as display text only: Vue interpolation escapes it, and it never
 * becomes preview HTML or another parsed page-element block. Legacy fields
 * and markup are deliberately not HTML-decoded. */
function authoredReferenceText(body: string, data: string): string {
  try {
    const value = JSON.parse(data);
    if (value?.kind === "user-authored-web-reference" && typeof value.excerpt === "string" && value.excerpt.length <= 12000) return value.excerpt;
  } catch { /* Older page-volunteered data need not be JSON. */ }
  return field(body, "text");
}

/**
 * parsePageElementBlocks splits a sent message into display prose and element
 * cards. Tolerant like the other envelope parsers: anything that does not
 * match the block shape stays in the display text, because a half-block in the
 * prose is information and silently eating it is not.
 */
export function parsePageElementBlocks(text: string): PickedDisplay {
  const cards: ElementCardData[] = [];
  const display = text.replace(BLOCK_RE, (_all, source: string, url: string, body: string) => {
    const data = field(body, "data");
    cards.push({
      title: attributeText(source),
      url: attributeText(url),
      selector: field(body, "selector"),
      element: field(body, "element"),
      text: authoredReferenceText(body, data),
      position: field(body, "position"),
      data,
      html: htmlField(body),
    });
    return "\x00"; // marker: cut, then collapse the seams below
  });
  return { display: display.split("\x00").map((s) => s.replace(/^\s+|\s+$/g, "")).join("\n").replace(/\n{3,}/g, "\n\n").trim(), cards };
}

// The transcript re-parses every user message on every stream tick, and the
// same text arrives many times. A plain memo keyed by the exact string is all
// the cleverness this needs; the cap keeps a long session from growing it
// without bound.
const parseCache = new Map<string, PickedDisplay>();
const PARSE_CACHE_MAX = 300;

export function parsePickedCached(text: string): PickedDisplay {
  const hit = parseCache.get(text);
  if (hit) return hit;
  const parsed = parsePageElementBlocks(text);
  if (parseCache.size >= PARSE_CACHE_MAX) {
    parseCache.delete(parseCache.keys().next().value as string);
  }
  parseCache.set(text, parsed);
  return parsed;
}
