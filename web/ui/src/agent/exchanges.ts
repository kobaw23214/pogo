import type { NoticeItem, TimelineItem, TurnItem, UserItem } from "./timeline";

// This module is deliberately free of Vue imports, like timeline.ts: it is a
// pure projection over the flat timeline, unit tested without a component.

/**
 * An exchange is one question and the work it set off.
 *
 * The flat timeline answers "what happened, in order"; the exchange grouping
 * answers "what did each question cost". The activity host retains this unit
 * while it runs and after it ends; completion itself does not fold its content.
 *
 * A new request starts an exchange. A server-marked steering message continues
 * its current exchange: a follow-up must never imply the original task ended.
 */
export interface Exchange {
  /** The opening user message id; a synthetic "head" if turns precede it. */
  id: string;
  user?: UserItem;
  turns: TurnItem[];
  /**
   * The last turn with visible text — the conclusion. It never enters the
   * fold: a folded exchange is its summary line plus this answer. (Codex once
   * shipped the bug of folding the answer in; the fold gate below exists
   * because an exchange without one has nothing to keep outside.)
   */
  answer?: TurnItem;
  stats: ExchangeStats;
  /** user.ts, falling back to the first settled turn; absent on old transcripts. */
  startedAt?: number;
  /** The last settled turn's ts; the live turn carries none. */
  endedAt?: number;
  /**
   * Harness nudges that landed inside this exchange (see NoticeItem in
   * timeline.ts). They ride along rather than splitting the exchange: the run
   * they interrupted was still this one, and a soft-cap checkpoint that opened
   * a new exchange restarted the turn numbering and doubled the work logs.
   */
  notices: Array<NoticeItem & { afterTurnId?: string; timelineOrder?: number }>;
  /** Follow-ups stay at their chronological position, including in replay. */
  steering?: Array<UserItem & { afterTurnId?: string; timelineOrder?: number }>;
  /**
   * The parent run ended or a newer question follows this exchange. Background
   * children can still report updates; this flag does not imply their success.
   */
  finished: boolean;
}

export interface ExchangeStats {
  calls: number;
  /** Calls whose result is an error — surfaced on the folded line, never buried. */
  failed: number;
  /** Distinct file paths that edit/write calls touched. */
  files: number;
  /** Call counts by tool name, for the folded line's "read (5), edit (2)" scent. */
  tools: Record<string, number>;
}

function pathOf(args: unknown): string | undefined {
  if (typeof args !== "object" || args === null) return undefined;
  const p = (args as { path?: unknown }).path;
  return typeof p === "string" ? p : undefined;
}

export function buildExchanges(items: TimelineItem[], runActive: boolean): Exchange[] {
  const out: Exchange[] = [];
  let current: Exchange | undefined;

  for (const [timelineOrder, item] of items.entries()) {
    if (item.kind === "user") {
      if (item.steering && current) {
        (current.steering ??= []).push({ ...item, afterTurnId: current.turns[current.turns.length - 1]?.id, timelineOrder });
        continue;
      }
      current = { id: item.id, user: item, turns: [], notices: [], stats: { calls: 0, failed: 0, files: 0, tools: {} }, finished: false };
      out.push(current);
      continue;
    }
    if (item.kind === "notice") {
      // A nudge with no exchange yet rides the synthetic head, the same place
      // orphan turns go: a restored session can open on either.
      if (!current) {
        current = { id: "head", turns: [], notices: [], stats: { calls: 0, failed: 0, files: 0, tools: {} }, finished: false };
        out.push(current);
      }
      current.notices.push({ ...item, afterTurnId: current.turns[current.turns.length - 1]?.id, timelineOrder });
      continue;
    }
    if (!current) {
      // Turns before any user message: a restored session can open mid-run.
      current = { id: "head", turns: [], notices: [], stats: { calls: 0, failed: 0, files: 0, tools: {} }, finished: false };
      out.push(current);
    }
    current.turns.push(item);
  }

  for (const ex of out) {
    const files = new Set<string>();
    for (const turn of ex.turns) {
      for (const call of turn.calls) {
        ex.stats.calls++;
        ex.stats.tools[call.name] = (ex.stats.tools[call.name] ?? 0) + 1;
        if (call.result?.is_error) ex.stats.failed++;
        if (call.name === "edit" || call.name === "write") {
          const p = pathOf(call.args);
          if (p) files.add(p);
        }
      }
      if (turn.text.trim() !== "") ex.answer = turn;
      if (turn.ts !== undefined) ex.endedAt = turn.ts;
    }
    ex.stats.files = files.size;
    ex.startedAt = ex.user?.ts ?? ex.turns.find((t) => t.ts !== undefined)?.ts;
  }

  // Only the last exchange can still be running; every earlier one is sealed
  // by the question that followed it.
  for (let i = 0; i < out.length; i++) {
    out[i].finished = i < out.length - 1 || !runActive;
  }
  return out;
}

/** foldable is the one rule the view needs: finished, and with a conclusion
 * that stays outside the fold. */
export function foldable(ex: Exchange): boolean {
  return ex.finished && ex.answer !== undefined && ex.turns.length > 0;
}
