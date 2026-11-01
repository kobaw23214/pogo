import { describe, expect, it } from "vitest";
import { hasResultArgs, isExactResultJSON, resultTextPreview, structuredResultText } from "./resultPresentation";

describe("literal external-result previews", () => {
  it("keeps short results exactly, including their leading whitespace", () => {
    expect(resultTextPreview("  first\nsecond\n")).toEqual({ text: "  first\nsecond\n", shortened: false });
  });
  it("retains an accurate bounded prefix and exposes that it is incomplete", () => {
    const text = Array.from({ length: 30 }, (_, n) => `row ${n}`).join("\n");
    const result = resultTextPreview(text);
    expect(result.text).toBe(text.slice(0, result.text.length));
    expect(result.text.split("\n")).toHaveLength(10);
    expect(result.shortened).toBe(true);
    expect(resultTextPreview("a".repeat(1400)).text).toHaveLength(1200);
  });
  it("does not split a surrogate pair at the character bound", () => {
    expect(resultTextPreview("x😀y", 2).text).toBe("x");
    expect(resultTextPreview("x😀y", 3).text).toBe("x😀");
  });
  it("keeps a small, pretty-printed structured result complete", () => {
    const text = JSON.stringify({ items: [{ id: 1, title: "first", score: 3 }, { id: 2, title: "second", score: 4 }] }, null, 2);
    expect(text.split("\n").length).toBeGreaterThan(10);
    expect(resultTextPreview(text)).toEqual({ text, shortened: false });
  });
  it("does not conflate different large integer text or parse malformed JSON", () => {
    expect(isExactResultJSON('{"id":9007199254740993}', { id: 9007199254740992 })).toBe(false);
    expect(isExactResultJSON('{"id":9007199254740992}', { id: 9007199254740992 })).toBe(true);
    expect(isExactResultJSON('{"id":1,"id":2}', { id: 2 })).toBe(false);
    expect(isExactResultJSON('{"id":1} extra', { id: 1 })).toBe(false);
    expect(isExactResultJSON('{ "id": 1 }', { id: 1 })).toBe(false);
  });
  it.each([false, 0, "", [], {}].map(value => [value]))("retains the structured value %j", value => {
    expect(structuredResultText(value)).toBe(JSON.stringify(value, null, 2));
  });
  it("omits only absent or empty arguments, never scalar arguments", () => {
    for (const value of [null, undefined, {}, []]) expect(hasResultArgs(value)).toBe(false);
    for (const value of [false, 0, "", "raw", { query: "abc" }]) expect(hasResultArgs(value)).toBe(true);
  });
});
