import type { SentinelSessionSummary } from "@/api/sentinelSessions";
import { SENTINEL_PREFIX, type DockTab } from "@/components/dockSheets";

const identity = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_-]{1,160}$/.test(value);

export function requestedSentinelId(search: string): string | null {
  const value = new URLSearchParams(search).get("sentinel");
  return identity(value) ? value : null;
}

/** Only a host-authorized detail response may supply the tab's identity and
 * parent. Local storage, task labels and result URLs are not ownership proof. */
export function sentinelTab(watch: SentinelSessionSummary, expectedAgent: string, parentAvailable = true): DockTab | null {
  if (!identity(watch.id) || watch.task_id !== `sentinel:${watch.id}` || !identity(watch.agent_id) || watch.agent_id !== expectedAgent ||
      watch.parent_session_id !== undefined && watch.parent_session_id !== "" && !identity(watch.parent_session_id)) return null;
  return { id: SENTINEL_PREFIX + watch.id, kind: "sentinel", sentinelId: watch.id, agentId: watch.agent_id,
    ...(parentAvailable && watch.parent_session_id ? { parentSession: watch.parent_session_id } : {}),
    label: watch.title || "Sentinel", closable: true };
}

export function sentinelDeepLink(conversationPath: string, watchId: string): string {
  if (!identity(watchId)) throw new Error("Invalid Sentinel identity");
  const split = conversationPath.indexOf("?");
  const base = split < 0 ? conversationPath : conversationPath.slice(0, split);
  const query = new URLSearchParams(split < 0 ? "" : conversationPath.slice(split + 1));
  query.set("sentinel", watchId);
  return `${base}?${query}`;
}
