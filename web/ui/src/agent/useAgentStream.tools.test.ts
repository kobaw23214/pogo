import { describe, expect, it, vi } from "vitest";
import { useAgentStream } from "./useAgentStream";
const config = { changed: false, deferred: true, revision: 1, desired_revision: 2, tools: ["read"] };
describe("tool configuration stream receipts", () => {
  it("accepts only explicitly scoped live host receipts and leaves conversation state unchanged", () => {
    const s = useAgentStream({ attachments: false });
    s.apply({ type: "user_message", seq: 1, ts: 1, message_id: "user", text: "keep this message" });
    const messages = s.messages.value;
    const event = { type: "tool_config" as const, seq: 2, ts: 2, tool_config: config };
    s.apply(event); expect(s.toolConfig.value).toBeNull();
    s.apply(event, { sessionId: "a", replay: true }); expect(s.toolConfig.value).toBeNull();
    s.apply({ ...event, tool_config: { ...config, tools: [null] } } as never, { sessionId: "a", replay: false }); expect(s.toolConfig.value).toBeNull();
    s.apply(event, { sessionId: "a", replay: false }); expect(s.toolConfig.value).toEqual({ sessionId: "a", config }); expect(s.messages.value).toBe(messages); expect(s.run.value.active).toBe(false);
    s.disconnect();
  });
});

describe("capability invalidation signals", () => {
  it("accepts only current scoped live events and leaves conversation content unchanged", () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {})));
    const stream = useAgentStream({ attachments: false });
    try {
      void stream.connect("current");
      stream.apply({ type: "user_message", seq: 1, ts: 1, message_id: "user", text: "keep history" });
      const messages = stream.messages.value;
      const event = { type: "capabilities_changed" as const, seq: 2, ts: 2 };
      stream.apply(event); expect(stream.capabilitiesChanged.value).toBeNull();
      stream.apply(event, { sessionId: "current", replay: true }); expect(stream.capabilitiesChanged.value).toBeNull();
      stream.apply(event, { sessionId: "other", replay: false }); expect(stream.capabilitiesChanged.value).toBeNull();
      stream.apply(event, { sessionId: "current", replay: false });
      expect(stream.capabilitiesChanged.value).toEqual({ sessionId: "current", version: 1 });
      stream.apply({ ...event, seq: 3 }, { sessionId: "current", replay: false });
      expect(stream.capabilitiesChanged.value?.version).toBe(2); expect(stream.messages.value).toBe(messages);
      void stream.connect("next"); expect(stream.capabilitiesChanged.value).toBeNull();
      stream.apply({ ...event, seq: 4 }, { sessionId: "current", replay: false }); expect(stream.capabilitiesChanged.value).toBeNull();
    } finally { stream.disconnect(); vi.unstubAllGlobals(); }
  });
});
