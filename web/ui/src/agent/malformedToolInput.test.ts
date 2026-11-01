import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type AgentEvent, type Message } from "@/api/types";
import { buildTimeline, transcriptAction } from "./timeline";
import { useAgentStream } from "./useAgentStream";

const malformed = String.raw`{"action":"create","content":"echo \$HOME <script>alert(1)</script>"}`;
const rejected: Message = { id: "m1", role: "assistant", content: [{ type: "tool_use", id: "bad", name: "shortcut_manage", input: malformed, input_invalid: true, input_error: "invalid JSON string escape" }] };

describe("malformed tool arguments in live and replayed conversations", () => {
  const streams: ReturnType<typeof useAgentStream>[] = [];
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => { for (const stream of streams.splice(0)) stream.disconnect(); vi.clearAllTimers(); vi.useRealTimers(); });

  it("retains opaque arguments across streamed events, a successful retry and a reconnect snapshot", () => {
    const stream = useAgentStream({ attachments: false }); streams.push(stream);
    const feed = (event: AgentEvent) => stream.apply(JSON.parse(JSON.stringify(event)));
    feed({ type: "message", seq: 1, ts: 1, message_id: rejected.id, role: "assistant", content: rejected.content });
    feed({ type: "tool_start", seq: 2, ts: 2, call_id: "bad", name: "shortcut_manage", args: malformed });
    let turns = buildTimeline(stream.messages.value, stream.results.value, stream.live.value).filter(item => item.kind === "turn");
    expect(turns[0]!.calls[0]).toMatchObject({ callId: "bad", inputInvalid: true, inputError: "invalid JSON string escape", args: malformed, running: false, orphaned: false });
    expect(transcriptAction(stream.messages.value, stream.results.value)).not.toContain("shortcut_manage");
    feed({ type: "tool_end", seq: 3, ts: 3, call_id: "bad", name: "shortcut_manage", is_error: true, text: "Arguments were malformed; not executed." });
    feed({ type: "message", seq: 4, ts: 4, message_id: "m2", role: "assistant", content: [{ type: "tool_use", id: "retry", name: "shortcut_manage", input: { action: "create", content: "echo $HOME" } }] });
    feed({ type: "tool_start", seq: 5, ts: 5, call_id: "retry", name: "shortcut_manage", args: { action: "create", content: "echo $HOME" } });
    feed({ type: "tool_end", seq: 6, ts: 6, call_id: "retry", name: "shortcut_manage", text: "Shortcut saved" });
    feed({ type: "run_end", seq: 7, ts: 7, usage: { input: 1234, output: 25 } });
    turns = buildTimeline(stream.messages.value, stream.results.value, stream.live.value).filter(item => item.kind === "turn");
    expect(turns[0]!.calls[0]).toMatchObject({ inputInvalid: true, args: malformed, result: { is_error: true } });
    expect(turns[1]!.calls[0]).toMatchObject({ callId: "retry", args: { action: "create", content: "echo $HOME" }, result: { text: "Shortcut saved" } });
    expect(turns[1]!.calls[0]).not.toHaveProperty("inputInvalid");
    const expected = turns;
    feed({ type: "snapshot", seq: 8, ts: 8, snapshot: { seq: 8, messages: stream.messages.value, results: stream.results.value, live: emptyLive(), run: { active: false }, policy: { mode: "standard" }, usage: { input: 1234, output: 25 }, context_tokens: 1234 } });
    expect(buildTimeline(stream.messages.value, stream.results.value, stream.live.value).filter(item => item.kind === "turn")).toEqual(expected);
    expect(stream.streamError.value).toBeNull();
  });

  it("keeps a rejected call visibly not executed when a snapshot has no result receipt yet", () => {
    const items = buildTimeline([rejected], {}, emptyLive());
    expect(items[0]!.kind).toBe("turn");
    if (items[0]!.kind !== "turn") throw new Error("Expected a turn");
    expect(items[0]!.calls[0]).toMatchObject({ inputInvalid: true, args: malformed, running: false, orphaned: false });
  });
});
