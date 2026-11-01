import type { InjectionKey, Ref } from "vue";
import type { AgentEvent } from "@/api/types";
import { agentWorkflowReceiptLocation, isAgentWorkflowDetails, validAgentWorkflowId } from "./agentWorkflowNavigation";

export interface AgentWorkflowUpdate {
  sessionId: string;
  agentId: string;
  workflowId: string;
  runId?: string;
  revision?: number;
  action: string;
  seq: number;
}

export const agentWorkflowUpdatesKey: InjectionKey<Readonly<Ref<AgentWorkflowUpdate | null>>> = Symbol("agentWorkflowUpdates");

/** Only a live, successful host tool receipt invalidates the canvas. Historical
 * transcripts and model prose never open a workflow or replace its definition. */
export function liveAgentWorkflowUpdate(event: AgentEvent, delivery?: { sessionId: string | null; replay: boolean }): AgentWorkflowUpdate | null {
  if (!delivery?.sessionId || delivery.replay || event.type !== "tool_end" || event.is_error || !event.seq || !isAgentWorkflowDetails(event.details)) return null;
  const detail = event.details;
  const actions: Record<string, readonly string[]> = { edit: ["create", "patch", "archive", "delete"], enable: ["enable", "disable"], run: ["trial", "start", "cancel", "retry"] };
  if (!detail.group || event.name !== `agent_workflow_${detail.group}` || !actions[detail.group]?.includes(detail.action) || detail.result.ok !== true) return null;
  const workflow = detail.result.workflow as Record<string, unknown> | undefined;
  const run = detail.result.run as Record<string, unknown> | undefined;
  const agentId = detail.agent_id ?? detail.result.agent_id ?? workflow?.agent_id ?? run?.agent_id;
  if (!validAgentWorkflowId(agentId)) return null;
  const location = agentWorkflowReceiptLocation(detail, agentId);
  if (!location) return null;
  return { sessionId: delivery.sessionId, agentId, workflowId: location.workflowId, ...(location.runId ? { runId: location.runId } : {}), ...(location.revision ? { revision: location.revision } : {}), action: detail.action, seq: event.seq };
}
