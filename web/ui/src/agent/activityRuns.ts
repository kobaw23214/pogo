import type { IncomingArgs } from "@/api/types";
import type { Exchange } from "./exchanges";
import type { NoticeItem, TimelineCall, TurnItem, UserItem } from "./timeline";
import { activityCallNeedsAttention, buildActivityProjection } from "./activityProjection";
import { answerReviewNoticeKind, showAnswerReviewNotice } from "./answerReview";
import { shortcutCallTool } from "./shortcutToolNames";

export interface ActivityCallItem {
  kind: "call";
  /** Shared by the generating-arguments placeholder and the actual call. */
  id: string;
  turn: TurnItem;
  call?: TimelineCall;
  incoming?: IncomingArgs;
}

export interface ActivityThinkingItem {
  kind: "thinking";
  id: string;
  turn: TurnItem;
}

export type ActivityRunItem = ActivityCallItem | ActivityThinkingItem;

export interface ActivityRun {
  kind: "run";
  id: string;
  items: ActivityRunItem[];
  calls: TimelineCall[];
  turnIds: string[];
  citeLabels: string[];
  /** A content boundary closed membership; running leaves may still exist. */
  closed: boolean;
}

export type ActivityRunNode = ActivityRun
  | { kind: "prose"; id: string; turn: TurnItem }
  | { kind: "review"; id: string; turn: TurnItem }
  | { kind: "card"; id: string; item: ActivityCallItem; turn: TurnItem; independent: boolean | undefined }
  | { kind: "steering"; id: string; message: UserItem }
  | { kind: "notice"; id: string; notice: NoticeItem };

export interface ActivityRunOptions {
  crossTurns?: boolean;
  /** The host retains ids first observed before their tool name was known.
   * Their neutral host must not move when the name subsequently arrives. */
  reservedCallIds?: ReadonlySet<string>;
}

const INDEPENDENT = new Set([
  "subagent", "todo", "request_user_input", "request_user_input_async", "skill_save",
  "shortcut_create", "shortcut_update", "shortcut_folder_create", "shortcut_folder_rename",
  "shortcut_move", "shortcut_delete", "shortcut_manage", "agent_workflow_enable",
]);
// These declarations always form a boundary, even in historical replay. Their
// eventual arguments only decide whether the fixed host shows a receipt or row.
const ARGUMENT_BOUNDARY = new Set(["shortcut_merge", "background_task"]);
const record = (value: unknown): Record<string, unknown> => value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};

export function isIndependentActivityCall(call: Pick<TimelineCall, "name" | "args">): boolean {
  const name = shortcutCallTool(call.name, call.args);
  if (INDEPENDENT.has(name)) return true;
  if (name === "shortcut_merge") return record(call.args).apply === true;
  return name === "background_task" && record(call.args).action === "create";
}

export function activityRunDisclosureKey(exchangeId: string, runId: string): string {
  return `row:${exchangeId}:${runId}`;
}

export function activityItemNeedsAttention(item: ActivityRunItem, finished = false): boolean {
  if (item.kind === "thinking") return item.turn.streaming;
  if (item.call && ["request_user_input", "request_user_input_async"].includes(item.call.name)
    && record(item.call.result?.details).status === "pending") return true;
  return !!item.incoming || !!item.call && activityCallNeedsAttention(item.call, finished);
}

export function isVisibleActivityNotice(notice: NoticeItem, turns: readonly TurnItem[]): boolean {
  // Unshown harness nudges do not split a run. Ordinary visible notices retain
  // the legacy behaviour; review notices disappear when their real card exists.
  if (notice.text.startsWith("[pogo] ") && !answerReviewNoticeKind(notice.text)) return false;
  return showAnswerReviewNotice(notice.text, turns);
}

/** A presentation-only projection. No result, error or approval changes a
 * call's original position, membership or identity. The existing turn-based
 * projection remains available to standalone TurnCard consumers. */
export function buildActivityRuns(exchange: Exchange, options: ActivityRunOptions = {}): ActivityRunNode[] {
  const nodes: ActivityRunNode[] = [];
  let current: ActivityRun | undefined;
  const seenCalls = new Set<string>();
  const close = () => { if (current) current.closed = true; current = undefined; };
  const append = (item: ActivityRunItem) => {
    if (!current) {
      current = { kind: "run", id: `run:${item.id}`, items: [], calls: [], turnIds: [], citeLabels: [], closed: false };
      nodes.push(current);
    }
    current.items.push(item);
    if (!current.turnIds.includes(item.turn.id)) current.turnIds.push(item.turn.id);
    if (item.kind === "call" && item.call) {
      current.calls.push(item.call);
      const label = item.call.result?.source?.label;
      if (label && !current.citeLabels.includes(label)) current.citeLabels.push(label);
    }
  };
  const appendCall = (item: ActivityCallItem) => {
    const callId = item.call?.callId ?? item.incoming!.call_id;
    if (seenCalls.has(callId)) return;
    seenCalls.add(callId);
    const name = item.call?.name ?? item.incoming!.name;
    const declaredBoundary = INDEPENDENT.has(name) || ARGUMENT_BOUNDARY.has(name) || !name || options.reservedCallIds?.has(callId);
    if (declaredBoundary || item.call && isIndependentActivityCall(item.call)) {
      close();
      nodes.push({ kind: "card", id: item.id, item, turn: item.turn,
        independent: item.call ? isIndependentActivityCall(item.call) : INDEPENDENT.has(name) ? true : undefined });
    } else append(item);
  };

  for (const node of buildActivityProjection(exchange)) {
    if (node.kind === "steering") { close(); nodes.push(node); continue; }
    if (node.kind === "notice") {
      if (isVisibleActivityNotice(node.notice, exchange.turns)) { close(); nodes.push(node); }
      continue;
    }
    const turn = node.turn;
    if (options.crossTurns === false) close();
    if (turn.answerReview) { close(); nodes.push({ kind: "review", id: `${turn.id}:review`, turn }); }
    if (turn.thinking) append({ kind: "thinking", id: `${turn.id}:thinking`, turn });
    if (turn.text) { close(); nodes.push({ kind: "prose", id: `${turn.id}:prose`, turn }); }
    // A source can contain its last pending placeholder and actual call in the
    // same snapshot. The actual call owns the one shared slot, never two rows.
    const declaredIds = new Set(turn.calls.map(call => call.callId));
    for (const call of turn.calls) appendCall({ kind: "call", id: `call:${call.callId}`, turn, call });
    for (const incoming of turn.incoming ?? []) {
      if (!declaredIds.has(incoming.call_id)) appendCall({ kind: "call", id: `call:${incoming.call_id}`, turn, incoming });
    }
  }
  if (exchange.finished) close();
  return nodes;
}

export function activityRunsDisclosureKeys(exchange: Exchange, options?: ActivityRunOptions): string[] {
  return buildActivityRuns(exchange, options).flatMap(node => node.kind === "run"
    ? [activityRunDisclosureKey(exchange.id, node.id), ...node.items.map(item => item.kind === "thinking" ? item.id : `${item.id}:details`)]
    : node.kind === "card" ? [`${node.item.id}:details`, `${node.item.id}:subagent:result`] : []);
}
