import { describe, expect, it } from "vitest";
import { emptyLive, type Message, type SubagentFrame, type ToolResult } from "@/api/types";
import { buildTimeline, subagentActivityRows, subagentSteps } from "./timeline";

const use = (id: string, task = "Same task"): Message => ({ id: `m-${id}`, role: "assistant", content: [{ type: "tool_use", id, name: "subagent", input: { task } }] });
const ack = (call: string, run: string): ToolResult => ({ call_id: call, text: "dispatch", details: { id: run, run_id: run, mode: "explore", background: true } });
const notice = (run: string, result: string) => `<task-notification>\nTask: ${run} (mode "explore") — Same task\nStatus: completed\nResult:\n${result}\n</task-notification>`;

describe("delegated identity joins and recent activity", () => {
  it("matches batched, out-of-order completions by exact run id and preserves dispatch order", () => {
    const items = buildTimeline([use("c1"), use("c2"), { id: "notices", role: "user", content: [{ type: "text", text: `${notice("run-2", "second result")}\n\n${notice("run-1", "first result")}` }] }],
      { c1: ack("c1", "run-1"), c2: ack("c2", "run-2") }, emptyLive());
    const calls = items.flatMap(item => item.kind === "turn" ? item.calls : []);
    expect(calls.map(call => [call.callId, call.taskNotice?.result])).toEqual([["c1", "first result"], ["c2", "second result"]]);
  });

  it("carries the host start time and only exact child approval origins", () => {
    const live = { ...emptyLive(), active: true, pending_tools: [{ call_id: "c1", name: "subagent" as const, started_at: 123 }],
      pending_gates: [{ gate_id: "exact", call_id: "run-1:tool", tool: "bash" as const, origin: "run-1", deadline: 1000 },
        { gate_id: "near", call_id: "run-10:tool", tool: "bash" as const, origin: "run-10", deadline: 1000 }] };
    const items = buildTimeline([use("c1")], { c1: ack("c1", "run-1") }, live);
    const call = items.flatMap(item => item.kind === "turn" ? item.calls : []).find(call => call.callId === "c1")!;
    expect(call.startedAt).toBe(123); expect(call.childGates?.map(gate => gate.gate_id)).toEqual(["exact"]);
  });

  it("keeps reused child call ids in different turns separate and ignores exact replay duplicates", () => {
    const frames: SubagentFrame[] = [{ type: "turn_start", turn: 1 }, { type: "tool_start", call_id: "same", name: "read", args: { path: "first" } },
      { type: "tool_end", call_id: "same", name: "read", text: "first result" }, { type: "tool_end", call_id: "same", name: "read", text: "first result" },
      { type: "turn_start", turn: 2 }, { type: "tool_start", call_id: "same", name: "read", args: { path: "second" } },
      { type: "tool_start", call_id: "same", name: "read", args: { path: "second" } }, { type: "tool_end", call_id: "same", name: "read", text: "second result" }];
    const calls = subagentSteps(frames).filter(step => step.kind === "tool");
    expect(calls.map(step => [step.identity, step.text])).toEqual([["turn:1:call:same", "first result"], ["turn:2:call:same", "second result"]]);
    expect(subagentSteps([{ type: "tool_end", call_id: "same", text: "orphan" }])[0].identity).toBeUndefined();
  });

  it("keeps the same explore wrapper identity when the second read arrives", () => {
    const first: SubagentFrame[] = [{ type: "turn_start", turn: 1 }, { type: "tool_start", call_id: "a", name: "read" }];
    const single = subagentActivityRows(subagentSteps(first))[1];
    const multiple = subagentActivityRows(subagentSteps([...first, { type: "tool_start", call_id: "b", name: "read" }]))[1];
    expect(single.kind).toBe("explore"); expect(multiple.kind).toBe("explore");
    if (single.kind !== "explore" || multiple.kind !== "explore") throw new Error("expected explore wrappers");
    expect(single.steps[0].identity).toBe(multiple.steps[0].identity);
  });
});
