import { api } from "./client";
import type { Snapshot } from "./types";

/** These identities come from the host's persisted Sentinel/session binding,
 * never from model prose or an inferred conversation title. */
export interface SentinelSessionSummary {
  id: string;
  task_id: string;
  agent_id: string;
  parent_session_id?: string;
  session_id?: string;
  title: string;
  state: string;
  enabled: boolean;
  expires_at?: number;
  pending_events: number;
  busy?: boolean;
}

export interface SentinelSessionList {
  watches: SentinelSessionSummary[];
}

export interface SentinelSessionDetail {
  watch: SentinelSessionSummary;
  session: null | { id: string; cwd: string; model: string; snapshot: Snapshot };
}

export const sentinelSessionsApi = {
  list: (parentId: string, signal?: AbortSignal): Promise<SentinelSessionList> => api.sentinelSessions(parentId, signal),
  detail: (watchId: string, signal?: AbortSignal): Promise<SentinelSessionDetail> => api.sentinelSession(watchId, signal),
};
