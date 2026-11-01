import { describe, expect, it } from "vitest";
import { emptyLive, type CiteSource, type Live, type Message, type ToolResult } from "@/api/types";
import { buildTimeline, type TurnItem } from "./timeline";

const question = (id: string): Message => ({ id, role: "user", content: [{ type: "text", text: id }] });
const answer = (id: string, text: string): Message => ({
  id, role: "assistant", content: [{ type: "text", text }],
});
const read = (id: string, callId: string): Message => ({
  id, role: "assistant", content: [{ type: "tool_use", id: callId, name: "read", input: { path: `${callId}.txt` } }],
});
const stamped = (callId: string, label: string): ToolResult => ({
  call_id: callId,
  text: `Evidence from ${callId}`,
  source: {
    label, type: "file_read", title: `${callId}.txt`, locator: `${callId}.txt`,
    call_id: callId, excerpt: `Evidence from ${callId}`, hash: callId, ts: 1,
  },
});
const turns = (messages: Message[], results: Record<string, ToolResult>, live: Live = emptyLive()): TurnItem[] =>
  buildTimeline(messages, results, live).filter((item): item is TurnItem => item.kind === "turn");
const labels = (sources: CiteSource[] | undefined) => sources?.map((source) => source.label);
const last = (items: TurnItem[]) => items[items.length - 1];

describe("buildTimeline citation scope", () => {
  it("carries completed tool sources through a later text-only final answer", () => {
    const projected = turns([
      question("u1"), read("m1", "c1"), read("m2", "c2"),
      answer("m3", "The report contains this finding. [1](#cite:S3)"),
    ], { c1: stamped("c1", "S3"), c2: stamped("c2", "S10") });

    expect(labels(projected[0].sources)).toEqual(["S3"]);
    expect(labels(projected[1].sources)).toEqual(["S3", "S10"]);
    expect(labels(projected[2].sources)).toEqual(["S3", "S10"]);
    expect(projected[2].calls).toEqual([]);
    expect(projected[2].sources?.[0]).toMatchObject({ call_id: "c1", excerpt: "Evidence from c1" });
  });

  it("makes completed sources available to a streaming final answer", () => {
    const projected = turns([question("u1"), read("m1", "c1")], { c1: stamped("c1", "S3") }, {
      ...emptyLive(), active: true, message_id: "m2", text: "A cited finding [1](#cite:S3)",
    });
    expect(projected[1].streaming).toBe(true);
    expect(labels(projected[1].sources)).toEqual(["S3"]);
  });

  it("reuses only explicitly cited sources from an earlier question, excluding code examples", () => {
    const projected = turns([
      question("u1"), read("m1", "c1"), read("m2", "c2"), answer("m3", "First answer."),
      question("u2"), read("m4", "c3"),
      answer("m5", "Earlier evidence [1](#cite:S2). Example: `[1](#cite:S1)`\n```md\n[1](#cite:S1)\n```"),
    ], { c1: stamped("c1", "S1"), c2: stamped("c2", "S2"), c3: stamped("c3", "S3") });
    expect(labels(projected[3].sources)).toEqual(["S3"]);
    expect(labels(projected[4].sources)).toEqual(["S2", "S3"]);
  });

  it("resets available sources at a new question while keeping cited history reachable during streaming", () => {
    const messages = [question("u1"), read("m1", "c1"), answer("m2", "Done."), question("u2")];
    const results = { c1: stamped("c1", "S1") };
    const live = { ...emptyLive(), active: true, message_id: "m3", text: "Thinking about the next question." };
    expect(last(turns(messages, results, live))?.sources).toEqual([]);
    expect(labels(last(turns(messages, results, { ...live, text: "As above [1](#cite:S1)" }))?.sources)).toEqual(["S1"]);
  });

  it("does not resolve an earlier answer against a later tool result already in the result table", () => {
    const projected = turns([
      question("u1"), answer("m1", "Premature citation [1](#cite:S2)"),
      read("m2", "c2"), answer("m3", "Now available [1](#cite:S2)"),
      question("u2"), read("m4", "c3"),
    ], { c2: stamped("c2", "S2"), c3: stamped("c3", "S3") });
    expect(projected[0].sources).toEqual([]);
    expect(labels(projected[2].sources)).toEqual(["S2"]);
    expect(labels(projected[3].sources)).toEqual(["S3"]);
  });

  it("does not surface orphan or rewound-branch result entries even when their labels are cited", () => {
    const projected = turns([
      question("u1"), read("m1", "c1"), answer("m2", "Unknown branch [1](#cite:S9)"),
    ], { c1: stamped("c1", "S1"), foreign: stamped("foreign", "S9") }, {
      ...emptyLive(), active: true, text: "Still not reachable [1](#cite:S9)",
    });
    for (const turn of projected) expect(labels(turn.sources)).toEqual(["S1"]);
  });

  it("keeps sources across an in-run harness notice", () => {
    const projected = turns([
      question("u1"), read("m1", "c1"),
      { id: "notice", role: "user", content: [{ type: "text", text: "[pogo] Continue the current run." }] },
      answer("m2", "Done."),
    ], { c1: stamped("c1", "S1") });
    expect(labels(projected[1].sources)).toEqual(["S1"]);
    expect(projected[1].index).toBe(2);
  });

  it("returns explicit empty source context for pending, unstamped, and synthetic live turns", () => {
    expect(turns([question("u1"), answer("m1", "Hello")], {})[0].sources).toEqual([]);
    expect(turns([question("u1"), read("m1", "c1")], { c1: { call_id: "c1", text: "No stamp" } })[0].sources).toEqual([]);
    expect(last(turns([question("u1"), read("m1", "c1")], {}, {
      ...emptyLive(), active: true, text: "Waiting [1](#cite:S1)",
      pending_tools: [{ call_id: "c1", name: "read", started_at: 1 }],
    }))?.sources).toEqual([]);
    expect(turns([question("u1")], {}, {
      ...emptyLive(), active: true,
      incoming: [{ call_id: "c1", name: "read", head: "{}", bytes: 2 }],
    })[0].sources).toEqual([]);
    expect(turns([question("u1")], {}, {
      ...emptyLive(), active: true,
      pending_gates: [{ gate_id: "g1", call_id: "c1", tool: "read", deadline: 1 }],
    })[0].sources).toEqual([]);
  });

  it("keeps source snapshots independent and leaves all input state unchanged", () => {
    const messages = [question("u1"), read("m1", "c1"), answer("m2", "Done.")];
    const results = { c1: stamped("c1", "S1") };
    const live = { ...emptyLive(), active: true, text: "More detail." };
    const before = structuredClone({ messages, results, live });
    Object.freeze(results.c1.source);
    Object.freeze(results.c1);
    const projected = turns(messages, results, live);
    expect({ messages, results, live }).toEqual(before);
    expect(projected[0].sources).not.toBe(projected[1].sources);
    expect(projected[1].sources).not.toBe(projected[2].sources);
    projected[2].sources?.pop();
    expect(labels(projected[0].sources)).toEqual(["S1"]);
    expect(labels(projected[1].sources)).toEqual(["S1"]);
    expect(labels(turns(messages, results, live)[2].sources)).toEqual(["S1"]);
  });
});
