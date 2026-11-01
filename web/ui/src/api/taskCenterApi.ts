import { taskCenterRequest } from "./client";

export type BackgroundTaskSource = "schedule" | "sentinel" | "workflow";
export type BackgroundRunStatus = "queued" | "running" | "completed" | "failed" | "cancelled" | "interrupted";
export interface BackgroundTask {
  id: string;
  source: BackgroundTaskSource;
  source_id: string;
  title: string;
  description: string;
  agent_id: string;
  agent_name?: string;
  workflow_id?: string;
  trigger: "time" | "condition";
  executor: "agent" | "workflow" | "legacy";
  enabled: boolean;
  state: string;
  next_run?: string | null;
  expires_at?: number;
  queued_count: number;
  running_count: number;
  pending_events: number;
  attention: boolean;
  result_url?: string;
  sentinel_id?: string;
  parent_session_id?: string;
  session_id?: string;
  revision?: number;
  can_manage: boolean;
}
export interface BackgroundTaskRun {
  test_run?: boolean;
  draft_revision?: number;
  definition_hash?: string;
  id: string;
  task_id: string;
  source: BackgroundTaskSource;
  source_id: string;
  title: string;
  agent_id: string;
  workflow_id?: string;
  workflow_run_id?: string;
  trigger: string;
  status: BackgroundRunStatus;
  revision: number;
  attempt: number;
  cancel_requested: boolean;
  created_at: number;
  started_at?: number;
  finished_at?: number;
  session_id?: string;
  sentinel_id?: string;
  parent_session_id?: string;
  result_url?: string;
  summary?: string;
  error?: string;
  end_reason?: string;
  can_cancel: boolean;
  can_retry: boolean;
  waiting_human: boolean;
}
export interface BackgroundTaskCenter {
  tasks: BackgroundTask[];
  runs: BackgroundTaskRun[];
  queue_error?: string;
}
function sourcePath(source: BackgroundTaskSource, id: string) {
  if ((source !== "schedule" && source !== "sentinel") || !id.trim()) throw new Error("Task identity is required");
  return `/api/tasks/${source}/${encodeURIComponent(id)}`;
}
function runPath(id: string, revision: number) {
  if (!id.trim() || !Number.isSafeInteger(revision) || revision < 1) throw new Error("Run identity and revision are required");
  return `/api/task-runs/${encodeURIComponent(id)}`;
}
export const taskCenterApi = {
  list: (agentId: string, signal?: AbortSignal) => {
    if (!agentId.trim()) throw new Error("Agent scope is required");
    return taskCenterRequest<BackgroundTaskCenter>("GET", `/api/tasks?${new URLSearchParams({ agent_id: agentId })}`, undefined, signal);
  },
  setEnabled: (source: BackgroundTaskSource, id: string, enabled: boolean) => taskCenterRequest("POST", `${sourcePath(source, id)}/${enabled ? "resume" : "pause"}`),
  cancel: (id: string, revision: number) => taskCenterRequest("POST", `${runPath(id, revision)}/cancel`, { revision }),
  retry: (id: string, revision: number) => taskCenterRequest("POST", `${runPath(id, revision)}/retry`, { revision }),
};
