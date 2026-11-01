import { describe, expect, it, vi } from "vitest";
import type { Workflow, WorkflowApprovalAssignment, WorkflowInput, WorkflowNode, WorkflowRun } from "./workflowTypes";
import { defaultWorkflowApprovalPolicy, newWorkflowHumanNode, workflowAgentApprovalPatch, workflowNodeKindPatch } from "./workflowApprovalPolicy";
import { toWorkflowInput, validateWorkflow, workflowDirty, workflowRunCan } from "./workflowPresentation";
import { saveWorkflowDraft } from "./workflowEditorSave";

const legacyHuman: WorkflowNode = { id: "review", kind: "human", agent: "", task: "", prompt: "Review this request" };
const assignment: WorkflowApprovalAssignment = { prevent_self_approval: true, assignment: "fixed", assignees: [{ principal_id: "bob-id", display_name: "bob" }] };
function run(): WorkflowRun {
  return { id: "run_a", workflow_id: "wf_a", revision: 1, status: "working", created_at: "", nodes: { review: { status: "waiting_human" } },
    definition: { id: "wf_a", name: "Review", description: "", nodes: [legacyHuman], revision: 1, published: null, dirty: false, created_at: "", updated_at: "" } };
}

describe("workflow approval policy editing and authority", () => {
  it("requires a configured reviewer for newly added human steps without upgrading legacy definitions", () => {
    const created = newWorkflowHumanNode("new_review");
    created.prompt = "Review";
    expect(created.approval_policy).toEqual({ prevent_self_approval: true, assignment: "fixed", approvers: [] });
    expect(validateWorkflow({ name: "Review", description: "", nodes: [created] }).nodes.new_review).toContain("approval_approvers_required");
    expect(validateWorkflow({ name: "Review", description: "", nodes: [legacyHuman] }).ok).toBe(true);
    expect(toWorkflowInput({ name: "Review", description: "", nodes: [legacyHuman] }).nodes[0]).not.toHaveProperty("approval_policy");
    created.approval_policy!.approvers = ["bob"];
    expect(validateWorkflow({ name: "Review", description: "", nodes: [created] }).ok).toBe(true);
  });

  it("persists a detached approval policy acknowledgement while later edits remain dirty", async () => {
    const node = { ...legacyHuman, approval_policy: { ...defaultWorkflowApprovalPolicy(), approvers: ["bob"] } };
    const draft = { name: "Review", description: "", nodes: [node] };
    let resolve!: (value: Workflow) => void;
    let sent!: WorkflowInput;
    const update = vi.fn((_id: string, input: WorkflowInput) => { sent = input; return new Promise<Workflow>(done => { resolve = done; }); });
    const saving = saveWorkflowDraft({ id: "wf_a", revision: 1 }, draft, update);
    node.approval_policy.approvers.push("carol");
    resolve({ id: "wf_a", revision: 2, ...sent, nodes: draft.nodes, published: null, dirty: true, created_at: "", updated_at: "" });
    const result = await saving;
    expect(result.savedInput.nodes[0]!.approval_policy!.approvers).toEqual(["bob"]);
    expect(workflowDirty(result.savedInput, toWorkflowInput(draft))).toBe(true);
  });

  it("initializes protection when explicitly enabling result approval and removes obsolete policies", () => {
    const agent: WorkflowNode = { id: "agent", agent: "ag_a", task: "Check" };
    const patch = workflowAgentApprovalPatch(agent, true);
    expect(patch).toEqual({ approval: true, approval_policy: defaultWorkflowApprovalPolicy() });
    expect(agent).not.toHaveProperty("approval_policy");
    expect(workflowAgentApprovalPatch({ ...agent, ...patch }, false).approval_policy).toBeUndefined();
    expect(workflowNodeKindPatch(agent, "human").approval_policy?.prevent_self_approval).toBe(true);
    expect(workflowNodeKindPatch({ ...legacyHuman, approval_policy: defaultWorkflowApprovalPolicy() }, "agent").approval_policy).toBeUndefined();
  });

  it("accepts backend assignment without a browser-owned fixed account selection", () => {
    const node: WorkflowNode = { ...legacyHuman, approval_policy: { prevent_self_approval: true, assignment: "business" } };
    expect(validateWorkflow({ name: "Review", description: "", nodes: [node] }).ok).toBe(true);
    expect(toWorkflowInput({ name: "Review", description: "", nodes: [node] }).nodes[0]!.approval_policy).toEqual(node.approval_policy);
  });

  it("requires exact server approval capabilities for policy runs even when legacy UI fallback is allowed", () => {
    const current = run();
    expect(workflowRunCan(current, "can_approve", true, "review")).toBe(true);
    current.approvals = { review: { ...assignment } };
    expect(workflowRunCan(current, "can_approve", true, "review")).toBe(false);
    current.capabilities = { can_read: true, can_cancel: false, can_approve: true, can_retry: false, can_read_artifacts: true, can_manage_participants: false };
    expect(workflowRunCan(current, "can_approve", true, "review")).toBe(false);
    current.capabilities.can_approve_nodes = ["review"];
    expect(workflowRunCan(current, "can_approve", true, "review")).toBe(true);
    current.approvals.review!.blocked_reason = "initiator_cannot_approve";
    expect(workflowRunCan(current, "can_approve", true, "review")).toBe(false);
  });
});
