import type { Exchange } from "./exchanges";
import type { NoticeItem, TimelineCall, TurnItem, UserItem } from "./timeline";
import { toolCallState } from "./toolPresentation";
import { isSubagentDetails } from "./timeline";

export interface ActivityToolGroup {
  id: string;
  turnId: string;
  explore: boolean;
  calls: TimelineCall[];
  citeLabels: string[];
}

export type ActivityNode =
  | { kind: "turn"; id: string; turn: TurnItem; groups: ActivityToolGroup[] }
  | { kind: "steering"; id: string; message: UserItem }
  | { kind: "notice"; id: string; notice: NoticeItem };

const EXPLORE = new Set(["read", "ls", "find", "grep"]);

/** Membership depends on declared tool identity and turn order, never on a
 * result arriving, an approval opening, or a call changing state. */
export function buildActivityGroups(turn: TurnItem): ActivityToolGroup[] {
  const groups: ActivityToolGroup[] = [];
  for (const call of turn.calls) {
    const explore = EXPLORE.has(call.name);
    const previous = groups[groups.length - 1];
    if (explore && previous?.explore) previous.calls.push(call);
    else groups.push({ id: `activity:${turn.id}:${call.callId}`, turnId: turn.id, explore, calls: [call], citeLabels: [] });
  }
  for (const group of groups) group.citeLabels = [...new Set(group.calls.flatMap(call => call.result?.source?.label ? [call.result.source.label] : []))];
  return groups;
}

export function activityDisclosureKey(exchangeId: string, nodeId: string): string {
  return `row:${exchangeId}:${nodeId}`;
}

export function legacyActivityRowId(group: ActivityToolGroup): string {
  return group.explore ? `${group.calls[0].callId}:explore` : group.calls[0].callId;
}

export function activityDisclosureKeys(exchange: Exchange): string[] {
  return [...new Set(exchange.turns.flatMap(turn => [
    `${turn.id}:thinking`,
    ...turn.calls.flatMap(call => [`call:${call.callId}:details`, `call:${call.callId}:subagent:result`]),
    ...buildActivityGroups(turn).flatMap(group => [activityDisclosureKey(exchange.id, group.id), activityDisclosureKey(exchange.id, legacyActivityRowId(group))]),
  ]))];
}

export function activityCallNeedsAttention(call: TimelineCall, finished = false): boolean {
  if (call.name === "subagent") {
    // Finishing execution and delivering its final response are separate facts.
    if (call.bgTask?.status === "done" && !call.taskNotice) return true;
    if (!call.result && call.liveFrames?.some(frame => frame.type === "run_end")) return true;
    const details = call.result?.details;
    if (!call.bgTask && !call.taskNotice && isSubagentDetails(details) && details.background) return true;
  }
  const state = toolCallState(call);
  // An explicit fold of completed work includes settled failures/incomplete
  // history too. Running work and approvals remain visible after the parent
  // ends, as does a child whose final response has not arrived yet.
  return state === "gate" || state === "running" || (!finished && state !== "done");
}

/** Exchange markers carry their original timeline ordinal when available.
 * Older fixtures without an anchor remain visible before the first turn;
 * timestamps are not used to invent missing ordering information. */
export function buildActivityProjection(exchange: Exchange): ActivityNode[] {
  const turnIds = new Set(exchange.turns.map(turn => turn.id));
  type Marker = { after?: string; order?: number; node: ActivityNode };
  const markers: Marker[] = [
    ...(exchange.notices ?? []).map(notice => ({ after: notice.afterTurnId, order: notice.timelineOrder, node: { kind: "notice" as const, id: `notice:${notice.id}`, notice } })),
    ...(exchange.steering ?? []).map(message => ({ after: message.afterTurnId, order: message.timelineOrder, node: { kind: "steering" as const, id: `steering:${message.id}`, message } })),
  ];
  markers.sort((a, b) => (a.order ?? Number.MAX_SAFE_INTEGER) - (b.order ?? Number.MAX_SAFE_INTEGER));
  const before: ActivityNode[] = [];
  const after = new Map<string, ActivityNode[]>();
  for (const marker of markers) {
    if (!marker.after || !turnIds.has(marker.after)) before.push(marker.node);
    else {
      let list = after.get(marker.after);
      if (!list) after.set(marker.after, list = []);
      list.push(marker.node);
    }
  }
  const nodes = [...before];
  for (const turn of exchange.turns) {
    nodes.push({ kind: "turn", id: `turn:${turn.id}`, turn, groups: buildActivityGroups(turn) });
    nodes.push(...(after.get(turn.id) ?? []));
  }
  return nodes;
}
