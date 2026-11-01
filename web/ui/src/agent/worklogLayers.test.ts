import { describe, expect, it } from "vitest";
import { setLocale } from "@/i18n";
import { buildTimeline } from "./timeline";
import { buildExchanges } from "./exchanges";
import { buildWorkLogRows, prosePreview } from "./worklogLayers";
import { emptyLive, type Message, type ToolResult } from "@/api/types";

// Pin the locale: the labels asserted below are Chinese.
setLocale("zh-CN");

const user = (id: string, text: string): Message => ({
  id,
  role: "user",
  content: [{ type: "text", text }],
});

const assistant = (id: string, content: Message["content"]): Message => ({
  id,
  role: "assistant",
  content,
});

const result = (callId: string, over: Partial<ToolResult> = {}): ToolResult => ({
  call_id: callId,
  text: "ok",
  ...over,
});

function rowsOf(messages: Message[], results: Record<string, ToolResult>) {
  const items = buildTimeline(messages, results, emptyLive());
  const ex = buildExchanges(items, false)[0];
  return { rows: buildWorkLogRows(ex), ex };
}

describe("buildWorkLogRows", () => {
  it("clusters consecutive explore calls into one row with identifiers", () => {
    const { rows } = rowsOf(
      [
        user("u1", "a"),
        assistant("m1", [
          { type: "tool_use", id: "c1", name: "read", input: { path: "config.go" } },
          { type: "tool_use", id: "c2", name: "grep", input: { pattern: "timeout" } },
          { type: "tool_use", id: "c3", name: "read", input: { path: "main.go" } },
        ]),
        assistant("m2", [{ type: "text", text: "结论" }]),
      ],
      { c1: result("c1"), c2: result("c2"), c3: result("c3") },
    );
    expect(rows.map((r) => r.kind)).toEqual(["explore"]);
    expect(rows[0].calls).toHaveLength(3);
    expect(rows[0].verb).toBe("探索");
    expect(rows[0].detail).toBe("config.go 等 3 项");
  });

  it("breaks the cluster when another tool kind interrupts it", () => {
    const { rows } = rowsOf(
      [
        user("u1", "a"),
        assistant("m1", [
          { type: "tool_use", id: "c1", name: "read", input: { path: "a.ts" } },
          { type: "tool_use", id: "c2", name: "bash", input: { command: "go build ./..." } },
          { type: "tool_use", id: "c3", name: "read", input: { path: "b.ts" } },
        ]),
        assistant("m2", [{ type: "text", text: "结论" }]),
      ],
      {
        c1: result("c1"),
        c2: result("c2", { details: { command: "go build ./...", exit_code: 0, duration_ms: 3200 } }),
        c3: result("c3"),
      },
    );
    expect(rows.map((r) => r.kind)).toEqual(["explore", "command", "explore"]);
    expect(rows[0].detail).toBe("a.ts");
    expect(rows[1].verb).toBe("已执行");
    expect(rows[1].detail).toBe("go build ./...");
    expect(rows[1].meta).toBe("3.2s");
  });

  it("marks a failed command red, with its exit code, and opens it by default", () => {
    const { rows } = rowsOf(
      [
        user("u1", "a"),
        assistant("m1", [{ type: "tool_use", id: "c1", name: "bash", input: { command: "make test" } }]),
        assistant("m2", [{ type: "text", text: "失败了" }]),
      ],
      { c1: result("c1", { is_error: true, details: { command: "make test", exit_code: 2, duration_ms: 800 } }) },
    );
    expect(rows[0].kind).toBe("command");
    expect(rows[0].failed).toBe(true);
    expect(rows[0].defaultOpen).toBe(true);
    expect(rows[0].meta).toBe("800ms · exit 2");
  });

  it("keeps the full command on the row so the opened label completes into it", () => {
    const long = "malquery family 2>&1 | head -50; echo ---; malquery stats 2>&1 | head -20";
    const multi = 'for h in $(cat hashes.txt); do\n  malquery show "$h"\ndone';
    const { rows } = rowsOf(
      [
        user("u1", "a"),
        assistant("m1", [
          { type: "tool_use", id: "c1", name: "bash", input: { command: long } },
          { type: "tool_use", id: "c2", name: "bash", input: { command: multi } },
        ]),
        assistant("m2", [{ type: "text", text: "结论" }]),
      ],
      {
        c1: result("c1", { details: { command: long, exit_code: 0, duration_ms: 98 } }),
        c2: result("c2", { details: { command: multi, exit_code: 0, duration_ms: 141 } }),
      },
    );
    // Collapsed: the clipped head, flattened to one line.
    expect(rows[0].detail).toBe(long.slice(0, 59) + "…");
    expect(rows[1].detail).toBe('for h in $(cat hashes.txt); do malquery show "$h" done');
    // Opened: the same label completes into the raw command, newlines kept.
    expect(rows[0].fullDetail).toBe(long);
    expect(rows[1].fullDetail).toBe(multi);
  });

  it("shows the +A −R a change made, and keeps subagent/todo as plain cards", () => {
    const { rows } = rowsOf(
      [
        user("u1", "a"),
        assistant("m1", [
          { type: "tool_use", id: "c1", name: "edit", input: { path: "src/very/long/path/that/keeps/going/file.ts" } },
          { type: "tool_use", id: "c2", name: "subagent", input: { task: "查一下" } },
        ]),
        assistant("m2", [{ type: "text", text: "结论" }]),
      ],
      {
        c1: result("c1", {
          duration_ms: 1200,
          details: { path: "x", edits: 1, diff: "d", patch: "p", added: 82, removed: 14 },
        }),
        c2: result("c2"),
      },
    );
    expect(rows[0].kind).toBe("change");
    expect(rows[0].verb).toBe("改动");
    expect(rows[0].detail).toContain("file.ts");
    expect(rows[0].meta).toBe("+82 −14 · 1.2s");
    expect(rows[1].kind).toBe("card");
  });

  it("sums call durations across an explore cluster", () => {
    const { rows } = rowsOf(
      [
        user("u1", "a"),
        assistant("m1", [
          { type: "tool_use", id: "c1", name: "read", input: { path: "a.ts" } },
          { type: "tool_use", id: "c2", name: "read", input: { path: "b.ts" } },
        ]),
        assistant("m2", [{ type: "text", text: "结论" }]),
      ],
      { c1: result("c1", { duration_ms: 800 }), c2: result("c2", { duration_ms: 500 }) },
    );
    expect(rows[0].kind).toBe("explore");
    expect(rows[0].meta).toBe("1.3s");
  });

  it("keeps thinking and mid-work prose as their own rows, the conclusion nowhere", () => {
    const { rows, ex } = rowsOf(
      [
        user("u1", "a"),
        assistant("m1", [
          { type: "thinking", text: "先看文件" },
          { type: "text", text: "好，先看一下：" },
          { type: "tool_use", id: "c1", name: "read", input: { path: "a.ts" } },
        ]),
        assistant("m2", [{ type: "text", text: "最终结论" }]),
      ],
      { c1: result("c1") },
    );
    // Calls before prose matches the turn card's own order (tools, then the
    // text they produced), not the message's block order.
    expect(rows.map((r) => r.kind)).toEqual(["thinking", "explore", "prose"]);
    expect(rows[0].verb).toBe("思考过程");
    expect(rows[2].text).toBe("好，先看一下：");
    // The conclusion is rendered outside the fold; it must not appear as a row.
    expect(ex.answer?.text).toBe("最终结论");
    expect(rows.some((r) => r.text === "最终结论")).toBe(false);
  });

  it("flags prose past the breathing-band size as long, by chars or by lines", () => {
    const longDraft = "草稿段落\n".repeat(8) + "x".repeat(500);
    const { rows } = rowsOf(
      [
        user("u1", "a"),
        assistant("m1", [
          { type: "text", text: "好，先看文件：" },
          { type: "tool_use", id: "c1", name: "read", input: { path: "a.ts" } },
        ]),
        assistant("m2", [
          { type: "text", text: longDraft },
          { type: "tool_use", id: "c2", name: "read", input: { path: "b.ts" } },
        ]),
        assistant("m3", [{ type: "text", text: "结论" }]),
      ],
      { c1: result("c1"), c2: result("c2") },
    );
    const prose = rows.filter((r) => r.kind === "prose");
    expect(prose).toHaveLength(2);
    expect(prose[0].long).toBeUndefined();
    expect(prose[1].long).toBe(true);
  });

  it("prosePreview takes the first non-empty line, clipped to one row", () => {
    expect(prosePreview("\n\n  第一行  \n第二行")).toBe("第一行");
    expect(prosePreview("短")).toBe("短");
    const clipped = prosePreview("x".repeat(120));
    expect(clipped).toHaveLength(80);
    expect(clipped.endsWith("…")).toBe(true);
  });
});
