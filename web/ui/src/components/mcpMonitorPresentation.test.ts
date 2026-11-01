import { describe, expect, it } from "vitest";
import { mcpCallFailed, mcpHighlightQueries, mcpHighlightSegments } from "./mcpMonitorPresentation";
import type { McpCall } from "./mcpMonitor";

describe("MCP search presentation", () => {
  it("combines only the committed full-text query and the matching field filter", () => {
    const filter = { q: " 金价 ", server: " yw-readonly ", tool: " get_price " };
    expect(mcpHighlightQueries(filter)).toEqual(["金价"]);
    expect(mcpHighlightQueries(filter, "server")).toEqual(["金价", "yw-readonly"]);
    expect(mcpHighlightQueries(filter, "tool")).toEqual(["金价", "get_price"]);
    expect(mcpHighlightQueries({ q: "Tool", server: " tool ", tool: "" }, "server")).toEqual(["Tool"]);
  });
  it("merges overlapping literal hits without changing text, including repeated Chinese", () => {
    const text = "金价 goldprice 金价";
    const parts = mcpHighlightSegments(text, ["金价", "gold", "price", "oldpr"]);
    expect(parts.map(part => part.text).join("")).toBe(text);
    expect(parts.filter(part => part.hit).map(part => part.text)).toEqual(["金价", "goldprice", "金价"]);
  });
  it("treats HTML and regex syntax as plain text and clearing removes every hit", () => {
    const text = '<img src=x onerror="alert(1)"> gold[.*]';
    expect(mcpHighlightSegments(text, ["[.*]"]).filter(part => part.hit)).toEqual([{ text: "[.*]", hit: true }]);
    expect(mcpHighlightSegments(text, ["<img"]).map(part => part.text).join("")).toBe(text);
    expect(mcpHighlightSegments(text, ["", " "])).toEqual([{ text, hit: false }]);
  });
  it("keeps Unicode casing length changes unhighlighted rather than corrupting text", () => {
    for (const [text, query] of [["İstanbul", "i"], ["i̇stanbul", "İ"]]) expect(mcpHighlightSegments(text!, [query!])).toEqual([{ text, hit: false }]);
  });
  it("only categorizes error and timeout as failed, never inferring a cause from error_kind", () => {
    for (const status of ["error", "timeout"]) expect(mcpCallFailed({ status } as McpCall)).toBe(true);
    for (const status of ["ok", "running", "cancelled", "interrupted", "unknown"]) expect(mcpCallFailed({ status, error_kind: "transport" } as McpCall)).toBe(false);
  });
});
