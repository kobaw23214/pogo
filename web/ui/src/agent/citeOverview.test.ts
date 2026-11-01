import { describe, expect, it } from "vitest";
import type { CiteSource } from "@/api/types";
import { collectSources, numberCitations } from "./cite";
import { groupSourceOverview, isBrowserOverviewOperation } from "./citeOverview";

const source = (label: string, over: Partial<CiteSource> = {}): CiteSource => ({
  label, type: "web_page", title: "Original material", locator: "https://example.test/guide",
  call_id: `tool-${label}`, excerpt: "Recorded page text", hash: label, ts: 1, ...over,
});

describe("legacy browser placeholders", () => {
  const blank = source("S1", { title: "about:blank", locator: "about:blank", excerpt: "runtime: native; tab: ; version: \npage: about:blank\n[]", content: undefined });
  const browser = source("S2", { type: "tool", title: "browser", locator: "tool://browser/tool-call-2", excerpt: "Browser connection was unavailable.", error: true });

  it("keeps old empty-page and generic browser records out of the default material overview", () => {
    const groups = groupSourceOverview([blank, browser, { ...browser, label: "S3", type: "web_page", error: undefined }], new Map());
    expect(groups.others).toEqual([]);
    expect(groups.operations.map(item => item.src.label)).toEqual(["S1", "S2", "S3"]);
  });

  it("recognizes the real legacy tab inventory shapes without new metadata", () => {
    for (const body of [
      "", "No matching elements or readable page text.", "[]",
      '[{"id":"t1","url":"about:blank","title":""}]',
      '[{"id":"t2","url":"https://gitlab.example.test/","title":"GitLab","control":"human","control_epoch":2}]',
    ]) expect(isBrowserOverviewOperation({ ...blank, content: `page: about:blank\n${body}` })).toBe(true);
  });

  it("never suppresses a real web source based on the Browser title", () => {
    for (const title of ["Browser", "browser", "GitLab", "about:blank"]) {
      expect(isBrowserOverviewOperation(source("S1", { title, locator: "https://gitlab.example.test/project" }))).toBe(false);
    }
    expect(isBrowserOverviewOperation({ ...browser, type: "mcp_tool" })).toBe(false);
    expect(isBrowserOverviewOperation({ ...browser, locator: "tool://browser-reader/task" })).toBe(false);
  });

  it("preserves actual page content written into about:blank and unfamiliar legacy content", () => {
    for (const body of ["An actual generated report.", '[{"id":"row1","url":"https://example.test","title":"Record","evidence":"Actual data"}]', "[truncated tab output"]) {
      expect(isBrowserOverviewOperation({ ...blank, content: `page: about:blank\n${body}` })).toBe(false);
    }
    expect(isBrowserOverviewOperation({ ...blank, title: "Generated report", content: "" })).toBe(false);
  });

  it("keeps explicit blank-page or error citations resolvable instead of creating broken links", () => {
    const groups = groupSourceOverview([blank, browser], new Map([["S2", 1], ["S1", 2]]));
    expect(groups.cited.map(item => [item.src.label, item.num])).toEqual([["S2", 1], ["S1", 2]]);
    expect(groups.operations).toEqual([]);
    expect(groups.others).toEqual([]);
  });
});
const receipt = "runtime: native; tab: t1; version: v1\npage: https://example.test/guide · Original material · 2 interactive\n";
const details = { action: "click", ok: true, url: "https://example.test/guide" };

describe("source overview", () => {
  it("counts explicit citations in answer order and preserves other materials separately", () => {
    const sources = [source("S1"), source("S2"), source("S3"), source("S2", { title: "duplicate must not overwrite" })];
    const groups = groupSourceOverview(sources, numberCitations("[1](#cite:S3) [1](#cite:S1)", new Set(sources.map(src => src.label))).nums);
    expect(groups.cited.map(item => [item.src.label, item.num])).toEqual([["S3", 1], ["S1", 2]]);
    expect(groups.others.map(item => item.src.label)).toEqual(["S2"]);
    expect(groups.others[0].src.title).toBe("Original material");
    expect(groups.operations).toEqual([]);
  });

  it("never promotes consulted materials to citations when the answer has no markers", () => {
    const groups = groupSourceOverview([source("S1"), source("S2")], new Map());
    expect(groups.cited).toEqual([]);
    expect(groups.others).toHaveLength(2);
  });

  it("preserves explicitly cited operation results and leaves only uncited empty receipts in the log", () => {
    const sources = [source("S1", { browser_status_only: true }), source("S2", { browser_status_only: true }), source("S3")];
    const groups = groupSourceOverview(sources, new Map([["S1", 1]]));
    expect(groups.cited.map(item => item.src.label)).toEqual(["S1"]);
    expect(groups.others.map(item => item.src.label)).toEqual(["S3"]);
    expect(groups.operations.map(item => item.src.label)).toEqual(["S2"]);
  });
});

describe("recorded browser status classification", () => {
  it.each(["navigate", "click", "scroll", "type", "press", "select", "hover", "tab"])("marks %s only when the complete result is an empty browser envelope", (action) => {
    const src = source("S1");
    const [collected] = collectSources([{ result: { source: src, text: receipt, details: { ...details, action } } }]);
    expect(collected.browser_status_only).toBe(true);
    expect(collected.content).toBe(receipt);
    expect(src.browser_status_only).toBeUndefined();
  });

  it.each([
    ["page text", receipt + "The limit is 30 requests per minute.", details],
    ["screenshot", receipt + "viewport screenshot: .pogo/page.png (1280x720 pixels)", details],
    ["data", receipt + 'data: {"count":2}', details],
    ["unknown action", receipt, { ...details, action: "future" }],
    ["snapshot", receipt, { ...details, action: "snapshot" }],
    ["unknown status", receipt, { action: "click", url: details.url }],
    ["missing metadata", receipt, undefined],
    ["another page", receipt, { ...details, url: "https://elsewhere.test/" }],
    ["truncated", receipt, { ...details, truncated: true }],
    ["spill output", receipt, { ...details, full_output_path: "artifact.txt" }],
    ["unstructured output", "OK", details],
  ])("keeps %s available as consulted material", (_label, text, metadata) => {
    const [collected] = collectSources([{ result: { source: source("S1"), text: text as string, details: metadata } }]);
    expect(collected.browser_status_only).not.toBe(true);
  });

  it("preserves errors, legacy stamps and non-browser sources", () => {
    for (const result of [
      { source: source("S1"), text: receipt, details, is_error: true },
      { source: source("S1"), details },
      { source: source("S1", { type: "mcp_tool" }), text: receipt, details },
      { source: source("S1", { full_path: "full-output.txt" }), text: receipt, details },
      { source: source("S1", { error: true }), text: receipt, details },
    ]) expect(collectSources([{ result }])[0].browser_status_only).not.toBe(true);
  });

  it("recomputes presentation metadata instead of trusting a stale flag on a stamp", () => {
    const [collected] = collectSources([{ result: { source: source("S1", { browser_status_only: true }), text: receipt + "New page content", details } }]);
    expect(collected.browser_status_only).toBeUndefined();
  });
});
