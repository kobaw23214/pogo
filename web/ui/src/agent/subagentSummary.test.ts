import { describe, expect, it } from "vitest";
import { setLocale } from "@/i18n";
import type { BgTask, SubagentFrame } from "@/api/types";
import { subagentSummary, type SubagentSummaryInput } from "./subagentSummary";

setLocale("zh-CN");
const background = (extra: Partial<BgTask> = {}): BgTask => ({ task_id: "run-1", mode: "explore", task: "clipped task", status: "running", started_at: 1000, ...extra });
const input = (extra: Partial<SubagentSummaryInput> = {}): SubagentSummaryInput => ({ running: false, args: { task: "Full task\nKeep these requirements", mode: "explore" }, ...extra });
const ack = { call_id: "parent", text: "DISPATCH ACK, NOT AN ANSWER", duration_ms: 2, details: { id: "run-1", mode: "explore", background: true } };

describe("factual delegated-task summaries", () => {
  it("prioritizes the full delegated task and uses only foreground original output", () => {
    const result = { call_id: "parent", text: "Original answer\nwith facts", duration_ms: 2400 };
    const value = subagentSummary(input({ result }));
    expect(value).toMatchObject({ task: "Full task\nKeep these requirements", state: "completed", resultText: result.text, durationMs: 2400 });
    expect(value).not.toHaveProperty("progress"); expect(value).not.toHaveProperty("artifacts");
  });

  it("never turns a background acknowledgement into a completed result", () => {
    const value = subagentSummary(input({ result: ack }));
    expect(value).toMatchObject({ state: "unavailable", background: true });
    expect(value.resultText).toBeUndefined(); expect(value.durationMs).toBeUndefined();
  });

  it("uses authoritative task state and duration while a true result is still syncing", () => {
    const live = subagentSummary(input({ result: ack, details: ack.details, bgTask: background() }));
    expect(live).toMatchObject({ state: "running", startedAt: 1000 }); expect(live.durationMs).toBeUndefined();
    const done = subagentSummary(input({ result: ack, details: ack.details, bgTask: background({ status: "done", duration_ms: 0 }) }));
    expect(done).toMatchObject({ state: "syncing", durationMs: 0 }); expect(done.resultText).toBeUndefined();
  });

  it("joins only the exact task result, never another task with the same title", () => {
    const notice = { id: "run-1", brief: "Same title", failed: false, result: "REAL BACKGROUND RESULT", turns: 3 };
    const value = subagentSummary(input({ result: ack, details: ack.details, notice }));
    expect(value).toMatchObject({ state: "completed", resultText: notice.result, turns: 3 });
    const other = subagentSummary(input({ result: ack, details: ack.details, notice: { ...notice, id: "run-2" } }));
    expect(other.state).toBe("unavailable"); expect(other.resultText).toBeUndefined();
  });

  it("keeps errors visible and distinguishes an explicit abort without matching prose", () => {
    const failed = subagentSummary(input({ bgTask: background({ status: "failed", error: "Connection lost" }) }));
    expect(failed).toMatchObject({ state: "failed", error: "Connection lost" });
    const stopped = subagentSummary(input({ result: { call_id: "p", text: "Partial work", is_error: true }, frames: [{ type: "run_end", stop_reason: "aborted" }] }));
    expect(stopped.state).toBe("stopped");
    const prose = subagentSummary(input({ result: { call_id: "p", text: "The word aborted is in this answer" } }));
    expect(prose.state).toBe("completed");
  });

  it("gives precisely associated approvals priority without changing their API", () => {
    const gates = [{ gate_id: "g", call_id: "run-1:c", tool: "bash" as const, origin: "run-1", deadline: 20000 }];
    expect(subagentSummary(input({ bgTask: background(), gates })).state).toBe("awaitingApproval");
  });

  it("reports simultaneous observed calls and does not reuse an earlier turn's action or thought", () => {
    const frames: SubagentFrame[] = [{ type: "turn_start", turn: 1 }, { type: "thinking", turn: 1, text: "old thought" },
      { type: "turn_start", turn: 2 }, { type: "tool_start", call_id: "a", name: "read", args: { path: "a.txt" } },
      { type: "tool_start", call_id: "b", name: "grep", args: { pattern: "needle" } }];
    const value = subagentSummary(input({ running: true, startedAt: 10, frames }));
    expect(value).toMatchObject({ state: "running", activeCount: 2, action: "搜索内容 · needle", recordedCalls: 2 });
    expect(value.recent).toBeUndefined();
    const fresh = subagentSummary(input({ running: true, frames: [...frames, { type: "turn_start", turn: 3 }] }));
    expect(fresh.action).toBeUndefined(); expect(fresh.recent).toBeUndefined(); expect(fresh.activeCount).toBe(0);
  });

  it("labels a bounded tail as an excerpt and does not invent totals for sparse records", () => {
    const value = subagentSummary(input({ running: true, frames: [{ type: "thinking", text: "tail only" }] }));
    expect(value.recent).toEqual({ kind: "thinking", text: "tail only" });
    expect(value.activeCount).toBeUndefined(); expect(value.turns).toBeUndefined(); expect(value.recordedCalls).toBe(0);
  });

  it("waits for actual output if a run_end arrives before the outer result", () => {
    const value = subagentSummary(input({ running: true, frames: [{ type: "run_end", stop_reason: "end_turn" }] }));
    expect(value).toMatchObject({ state: "syncing", live: false });
    expect(value.resultText).toBeUndefined();
  });

  it("distinguishes a background end in transit from an unknown historical acknowledgement", () => {
    const inTransit = subagentSummary(input({ result: ack, bgTask: background({ frames: [{ type: "run_end", stop_reason: "end_turn" }] }) }));
    expect(inTransit).toMatchObject({ state: "syncing", live: false });
    const registryEnded = subagentSummary(input({ result: ack, bgTask: background({ status: "done" }) }));
    expect(registryEnded.state).toBe("syncing");
    expect(subagentSummary(input({ result: ack })).state).toBe("unavailable");
  });
});
