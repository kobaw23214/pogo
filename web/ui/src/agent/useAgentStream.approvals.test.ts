import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type PendingGate, type ToolName } from "@/api/types";
import { useAgentStream } from "./useAgentStream";

describe("pending approval stream lifecycle", () => {
  const streams: ReturnType<typeof useAgentStream>[] = [];
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    for (const stream of streams.splice(0)) stream.disconnect();
    vi.clearAllTimers();
    vi.useRealTimers();
  });
  function createStream() {
    const stream = useAgentStream({ attachments: false });
    streams.push(stream);
    return stream;
  }

  it("keeps a browser arm request with no call id and deduplicates repeated live receipts", () => {
    const stream = createStream();
    stream.apply({ type: "gate_request", seq: 1, ts: 1000, gate_id: "arm", name: "browser-arm" as ToolName, args: { url: "https://example.test" }, deadline: 61000 });
    stream.apply({ type: "gate_request", seq: 2, ts: 1001, gate_id: "arm", name: "browser-arm" as ToolName, args: { url: "https://example.test/new" }, deadline: 61001 });
    expect(stream.live.value.pending_gates).toEqual([{ gate_id: "arm", call_id: "", tool: "browser-arm", args: { url: "https://example.test/new" }, deadline: 61001 }]);
    stream.apply({ type: "gate_resolved", seq: 3, ts: 1002, gate_id: "arm", allow: true });
    expect(stream.live.value.pending_gates).toEqual([]);
  });

  it("retains child approvals while the parent ends and starts another run", () => {
    const stream = createStream();
    stream.apply({ type: "gate_request", seq: 1, ts: 1000, gate_id: "local", call_id: "c1", name: "write", deadline: 61000 });
    stream.apply({ type: "gate_request", seq: 2, ts: 1001, gate_id: "child", call_id: "c2", name: "mcp__example__write" as ToolName, origin: "research", deadline: 61001 });
    stream.apply({ type: "run_end", seq: 3, ts: 1002 });
    expect(stream.run.value.active).toBe(false);
    expect(stream.live.value.pending_gates.map(g => g.gate_id)).toEqual(["child"]);
    expect(stream.live.value.pending_gates[0]?.origin).toBe("research");
    stream.apply({ type: "run_start", seq: 4, ts: 1003, run_id: "next" });
    expect(stream.live.value.pending_gates.map(g => g.gate_id)).toEqual(["child"]);
    stream.apply({ type: "gate_resolved", seq: 5, ts: 1004, gate_id: "child", allow: false });
    expect(stream.live.value.pending_gates).toEqual([]);
  });

  it("restores a frozen approval on reconnect and applies thawed deadlines", () => {
    const stream = createStream();
    const gate: PendingGate = { gate_id: "child", call_id: "c1", tool: "bash", origin: "analysis", deadline: 1000, frozen: true, remaining_ms: 40000, args: { command: "pwd" } };
    stream.apply({ type: "snapshot", seq: 1, ts: 90000, snapshot: {
      seq: 1, messages: [], results: {}, live: { ...emptyLive(), pending_gates: [gate] },
      run: { active: false }, policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0,
    } });
    expect(stream.live.value.pending_gates[0]).toMatchObject({ frozen: true, remaining_ms: 40000, origin: "analysis" });
    stream.apply({ type: "gate_deadline", seq: 2, ts: 90001, gate_id: "child", deadline: 130001, frozen: false });
    expect(stream.live.value.pending_gates[0]).toMatchObject({ frozen: false, deadline: 130001 });
    stream.apply({ type: "gate_deadline", seq: 3, ts: 90002, gate_id: "child", deadline: 130001, frozen: true, remaining_ms: 39999 });
    expect(stream.live.value.pending_gates[0]).toMatchObject({ frozen: true, remaining_ms: 39999 });
  });
});
