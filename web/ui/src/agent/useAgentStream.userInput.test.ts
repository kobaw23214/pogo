import { afterEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type PendingUserInput } from "@/api/types";
import { useAgentStream } from "./useAgentStream";
import { reduceTaskSessionEvent } from "./taskSessionSnapshot";

const request: PendingUserInput = {
  request_id: "q-reconnect", call_id: "call-reconnect", revision: 1, deadline: 100000,
  questions: [{ id: "choice", header: "方案", question: "如何选择？", multi_select: false, options: [{ label: "A", description: "局部修改" }, { label: "B", description: "完整迁移" }] }],
};

describe("user input stream lifecycle", () => {
  const streams: ReturnType<typeof useAgentStream>[] = [];
  afterEach(() => { for (const stream of streams.splice(0)) stream.disconnect(); vi.clearAllTimers(); });
  it("deduplicates requests, resolves only the matching ID and clears at run end", () => {
    vi.useFakeTimers();
    const stream = useAgentStream();
    streams.push(stream);
    stream.apply({ type: "user_input_request", seq: 1, ts: 1, user_input: request });
    stream.apply({ type: "user_input_request", seq: 2, ts: 2, user_input: request });
    expect(stream.live.value.pending_user_inputs).toEqual([request]);
    stream.apply({ type: "user_input_resolved", seq: 3, ts: 3, user_input_resolution: { request_id: "other", status: "cancelled" } });
    expect(stream.live.value.pending_user_inputs).toHaveLength(1);
    stream.apply({ type: "user_input_resolved", seq: 4, ts: 4, user_input_resolution: { request_id: request.request_id, status: "answered" } });
    expect(stream.live.value.pending_user_inputs).toEqual([]);
    stream.apply({ type: "user_input_request", seq: 5, ts: 5, user_input: request });
    stream.apply({ type: "run_end", seq: 6, ts: 6 });
    expect(stream.live.value.pending_user_inputs).toEqual([]);
  });

  it("restores a pending question from a reconnect snapshot and accepts older snapshots", () => {
    vi.useFakeTimers();
    const stream = useAgentStream();
    streams.push(stream);
    stream.apply({ type: "snapshot", seq: 1, ts: 1, snapshot: { seq: 1, messages: [], results: {}, live: { ...emptyLive(), pending_user_inputs: [request] }, run: { active: true }, policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0 } });
    expect(stream.live.value.pending_user_inputs).toEqual([request]);
    stream.apply({ type: "snapshot", seq: 2, ts: 2, snapshot: { seq: 2, messages: [], results: {}, live: { active: false, pending_tools: [], pending_gates: [], pending_user_inputs: [] }, run: { active: false }, policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0 } });
    expect(stream.live.value.pending_user_inputs).toEqual([]);
  });

  it("keeps the read-only task-session mirror in the same lifecycle", () => {
    let snapshot = reduceTaskSessionEvent(null, { type: "snapshot", seq: 1, ts: 1, snapshot: { seq: 1, messages: [], results: {}, live: emptyLive(), run: { active: true }, policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0 } });
    snapshot = reduceTaskSessionEvent(snapshot, { type: "user_input_request", seq: 2, ts: 2, user_input: request });
    expect(snapshot?.live.pending_user_inputs).toEqual([request]);
    snapshot = reduceTaskSessionEvent(snapshot, { type: "user_input_resolved", seq: 3, ts: 3, user_input_resolution: { request_id: request.request_id, status: "answered" } });
    expect(snapshot?.live.pending_user_inputs).toEqual([]);
  });
});
