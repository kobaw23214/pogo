import { createContextUsageReader, type ContextReading } from "./contextUsage";
import type { TaskContextSnapshot } from "./taskSessionSnapshot";

/** The reading is carried with its authenticated execution scope, so a parent
 * inspector cannot display a sibling node's numbers after switching tabs. */
export interface TaskContextReading extends ContextReading {
  sessionId: string;
  activeRunId?: string;
  model?: string;
  clearTrigger?: number;
  stale?: boolean;
}

export interface TaskContextScope {
  agentId: string;
  sessionId: string;
  activeRunId?: string;
  workflowRunId?: string;
  /** Closing a process tab may retain a scoped last reading in its inspector. */
  unmounted?: true;
}

export function createTaskContextUsageReader() {
  const read = createContextUsageReader();
  let previous: TaskContextReading | null = null;
  return (sessionId: string, snapshot: TaskContextSnapshot | null, activeRunId?: string, stale = false): TaskContextReading | null => {
    if (!snapshot || snapshot.task_context_unavailable || !sessionId) { previous = null; return null; }
    // Defence in depth for callers supplying an unprojected shared snapshot.
    if (activeRunId) {
      const latestUser = [...snapshot.messages].reverse().find(message => message.role === "user");
      const liveRunId = snapshot.run.active ? snapshot.run.run_id || snapshot.live.run_id : snapshot.live.active ? snapshot.live.run_id : undefined;
      if (latestUser?.run_id !== activeRunId || liveRunId && liveRunId !== activeRunId) { previous = null; return null; }
    }
    const reading = read({ used: snapshot.context_tokens || 0, window: snapshot.run.context_window || 0,
      overhead: snapshot.overhead_tokens || 0, estimated: snapshot.context_estimated, messages: snapshot.messages, results: snapshot.results });
    const next: TaskContextReading = { ...reading, sessionId, ...(activeRunId ? { activeRunId } : {}), ...(snapshot.run.model ? { model: snapshot.run.model } : {}),
      ...(snapshot.clear_trigger ? { clearTrigger: snapshot.clear_trigger } : {}), ...(stale ? { stale: true } : {}) };
    // Tool/token deltas change snapshots much more frequently than the prompt
    // reading. Keep the same reference so they do not repaint the outer card.
    if (previous && JSON.stringify(previous) === JSON.stringify(next)) return previous;
    previous = next; return next;
  };
}
