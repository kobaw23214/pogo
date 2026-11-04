import { describe, expect, it } from "vitest";
import { compactionReduction, compactionSummaryExcerpt, compactionTokens } from "./compactionPresentation";

describe("compaction presentation", () => {
  it("computes reduction and remaining usage from real token counts", () => {
    expect(compactionReduction(8710, 1930)).toBe(78);
    expect(100 - compactionReduction(8710, 1930)!).toBe(22);
    expect(compactionReduction(0, 100)).toBeNull();
    expect(compactionReduction(100, 120)).toBe(0);
  });

  it("formats token counts without repeating the unit", () => {
    expect(compactionTokens(8710)).toBe("8.7K");
    expect(compactionTokens(1930)).toBe("1.9K");
    expect(compactionTokens(1_000_000)).toBe("1M");
    expect(compactionTokens(930)).toBe("930");
  });

  it("uses the first meaningful summary item as the concise overview", () => {
    const summary = "## 任务目标\n\n- 保留销售折线图的同比数据和交互。\n- 第二段只用于完整摘要。";
    expect(compactionSummaryExcerpt(summary)).toBe("保留销售折线图的同比数据和交互。");
    expect(compactionSummaryExcerpt("## 标题\n\n")).toBe("");
    expect(compactionSummaryExcerpt(`- ${"长".repeat(80)}`, 40)).toHaveLength(40);
  });
});
