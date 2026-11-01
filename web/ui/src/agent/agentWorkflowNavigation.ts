import type { InjectionKey } from "vue";

export interface AgentWorkflowLocation { workflowId: string; runId?: string; revision?: number }
export interface AgentWorkflowNavigation {
  agentId(): string | undefined;
  open(location: AgentWorkflowLocation): void;
}
export const agentWorkflowNavigationKey: InjectionKey<AgentWorkflowNavigation> = Symbol("agentWorkflowNavigation");
export const validAgentWorkflowId = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_-]{1,160}$/.test(value);
const record = (value: unknown): Record<string, unknown> => value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
export interface AgentWorkflowDetails { type: "agent_workflow"; action: string; group?: string; agent_id?: string; result: Record<string, unknown> }
export function isAgentWorkflowDetails(value: unknown): value is AgentWorkflowDetails {
  const detail = record(value);
  return detail.type === "agent_workflow" && typeof detail.action === "string" && !!detail.result && typeof detail.result === "object" && !Array.isArray(detail.result);
}
export function agentWorkflowReceiptLocation(details: AgentWorkflowDetails, currentAgentId: string | undefined): AgentWorkflowLocation | null {
  if (!validAgentWorkflowId(currentAgentId)) return null;
  const result = details.result, workflow = record(result.workflow), run = record(result.run);
  const agents = [details.agent_id, result.agent_id, workflow.agent_id, run.agent_id].filter(value => value !== undefined);
  if (agents.some(value => value !== currentAgentId)) return null;
  const workflowId = result.workflow_id ?? workflow.id ?? run.workflow_id;
  const runId = result.run_id ?? run.id;
  if (!validAgentWorkflowId(workflowId) || runId !== undefined && !validAgentWorkflowId(runId)) return null;
  if ([result.workflow_id, workflow.id, run.workflow_id].some(value => value !== undefined && value !== workflowId)) return null;
  if ([result.run_id, run.id].some(value => value !== undefined && value !== runId)) return null;
  const revision = result.revision ?? workflow.revision;
  return { workflowId, ...(typeof runId === "string" ? { runId } : {}), ...(typeof revision === "number" && Number.isSafeInteger(revision) && revision > 0 ? { revision } : {}) };
}

export function matchesAgentWorkflowExecution(value: unknown, agentId: string, workflowId: string, runId: string): boolean {
  const identity = record(value);
  return [agentId, workflowId, runId].every(validAgentWorkflowId) && typeof identity.owner_id === "string" && !!identity.owner_id &&
    identity.agent_id === agentId && identity.workflow_id === workflowId && identity.run_id === runId;
}
