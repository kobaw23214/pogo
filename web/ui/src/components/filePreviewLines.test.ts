import { describe, expect, it } from "vitest";
import type { FileContent } from "@/api/types";
import { fileLineSelection, preservePreviewTrailingLine } from "./filePreviewLines";

const textFile = (text: string, overrides: Partial<FileContent> = {}): FileContent => ({
  path: "src/example.ts", text, size: text.length, ...overrides,
});

describe("fileLineSelection", () => {
  it("keeps ordinary previews unselected", () => {
    expect(fileLineSelection(textFile("one\ntwo"))).toEqual({ status: "none" });
  });

  it("uses one-based inclusive line numbers and includes blank lines", () => {
    expect(fileLineSelection(textFile("one\n\nthree\n"), 2, 3)).toEqual({ status: "available", lineStart: 2, lineEnd: 3 });
    expect(fileLineSelection(textFile("one\r\ntwo\r\n"), 2)).toEqual({ status: "available", lineStart: 2, lineEnd: 2 });
    expect(fileLineSelection(textFile("\n\n"), 2)).toEqual({ status: "available", lineStart: 2, lineEnd: 2 });
  });

  it("never clamps an unavailable target onto a different line", () => {
    expect(fileLineSelection(textFile("one\ntwo\n"), 3)).toEqual({ status: "unavailable", reason: "outOfRange" });
    expect(fileLineSelection(textFile("one\ntwo"), 1, 3)).toEqual({ status: "unavailable", reason: "outOfRange" });
    expect(fileLineSelection(textFile(""), 1)).toEqual({ status: "unavailable", reason: "outOfRange" });
  });

  it("rejects reversed, nonpositive, fractional, unsafe, or incomplete ranges", () => {
    for (const [start, end] of [[0, 1], [-1, 2], [2, 1], [1.5, 2], [1, 2.5], [NaN, 1], [1, Infinity], [1, Number.MAX_SAFE_INTEGER + 1], [undefined, 2]]) {
      expect(fileLineSelection(textFile("one\ntwo"), start, end)).toEqual({ status: "unavailable", reason: "invalid" });
    }
  });

  it("does not pretend binary content has source lines", () => {
    expect(fileLineSelection({ path: "report.pdf", binary: true, size: 300 }, 1)).toEqual({ status: "unavailable", reason: "nonText" });
  });

  it("only permits complete rows before a byte cutoff", () => {
    const content = textFile("first\npart", { truncated: true, truncated_by: "bytes" });
    expect(fileLineSelection(content, 1)).toEqual({ status: "available", lineStart: 1, lineEnd: 1 });
    expect(fileLineSelection(content, 2)).toEqual({ status: "unavailable", reason: "truncated" });
    expect(fileLineSelection(content, 1, 2)).toEqual({ status: "unavailable", reason: "truncated" });
    expect(fileLineSelection(textFile("first\n", { truncated: true, truncated_by: "bytes" }), 1)).toEqual({ status: "available", lineStart: 1, lineEnd: 1 });
  });

  it("handles the last empty row at a line cutoff without dropping or inventing rows", () => {
    const content = textFile("first\n", { truncated: true, truncated_by: "lines" });
    expect(preservePreviewTrailingLine(content)).toBe(true);
    expect(fileLineSelection(content, 2)).toEqual({ status: "available", lineStart: 2, lineEnd: 2 });
    expect(fileLineSelection(content, 3)).toEqual({ status: "unavailable", reason: "truncated" });
    expect(preservePreviewTrailingLine(textFile("first\n"))).toBe(false);
  });

  it("is conservative when an older truncated response omits its cutoff kind", () => {
    expect(fileLineSelection(textFile("first\npartial", { truncated: true }), 2)).toEqual({ status: "unavailable", reason: "truncated" });
  });
});
