import type { Live, RunInfo } from "@/api/types";
import type { Exchange } from "./exchanges";
import type { RetryIdentity } from "./runSummary";

export interface ActivityExecutionInput {
  sessionId?: string;
  exchanges: readonly Exchange[];
  live: Live;
  runInfo: RunInfo;
}
export interface ActivityExecutionSnapshot {
  callOrigins: ReadonlyMap<string, RetryIdentity>;
  endReasons: ReadonlyMap<string, string>;
}
interface ObservedRun { exchangeId?: string; order: number; ambiguous?: boolean }
interface SessionLedger {
  runs: Map<string, ObservedRun>;
  latest: Map<string, string>;
  callOrigins: Map<string, RetryIdentity>;
  ambiguousCalls: Set<string>;
  endReasons: Map<string, string>;
  order: number;
}
const TERMINAL_REASONS = new Set(["completed", "waiting_human", "aborted", "max_tokens", "turn_limit", "time_budget", "token_budget", "cost_budget", "context_overflow", "transport_error", "tool_protocol", "stagnation", "not_started"]);
export function isRecordedActivityEndReason(value: unknown): value is string {
  return typeof value === "string" && TERMINAL_REASONS.has(value);
}
const nonempty = (value: string | undefined): value is string => typeof value === "string" && value.length > 0;
const createSession = (): SessionLedger => ({ runs: new Map(), latest: new Map(), callOrigins: new Map(), ambiguousCalls: new Set(), endReasons: new Map(), order: 0 });
const snapshot = (state?: SessionLedger): ActivityExecutionSnapshot => ({
  callOrigins: new Map([...state?.callOrigins ?? []].map(([id, origin]) => [id, { ...origin }])),
  endReasons: new Map(state?.endReasons),
});

/** A page-lifetime ledger of observed execution identities. It does not write
 * into transcripts or infer run membership from "the last exchange", timing,
 * text or a result that happened to arrive nearby. A fresh instance restores
 * only facts contained in its new snapshot. */
export function createActivityExecutionLedger() {
  const sessions = new Map<string, SessionLedger>();
  return {
    update(input: ActivityExecutionInput): ActivityExecutionSnapshot {
      const { sessionId, exchanges, live, runInfo } = input;
      if (!nonempty(sessionId)) return snapshot();
      let state = sessions.get(sessionId);
      if (!state) sessions.set(sessionId, state = createSession());
      // A transition can briefly expose the previous live/run pair. Waiting
      // for matching ids is safer than joining calls to either side by timing.
      if (nonempty(live.run_id) && nonempty(runInfo.run_id) && live.run_id !== runInfo.run_id) return snapshot(state);
      const runId = runInfo.run_id || live.run_id;
      if (!nonempty(runId)) return snapshot(state);
      const active = runInfo.active && live.active;
      let run = state.runs.get(runId);
      if (!run) { run = { order: active ? ++state.order : 0 }; state.runs.set(runId, run); }
      else if (active && !run.order) run.order = ++state.order;

      const locations = new Map<string, Set<string>>();
      for (const ex of exchanges) for (const call of ex.turns.flatMap(turn => turn.calls)) {
        let owners = locations.get(call.callId);
        if (!owners) locations.set(call.callId, owners = new Set());
        owners.add(ex.id);
      }
      const candidates = new Set(exchanges.filter(ex => ex.user?.runId === runId).map(ex => ex.id));
      if (run.exchangeId) candidates.add(run.exchangeId);
      // Pending tools belong to this exact live run; settled historical calls
      // alone cannot prove which of several runs inside an exchange owned them.
      if (active) for (const pending of live.pending_tools) {
        const owners = locations.get(pending.call_id);
        if (owners?.size === 1) candidates.add([...owners][0]);
        else if (owners && owners.size > 1) {
          state.ambiguousCalls.add(pending.call_id);
          state.callOrigins.delete(pending.call_id);
        }
      }
      if (candidates.size > 1) run.ambiguous = true;
      if (run.ambiguous) {
        for (const [id, origin] of state.callOrigins) if (origin.runId === runId) state.callOrigins.delete(id);
        for (const [id, latest] of state.latest) if (latest === runId) { state.latest.delete(id); state.endReasons.delete(id); }
        return snapshot(state);
      }
      const exchangeId = [...candidates][0];
      if (!exchangeId) return snapshot(state);
      run.exchangeId = exchangeId;

      const latestId = state.latest.get(exchangeId), latest = latestId && state.runs.get(latestId);
      // Active observation establishes relative order. An unfamiliar late
      // terminal event cannot replace an already observed newer run.
      if (!latestId || latestId === runId || active && run.order > (latest && latest.order || 0)) {
        state.latest.set(exchangeId, runId);
        if (active || latestId !== runId) state.endReasons.delete(exchangeId);
      }
      if (active) for (const pending of live.pending_tools) {
        const id = pending.call_id, owners = locations.get(id);
        if (!owners || owners.size !== 1 || !owners.has(exchangeId) || state.ambiguousCalls.has(id)) continue;
        const previous = state.callOrigins.get(id);
        if (previous && (previous.exchangeId !== exchangeId || previous.runId !== runId)) {
          state.callOrigins.delete(id); state.ambiguousCalls.add(id); continue;
        }
        state.callOrigins.set(id, { sessionId, exchangeId, runId });
      }
      if (!runInfo.active && !live.active && runInfo.run_id === runId && state.latest.get(exchangeId) === runId
        && isRecordedActivityEndReason(runInfo.end_reason)) state.endReasons.set(exchangeId, runInfo.end_reason);
      return snapshot(state);
    },
    clear(sessionId?: string): void {
      if (sessionId === undefined) sessions.clear(); else sessions.delete(sessionId);
    },
  };
}
