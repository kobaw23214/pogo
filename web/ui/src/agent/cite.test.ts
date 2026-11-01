import { describe, expect, it } from "vitest";
import {
  collectSources,
  compareSourceLabels,
  describeSource,
  groupSources,
  numberCitations,
  planCitations,
  rewriteCitations,
  shortHost,
  sourceTypeName,
  stripCode,
} from "./cite";
import type { CiteSource } from "@/api/types";

const stamp = (label: string, over: Partial<CiteSource> = {}): CiteSource => ({
  label,
  type: "command_exec",
  title: `exit 0 · cmd ${label}`,
  locator: `file://x/${label}`,
  call_id: `tool_${label}`,
  excerpt: "out",
  hash: "abc",
  ts: 1785780000000,
  ...over,
});

describe("numberCitations", () => {
  it("numbers by first appearance, not by wire number or label order", () => {
    const { nums, unknown } = numberCitations("见 [1](#cite:S7) 与 [1](#cite:S2)，再 [1](#cite:S7)", new Set(["S2", "S7"]));
    expect(nums.get("S7")).toBe(1);
    expect(nums.get("S2")).toBe(2);
    expect(unknown).toEqual([]);
  });

  it("marks labels the known set does not cover, without numbering them", () => {
    const { nums, unknown } = numberCitations("[1](#cite:S1) 和 [1](#cite:S9)", new Set(["S1"]));
    expect(nums.get("S1")).toBe(1);
    expect(nums.has("S9")).toBe(false);
    expect(unknown).toEqual(["S9"]);
  });

  it("numbers everything when the context never carried sources (null known set)", () => {
    const { nums, unknown } = numberCitations("[1](#cite:S9)", null);
    expect(nums.get("S9")).toBe(1);
    expect(unknown).toEqual([]);
  });

  it("exempts anchors inside fenced code and inline code", () => {
    const text = [
      "```",
      "[1](#cite:S1) 在围栏里不算",
      "```",
      "正文 [1](#cite:S2)，还有行内 `[1](#cite:S3)`",
    ].join("\n");
    const { nums } = numberCitations(text, new Set(["S1", "S2", "S3"]));
    expect([...nums.keys()]).toEqual(["S2"]);
  });

  it("handles adjacent anchors and stray whitespace", () => {
    const { nums } = numberCitations("[1](#cite:S1)[ 2 ](#cite:S2)", new Set(["S1", "S2"]));
    expect(nums.get("S1")).toBe(1);
    expect(nums.get("S2")).toBe(2);
  });
});

describe("planCitations", () => {
  it("keeps known reference numbers stable when earlier metadata arrives", () => {
    const text = "[1](#cite:S9) [1](#cite:S2)";
    const pending = planCitations(text, new Set(["S2"]));
    const ready = planCitations(text, new Set(["S9", "S2"]));
    expect(pending[0].num).toBeUndefined();
    expect(pending[1].num).toBe(2);
    expect(ready[1].num).toBe(2);
    expect(numberCitations(text, new Set(["S2"])).nums.has("S9")).toBe(false);
  });
  it("does not count adjacent repetitions as additional sources", () => {
    const plan = planCitations("[1](#cite:S1) [1](#cite:S1) [1](#cite:S2)", new Set(["S1", "S2"]));
    expect(plan.map((p) => p.size)).toEqual([2, 2, 2]);
    expect(plan.map((p) => p.head)).toEqual([true, false, false]);
    expect(planCitations("[1](#cite:S1) [1](#cite:S1)", new Set(["S1"]))[0].size).toBe(1);
  });
  it("folds adjacent anchors into one group, head first", () => {
    const plan = planCitations("[1](#cite:S1) [1](#cite:S2)", new Set(["S1", "S2"]));
    expect(plan).toHaveLength(2);
    expect(plan[0]).toMatchObject({ label: "S1", num: 1, unknown: false, group: 0, index: 0, size: 2, head: true });
    expect(plan[1]).toMatchObject({ label: "S2", num: 2, unknown: false, group: 0, index: 1, size: 2, head: false });
  });

  it("folds runs of three, tab-separated too", () => {
    const plan = planCitations("[1](#cite:S1)\t[1](#cite:S2) [1](#cite:S3)", new Set(["S1", "S2", "S3"]));
    expect(plan.map((p) => p.group)).toEqual([0, 0, 0]);
    expect(plan.map((p) => p.size)).toEqual([3, 3, 3]);
    expect(plan.map((p) => p.index)).toEqual([0, 1, 2]);
  });

  it("does not fold across a newline or intervening text", () => {
    const plan = planCitations("[1](#cite:S1)\n[1](#cite:S2)，再 [1](#cite:S3)", new Set(["S1", "S2", "S3"]));
    expect(plan.map((p) => p.group)).toEqual([0, 1, 2]);
    expect(plan.every((p) => p.size === 1 && p.head)).toBe(true);
  });

  it("keeps unknown anchors out of groups and lets them break a run", () => {
    const plan = planCitations("[1](#cite:S1) [1](#cite:S9) [1](#cite:S2)", new Set(["S1", "S2"]));
    expect(plan.map((p) => [p.label, p.group, p.size])).toEqual([
      ["S1", 0, 1],
      ["S9", 1, 1],
      ["S2", 2, 1],
    ]);
    expect(plan[1].unknown).toBe(true);
    expect(plan[1].num).toBeUndefined();
  });

  it("reuses a label's number when it reappears in a later group", () => {
    const plan = planCitations("[1](#cite:S1) [1](#cite:S2) 文字 [1](#cite:S1)", new Set(["S1", "S2"]));
    expect(plan.map((p) => [p.label, p.num, p.group])).toEqual([
      ["S1", 1, 0],
      ["S2", 2, 0],
      ["S1", 1, 1],
    ]);
  });

  it("ignores anchors inside fenced and inline code", () => {
    const text = ["```", "[1](#cite:S1)", "```", "正文 [1](#cite:S2) `[1](#cite:S3)`"].join("\n");
    const plan = planCitations(text, new Set(["S1", "S2", "S3"]));
    expect(plan.map((p) => p.label)).toEqual(["S2"]);
  });

  it("numbers every anchor when the context never carried sources (null known set)", () => {
    const plan = planCitations("[1](#cite:S9) [1](#cite:S4)", null);
    expect(plan.map((p) => [p.num, p.group])).toEqual([
      [1, 0],
      [2, 0],
    ]);
  });
});

describe("rewriteCitations", () => {
  it("degrades numbered anchors to bare display numbers", () => {
    const { nums } = numberCitations("见 [1](#cite:S7) 与 [1](#cite:S2)", new Set(["S7", "S2"]));
    expect(rewriteCitations("见 [1](#cite:S7) 与 [1](#cite:S2)", nums)).toBe("见 [1] 与 [2]");
  });

  it("strips the link but keeps the bracket on unknown labels", () => {
    expect(rewriteCitations("[1](#cite:S9)", new Map())).toBe("[1]");
  });
});

describe("stripCode", () => {
  it("removes fences by matching marker, and tilde fences too", () => {
    expect(stripCode("a\n```\nb\n```\nc")).toBe("a\nc");
    expect(stripCode("a\n~~~\nb\n~~~\nc")).toBe("a\nc");
    // An unterminated fence swallows the rest, as markdown would.
    expect(stripCode("a\n```\nb")).toBe("a");
  });
});

describe("describeSource / sourceTypeName", () => {
  it("assembles the one-line description from type and title", () => {
    expect(describeSource(stamp("S3"))).toBe("bash · exit 0 · cmd S3");
    expect(describeSource(stamp("S1", { type: "file_read", title: "agent/loop.go" }))).toBe("read · agent/loop.go");
    expect(describeSource(stamp("S1", { title: "" }))).toBe("bash");
  });

  it("maps every documented type to a name, others to tool", () => {
    expect(sourceTypeName("command_exec")).toBe("bash");
    expect(sourceTypeName("file_read")).toBe("read");
    expect(sourceTypeName("file_write")).toBe("write");
    expect(sourceTypeName("search_result")).toBe("search");
    expect(sourceTypeName("web_page")).toBe("web");
    expect(sourceTypeName("mcp_tool")).toBe("mcp");
    expect(sourceTypeName("subagent")).toBe("subagent");
    expect(sourceTypeName("something-else")).toBe("tool");
  });
});

describe("shortHost", () => {
  it("reduces a web locator to its host", () => {
    expect(shortHost("https://example.com/some/page?q=1")).toBe("example.com");
  });

  it("keeps a file locator's path and line range", () => {
    expect(shortHost("file://agent/loop.go#L611-680")).toBe("agent/loop.go#L611-680");
  });

  it("passes other locators through", () => {
    expect(shortHost("agent/loop.go")).toBe("agent/loop.go");
  });
});

describe("collectSources / groupSources", () => {
  it("keeps the recorded output for contextual previews without mutating the stamp", () => {
    const source = stamp("S1");
    const [collected] = collectSources([{ result: { source, text: "later page evidence", details: { exit_code: 2 }, is_error: true } }]);
    expect(collected).toMatchObject({ content: "later page evidence", exit_code: 2, error: true });
    expect(source.content).toBeUndefined();
    expect(source.excerpt).toBe("out");
  });
  it("dedupes by label and sorts numerically, not lexicographically", () => {
    const calls = [
      { result: { source: stamp("S10") } },
      { result: { source: stamp("S2") } },
      { result: { source: stamp("S2", { title: "second stamp, same label" }) } },
      { result: {} },
    ];
    const out = collectSources(calls);
    expect(out.map((s) => s.label)).toEqual(["S2", "S10"]);
    expect(out[0].title).toBe("exit 0 · cmd S2");
  });

  it("orders labels with numeric tails first", () => {
    expect(["S10", "x", "S2"].sort(compareSourceLabels)).toEqual(["S2", "S10", "x"]);
  });

  it("splits cited from uncited, cited in citation order", () => {
    const sources = collectSources([{ result: { source: stamp("S1") } }, { result: { source: stamp("S2") } }, { result: { source: stamp("S3") } }]);
    const { nums } = numberCitations("[1](#cite:S3) … [1](#cite:S1)", new Set(sources.map((s) => s.label)));
    const { cited, others } = groupSources(sources, nums);
    expect(cited.map((g) => [g.src.label, g.num])).toEqual([
      ["S3", 1],
      ["S1", 2],
    ]);
    expect(others.map((g) => g.src.label)).toEqual(["S2"]);
  });
});
