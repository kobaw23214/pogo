import { describe, expect, it } from "vitest";
import type { Exchange } from "./exchanges";
import type { TimelineCall, TurnItem } from "./timeline";
import { activityItemNeedsAttention, activityRunsDisclosureKeys, buildActivityRuns, type ActivityRun } from "./activityRuns";

const call = (id: string, name = "read", args: unknown = { path: `${id}.txt` }): TimelineCall => ({ callId: id, name, args, running: false, orphaned: false });
const turn = (id: string, calls: TimelineCall[] = [], extra: Partial<TurnItem> = {}): TurnItem => ({ kind: "turn", id, index: 1, thinking: "", text: "", calls, streaming: false, ...extra });
const exchange = (turns: TurnItem[], extra: Partial<Exchange> = {}): Exchange => ({ id: "u1", turns, notices: [], finished: false, stats: { calls: 0, failed: 0, files: 0, tools: {} }, ...extra });
const runs = (ex: Exchange) => buildActivityRuns(ex).filter((node): node is ActivityRun => node.kind === "run");

describe("cross-turn activity runs", () => {
  it("joins ordinary calls across model turns and preserves the first item's identity", () => {
    const a = call("a"), ex = exchange([turn("m1", [a])]);
    const first = runs(ex)[0];
    ex.turns.push(turn("m2", [call("b", "bash", { command: "pwd" })]));
    const next = runs(ex);
    expect(next).toHaveLength(1); expect(next[0].id).toBe(first.id);
    expect(next[0].items[0].id).toBe(first.items[0].id);
    expect(next[0].calls).toEqual([a, ex.turns[1].calls[0]]);
    expect(next[0].turnIds).toEqual(["m1", "m2"]);
    expect(buildActivityRuns(ex, { crossTurns: false }).filter(node => node.kind === "run")).toHaveLength(2);
  });

  it("never consumes short findings, punctuation or growing prose into a run", () => {
    const ex = exchange([turn("m1", [call("a")]), turn("m2", [call("b")], { text: "已找到原因：旧配置仍在生效。", streaming: true })]);
    const before = buildActivityRuns(ex);
    expect(before.map(node => node.kind)).toEqual(["run", "prose", "run"]);
    for (const text of ["a".repeat(39), "a".repeat(41) + ":", "a".repeat(81), "first\nsecond"]) {
      ex.turns[1].text = text;
      expect(buildActivityRuns(ex).map(node => node.id)).toEqual(before.map(node => node.id));
    }
  });

  it("closes a thinking run at the first prose without moving existing thinking", () => {
    const t = turn("m1", [], { thinking: "considering", streaming: true }), ex = exchange([t]);
    const initial = runs(ex)[0];
    t.text = "I will read the file";
    t.calls.push(call("a"));
    const next = buildActivityRuns(ex);
    expect(next.map(node => node.kind)).toEqual(["run", "prose", "run"]);
    expect(next[0].id).toBe(initial.id);
    expect((next[0] as ActivityRun).closed).toBe(true);
    expect((next[0] as ActivityRun).items[0].id).toBe(initial.items[0].id);
  });

  it("does not close a run just because all tool results have arrived", () => {
    const a = call("a"), b = call("b"), ex = exchange([turn("m1", [a, b])]);
    a.running = b.running = true;
    const before = runs(ex)[0];
    b.running = false; b.result = { call_id: "b", text: "done" };
    a.running = false; a.result = { call_id: "a", text: "failed", is_error: true };
    const after = runs(ex)[0];
    expect(after.closed).toBe(false); expect(after.id).toBe(before.id);
    expect(after.calls.map(call => call.callId)).toEqual(["a", "b"]);
    ex.finished = true; expect(runs(ex)[0].closed).toBe(true);
  });

  it("keeps cards separate without result-driven membership", () => {
    const todo = call("todo", "todo"), delegate = call("child", "subagent");
    const ex = exchange([turn("m1", [call("a"), todo, call("b"), delegate, call("c")])]);
    const before = buildActivityRuns(ex);
    expect(before.map(node => node.kind)).toEqual(["run", "card", "run", "card", "run"]);
    todo.result = { call_id: "todo", text: "failed", is_error: true };
    delegate.gate = { gate_id: "gate", call_id: "child", tool: "subagent", deadline: 1000 };
    expect(buildActivityRuns(ex).map(node => [node.kind, node.id])).toEqual(before.map(node => [node.kind, node.id]));
  });

  it("uses a fixed neutral host for parameter-dependent preview and mutation", () => {
    const t = turn("m1", [call("a")], { incoming: [{ call_id: "merge", name: "shortcut_merge", head: '{"apply":', bytes: 10 }] });
    const ex = exchange([t]);
    const pending = buildActivityRuns(ex)[1];
    expect(pending.kind).toBe("card");
    expect(pending.kind === "card" && pending.independent).toBeUndefined();
    for (const apply of [false, true]) {
      t.incoming = []; t.calls = [call("a"), call("merge", "shortcut_merge", { apply })];
      const settled = buildActivityRuns(ex)[1];
      expect(settled.kind).toBe("card"); expect(settled.id).toBe(pending.id);
      expect(settled.kind === "card" && settled.independent).toBe(apply);
    }
  });

  it("keeps an unknown-name placeholder in its reserved host after its name arrives", () => {
    const t = turn("m1", [call("a")], { incoming: [{ call_id: "incoming", name: "", bytes: 0 }] });
    const ex = exchange([t]), reservedCallIds = new Set(["incoming"]);
    const pending = buildActivityRuns(ex, { reservedCallIds });
    t.incoming = []; t.calls.push(call("incoming"));
    expect(buildActivityRuns(ex, { reservedCallIds }).map(node => node.id)).toEqual(pending.map(node => node.id));
  });

  it("shares the same slot across arguments, executing call and settled turn id", () => {
    const t = turn("live", [], { incoming: [{ call_id: "a", name: "read", bytes: 10 }] });
    const ex = exchange([t]), before = runs(ex)[0];
    t.calls = [call("a")]; t.calls[0].running = true;
    expect(runs(ex)[0].items).toHaveLength(1); // duplicate snapshot placeholder
    t.incoming = []; t.id = "m1"; t.calls[0].running = false; t.calls[0].result = { call_id: "a", text: "file" };
    const after = runs(ex)[0];
    expect(after.id).toBe(before.id); expect(after.items[0].id).toBe(before.items[0].id);
  });

  it("keeps anchored steering and visible notices ordered without counting hidden nudges", () => {
    const ex = exchange([turn("m1", [call("a")]), turn("m2", [call("b")])]);
    ex.notices = [{ kind: "notice", id: "nudge", text: "[pogo] check your remaining work", afterTurnId: "m1", timelineOrder: 1 }];
    expect(runs(ex)).toHaveLength(1);
    ex.notices.push({ kind: "notice", id: "check", text: "[pogo] citation check: review", afterTurnId: "m1", timelineOrder: 3 });
    ex.steering = [{ kind: "user", id: "s1", text: "follow up", afterTurnId: "m1", timelineOrder: 2 }];
    expect(buildActivityRuns(ex).map(node => node.id)).toEqual(["run:call:a", "steering:s1", "notice:check", "run:call:b"]);
  });

  it("exposes incoming, active tools and background waiting without treating finished work as live", () => {
    const incoming = { kind: "call" as const, id: "call:a", turn: turn("m"), incoming: { call_id: "a", name: "write", bytes: 4 } };
    expect(activityItemNeedsAttention(incoming, true)).toBe(true);
    const active = call("a"); active.running = true;
    expect(activityItemNeedsAttention({ ...incoming, incoming: undefined, call: active }, true)).toBe(true);
    active.running = false; active.result = { call_id: "a", text: "failed", is_error: true };
    expect(activityItemNeedsAttention({ ...incoming, incoming: undefined, call: active }, true)).toBe(false);
    expect(activityRunsDisclosureKeys(exchange([turn("m", [active])]))).toEqual(["row:u1:run:call:a", "call:a:details"]);
  });
});
