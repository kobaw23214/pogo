import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type OverheadPart } from "@/api/types";
import { useAgentStream } from "./useAgentStream";

// The itemized overhead travels with the total: a snapshot carries both, a
// turn_start carries both only when the server's tool view changed them, and
// a model change that reports a total without parts clears stale parts.
describe("overhead breakdown updates", () => {
  const streams: ReturnType<typeof useAgentStream>[] = [];
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    for (const stream of streams.splice(0)) stream.disconnect();
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it("follows snapshots, changed turn starts and model changes", () => {
    const stream = useAgentStream({ attachments: false });
    streams.push(stream);
    const initial: OverheadPart[] = [{ kind: "prompt", name: "base", tokens: 500 }, { kind: "tool", name: "read", tokens: 200 }];
    stream.apply({ type: "snapshot", seq: 1, ts: 1, snapshot: {
      seq: 1, messages: [], results: {}, live: emptyLive(), run: { active: true, model: "m", context_window: 100000 },
      policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0, overhead_tokens: 700, overhead_breakdown: initial,
    } });
    expect(stream.overheadTokens.value).toBe(700);
    expect(stream.overheadBreakdown.value).toEqual(initial);

    stream.apply({ type: "turn_start", seq: 2, ts: 2, turn: 1, message_id: "m1" });
    expect(stream.overheadTokens.value).toBe(700);
    expect(stream.overheadBreakdown.value).toEqual(initial);

    const loaded: OverheadPart[] = [...initial, { kind: "skill_tools", name: "agent-workflows", tokens: 6500, count: 4 }];
    stream.apply({ type: "turn_start", seq: 3, ts: 3, turn: 2, message_id: "m2", overhead_tokens: 7200, overhead_breakdown: loaded });
    expect(stream.overheadTokens.value).toBe(7200);
    expect(stream.overheadBreakdown.value).toEqual(loaded);

    stream.apply({ type: "model_changed", seq: 4, ts: 4, model: "m", provider: "p", context_window: 100000, overhead_tokens: 7300 });
    expect(stream.overheadTokens.value).toBe(7300);
    expect(stream.overheadBreakdown.value).toEqual([]);
  });

  it("treats a snapshot without parts as having none", () => {
    const stream = useAgentStream({ attachments: false });
    streams.push(stream);
    stream.apply({ type: "turn_start", seq: 1, ts: 1, turn: 1, message_id: "m1", overhead_tokens: 50, overhead_breakdown: [{ kind: "tool", name: "read", tokens: 50 }] });
    stream.apply({ type: "snapshot", seq: 2, ts: 2, snapshot: {
      seq: 2, messages: [], results: {}, live: emptyLive(), run: { active: false },
      policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0, overhead_tokens: 40,
    } });
    expect(stream.overheadTokens.value).toBe(40);
    expect(stream.overheadBreakdown.value).toEqual([]);
  });
});
