import { describe, expect, it } from "vitest";
import { agentWorkflowCanvasSize, isAgentWorkflowWorkspace, shouldRevealCreatedAgentWorkflow } from "./agentWorkflowWorkspace";

describe("Agent workflow content workspace", () => {
  it("leaves the conversation visible until immersion is explicitly requested", () => {
    expect(isAgentWorkflowWorkspace("workflows", true, "agent-a")).toBe(false);
    expect(isAgentWorkflowWorkspace("workflows", true, "agent-a", false, true)).toBe(true);
    expect(isAgentWorkflowWorkspace("workflows", true, "agent-a", false, false)).toBe(false);
    expect(isAgentWorkflowWorkspace("workflows", false, "agent-a", false, true)).toBe(false);
    expect(isAgentWorkflowWorkspace("workflows", true, undefined, false, true)).toBe(false);
    for (const active of [null, "hub", "files", "skills"]) expect(isAgentWorkflowWorkspace(active, true, "agent-a", false, true)).toBe(false);
    expect(isAgentWorkflowWorkspace("workflows", true, "agent-a", true, true)).toBe(false);
  });

  it("reveals only a workflow created in the current Agent conversation without taking over another view", () => {
    const update = { action: "create", agentId: "agent-a", sessionId: "session-a", workflowId: "flow-a", seq: 42 };
    expect(shouldRevealCreatedAgentWorkflow(update, null, "agent-a", "session-a")).toBe(true);
    for (const active of ["workflows", "hub", "files", "skills"]) expect(shouldRevealCreatedAgentWorkflow(update, active, "agent-a", "session-a")).toBe(false);
    for (const action of ["patch", "enable", "trial"]) expect(shouldRevealCreatedAgentWorkflow({ ...update, action }, null, "agent-a", "session-a")).toBe(false);
    expect(shouldRevealCreatedAgentWorkflow({ ...update, agentId: "agent-b" }, null, "agent-a", "session-a")).toBe(false);
    expect(shouldRevealCreatedAgentWorkflow({ ...update, sessionId: "old-session" }, null, "agent-a", "session-a")).toBe(false);
    expect(shouldRevealCreatedAgentWorkflow(null, null, "agent-a", "session-a")).toBe(false);
    expect(shouldRevealCreatedAgentWorkflow(update, null, undefined, "session-a")).toBe(false);
    expect(shouldRevealCreatedAgentWorkflow(update, null, "agent-a", null)).toBe(false);
  });

  it("uses the entire measured content area without a conversation split or minimum-width overflow", () => {
    expect(agentWorkflowCanvasSize(960, 720)).toEqual({ width: "960px", height: "720px" });
    expect(agentWorkflowCanvasSize(180, 400)).toEqual({ width: "180px", height: "400px" });
    expect(agentWorkflowCanvasSize(0, NaN)).toEqual({ width: "100%", height: "100%" });
  });

});
