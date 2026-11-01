import { describe, expect, it } from "vitest";
import { agentWorkflowReceiptLocation, isAgentWorkflowDetails, matchesAgentWorkflowExecution } from "./agentWorkflowNavigation";

describe("Agent internal workflow receipt scope", () => {
  it("opens only a returned identity in the currently bound Agent", () => {
    const details = { type: "agent_workflow" as const, action: "edit", result: { workflow: { id: "af_123", agent_id: "ag_current", revision: 2 } } };
    expect(agentWorkflowReceiptLocation(details, "ag_current")).toEqual({ workflowId: "af_123", revision: 2 });
    expect(agentWorkflowReceiptLocation(details, "ag_other")).toBeNull();
    expect(agentWorkflowReceiptLocation(details, undefined)).toBeNull();
    expect(agentWorkflowReceiptLocation({ ...details, agent_id: "ag_other" }, "ag_current")).toBeNull();
    expect(agentWorkflowReceiptLocation({ ...details, result: { ...details.result, workflow_id: "af_other" } }, "ag_current")).toBeNull();
  });
  it("keeps run identity and rejects path-like or mismatched responses", () => {
    expect(agentWorkflowReceiptLocation({ type: "agent_workflow", action: "run", result: { run: { id: "afr_1", workflow_id: "af_1", agent_id: "ag_1" } } }, "ag_1")).toEqual({ workflowId: "af_1", runId: "afr_1" });
    expect(agentWorkflowReceiptLocation({ type: "agent_workflow", action: "read", result: { workflow_id: "../../other" } }, "ag_1")).toBeNull();
    expect(isAgentWorkflowDetails({ type: "workflow_authoring", action: "edit", result: {} })).toBe(false);
  });
  it("never borrows a different run's execution session", () => {
    const identity = { owner_id: "owner", agent_id: "ag_1", workflow_id: "af_1", run_id: "afr_1" };
    expect(matchesAgentWorkflowExecution(identity, "ag_1", "af_1", "afr_1")).toBe(true);
    expect(matchesAgentWorkflowExecution(identity, "ag_2", "af_1", "afr_1")).toBe(false);
    expect(matchesAgentWorkflowExecution(identity, "ag_1", "af_2", "afr_1")).toBe(false);
    expect(matchesAgentWorkflowExecution(identity, "ag_1", "af_1", "afr_2")).toBe(false);
    expect(matchesAgentWorkflowExecution({ ...identity, owner_id: "" }, "ag_1", "af_1", "afr_1")).toBe(false);
    expect(matchesAgentWorkflowExecution(undefined, "ag_1", "af_1", "afr_1")).toBe(false);
  });
});
