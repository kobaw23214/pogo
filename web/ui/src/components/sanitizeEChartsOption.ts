/**
 * Sanitizing for chart options that came out of a model's answer.
 *
 * The fence contract says the block is a JSON option object, and JSON.parse is
 * the first line of defense: functions, computed keys and expressions simply
 * cannot exist in parsed JSON, which is what rules out the option fields
 * ECharts treats as code. What survives parsing is data — but ECharts embeds
 * some of that data as raw HTML, CSS and URLs (its own security guide lists
 * the fields), and a model's output is untrusted input like any other: a
 * prompt-injected answer must not get script into the page through a tooltip.
 *
 * So the walk keeps JSON types and drops the handful of fields the guide names,
 * with budgets so a hostile block cannot instead exhaust the tab.
 */

/** Reject rather than walk: bounds the sanitize pass itself. */
const MAX_NODES = 20_000;
const MAX_STRING_LENGTH = 20_000;
const MAX_DEPTH = 64;

/** Fields ECharts renders as raw HTML or CSS, no matter where they sit. */
const DROP_KEYS = new Set(["extraCssText", "optionToContent", "contentToOption", "dataView"]);
/** dataset transform configs compile this to a RegExp and run it per row. */
const DROP_REGEXP_KEYS = new Set(["reg"]);
/** Fields used as URLs as-is; javascript: and data: execute or smuggle. */
const URL_KEYS = new Set(["link", "sublink", "image", "src", "backgroundImage"]);
/** A formatter string containing markup is a tooltip rendered as HTML. */
const FORMATTER_MARKUP = /[<]|&#|&[a-zA-Z]+;|javascript:/i;

export type SanitizeResult =
  | { ok: true; option: Record<string, unknown> }
  | { ok: false; reason: "json" | "shape" | "budget" };

function isHTTPURL(value: string): boolean {
  return /^https?:\/\//i.test(value.trim()) || value.trim().startsWith("/");
}

/**
 * walk returns a sanitized copy, or null once the budget is blown — the caller
 * treats that as a refusal, because at that size the block is hostile or
 * useless either way.
 */
function walk(value: unknown, depth: number, budget: { count: number }): unknown | null {
  if (++budget.count > MAX_NODES || depth > MAX_DEPTH) return null;

  if (Array.isArray(value)) {
    const out = new Array(value.length);
    for (const [i, item] of value.entries()) {
      const safe = walk(item, depth + 1, budget);
      if (safe === null) return null;
      out[i] = safe;
    }
    return out;
  }
  if (typeof value === "object" && value !== null) {
    const out: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(value)) {
      const safe = cleanEntry(key, item, depth, budget);
      if (safe === null) return null;
      out[key] = safe;
    }
    return out;
  }
  if (typeof value === "string" && value.length > MAX_STRING_LENGTH) return null;
  return value;
}

function cleanEntry(key: string, value: unknown, depth: number, budget: { count: number }): unknown | null {
  if (DROP_KEYS.has(key) || DROP_REGEXP_KEYS.has(key)) return undefined;

  if (URL_KEYS.has(key)) {
    if (typeof value === "string" && !isHTTPURL(value)) return undefined;
    // Non-string values under these keys are inert; let ECharts reject them.
  }

  if (key === "formatter" && typeof value === "string" && FORMATTER_MARKUP.test(value)) {
    // "{b}: {c}" is a text template and stays; "<b>{b}</b>" is HTML and goes.
    return undefined;
  }

  return walk(value, depth + 1, budget);
}

/**
 * sanitizeEChartsOption parses and cleans a ```echarts fence body.
 *
 * The result is a fresh object tree (nothing shared with the input), safe to
 * hand to setOption with notMerge: true.
 */
export function sanitizeEChartsOption(source: string): SanitizeResult {
  let parsed: unknown;
  try {
    parsed = JSON.parse(source);
  } catch {
    return { ok: false, reason: "json" };
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    return { ok: false, reason: "shape" };
  }
  const safe = walk(parsed, 0, { count: 0 });
  if (safe === null || typeof safe !== "object") return { ok: false, reason: "budget" };
  return { ok: true, option: safe as Record<string, unknown> };
}
