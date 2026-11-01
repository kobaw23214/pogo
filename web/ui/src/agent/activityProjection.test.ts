import { describe, expect, it } from "vitest";
import type { Exchange } from "./exchanges";
import type { TimelineCall, TurnItem } from "./timeline";
import type { CiteSource } from "@/api/types";
import { activityCallNeedsAttention, activityDisclosureKeys, buildActivityGroups, buildActivityProjection } from "./activityProjection";

const call = (id: string, name = "read"): TimelineCall => ({ callId: id, name: name as TimelineCall["name"], args: { path: `${id}.txt` }, running: false, orphaned: false });
const turn = (id: string, calls: TimelineCall[]): TurnItem => ({ kind: "turn", id, index: 1, thinking: "reasoning", text: "answer", calls, streaming: false });
const exchange = (turns: TurnItem[]): Exchange => ({ id: "u1", turns, notices: [], finished: false, stats: { calls: 0, files: 0, failed: 0, tools: {} } });

describe("shared activity projection", () => {
  it("groups only consecutive built-in exploration within the same turn", () => {
    const first = turn("m1", [call("a"), call("b", "ls"), call("c", "bash"), call("d", "grep"), call("e", "mcp_read"), call("f", "skill_read"), call("g", "browser"), call("h", "docs_office"), call("i", "subagent"), call("j", "write"), call("k", "read")]);
    const groups = buildActivityGroups(first);
    expect(groups.map(group => group.calls.map(call => call.callId))).toEqual([["a", "b"], ["c"], ["d"], ["e"], ["f"], ["g"], ["h"], ["i"], ["j"], ["k"]]);
    const projected = buildActivityProjection(exchange([first, turn("m2", [call("l")])]));
    expect(projected.filter(node => node.kind === "turn").map(node => node.groups.length)).toEqual([10, 1]);
  });

  it("keeps a first call's group identity as it grows, fails, receives approval and settles", () => {
    const first = call("a"); const current = turn("m1", [first]);
    const id = buildActivityGroups(current)[0].id;
    current.calls.push(call("b", "grep"));
    first.running = true;
    expect(buildActivityGroups(current)[0].id).toBe(id);
    first.running = false; first.result = { call_id: "a", text: "failed", is_error: true, source: { label: "S1" } as CiteSource };
    current.calls[1].gate = { gate_id: "g", call_id: "b", tool: "grep", deadline: 1000 };
    const after = buildActivityGroups(current);
    expect(after).toHaveLength(1); expect(after[0].id).toBe(id); expect(after[0].citeLabels).toEqual(["S1"]);
    expect(activityCallNeedsAttention(first)).toBe(true); expect(activityCallNeedsAttention(current.calls[1])).toBe(true);
    expect(activityCallNeedsAttention(call("settled"))).toBe(false);
  });

  it("keeps steering and notices at their known anchors and original mixed order", () => {
    const ex = exchange([turn("m1", [call("a")]), turn("m2", [call("b")])]);
    ex.notices = [{ kind: "notice", id: "old", text: "old fixture" }, { kind: "notice", id: "n1", text: "notice", afterTurnId: "m1", timelineOrder: 3 }];
    ex.steering = [{ kind: "user", id: "s1", text: "follow up", afterTurnId: "m1", timelineOrder: 2 }];
    expect(buildActivityProjection(ex).map(node => node.id)).toEqual(["notice:old", "turn:m1", "steering:s1", "notice:n1", "turn:m2"]);
    expect(buildActivityProjection(ex).filter(node => node.kind === "turn").map(node => node.turn.text)).toEqual(["answer", "answer"]);
  });

  it("exports new and legacy disclosure identities without content or state in the keys", () => {
    expect(activityDisclosureKeys(exchange([turn("m1", [call("a"), call("b")])]))).toEqual(expect.arrayContaining(["m1:thinking", "call:a:details", "call:a:subagent:result", "call:b:details", "row:u1:activity:m1:a", "row:u1:a:explore"]));
  });

  it("keeps child approvals and an unconfirmed background acknowledgement visible", () => {
    const dispatched = call("delegate", "subagent");
    dispatched.result = { call_id: "delegate", text: "ACK", details: { id: "bg1", mode: "explore", background: true } };
    expect(activityCallNeedsAttention(dispatched)).toBe(true);
    dispatched.taskNotice = { id: "bg1", brief: "review", failed: false, result: "finished" };
    expect(activityCallNeedsAttention(dispatched)).toBe(false);
    dispatched.childGates = [{ gate_id: "child-gate", call_id: "nested", tool: "read", deadline: 1000, origin: "bg1" }];
    expect(activityCallNeedsAttention(dispatched)).toBe(true);
    expect(buildActivityGroups(turn("m1", [dispatched]))[0].id).toBe("activity:m1:delegate");
  });

  it("keeps a finished execution visible until its matching final response arrives", () => {
    const background = call("delegate", "subagent");
    background.result = { call_id: "delegate", text: "dispatch acknowledgement", details: { id: "bg1", mode: "explore", background: true } };
    background.bgTask = { task_id: "bg1", task: "review", mode: "explore", status: "done", started_at: 1 };
    expect(activityCallNeedsAttention(background)).toBe(true);
    background.taskNotice = { id: "bg1", brief: "review", failed: false, result: "final response" };
    expect(activityCallNeedsAttention(background)).toBe(false);
    const foreground = call("foreground", "subagent");
    foreground.liveFrames = [{ type: "run_end" }];
    expect(activityCallNeedsAttention(foreground)).toBe(true);
    foreground.result = { call_id: "foreground", text: "final response" };
    expect(activityCallNeedsAttention(foreground)).toBe(false);
  });

  it("lets a finished process fold settled failures and incomplete history while retaining real current work", () => {
    const failed = call("failed"); failed.result = { call_id: "failed", text: "failure", is_error: true };
    const orphan = call("orphan"); orphan.orphaned = true;
    expect(activityCallNeedsAttention(failed, true)).toBe(false);
    expect(activityCallNeedsAttention(orphan, true)).toBe(false);
    const running = call("running"); running.running = true;
    expect(activityCallNeedsAttention(running, true)).toBe(true);
    const gate = call("gate"); gate.childGates = [{ gate_id: "g1", call_id: "nested", tool: "write", deadline: 1000, origin: "bg1" }];
    expect(activityCallNeedsAttention(gate, true)).toBe(true);
    const awaiting = call("awaiting", "subagent"); awaiting.bgTask = { task_id: "bg1", task: "review", mode: "explore", status: "done", started_at: 1 };
    expect(activityCallNeedsAttention(awaiting, true)).toBe(true);
    awaiting.taskNotice = { id: "bg1", brief: "review", failed: false, result: "final response" };
    expect(activityCallNeedsAttention(awaiting, true)).toBe(false);
  });
});
