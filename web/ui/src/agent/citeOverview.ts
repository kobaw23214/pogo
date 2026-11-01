import type { CiteSource } from "@/api/types";
import { groupSources, type SourceGroup } from "./cite";

export interface SourceOverview {
  cited: SourceGroup[];
  others: SourceGroup[];
  operations: SourceGroup[];
}

/** Old transcripts predate UI receipt metadata, but keep their source type
 * and locator. Recognize those browser placeholders without hiding a real
 * website merely because its page title happens to be "Browser". */
export function isBrowserOverviewOperation(src: CiteSource): boolean {
  if (src.browser_status_only) return true;
  if (src.type !== "web_page" && src.type !== "tool") return false;
  const locator = src.locator.trim();
  // This locator identifies an invocation (often a connection/lease error),
  // not a web document. It remains available in the work log and can still be
  // cited explicitly when the answer is about that diagnostic.
  if (/^tool:\/\/browser\/[^\s]+$/i.test(locator)) return true;
  if (src.type !== "web_page" || !/^about:blank(?:[?#].*)?$/i.test(locator)) return false;
  if (!["", "about:blank", "browser"].includes(src.title.trim().toLowerCase())) return false;

  // A page can write real content into about:blank. Retain such material;
  // filter only an empty snapshot or the browser's tab-inventory envelope.
  const lines = (src.content ?? src.excerpt).trim().split(/\r?\n/);
  if (/^\[cite:[^\r\n]*\]$/.test(lines[0] ?? "")) lines.shift();
  if (/^runtime: [^;\r\n]+; tab: [^;\r\n]*; version: [^\r\n]*$/.test(lines[0] ?? "")) lines.shift();
  const prefix = `page: ${locator}`;
  if (lines[0] === prefix || lines[0]?.startsWith(prefix + " · ")) lines.shift();
  const body = lines.join("\n").trim();
  if (!body || body === "No matching elements or readable page text.") return true;
  if (body.length > 32_000) return false;
  try {
    const tabs: unknown = JSON.parse(body);
    const keys = new Set(["id", "url", "title", "control", "control_epoch", "active", "favicon"]);
    return Array.isArray(tabs) && tabs.every(tab => tab && typeof tab === "object" && !Array.isArray(tab)
      && typeof tab.id === "string" && typeof tab.url === "string" && typeof tab.title === "string"
      && Object.keys(tab).every(key => keys.has(key)));
  } catch {
    return false;
  }
}

/**
 * A source list is a reading aid, not a count of tool calls. Preserve every
 * explicit citation and all uncertain materials. Known empty browser receipts
 * and legacy browser placeholders stay in the work log when not cited.
 */
export function groupSourceOverview(sources: CiteSource[], nums: Map<string, number>): SourceOverview {
  const unique = new Map<string, CiteSource>();
  for (const source of sources) if (!unique.has(source.label)) unique.set(source.label, source);
  const groups = groupSources([...unique.values()], nums);
  return {
    cited: groups.cited,
    others: groups.others.filter(({ src }) => !isBrowserOverviewOperation(src)),
    operations: groups.others.filter(({ src }) => isBrowserOverviewOperation(src)),
  };
}
