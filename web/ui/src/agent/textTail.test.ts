import { describe, expect, it } from "vitest";
import { continuesLine, packLines, recentTailLines, revealStep, splitClauses } from "./textTail";

describe("splitClauses", () => {
  it("splits at CJK terminals and commas, keeping punctuation attached", () => {
    expect(splitClauses("先看文件，再改代码。现在正在跑测试")).toEqual([
      "先看文件，",
      "再改代码。",
      "现在正在跑测试",
    ]);
  });

  it("splits at Latin punctuation and line breaks", () => {
    expect(splitClauses("Read the file. Now editing it, carefully")).toEqual([
      "Read the file.",
      "Now editing it,",
      "carefully",
    ]);
    expect(splitClauses("第一行\n第二行\n第三行")).toEqual(["第一行", "第二行", "第三行"]);
  });

  it("returns one in-flight clause when there is no boundary yet", () => {
    expect(splitClauses("还没写完的一句话")).toEqual(["还没写完的一句话"]);
  });

  it("drops empty pieces from trailing or doubled boundaries", () => {
    expect(splitClauses("。。好")).toEqual(["好"]);
    expect(splitClauses("完了。\n\n")).toEqual(["完了。"]);
  });

  it("returns empty for empty input", () => {
    expect(splitClauses("")).toEqual([]);
    expect(splitClauses("   ")).toEqual([]);
  });
});

describe("revealStep", () => {
  it("reveals one line per tick while the backlog is small", () => {
    expect(revealStep(0)).toBe(0);
    expect(revealStep(-1)).toBe(0);
    expect(revealStep(1)).toBe(1);
    expect(revealStep(4)).toBe(1);
  });

  it("accelerates smoothly on a backlog instead of skipping lines", () => {
    expect(revealStep(5)).toBe(2);
    expect(revealStep(10)).toBe(3);
    expect(revealStep(50)).toBe(11);
  });
});

describe("packLines", () => {
  it("accumulates short clauses onto one line instead of one clause per line", () => {
    // 每句 6 全角字 = 12 units；预算 72 units 攒得下 6 句
    const out = packLines("先看文件，再改代码，然后跑测试，最后提交，收尾清理，写总结。", 72);
    expect(out).toEqual(["先看文件，再改代码，然后跑测试，最后提交，收尾清理，写总结。"]);
  });

  it("breaks when adding the next clause would overflow the budget", () => {
    // 每句 7 汉字 = 14 units；预算 24：两句 28 超预算，故每行至多一句，
    // 末行 16+8=24 恰好放下两句。
    const out = packLines("一句一句攒着，攒到预算为止，下一句另起一行，就这样。", 24);
    expect(out).toEqual(["一句一句攒着，", "攒到预算为止，", "下一句另起一行，就这样。"]);
  });

  it("a hard newline from the stream forces a line break", () => {
    const out = packLines("短句一\n短句二", 72);
    expect(out).toEqual(["短句一", "短句二"]);
  });

  it("re-joins Latin clauses with a space but not CJK ones", () => {
    const out = packLines("Read the file. Now edit it, then run tests", 72);
    expect(out).toEqual(["Read the file. Now edit it, then run tests"]);
  });

  it("counts a CJK glyph as one ASCII pair when budgeting", () => {
    // 36 个汉字 = 72 units 恰好一行；第 37 个字另起
    const out = packLines("汉".repeat(36) + "。尾", 72);
    expect(out).toEqual(["汉".repeat(36) + "。", "尾"]);
  });

  it("keeps a single overlong clause on its own line", () => {
    const out = packLines("很长的没有标点的句子".repeat(10), 72);
    expect(out.length).toBe(1);
  });

  it("returns empty for empty input", () => {
    expect(packLines("")).toEqual([]);
  });
});

describe("continuesLine", () => {
  it("treats appended text as the same line growing", () => {
    expect(continuesLine("正在读配", "正在读配置文件")).toBe(true);
  });

  it("treats a different line as a replacement, not a continuation", () => {
    expect(continuesLine("第一句话还没写完", "第二句话已经开始了")).toBe(false);
  });

  it("counts the first text of a stream as a continuation of nothing", () => {
    expect(continuesLine("", "第一句")).toBe(true);
  });
});

describe("recentTailLines", () => {
  it("shows the first unfinished fragment without waiting for a complete line", () => {
    expect(recentTailLines("正在")).toEqual([{ key: 0, text: "正在" }]);
  });
  it("keeps the latest rows immediately after a burst rather than replaying a backlog", () => {
    const text = Array.from({ length: 30 }, (_, n) => `line ${n}`).join("\n");
    expect(recentTailLines(text).map(row => row.text)).toEqual(["line 27", "line 28", "line 29"]);
    for (const row of recentTailLines(text)) expect(text.slice(row.key, row.key + row.text.length)).toBe(row.text);
  });
  it("wraps long unfinished clauses while keeping literal text and stable row identities", () => {
    const first = recentTailLines("abcdefghijkl", 5);
    expect(first).toEqual([{ key: 0, text: "abcde" }, { key: 5, text: "fghij" }, { key: 10, text: "kl" }]);
    expect(recentTailLines("abcdefghijklm", 5)).toEqual([{ key: 0, text: "abcde" }, { key: 5, text: "fghij" }, { key: 10, text: "klm" }]);
  });
  it("does not split emoji and counts CJK width when selecting rows", () => {
    expect(recentTailLines("汉😀字终", 4).map(row => row.text)).toEqual(["汉😀", "字终"]);
    expect(recentTailLines("汉😀字终", 4)[1].key).toBe(3);
  });
  it("retains whitespace without modifying the original stream", () => {
    const text = "  first\n\nsecond ";
    expect(recentTailLines(text).map(row => row.text)).toEqual(["  first", "", "second "]);
    expect(text).toBe("  first\n\nsecond ");
    expect(recentTailLines("")).toEqual([]);
  });
  it("uses a compatible custom window size and safe defaults for invalid dimensions", () => {
    expect(recentTailLines("one\ntwo\nthree", 72, 2).map(row => row.text)).toEqual(["two", "three"]);
    expect(recentTailLines("short", Number.NaN, 0)).toEqual([{ key: 0, text: "short" }]);
  });
});
