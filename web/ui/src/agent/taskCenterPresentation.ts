import { getLocale } from "@/i18n";
import type { BackgroundTask, BackgroundTaskRun } from "@/api/taskCenterApi";

export type TaskCenterView = "tasks" | "queue" | "history";
export type TaskRunLane = "waiting" | "running" | "queued";
export function latestTaskRun(taskId: string, runs: BackgroundTaskRun[]): BackgroundTaskRun | undefined {
  let latest: BackgroundTaskRun | undefined;
  for (const run of runs) {
    if (run.task_id !== taskId) continue;
    if (!latest || run.created_at > latest.created_at || run.created_at === latest.created_at && run.id > latest.id) latest = run;
  }
  return latest;
}
export function isPendingTaskRun(run: BackgroundTaskRun): boolean {
  return run.status === "queued" || run.status === "running" || run.waiting_human;
}
export function taskRunNeedsAttention(run: BackgroundTaskRun): boolean {
  return run.waiting_human || run.status === "failed" || run.status === "interrupted" || run.source === "sentinel" && run.status === "cancelled" && run.can_retry;
}
export function taskRunLane(run: BackgroundTaskRun): TaskRunLane {
  return run.waiting_human ? "waiting" : run.status === "running" ? "running" : "queued";
}
export function taskRunState(run: BackgroundTaskRun): string {
  if (run.waiting_human) return "waiting";
  if (run.cancel_requested && run.status === "running") return "stopping";
  return run.status;
}
export function backgroundTaskState(task: BackgroundTask): string {
  if (task.state === "completed") return "completed";
  if (task.state === "expired") return "expired";
  if (task.state === "autoPaused") return "error";
  if (!task.enabled) return "paused";
  if (task.state === "waiting_capacity") return "waiting_capacity";
  if (["error", "blocked", "unavailable", "autoPaused", "expired"].includes(task.state)) return "error";
  return task.trigger === "condition" ? "listening" : "scheduled";
}
export function taskCenterNeedsAttention(tasks: BackgroundTask[], runs: BackgroundTaskRun[]): boolean {
  const latest = new Map<string, BackgroundTaskRun>();
  for (const run of runs) if (!latest.has(run.task_id) || latest.get(run.task_id)!.created_at < run.created_at) latest.set(run.task_id, run);
  return tasks.some(task => task.attention) || runs.some(run => run.waiting_human) || [...latest.values()].some(taskRunNeedsAttention);
}
export function matchesTaskQuery(item: BackgroundTask | BackgroundTaskRun, query: string): boolean {
  const needle = query.trim().toLocaleLowerCase();
  if (!needle) return true;
  return [item.title, "description" in item ? item.description : item.summary, "error" in item ? item.error : "", item.agent_id].some(value => value?.toLocaleLowerCase().includes(needle));
}
export function taskCenterDate(value: string | number | null | undefined, locale: string = getLocale()): string {
  if (!value || typeof value === "string" && value.startsWith("0001-")) return "—";
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return "—";
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(date);
}
export function runResultIdentity(run: Pick<BackgroundTaskRun, "session_id" | "workflow_id" | "agent_id" | "result_url">): { type: "session"; id: string; agentId: string } | null {
  if (run.workflow_id) return null;
  if (run.session_id) return { type: "session", id: run.session_id, agentId: run.agent_id };
  // A safe fallback for old records that have only the host-authored link.
  const url = run.result_url || "";
  try {
    const session = /^\/manage\/agents\/([^/?#]+)\/chat\?(?:task_session|c)=([^&#]+)$/.exec(url);
    if (session && decodeURIComponent(session[1]) === run.agent_id) return { type: "session", id: decodeURIComponent(session[2]), agentId: run.agent_id };
  } catch { return null; }
  return null;
}
