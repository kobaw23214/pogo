/** Workflow orchestration's product contract (SessionDir/workflows.json), v2.
    A workflow is a DAG of nodes with conditional edges; each node borrows an
    existing studio Agent (or asks a person), adds the task for this one step,
    and wires its dependencies with when conditions. */

/** Edge condition: the upstream outcome that releases the dependent node. */
export type DepWhen = "success" | "failed" | "rejected" | "always";

/** One incoming edge. The wire form is dual — a plain string reads as
    {node, success} — but the editor's internal model is always the object. */
export interface WorkflowDep {
  node: string;
  when: DepWhen;
}

/** The wire spelling of after[]: success deps serialize as plain strings. */
export type WorkflowDepWire = string | { node: string; when?: DepWhen };

export interface WorkflowExpect {
  /** Workspace-relative artifact path the node is expected to leave behind. */
  path?: string;
}

/** One form field a human node asks for (text or select only). */
export interface HumanFieldSpec {
  key: string;
  label?: string;
  type?: "text" | "select";
  options?: string[];
  required?: boolean;
}

export interface OrchestrateCandidate {
  agent: string;
  hint?: string;
}

/** Dynamic dispatch config: the node's agent gets one synthesized tool per
    candidate and decides whom to delegate to at run time. */
export interface OrchestrateConfig {
  candidates: OrchestrateCandidate[];
  max_dispatches?: number;
  max_parallel?: number;
}

export type WorkflowNodeKind = "agent" | "human";

export interface WorkflowApprovalPolicy {
  prevent_self_approval: boolean;
  assignment: "fixed" | "business";
  /** Fixed local account names selected from the scoped account catalog. */
  approvers?: string[];
}

export interface WorkflowApprovalSubject {
  principal_id: string;
  username: string;
  display_name: string;
}

export interface WorkflowApprovalAssignment {
  prevent_self_approval: boolean;
  assignment: string;
  assignees: { principal_id: string; display_name: string }[];
  initial_assignees?: { principal_id: string; display_name: string }[];
  blocked_reason?: string;
}

export interface WorkflowNode {
  id: string;
  kind?: WorkflowNodeKind;
  /** Referenced studio Agent id (required for kind agent). */
  agent: string;
  /** This step's task text (required for kind agent, ≤4000; {{ }} templates). */
  task: string;
  expect?: WorkflowExpect;
  /** Incoming edges; internal model is always objects (see WorkflowDepWire). */
  after?: WorkflowDep[];
  /** Tool-name allowlist narrowing the Agent's own set; omitted inherits. */
  tools?: string[];
  /** Model override for this step; omitted uses the Agent's model. */
  model?: string;
  max_turns?: number;
  /** Ask a person to approve the agent's result before it counts as success. */
  approval?: boolean;
  approval_policy?: WorkflowApprovalPolicy;
  orchestrate?: OrchestrateConfig;
  /* human kind: what the approver sees and answers. */
  prompt?: string;
  fields?: HumanFieldSpec[];
  actions?: string[];
  /** Wait bound, Go duration spelling ("72h"); sweep settles on_timeout. */
  timeout?: string;
  on_timeout?: "fail" | "reject";
}

/** The publish pointer: which immutable version a run starts from. */
export interface PublishedPointer {
  version_id: string;
  name: string;
  desc: string;
  published_at: string;
}

export interface WorkflowVersion {
  version_id: string;
  name: string;
  desc: string;
  published_at: string;
  current: boolean;
}

/** The workflow-facing summary of its one schedule job. */
export interface ScheduleSummary {
  job_id: string;
  kind: string;
  cron?: string;
  every_sec?: number;
  at?: string;
  timezone?: string;
  effective_timezone?: string;
  effective_timezone_label?: string;
  enabled: boolean;
  next_run?: string;
  last_run_status?: string;
}

export interface ScheduleInput {
  kind: "cron" | "every" | "at";
  cron?: string;
  every_sec?: number;
  at?: string;
  timezone?: string;
  enabled?: boolean;
}

export interface Workflow {
  id: string;
  definition_hash?: string;
  name: string;
  description: string;
  revision: number;
  nodes: WorkflowNode[];
  /** Null until first published; the tri-state badge reads this + dirty. */
  published: PublishedPointer | null;
  /** Draft moved past the published snapshot. */
  dirty: boolean;
  schedule?: ScheduleSummary;
  created_at: string;
  updated_at: string;
  deleted?: boolean;
  capabilities?: {
    can_edit: boolean;
    can_publish: boolean;
    can_invoke: boolean;
    can_delete?: boolean;
    can_schedule?: boolean;
    can_manage_access?: boolean;
    can_manage_publications?: boolean;
  };
}

/** The save payload's node: identical to the editor's WorkflowNode except
    after, which serializes to the dual wire form (success deps as strings). */
export type WorkflowInputNode = Omit<WorkflowNode, "after"> & { after?: WorkflowDepWire[] };

export interface WorkflowInput {
  name: string;
  description: string;
  nodes: WorkflowInputNode[];
}

export type WorkflowRunStatus = "queued" | "working" | "running" | "waiting_human" | "completed" | "failed" | "cancelled" | "interrupted";

export interface WorkflowRunCapabilities {
  can_read: boolean;
  can_cancel: boolean;
  can_approve: boolean;
  can_retry: boolean;
  can_read_artifacts: boolean;
  can_manage_participants: boolean;
  can_approve_nodes?: string[];
  can_read_artifact_nodes?: string[];
}

export interface WorkflowRunParticipant {
  principal_id: string;
  display_name?: string;
  actions: string[];
  node_id?: string;
}

export interface WorkflowRunParticipants {
  participants: WorkflowRunParticipant[];
  subjects: { principal_id: string; display_name: string }[];
}

export interface WorkflowAccess {
  grants: WorkflowRunParticipant[];
  subjects: WorkflowRunParticipants["subjects"];
}

export interface WorkflowSpaceRoles {
  space_id: string;
  roles: { principal_id: string; role: string }[];
  subjects: WorkflowRunParticipants["subjects"];
}

export type WorkflowNodeState =
  | "waiting"
  | "running"
  | "success"
  | "failed"
  | "skipped"
  | "cancelled"
  | "waiting_human"
  | "rejected";

/** A wait on a person, self-contained: the approval UI renders it without
    joining against the definition. */
export interface HumanState {
  prompt: string;
  fields?: HumanFieldSpec[];
  actions: string[];
  deadline: string;
  on_timeout: string;
  action?: string;
  values?: Record<string, string>;
}

export interface DispatchRecord {
  candidate: string;
  task: string;
  /** running in flight; success | failed | cancelled once settled. */
  status: string;
  summary?: string;
  artifact?: string;
  error?: string;
  usage?: { input: number; output: number };
  started_at?: string;
  ended_at?: string;
}

export interface WorkflowNodeRecord {
  status: WorkflowNodeState;
  started_at?: string;
  ended_at?: string;
  artifact?: string;
  summary?: string;
  error?: string;
  usage?: { input: number; output: number };
  /** The edge condition that released the node (empty for roots). */
  branch?: string;
  /** The upstream whose outcome closed this node's edges. */
  skipped_by?: string;
  /** What the node's template references resolved to — what the node saw. */
  inputs?: Record<string, string>;
  /** Set while the node waits on a person; keeps the decision after. */
  human?: HumanState;
  /** An orchestrator node's delegations, settled and running alike. */
  dispatches?: DispatchRecord[];
}

/** The run record (wf-runs/{id}/run.json) — the authoritative state; the
    definition snapshot (the published version) rides along so a run stays
    readable after edits. */
export interface WorkflowRun {
  id: string;
  test_run?: boolean;
  draft_revision?: number;
  definition_hash?: string;
  workflow_id: string;
  revision: number;
  definition: Workflow;
  status: WorkflowRunStatus;
  error?: string;
  /** Host diagnostic; status/nodes still describe the last saved record. */
  persistence_error?: string;
  nodes: Record<string, WorkflowNodeRecord>;
  created_at: string;
  ended_at?: string;
  capabilities?: WorkflowRunCapabilities;
  origin?: { source?: string; source_id?: string; publication_id?: string; integration_id?: string; external_subject?: string; business_id?: string; version_id?: string; retry_of?: string };
  inputs?: Record<string, string>;
  approvals?: Record<string, WorkflowApprovalAssignment>;
}

/** The workflow stream's frame envelope (web/wire.go's wide Event): the
    whole record rides workflow_run on structural events; dispatch events
    carry just the delegation. */
export interface WorkflowStreamEvent {
  type: string;
  workflow_id?: string;
  node_id?: string;
  node_status?: string;
  workflow_run?: WorkflowRun;
  dispatch?: DispatchRecord;
  error?: string;
}
