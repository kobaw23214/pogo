import { describe, expect, it } from "vitest";
import { emptyLive, type Live, type RunInfo } from "@/api/types";
import type { Exchange } from "./exchanges";
import { createActivityExecutionLedger } from "./activityExecution";

const exchange = (id = "u1", callId = "a", runId?: string): Exchange => ({
  id, user: { id, kind: "user", text: "task", ...(runId ? { runId } : {}) }, finished: false,
  notices: [], stats: { calls: 1, failed: 0, files: 0, tools: {} },
  turns: [{ kind: "turn", id: "m1", index: 1, text: "", thinking: "", streaming: false,
    calls: [{ callId, name: "read", args: { path: "a.txt" }, running: true, orphaned: false }] }],
});
const live = (runId = "r1", callId = "a"): Live => ({ ...emptyLive(), active: true, run_id: runId, pending_tools: [{ call_id: callId, name: "read", started_at: 10 }] });
const run = (runId = "r1", extra: Partial<RunInfo> = {}): RunInfo => ({ active: true, run_id: runId, ...extra });

describe("activity execution evidence ledger", () => {
  it("records only live pending calls with matching run ids and a unique exchange", () => {
    const ledger = createActivityExecutionLedger(), ex = exchange();
    const input = { sessionId: "s1", exchanges: [ex], live: live(), runInfo: run() };
    const first = ledger.update(input);
    expect([...first.callOrigins]).toEqual([["a", { sessionId: "s1", exchangeId: "u1", runId: "r1" }]]);
    expect(ledger.update(input)).toEqual(first);
    ex.turns[0].calls.push({ ...ex.turns[0].calls[0], callId: "historical", running: false, result: { call_id: "historical", text: "ok" } });
    expect(ledger.update(input).callOrigins.has("historical")).toBe(false);
  });

  it("does not guess the last exchange or join mismatched transition snapshots", () => {
    const ledger = createActivityExecutionLedger();
    expect(ledger.update({ sessionId: "s", exchanges: [exchange()], live: live("old"), runInfo: run("new") }).callOrigins.size).toBe(0);
    expect(ledger.update({ sessionId: "s", exchanges: [exchange()], live: emptyLive(), runInfo: run("r", { active: false, end_reason: "completed" }) }).endReasons.size).toBe(0);
    expect(ledger.update({ exchanges: [exchange()], live: live(), runInfo: run() }).callOrigins.size).toBe(0);
  });

  it("associates an observed run's terminal reason after its pending call disappears", () => {
    const ledger = createActivityExecutionLedger(), ex = exchange();
    ledger.update({ sessionId: "s", exchanges: [ex], live: live(), runInfo: run() });
    const done = ledger.update({ sessionId: "s", exchanges: [ex], live: emptyLive(), runInfo: run("r1", { active: false, end_reason: "transport_error" }) });
    expect(done.endReasons.get("u1")).toBe("transport_error");
    expect(done.callOrigins.get("a")?.runId).toBe("r1");
  });

  it("can restore the latest terminal reason from an explicit opening message run id", () => {
    const result = createActivityExecutionLedger().update({ sessionId: "s", exchanges: [exchange("u1", "a", "r1"), exchange("u2", "b")], live: emptyLive(), runInfo: run("r1", { active: false, end_reason: "waiting_human" }) });
    expect([...result.endReasons]).toEqual([["u1", "waiting_human"]]);
    expect(result.callOrigins.size).toBe(0);
  });

  it("clears a known old reason when a new actual run is observed in the same exchange", () => {
    const ledger = createActivityExecutionLedger(), ex = exchange();
    ledger.update({ sessionId: "s", exchanges: [ex], live: live(), runInfo: run() });
    ledger.update({ sessionId: "s", exchanges: [ex], live: emptyLive(), runInfo: run("r1", { active: false, end_reason: "aborted" }) });
    ex.turns[0].calls.push({ ...ex.turns[0].calls[0], callId: "b" });
    const next = ledger.update({ sessionId: "s", exchanges: [ex], live: live("r2", "b"), runInfo: run("r2") });
    expect(next.endReasons.has("u1")).toBe(false);
    expect(next.callOrigins.get("a")?.runId).toBe("r1"); expect(next.callOrigins.get("b")?.runId).toBe("r2");
    const late = ledger.update({ sessionId: "s", exchanges: [ex], live: emptyLive(), runInfo: run("r1", { active: false, end_reason: "completed" }) });
    expect(late.endReasons.has("u1")).toBe(false);
    const done = ledger.update({ sessionId: "s", exchanges: [ex], live: emptyLive(), runInfo: run("r2", { active: false, end_reason: "completed" }) });
    expect(done.endReasons.get("u1")).toBe("completed");
  });

  it("does not let an unfamiliar late terminal replace a newer observed run", () => {
    const ledger = createActivityExecutionLedger(), ex = exchange("u1", "a", "r0");
    // Opening run id differs from the current resumed run; the pending call
    // proves r2 belongs to the same exchange, without changing old history.
    ledger.update({ sessionId: "s", exchanges: [ex], live: live("r2"), runInfo: run("r2") });
    const late = ledger.update({ sessionId: "s", exchanges: [ex], live: emptyLive(), runInfo: run("r0", { active: false, end_reason: "aborted" }) });
    expect(late.endReasons.size).toBe(0);
  });

  it("isolates sessions and only restores observed page-lifetime facts", () => {
    const ledger = createActivityExecutionLedger(), ex = exchange();
    ledger.update({ sessionId: "one", exchanges: [ex], live: live(), runInfo: run() });
    ledger.update({ sessionId: "one", exchanges: [ex], live: emptyLive(), runInfo: run("r1", { active: false, end_reason: "aborted" }) });
    expect(ledger.update({ sessionId: "two", exchanges: [ex], live: emptyLive(), runInfo: run("r1", { active: false, end_reason: "aborted" }) }).endReasons.size).toBe(0);
    expect(ledger.update({ sessionId: "one", exchanges: [ex], live: emptyLive(), runInfo: run("r1", { active: false, end_reason: "aborted" }) }).endReasons.get("u1")).toBe("aborted");
    ledger.clear("one");
    expect(ledger.update({ sessionId: "one", exchanges: [ex], live: emptyLive(), runInfo: run("r1", { active: false, end_reason: "aborted" }) }).endReasons.size).toBe(0);
  });

  it("rejects ambiguous calls or a run claimed by two different exchanges", () => {
    const ledger = createActivityExecutionLedger();
    const duplicate = ledger.update({ sessionId: "s", exchanges: [exchange("u1"), exchange("u2")], live: live(), runInfo: run() });
    expect(duplicate.callOrigins.size).toBe(0);
    const conflict = ledger.update({ sessionId: "s2", exchanges: [exchange("u1", "a", "r1"), exchange("u2", "b", "r1")], live: live(), runInfo: run() });
    expect(conflict.callOrigins.size).toBe(0); expect(conflict.endReasons.size).toBe(0);
  });

  it("removes a call origin when a later snapshot duplicates the call across exchanges", () => {
    const ledger = createActivityExecutionLedger();
    expect(ledger.update({ sessionId: "s", exchanges: [exchange()], live: live(), runInfo: run() }).callOrigins.size).toBe(1);
    expect(ledger.update({ sessionId: "s", exchanges: [exchange(), exchange("u2")], live: live(), runInfo: run() }).callOrigins.size).toBe(0);
  });

  it("revokes previously recorded associations if contradictory identity is later observed", () => {
    const ledger = createActivityExecutionLedger(), ex = exchange();
    ledger.update({ sessionId: "s", exchanges: [ex], live: live(), runInfo: run() });
    const conflict = ledger.update({ sessionId: "s", exchanges: [ex, exchange("u2", "b", "r1")], live: live(), runInfo: run() });
    expect(conflict.callOrigins.size).toBe(0);
    const done = ledger.update({ sessionId: "s", exchanges: [ex], live: emptyLive(), runInfo: run("r1", { active: false, end_reason: "completed" }) });
    expect(done.endReasons.size).toBe(0);
  });

  it("does not show unknown reason strings or an active run's stale terminal reason", () => {
    const ledger = createActivityExecutionLedger(), ex = exchange("u", "a", "r1");
    expect(ledger.update({ sessionId: "s", exchanges: [ex], live: live(), runInfo: run("r1", { end_reason: "aborted" }) }).endReasons.size).toBe(0);
    expect(ledger.update({ sessionId: "s", exchanges: [ex], live: emptyLive(), runInfo: run("r1", { active: false, end_reason: "not-a-real-reason" }) }).endReasons.size).toBe(0);
  });
});
