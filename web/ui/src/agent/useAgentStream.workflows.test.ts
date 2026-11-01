import { describe, expect, it } from "vitest";
import { useAgentStream } from "./useAgentStream";
import type { AgentEvent } from "@/api/types";

const receipt = (seq = 2, change: Record<string, unknown> = {}) => ({
  type: "tool_end", name: "agent_workflow_edit", seq, ts: seq, call_id: `edit-${seq}`,
  details: { type: "agent_workflow", group: "edit", action: "patch", result: { ok: true, workflow: { id: "aw_one", agent_id: "ag_one", revision: seq } } },
  ...change,
} as AgentEvent);

describe("live Agent workflow changes", () => {
  it("publishes each successful save while the Agent conversation is still running", () => {
    const stream = useAgentStream({ attachments: false });
    stream.apply({ type: "run_start", seq: 1, ts: 1, run_id: "r1" });
    const messages = stream.messages.value;
    stream.apply(receipt(), { sessionId: "chat-one", replay: false });
    expect(stream.agentWorkflowUpdate.value).toMatchObject({ sessionId: "chat-one", agentId: "ag_one", workflowId: "aw_one", revision: 2, action: "patch" });
    expect(stream.busy.value).toBe(true);
    expect(stream.messages.value).toBe(messages);
    stream.apply(receipt(3), { sessionId: "chat-one", replay: false });
    expect(stream.agentWorkflowUpdate.value?.revision).toBe(3);
    const current = stream.agentWorkflowUpdate.value;
    stream.apply(receipt(2), { sessionId: "chat-one", replay: false });
    expect(stream.agentWorkflowUpdate.value).toBe(current);
    stream.disconnect();
  });

  it("ignores replay, errors, read-only results and inconsistent Agent scope", () => {
    const stream = useAgentStream({ attachments: false });
    stream.apply(receipt());
    stream.apply(receipt(), { sessionId: "chat-one", replay: true });
    stream.apply(receipt(3, { is_error: true }), { sessionId: "chat-one", replay: false });
    stream.apply(receipt(4, { name: "agent_workflow_read" }), { sessionId: "chat-one", replay: false });
    stream.apply(receipt(5, { details: { type: "agent_workflow", group: "edit", action: "patch", agent_id: "ag_other", result: { ok: true, workflow: { id: "aw_one", agent_id: "ag_one", revision: 2 } } } }), { sessionId: "chat-one", replay: false });
    expect(stream.agentWorkflowUpdate.value).toBeNull();
    stream.disconnect();
  });

  it("refreshes a workflow deleted through conversation without treating replay or a failed deletion as current state", () => {
    const stream = useAgentStream({ attachments: false });
    const removed = (seq: number, change: Record<string, unknown> = {}) => receipt(seq, {
      details: { type: "agent_workflow", group: "edit", action: "delete", result: { ok: true, workflow: { id: "aw_one", agent_id: "ag_one", revision: 3, deleted: true, archived: true } } },
      ...change,
    });
    stream.apply(removed(2), { sessionId: "chat-one", replay: true });
    stream.apply(removed(3, { is_error: true }), { sessionId: "chat-one", replay: false });
    expect(stream.agentWorkflowUpdate.value).toBeNull();
    stream.apply(removed(4), { sessionId: "chat-one", replay: false });
    expect(stream.agentWorkflowUpdate.value).toMatchObject({ sessionId: "chat-one", agentId: "ag_one", workflowId: "aw_one", revision: 3, action: "delete" });
    stream.disconnect();
  });
});
