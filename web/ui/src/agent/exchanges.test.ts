import { describe, expect, it } from "vitest";
import { buildTimeline } from "./timeline";
import { buildExchanges, foldable } from "./exchanges";
import { emptyLive, type Message, type ToolResult } from "@/api/types";

// Fixture helpers mirror timeline.test.ts: buildTimeline is the real input
// pipeline, so the grouping is tested against what the view actually renders.
const user = (id: string, text: string, ts?: number): Message => ({
  id,
  role: "user",
  content: [{ type: "text", text }],
  ts,
});

const assistant = (id: string, content: Message["content"], ts?: number): Message => ({
  id,
  role: "assistant",
  content,
  ts,
});

const result = (callId: string, over: Partial<ToolResult> = {}): ToolResult => ({
  call_id: callId,
  text: "ok",
  ...over,
});

describe("buildExchanges", () => {
  it("anchors mixed notices and steering in their original timeline order", () => {
    const [exchange] = buildExchanges([
      { kind: "user", id: "u", text: "原任务" },
      { kind: "notice", id: "early", text: "开始前提示" },
      { kind: "turn", id: "m", index: 1, text: "处理中", thinking: "", calls: [], streaming: false, sources: [] },
      { kind: "notice", id: "n1", text: "提示一" },
      { kind: "user", id: "s", text: "补充", steering: true },
      { kind: "notice", id: "n2", text: "提示二" },
    ], true);
    expect(exchange.notices.map(item => [item.id, item.afterTurnId, item.timelineOrder])).toEqual([
      ["early", undefined, 1], ["n1", "m", 3], ["n2", "m", 5],
    ]);
    expect(exchange.steering?.[0]).toMatchObject({ id: "s", afterTurnId: "m", timelineOrder: 4 });
  });
  it("keeps delivered steering in the original task with continuous turn numbering", () => {
    const followup = { ...user("u2", "补充要求", 1200), steering: true, inbox_id: "i1", run_id: "r1" };
    const items = buildTimeline([user("u1", "原任务", 1000), assistant("m1", [{ type: "text", text: "正在处理" }]), followup, assistant("m2", [{ type: "text", text: "一起完成" }])], {}, emptyLive());
    const [exchange] = buildExchanges(items, true);
    expect(buildExchanges(items, true)).toHaveLength(1);
    expect(exchange.finished).toBe(false);
    expect(exchange.turns.map(turn => turn.index)).toEqual([1, 2]);
    expect(exchange.steering?.[0]).toMatchObject({ id: "u2", inboxId: "i1", afterTurnId: "m1" });
    expect(buildExchanges(items, false)[0].answer?.text).toBe("一起完成");
  });

  it("starts an ordinary queued follow-up as a new exchange after the run", () => {
    const items = buildTimeline([user("u1", "原任务"), assistant("m1", [{ type: "text", text: "完成" }]), { ...user("u2", "下一任务"), inbox_id: "i2", run_id: "r2" }], {}, emptyLive());
    expect(buildExchanges(items, true).map(ex => ex.finished)).toEqual([true, false]);
  });

  it("keeps materials consulted before steering attached to the continuing task", () => {
    const source = { label: "S1", type: "file", title: "资料", locator: "notes.txt", call_id: "c1", excerpt: "facts", hash: "hash", ts: 1 };
    const items = buildTimeline([
      user("u1", "阅读资料"),
      assistant("m1", [{ type: "tool_use", id: "c1", name: "read", input: { path: "notes.txt" } }]),
      { ...user("u2", "继续补充分析"), steering: true },
      assistant("m2", [{ type: "text", text: "已完成分析" }]),
    ], { c1: result("c1", { source }) }, emptyLive());
    expect(buildExchanges(items, false)[0]?.answer?.sources?.map(item => item.label)).toEqual(["S1"]);
  });

  it("groups turns under their question, restarting at each user message", () => {
    const items = buildTimeline(
      [
        user("u1", "第一问"),
        assistant("m1", [{ type: "text", text: "答一" }]),
        user("u2", "第二问"),
        assistant("m2", [{ type: "text", text: "答二" }]),
      ],
      {},
      emptyLive(),
    );
    const exs = buildExchanges(items, false);
    expect(exs.map((e) => e.id)).toEqual(["u1", "u2"]);
    expect(exs[0].turns.map((t) => t.index)).toEqual([1]);
    expect(exs[1].answer?.text).toBe("答二");
  });

  it("puts turns before any user message into a head exchange", () => {
    const items = buildTimeline([assistant("m1", [{ type: "text", text: "hi" }])], {}, emptyLive());
    const exs = buildExchanges(items, false);
    expect(exs).toHaveLength(1);
    expect(exs[0].id).toBe("head");
    expect(exs[0].user).toBeUndefined();
  });

  it("marks only the last exchange unfinished while a run is active", () => {
    const items = buildTimeline(
      [user("u1", "a"), assistant("m1", [{ type: "text", text: "A" }]), user("u2", "b")],
      {},
      emptyLive(),
    );
    const running = buildExchanges(items, true);
    expect(running.map((e) => e.finished)).toEqual([true, false]);
    const idle = buildExchanges(items, false);
    expect(idle.map((e) => e.finished)).toEqual([true, true]);
  });

  it("folds only exchanges that are finished and have a conclusion", () => {
    const withAnswer = buildTimeline(
      [user("u1", "a"), assistant("m1", [{ type: "text", text: "A" }])],
      {},
      emptyLive(),
    );
    expect(foldable(buildExchanges(withAnswer, false)[0])).toBe(true);

    // A run that died without ever answering stays flat: there is no
    // conclusion to keep outside the fold, so the work is all there is.
    const silent = buildTimeline(
      [
        user("u1", "a"),
        assistant("m1", [{ type: "tool_use", id: "c1", name: "read", input: { path: "x" } }]),
      ],
      { c1: result("c1") },
      emptyLive(),
    );
    expect(foldable(buildExchanges(silent, false)[0])).toBe(false);
  });

  it("counts calls, failures and touched files", () => {
    const items = buildTimeline(
      [
        user("u1", "a"),
        assistant("m1", [
          { type: "tool_use", id: "c1", name: "edit", input: { path: "a.ts" } },
          { type: "tool_use", id: "c2", name: "write", input: { path: "b.ts" } },
          { type: "tool_use", id: "c3", name: "edit", input: { path: "a.ts" } },
          { type: "tool_use", id: "c4", name: "bash", input: { command: "go build" } },
        ]),
        assistant("m2", [{ type: "text", text: "done" }]),
      ],
      { c1: result("c1"), c2: result("c2", { is_error: true }), c3: result("c3"), c4: result("c4") },
      emptyLive(),
    );
    const ex = buildExchanges(items, false)[0];
    expect(ex.stats).toEqual({ calls: 4, failed: 1, files: 2, tools: { bash: 1, edit: 2, write: 1 } });
  });

  it("reads the duration off message timestamps and tolerates their absence", () => {
    const items = buildTimeline(
      [
        user("u1", "a", 1000),
        assistant("m1", [{ type: "text", text: "A" }], 61_000),
        user("u2", "b"),
        assistant("m2", [{ type: "text", text: "B" }]),
      ],
      {},
      emptyLive(),
    );
    const exs = buildExchanges(items, false);
    expect(exs[0].startedAt).toBe(1000);
    expect(exs[0].endedAt).toBe(61_000);
    expect(exs[1].startedAt).toBeUndefined();
    expect(exs[1].endedAt).toBeUndefined();
  });

  it("keeps a harness nudge inside the exchange it interrupted", () => {
    const items = buildTimeline(
      [
        user("u1", "do x"),
        assistant("m1", [{ type: "tool_use", id: "c1", name: "read", input: {} }]),
        user("u2", "[pogo] Turn checkpoint: this run has used 10 turn(s) (soft cap 10, hard cap 50)."),
        assistant("m2", [{ type: "text", text: "还差数据" }]),
      ],
      { c1: result("c1") },
      emptyLive(),
    );
    const exs = buildExchanges(items, false);
    // One exchange, not two: the checkpoint did not split it, and the second
    // turn kept counting from where the first stopped.
    expect(exs).toHaveLength(1);
    expect(exs[0].turns.map((t) => t.index)).toEqual([1, 2]);
    expect(exs[0].notices).toHaveLength(1);
    expect(exs[0].notices[0].text).toContain("Turn checkpoint");
  });

  it("gives a nudge before any user message the synthetic head exchange", () => {
    const items = buildTimeline(
      [user("u0", "[pogo] Your task list still has 1 item(s) in progress"), assistant("m1", [{ type: "text", text: "ok" }])],
      {},
      emptyLive(),
    );
    const exs = buildExchanges(items, false);
    expect(exs).toHaveLength(1);
    expect(exs[0].id).toBe("head");
    expect(exs[0].notices).toHaveLength(1);
    expect(exs[0].turns).toHaveLength(1);
  });
});
