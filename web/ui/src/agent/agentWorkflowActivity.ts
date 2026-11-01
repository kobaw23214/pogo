import type { AgentWorkflowRun } from '@/api/agentWorkflows';
import { validAgentWorkflowId } from './agentWorkflowNavigation';

export interface AgentWorkflowNodeActivity {
  step_id: string;
  status: string;
  phase: string;
  observed: boolean;
  session_id?: string;
  active_run_id?: string;
  turn?: number;
  tool_name?: string;
  tool_count?: number;
  pending_tool_count?: number;
  approval_count?: number;
  updated_at: number;
  connected?: boolean;
}
export interface AgentWorkflowActivitySnapshot {
  agent_id: string;
  workflow_id: string;
  run_id: string;
  status: string;
  updated_at: number;
  nodes: AgentWorkflowNodeActivity[];
}
const phases = new Set(['queued', 'preparing', 'running', 'model', 'thinking', 'answer', 'tool_input', 'tool', 'approval', 'completed', 'failed', 'interrupted', 'cancelled', 'skipped']);
const states = new Set(['queued', 'running', 'waiting_confirmation', 'completed', 'failed', 'interrupted', 'cancelled', 'skipped', 'pending', 'waiting']);
const count = (value: unknown) => value === undefined || Number.isSafeInteger(value) && (value as number) >= 0 && (value as number) <= 1_000_000;

/** Validate the transport boundary before allowing a Run to affect its canvas. */
export function workflowActivitySnapshot(value: unknown, agentId: string, workflowId: string, runId: string): AgentWorkflowActivitySnapshot | null {
  if (!value || typeof value !== 'object') return null;
  const raw = value as AgentWorkflowActivitySnapshot;
  if (raw.agent_id !== agentId || raw.workflow_id !== workflowId || raw.run_id !== runId || !states.has(raw.status) || !Number.isSafeInteger(raw.updated_at) || !Array.isArray(raw.nodes) || raw.nodes.length > 200) return null;
  const seen = new Set<string>();
  for (const node of raw.nodes) {
    if (!node || !validAgentWorkflowId(node.step_id) || seen.has(node.step_id) || !states.has(node.status) || !phases.has(node.phase) || typeof node.observed !== 'boolean' || !Number.isSafeInteger(node.updated_at)) return null;
    if (node.session_id !== undefined && !validAgentWorkflowId(node.session_id) || node.active_run_id !== undefined && !validAgentWorkflowId(node.active_run_id)) return null;
    if (!count(node.turn) || !count(node.tool_count) || !count(node.pending_tool_count) || !count(node.approval_count) || node.tool_name !== undefined && (typeof node.tool_name !== 'string' || node.tool_name.length > 256)) return null;
    seen.add(node.step_id);
  }
  return raw;
}

/** A live session/attempt must never be projected onto another node or retry. */
export function workflowNodeActivities(snapshot: AgentWorkflowActivitySnapshot | null, run: AgentWorkflowRun | null, connected: boolean): Record<string, AgentWorkflowNodeActivity> {
  const result = Object.create(null) as Record<string, AgentWorkflowNodeActivity>;
  if (!snapshot || !run || snapshot.run_id !== run.id || snapshot.workflow_id !== run.workflow_id || snapshot.agent_id !== run.agent_id) return result;
  for (const node of snapshot.nodes) {
    const definition = run.definition.steps.find(step => step.id === node.step_id);
    const stored = run.steps?.find(step => step.step_id === node.step_id);
    if (!definition || stored?.session_id && node.session_id && stored.session_id !== node.session_id || stored?.active_run_id && node.active_run_id && stored.active_run_id !== node.active_run_id) continue;
    result[node.step_id] = { ...node, connected };
  }
  return result;
}
