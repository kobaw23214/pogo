import type { WorkflowApprovalPolicy, WorkflowNode, WorkflowNodeKind } from "./workflowTypes";

export function defaultWorkflowApprovalPolicy(): WorkflowApprovalPolicy {
  return { prevent_self_approval: true, assignment: "fixed", approvers: [] };
}

export function cloneWorkflowApprovalPolicy(policy: WorkflowApprovalPolicy): WorkflowApprovalPolicy {
  return { ...policy, ...(policy.approvers ? { approvers: [...policy.approvers] } : {}) };
}

/** Explicitly changing a node kind or enabling review starts a new policy;
 * simply opening a historical node never upgrades its compatibility rules. */
export function workflowNodeKindPatch(node: WorkflowNode, kind: WorkflowNodeKind): Partial<WorkflowNode> {
  return { kind, approval_policy: kind === "human" || node.approval
    ? node.approval_policy ? cloneWorkflowApprovalPolicy(node.approval_policy) : defaultWorkflowApprovalPolicy()
    : undefined };
}

export function workflowAgentApprovalPatch(node: WorkflowNode, enabled: boolean): Partial<WorkflowNode> {
  return { approval: enabled || undefined, approval_policy: enabled
    ? node.approval_policy ? cloneWorkflowApprovalPolicy(node.approval_policy) : defaultWorkflowApprovalPolicy()
    : undefined };
}

export function newWorkflowHumanNode(id: string): WorkflowNode {
  return { id, kind: "human", agent: "", task: "", prompt: "", approval_policy: defaultWorkflowApprovalPolicy() };
}
