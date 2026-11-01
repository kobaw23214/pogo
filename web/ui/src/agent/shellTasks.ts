import type { ShellJobRecord, ShellJobStatus } from "@/api/types";

const statuses: readonly ShellJobStatus[] = ["running", "completed", "failed", "cancelled", "timed_out", "interrupted"];
const nonNegativeInteger = (value: unknown): value is number => typeof value === "number" && Number.isSafeInteger(value) && value >= 0;

/** Both streamed updates and snapshots must pass the same host-record boundary. */
export function validShellJobRecord(value: unknown): value is ShellJobRecord {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return false;
  const task = value as Record<string, unknown>;
  return typeof task.task_id === "string" && /^[A-Za-z0-9_-]{1,160}$/.test(task.task_id) && !["__proto__", "constructor", "prototype"].includes(task.task_id) &&
    typeof task.command === "string" && typeof task.workdir === "string" &&
    (task.description === undefined || typeof task.description === "string") &&
    typeof task.status === "string" && statuses.includes(task.status as ShellJobStatus) &&
    nonNegativeInteger(task.started_at) && (task.ended_at === undefined || nonNegativeInteger(task.ended_at)) &&
    (task.exit_code === undefined || typeof task.exit_code === "number" && Number.isSafeInteger(task.exit_code)) &&
    (task.error === undefined || typeof task.error === "string") &&
    (task.persistence_error === undefined || typeof task.persistence_error === "string") &&
    typeof task.output_tail === "string" && nonNegativeInteger(task.output_bytes) &&
    typeof task.output_truncated === "boolean" && nonNegativeInteger(task.revision) && typeof task.background === "boolean";
}

/** A snapshot replaces this session's state without replaying file invalidations. */
export function shellJobSnapshot(value: unknown): Record<string, ShellJobRecord> {
  const records: Record<string, ShellJobRecord> = {};
  if (!Array.isArray(value)) return records;
  for (const task of value) {
    if (!validShellJobRecord(task)) continue;
    const previous = records[task.task_id];
    if (previous && (previous.revision >= task.revision || previous.status !== "running" && task.status === "running")) continue;
    records[task.task_id] = { ...task };
  }
  return records;
}
