import { describe, expect, it } from "vitest";
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
