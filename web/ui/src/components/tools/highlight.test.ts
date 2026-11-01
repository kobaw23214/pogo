import { describe, expect, it } from "vitest";
import { highlightSegments } from "./highlight";

describe("highlightSegments", () => {
  it("returns the text untouched for an empty query", () => {
    expect(highlightSegments("mal_search", "")).toEqual([{ text: "mal_search", hit: false }]);
    expect(highlightSegments("mal_search", "   ")).toEqual([{ text: "mal_search", hit: false }]);
  });

  it("marks a single case-insensitive occurrence", () => {
    expect(highlightSegments("mal_search", "search")).toEqual([
      { text: "mal_", hit: false },
      { text: "search", hit: true },
    ]);
    expect(highlightSegments("Malware analysis", "malware")).toEqual([
      { text: "Malware", hit: true },
      { text: " analysis", hit: false },
    ]);
  });

  it("marks every occurrence, not just the first", () => {
    expect(highlightSegments("sample hash of the sample", "sample")).toEqual([
      { text: "sample", hit: true },
      { text: " hash of the ", hit: false },
      { text: "sample", hit: true },
    ]);
  });

  it("reports no hits when the query is absent", () => {
    expect(highlightSegments("mal_search", "wipe")).toEqual([{ text: "mal_search", hit: false }]);
  });

  it("degrades to unhighlighted text when lowercasing changes the length", () => {
    // "İ".toLowerCase() is two code units ("i̇"), which would shift every
    // index after it — the guard must refuse rather than mark the wrong run.
    const text = "İstanbul sample";
    expect(highlightSegments(text, "sample")).toEqual([{ text, hit: false }]);
  });

  it("highlights CJK queries the same as ASCII", () => {
    expect(highlightSegments("样本查询：按哈希查详情", "样本")).toEqual([
      { text: "样本", hit: true },
      { text: "查询：按哈希查详情", hit: false },
    ]);
  });
});
