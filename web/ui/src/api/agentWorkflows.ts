import { agentWorkflowRequest } from "./client";
import { validAgentWorkflowId } from "@/agent/agentWorkflowNavigation";

export type AgentWorkflowInputType = "text" | "boolean" | "select" | "number" | "integer" | "object" | "array";
export interface AgentWorkflowInputOption { value: string; label?: string }
export interface AgentWorkflowInput { key: string; label?: string; required?: boolean; type?: AgentWorkflowInputType; options?: AgentWorkflowInputOption[]; default?: string }
export type AgentWorkflowStepKind = "agent" | "code" | "confirm" | "condition" | "parallel" | "join" | "iteration" | "loop";
export interface AgentWorkflowCondition { source: string; operator: "equals" | "not_equals" | "contains" | "not_contains" | "is_empty" | "not_empty" | "gt" | "gte" | "lt" | "lte"; value?: string }
export interface AgentWorkflowEdge { source: string; target: string; branch?: "true" | "false" }
export interface AgentWorkflowDAG { version: 1; edges: AgentWorkflowEdge[] }
export type AgentWorkflowValueType = "string" | "number" | "integer" | "boolean" | "object" | "array";
export interface AgentWorkflowValueSchema { type: AgentWorkflowValueType; items?: AgentWorkflowValueSchema; properties?: Record<string, AgentWorkflowValueSchema>; required?: string[] }
export interface AgentWorkflowCodeBinding { type: AgentWorkflowValueType; source?: string; value?: unknown }
export interface AgentWorkflowCodeSpec { language: "python" | "javascript"; source: string; inputs: Record<string, AgentWorkflowCodeBinding>; outputs: Record<string, AgentWorkflowValueSchema>; timeout_seconds?: number }
export interface AgentWorkflowCodeExecution { language: string; runtime?: string; source_sha256?: string; stdout?: string; stderr?: string; stdout_truncated?: boolean; stderr_truncated?: boolean; exit_code?: number; timed_out?: boolean; duration_ms?: number; error_code?: string }
export interface AgentWorkflowCodePolicy { max_retries?: number; retry_delay_ms?: number; on_failure?: "stop" | "fallback"; fallback?: Record<string, unknown> }
export interface AgentWorkflowIterationSpec { items: AgentWorkflowCodeBinding; body: AgentWorkflowStep; concurrency?: number; max_items?: number; on_item_error?: "stop" | "continue" }
export interface AgentWorkflowLoopSpec { initial: AgentWorkflowCodeBinding; body: AgentWorkflowStep; until: AgentWorkflowCondition; max_iterations: number }
export interface AgentWorkflowCodeAttempt { number: number; started_at?: number; finished_at?: number; execution?: AgentWorkflowCodeExecution; output?: Record<string,unknown>; error?: string }
export interface AgentWorkflowRepeatItem { index: number; status: string; input?: unknown; output?: unknown; summary?: string; error?: string; session_id?: string; active_run_id?: string; code_inputs?: Record<string,unknown>; code_execution?: AgentWorkflowCodeExecution; attempts?: AgentWorkflowCodeAttempt[]; fallback_used?: boolean; artifact?: string; artifact_sha256?: string; started_at?: number; finished_at?: number }
export interface AgentWorkflowRepeatRun { kind: "iteration" | "loop"; total: number; items: AgentWorkflowRepeatItem[]; state?: Record<string,unknown>; limit_reached?: boolean }
export interface AgentWorkflowStep { id: string; kind: AgentWorkflowStepKind; title?: string; task?: string; prompt?: string; output_file?: string; condition?: AgentWorkflowCondition; code?: AgentWorkflowCodeSpec; iteration?: AgentWorkflowIterationSpec; loop?: AgentWorkflowLoopSpec; policy?: AgentWorkflowCodePolicy; skills?: string[]; shortcuts?: string[] }
export interface AgentWorkflowSkillResource { id: string; name: string; description?: string; version: string; available: boolean; reason?: string }
export interface AgentWorkflowShortcutResource { id: string; title: string; source?: string; version: string; available: boolean; reason?: string }
export interface AgentWorkflowFileResource { path: string; name: string; size: number }
export interface AgentWorkflowResourceCatalog { skills: AgentWorkflowSkillResource[]; shortcuts: AgentWorkflowShortcutResource[]; files: AgentWorkflowFileResource[]; files_truncated?: boolean; file_limit?: number; source_session_id?: string }
export interface AgentWorkflowFrozenSkill { id: string; name: string; version: string; path: string; sha256: string }
export interface AgentWorkflowFrozenShortcut { id: string; title: string; version: string; path: string; sha256: string }
export interface AgentWorkflowFrozenFile { path: string; name: string; size: number; sha256: string }
export interface AgentWorkflowNodeResources { skills?: AgentWorkflowFrozenSkill[]; shortcuts?: AgentWorkflowFrozenShortcut[]; files?: AgentWorkflowFrozenFile[] }
export interface AgentWorkflowRunResources { version: 1; nodes: Record<string, AgentWorkflowNodeResources> }
export type AgentWorkflowToolApprovalMode = "standard" | "strict" | "auto";
export interface AgentWorkflowDefinition {
  id: string; agent_id?: string; name: string; description?: string; revision: number; definition_hash: string;
  enabled_version?: string; archived?: boolean; deleted?: boolean; tool_approval_mode?: AgentWorkflowToolApprovalMode; inputs?: AgentWorkflowInput[]; steps: AgentWorkflowStep[]; graph?: AgentWorkflowDAG; created_at?: number; updated_at?: number;
}
export interface AgentWorkflowStepRun { step_id: string; status: string; summary?: string; artifact?: string; feedback?: string; session_id?: string; active_run_id?: string; started_at?: number; finished_at?: number; branch?: "true" | "false"; output?: Record<string,unknown>; code_inputs?: Record<string,unknown>; code_execution?: AgentWorkflowCodeExecution; attempts?: AgentWorkflowCodeAttempt[]; fallback_used?: boolean; repeat?: AgentWorkflowRepeatRun }
export interface AgentWorkflowRun {
  id: string; workflow_id: string; agent_id: string; status: string; trial?: boolean; version_id?: string;
  source_session_id?: string; execution_session_id?: string; inputs?: Record<string, string>; steps?: AgentWorkflowStepRun[];
  current_step?: number; error?: string; summary?: string; retry_of?: string; created_at?: number; updated_at?: number;
  cancel_requested?: boolean; definition: AgentWorkflowDefinition;
  resources?: AgentWorkflowRunResources;
  node_test?: { step_id: string; inputs: Record<string,unknown> };
}
export interface AgentWorkflowRunStatusItem {
  id: string; workflow_id: string; name: string; status: "queued" | "running" | "waiting_confirmation";
  cancel_requested: boolean; created_at: number; updated_at: number;
}
export interface AgentWorkflowCapabilities { read?: boolean; edit?: boolean; enable?: boolean; run?: boolean; confirm?: boolean; tools?: string[]; missing_tools?: string[]; reason?: string; max_steps?: number; max_parallel_nodes?: number; tool_approval_modes?: AgentWorkflowToolApprovalMode[]; default_tool_approval_mode?: AgentWorkflowToolApprovalMode }
export interface AgentWorkflowVersionSummary {
  version_id: string; revision: number; name: string; updated_at: number; step_count: number;
  is_current_draft: boolean; is_enabled: boolean; sources: string[];
}
export interface AgentWorkflowDiagnostic { code?: string; severity?: string; step_id?: string; field_path?: string; message: string; fix_hint?: string }
export interface AgentWorkflowPosition { x: number; y: number }
export interface AgentWorkflowViewport { x: number; y: number; zoom: number }
export interface AgentWorkflowLayout {
  workflow_id: string; layout_revision: number; definition_revision: number;
  positions: Record<string, AgentWorkflowPosition>; viewport?: AgentWorkflowViewport;
}
export interface AgentWorkflowLayoutSave {
  request_id: string; expected_layout_revision: number; expected_definition_revision: number;
  positions: Record<string, AgentWorkflowPosition>; viewport?: AgentWorkflowViewport;
}
export interface AgentWorkflowResponse {
  agent_id?: string; run_status?: AgentWorkflowRunStatusItem[];
  ok: boolean; workflows?: AgentWorkflowDefinition[]; workflow?: AgentWorkflowDefinition; runs?: AgentWorkflowRun[]; run?: AgentWorkflowRun;
  capabilities?: AgentWorkflowCapabilities; diagnostics?: AgentWorkflowDiagnostic[]; valid?: boolean; definition_hash?: string;
  text?: string; path?: string; layout?: AgentWorkflowLayout; reused?: boolean;
  versions?: AgentWorkflowVersionSummary[]; version?: AgentWorkflowVersionSummary; enabled_version?: AgentWorkflowVersionSummary;
  has_more?: boolean; next_before_revision?: number;
  resources?: AgentWorkflowResourceCatalog;
}
function path(agentId: string, group?: "read" | "edit" | "enable" | "run" | "confirm") {
  if (!validAgentWorkflowId(agentId)) throw new Error("Agent scope is required");
  return `/api/apps/${encodeURIComponent(agentId)}/agent-workflows${group ? `/${group}` : ""}`;
}
function layoutPath(agentId: string, workflowId: string) {
  if (!validAgentWorkflowId(workflowId)) throw new Error("Workflow scope is required");
  return `${path(agentId)}/${encodeURIComponent(workflowId)}/layout`;
}
export const agentWorkflowsApi = {
  list: (agentId: string, signal?: AbortSignal) => agentWorkflowRequest<AgentWorkflowResponse>("GET", path(agentId), undefined, signal),
  call: (agentId: string, group: "read" | "edit" | "enable" | "run" | "confirm", body: Record<string, unknown>, signal?: AbortSignal) => agentWorkflowRequest<AgentWorkflowResponse>("POST", path(agentId, group), body, signal),
  getLayout: (agentId: string, workflowId: string, signal?: AbortSignal) => agentWorkflowRequest<AgentWorkflowResponse>("GET", layoutPath(agentId, workflowId), undefined, signal),
  saveLayout: (agentId: string, workflowId: string, body: AgentWorkflowLayoutSave, signal?: AbortSignal) => agentWorkflowRequest<AgentWorkflowResponse>("PUT", layoutPath(agentId, workflowId), body, signal),
};
