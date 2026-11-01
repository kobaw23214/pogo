import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type AgentEvent, type Message } from "@/api/types";
import { createContextEstimator } from "./contextEstimate";
import { messageText } from "./searchHighlight";
import { buildTimeline, transcriptAction, transcriptSettled } from "./timeline";
import { parseFrame } from "./sse";
import { useAgentStream } from "./useAgentStream";

describe("empty and legacy messages during conversation recovery", () => {
  const streams: ReturnType<typeof useAgentStream>[] = [];
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => { streams.splice(0).forEach(stream => stream.disconnect()); vi.clearAllTimers(); vi.useRealTimers(); });
  const create = () => { const stream = useAgentStream({ attachments: false }); streams.push(stream); return stream; };
  const feed = (stream: ReturnType<typeof useAgentStream>, value: unknown) => {
    const event = parseFrame(`data: ${JSON.stringify(value)}\n\n`);
    if (!event) throw new Error("Expected an SSE event");
    stream.apply(event);
  };

  it("recovers a snapshot containing the empty aborted message that caused the crash", () => {
    const stream = create();
    feed(stream, { type: "snapshot", seq: 5, ts: 5, snapshot: {
      seq: 5, messages: [
        { id: "u1", role: "user", content: [{ type: "text", text: "先分析" }] },
        { id: "m2", role: "assistant", content: [{ type: "text", text: "已有结论 [1]" }] },
        { id: "m3", role: "", content: null },
      ], results: {}, live: emptyLive(), run: { active: false }, policy: { mode: "standard" }, usage: { input: 100, output: 20 }, context_tokens: 100,
    } });
    expect(stream.messages.value[2]).toMatchObject({ id: "m3", role: "assistant", content: [] });
    const estimator = createContextEstimator();
    expect(() => stream.messages.value.map(message => estimator.ofMessage(message))).not.toThrow();
    expect(() => buildTimeline(stream.messages.value, stream.results.value, stream.live.value)).not.toThrow();
    feed(stream, { type: "user_message", seq: 6, ts: 6, message_id: "u4", text: "继续" });
    feed(stream, { type: "message", seq: 7, ts: 7, message_id: "m5", role: "assistant", content: [{ type: "text", text: "可以继续对话" }] });
    expect(messageText(stream.messages.value[1]!)).toBe("已有结论 [1]");
    expect(transcriptSettled(stream.messages.value)).toBe(true);
    expect(stream.streamError.value).toBeNull();
  });

  it("normalizes live messages, user echoes and image enrichment without breaking later events", () => {
    const stream = create();
    for (const content of [undefined, null, {}, "legacy prose", { type: "text", text: "single block" }]) {
      feed(stream, { type: "message", seq: 1, ts: 1, role: "", content });
    }
    feed(stream, { type: "user_message", seq: 2, ts: 2, message_id: "user", content: [null, { type: "image", image_ref: "uploads/a.png" }, { type: "text", text: "看看" }] });
    feed(stream, { type: "image_describe_end", seq: 3, ts: 3, image_ref: "uploads/a.png", text: "图片描述" });
    expect(stream.messages.value.every(message => Array.isArray(message.content))).toBe(true);
    expect(stream.messages.value[3]?.content).toEqual([{ type: "text", text: "legacy prose" }]);
    expect(stream.messages.value[stream.messages.value.length - 1]?.content[0]).toMatchObject({ type: "image", image_ref: "uploads/a.png", image_alt: "图片描述" });
    expect(() => buildTimeline(stream.messages.value, {}, emptyLive())).not.toThrow();
  });

  it("also protects standalone consumers of old transcripts", () => {
    const bad = { id: "m1", role: "assistant", content: null } as unknown as Message;
    expect(createContextEstimator().ofMessage(bad)).toEqual({ tokens: 0, first: "" });
    expect(messageText(bad)).toBe("");
    expect(transcriptSettled([bad])).toBe(false);
    expect(() => transcriptAction([bad], {})).not.toThrow();
    expect(() => buildTimeline([bad], {}, emptyLive())).not.toThrow();
    const event: AgentEvent = { type: "message", seq: 1, ts: 1, content: [] };
    const stream = create(); stream.apply(event);
    expect(stream.messages.value[0]?.content).toEqual([]);
  });
});
