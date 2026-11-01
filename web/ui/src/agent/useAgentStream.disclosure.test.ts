import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type AgentEvent } from "@/api/types";
import { useAgentStream } from "./useAgentStream";

const snapshot = (active = false): AgentEvent => ({ type: "snapshot", seq: 1, ts: 1000, snapshot: {
  seq: 1, messages: [{ id: "u1", role: "user", content: [{ type: "text", text: "same ids" }] }],
  results: {}, live: { ...emptyLive(), active }, run: { active }, policy: { mode: "standard" },
  usage: { input: 0, output: 0 }, context_tokens: 0,
} });

describe("displayed transcript identity", () => {
  const streams: ReturnType<typeof useAgentStream>[] = [];
  beforeEach(() => { vi.useFakeTimers(); vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {}))); });
  afterEach(() => { streams.splice(0).forEach(stream => stream.disconnect()); vi.clearAllTimers(); vi.useRealTimers(); vi.unstubAllGlobals(); });
  const create = () => { const stream = useAgentStream({ attachments: false }); streams.push(stream); return stream; };

  it("holds the displayed session through a pending switch, then switches with its snapshot", () => {
    const stream = create();
    void stream.connect("a");
    expect(stream.displaySessionId.value).toBeNull();
    stream.apply(snapshot(true), { sessionId: "a", replay: true });
    expect(stream.displaySessionId.value).toBe("a");
    void stream.connect("b");
    expect(stream.switching.value).toBe(true);
    expect(stream.messages.value[0].id).toBe("u1");
    expect(stream.displaySessionId.value).toBe("a");
    stream.apply(snapshot(), { sessionId: "b", replay: true });
    expect(stream.displaySessionId.value).toBe("b");
    expect(stream.switching.value).toBe(false);
  });

  it("keeps identity for a same-session reconnect and does not invent one for bare replay", () => {
    const stream = create();
    stream.apply(snapshot());
    expect(stream.displaySessionId.value).toBeNull();
    void stream.connect("a");
    stream.apply(snapshot(true));
    expect(stream.displaySessionId.value).toBe("a");
    void stream.retryNow();
    stream.apply(snapshot());
    expect(stream.displaySessionId.value).toBe("a");
    expect(stream.busy.value).toBe(false);
  });
});
