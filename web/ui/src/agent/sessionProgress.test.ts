import { afterEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type AgentEvent } from "@/api/types";
import { foldSessionProgress, sessionProgressTone } from "./sessionProgress";
import { useAgentStream } from "./useAgentStream";

const e = (type: AgentEvent["type"], seq: number, extra: Partial<AgentEvent> = {}): AgentEvent => ({ type, seq, ts: seq * 1000, ...extra });
describe("minimized session public activity", () => {
  afterEach(() => { vi.clearAllTimers(); vi.useRealTimers(); });
  it("keeps only 8 real lifecycle events and excludes private bodies", () => {
    let feed = foldSessionProgress([], e("run_start", 1));
    for (let i = 2; i < 14; i++) feed = foldSessionProgress(feed, e(i % 2 ? "tool_end" : "tool_start", i, {
      name: "browser", call_id: `call-${i}`, args: { secret: "PRIVATE_ARGUMENT" }, text: "PRIVATE_OUTPUT", error: "PRIVATE_ERROR", details: undefined,
    }));
    expect(feed).toHaveLength(8);
    expect(JSON.stringify(feed)).not.toMatch(/PRIVATE/);
    expect(feed[0]!.ts).toBe(6000);
    expect(feed[7]!.kind).toBe("toolDone");
  });
  it("never puts streamed thought or reply contents into the feed or duplicates deltas", () => {
    const thought = foldSessionProgress([], e("thinking", 1, { text: "PRIVATE_THOUGHT" }));
    expect(foldSessionProgress(thought, e("thinking", 2, { text: "MORE_THOUGHT" }))).toBe(thought);
    const reply = foldSessionProgress(thought, e("token", 3, { text: "PRIVATE_REPLY" }));
    expect(reply.map(item => item.kind)).toEqual(["thinking", "answering"]);
    expect(JSON.stringify(reply)).not.toMatch(/PRIVATE|THOUGHT/);
  });
  it("distinguishes failure, stopping and run completion without claiming task success", () => {
    expect(foldSessionProgress([], e("tool_end", 1, { is_error: true }))[0]!.kind).toBe("toolFailed");
    expect(foldSessionProgress([], e("run_end", 2, { stop_reason: "aborted" }))[0]!.kind).toBe("stopped");
    expect(foldSessionProgress([], e("run_end", 3, { stop_reason: "error" }))[0]!.kind).toBe("failed");
    expect(foldSessionProgress([], e("run_end", 4, { stop_reason: "max_tokens" }))[0]!.kind).toBe("limited");
    expect(foldSessionProgress([], e("run_end", 5, { stop_reason: "end_turn" }))[0]!.kind).toBe("ended");
    expect(sessionProgressTone("failed")).toBe("error"); expect(sessionProgressTone("stopped")).toBe("attention");
  });
  it("reconnect snapshots expose only the currently known state and cannot invent success", () => {
    const feed = foldSessionProgress([], e("tool_end", 1, { name: "browser" }));
    const snapshot = { seq: 2, messages: [], results: {}, live: emptyLive(), run: { active: false }, policy: { mode: "standard" as const }, usage: { input: 0, output: 0 }, context_tokens: 0 };
    expect(foldSessionProgress(feed, e("snapshot", 2, { snapshot }))).toEqual([]);
    snapshot.live.pending_gates = [{ gate_id: "g", call_id: "c", tool: "bash", args: { secret: "PRIVATE" }, deadline: 1 }];
    const waiting = foldSessionProgress(feed, e("snapshot", 3, { snapshot }));
    expect(waiting[0]).toMatchObject({ kind: "approval", tool: "bash" });
    expect(JSON.stringify(waiting)).not.toContain("PRIVATE");
  });
  it("new runs start a fresh history; replayed event IDs do not duplicate", () => {
    const event = e("tool_start", 2, { call_id: "c", name: "read" });
    const feed = foldSessionProgress([], event);
    expect(foldSessionProgress(feed, event)).toBe(feed);
    expect(foldSessionProgress(feed, e("run_start", 3))).toHaveLength(1);
  });
  it.each(["turn_limit", "token_budget", "cost_budget", "time_budget", "context_overflow", "max_tokens"])("uses harness limit %s before the provider stop reason", end_reason => {
    expect(foldSessionProgress([], e("run_end", 5, { end_reason, stop_reason: "end_turn", error: "budget explanation" }))[0]!.kind).toBe("limited");
  });
  it.each(["stagnation", "transport_error", "tool_protocol"])("shows harness failure %s without reading its raw error", end_reason => {
    expect(foldSessionProgress([], e("run_end", 5, { end_reason, stop_reason: "end_turn" }))[0]!.kind).toBe("failed");
  });
  it("uses neutral ended status for unknown endings and distinguishes explicit cancellation", () => {
    const unknown = foldSessionProgress([], e("run_end", 5, { end_reason: "future_reason", stop_reason: "end_turn" }))[0]!;
    expect(unknown.kind).toBe("ended"); expect(sessionProgressTone(unknown.kind)).toBe("idle");
    expect(foldSessionProgress([], e("run_end", 6, { end_reason: "aborted", stop_reason: "end_turn" }))[0]!.kind).toBe("stopped");
  });
  it("wires through the real stream fold, including gates and resets", () => {
    vi.useFakeTimers();
    const stream = useAgentStream({ attachments: false });
    stream.apply(e("run_start", 1)); stream.apply(e("tool_start", 2, { call_id: "c", name: "browser" }));
    expect(stream.progressEvents.value[1]).toMatchObject({ kind: "toolRunning", tool: "browser" });
    stream.apply(e("gate_request", 3, { call_id: "g", gate_id: "g", name: "bash" }));
    expect(stream.progressEvents.value[2]!.kind).toBe("approval");
    stream.apply(e("gate_resolved", 4, { gate_id: "g", allow: false }));
    expect(stream.progressEvents.value[3]!.kind).toBe("denied");
    stream.apply(e("compacted", 5));
    expect(stream.progressEvents.value).toEqual([]);
    stream.disconnect();
  });
});
