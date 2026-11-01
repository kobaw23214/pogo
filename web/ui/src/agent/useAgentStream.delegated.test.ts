import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type AgentEvent, type ToolName } from "@/api/types";

const mocks = vi.hoisted(() => ({
  delegatedUpdates: vi.fn(),
  sessionAttachments: vi.fn(),
  session: vi.fn(),
  fetch: vi.fn(),
}));

vi.mock("@/api/delegated", () => ({
  isDelegatedEmbed: true,
  delegatedUpdates: mocks.delegatedUpdates,
}));
vi.mock("@/api/client", () => ({
  api: { sessionAttachments: mocks.sessionAttachments, session: mocks.session },
  authHeaders: vi.fn(() => ({})),
  streamURL: vi.fn(() => "/forbidden-legacy-stream"),
}));
vi.mock("@/components/fileTreeStore", () => ({
  flushTreeInvalidate: vi.fn(),
  invalidateIndex: vi.fn(),
  scheduleTreeInvalidate: vi.fn(),
}));
vi.mock("@/components/changesStore", () => ({ bumpWorkspaceChanges: vi.fn() }));
vi.mock("./browserView", () => ({ createBrowserReveal: () => ({ selectSession: vi.fn(), observe: vi.fn() }), browserStreamBacklogThrough: vi.fn() }));
vi.mock("@/i18n", () => ({
  gt: (key: string, params?: Record<string, unknown>) => params ? `${key}:${JSON.stringify(params)}` : key,
}));

import { buildTimeline } from "./timeline";
import { useAgentStream } from "./useAgentStream";

type Batch = { events: AgentEvent[] };
type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason: unknown) => void;
};
type Poll = Deferred<Batch> & { sid: string; from?: number; signal?: AbortSignal };

// The legacy UI union lists built-ins, but Hub events also carry MCP wire names.
const businessToolName = "mcp_yw_echo" as ToolName;

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((accept, fail) => { resolve = accept; reject = fail; });
  return { promise, resolve, reject };
}

function snapshot(seq: number, text = "", messageID = "seed"): AgentEvent {
  return {
    type: "snapshot", seq, ts: Date.now(),
    snapshot: {
      seq,
      messages: text ? [{ id: messageID, role: "user", content: [{ type: "text", text }] }] : [],
      results: {}, live: emptyLive(), run: { active: false, model: "synthetic-model" },
      policy: { mode: "standard" }, usage: { input: 12, output: 3 }, context_tokens: 12,
    },
  };
}

function event(type: AgentEvent["type"], seq: number, fields: Partial<AgentEvent> = {}): AgentEvent {
  return { type, seq, ts: Date.now(), ...fields };
}

function httpError(status: number): Error & { status: number } {
  return Object.assign(new Error(`synthetic HTTP ${status}`), { status });
}

let polls: Poll[];
let streams: ReturnType<typeof useAgentStream>[];

function stream() {
  const result = useAgentStream();
  streams.push(result);
  return result;
}

async function flush() {
  await vi.advanceTimersByTimeAsync(0);
}

function poll(index: number): Poll {
  expect(polls.length, `poll ${index} was not requested`).toBeGreaterThan(index);
  return polls[index]!;
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-09-10T08:00:00Z"));
  vi.clearAllMocks();
  mocks.sessionAttachments.mockResolvedValue([]);
  mocks.session.mockResolvedValue({});
  mocks.fetch.mockRejectedValue(new Error("delegated streams must not call global fetch"));
  vi.stubGlobal("fetch", mocks.fetch);
  polls = [];
  streams = [];
  mocks.delegatedUpdates.mockImplementation((sid: string, from?: number, signal?: AbortSignal) => {
    const pending = { ...deferred<Batch>(), sid, from, signal };
    polls.push(pending);
    // Deliberately ignore abort: an already received response can settle after
    // cancellation. The stream's epoch checks must still reject that response.
    return pending.promise;
  });
});

afterEach(async () => {
  for (const s of streams) s.disconnect();
  for (const pending of polls) pending.resolve({ events: [] });
  await flush();
  expect(mocks.fetch).not.toHaveBeenCalled();
  expect(mocks.sessionAttachments).not.toHaveBeenCalled();
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("delegated Agent Hub updates", () => {
  it("retains an unfinished-run notice through empty successful polls and restores it on snapshot", async () => {
    const s = stream();
    void s.connect("report-session");
    const seed = snapshot(1);
    seed.snapshot!.run.end_reason = "max_tokens";
    poll(0).resolve({ events: [seed] });
    await flush();
    expect(s.streamError.value).toBe("stream.outputLimited");
    poll(1).resolve({ events: [] });
    await flush();
    expect(s.streamError.value).toBe("stream.outputLimited");
    poll(2).resolve({ events: [event("run_start", 2, { run_id: "next" })] });
    await flush();
    expect(s.streamError.value).toBeNull();
  });
  it("folds snapshots, live MCP tool events and the settled answer into the existing timeline", async () => {
    const s = stream();
    void s.connect("alice-session");
    expect(poll(0)).toMatchObject({ sid: "alice-session", from: undefined });
    expect(poll(0).signal).toBeInstanceOf(AbortSignal);
    poll(0).resolve({ events: [snapshot(7)] });
    await flush();
    expect(s.connected.value).toBe(true);
    expect(s.switching.value).toBe(false);
    expect(s.seq.value).toBe(7);
    expect(poll(1)).toMatchObject({ sid: "alice-session", from: 7 });

    poll(1).resolve({ events: [
      event("run_start", 8, { run_id: "run-alice" }),
      event("user_message", 9, { message_id: "question", text: "summarize my assets" }),
      event("turn_start", 10, { turn: 1, message_id: "tool-message" }),
      event("message", 11, { message_id: "tool-message", role: "assistant", content: [
        { type: "tool_use", id: "call-assets", name: businessToolName, input: { user: "alice" } },
      ] }),
      event("tool_start", 12, { call_id: "call-assets", name: businessToolName, args: { user: "alice" } }),
      event("tool_partial", 13, { call_id: "call-assets", text: "loading evidence" }),
    ] });
    await flush();
    expect(s.busy.value).toBe(true);
    expect(s.run.value.run_id).toBe("run-alice");
    expect(s.live.value.pending_tools).toEqual([expect.objectContaining({
      call_id: "call-assets", name: "mcp_yw_echo", output: "loading evidence",
    })]);
    expect(poll(2).from).toBe(13);

    const details = { tool: "echo", structured_content: { actor: "alice", total: 3 } };
    poll(2).resolve({ events: [
      event("tool_end", 14, {
        call_id: "call-assets", name: businessToolName, text: "3 assets", duration_ms: 24,
        // Server-owned MCP details are opaque to the legacy tool detail union.
        details: details as unknown as AgentEvent["details"],
      }),
      event("turn_start", 15, { turn: 2, message_id: "answer" }),
      event("token", 16, { text: "You have " }),
      event("message", 17, { message_id: "answer", role: "assistant", content: [{ type: "text", text: "You have 3 assets." }] }),
      event("run_end", 18, { usage: { input: 50, output: 9 }, stop_reason: "end_turn" }),
    ] });
    await flush();
    expect(s.seq.value).toBe(18);
    expect(s.busy.value).toBe(false);
    expect(s.live.value.pending_tools).toHaveLength(0);
    expect(s.results.value["call-assets"]).toMatchObject({ text: "3 assets", details, duration_ms: 24 });
    expect(s.usage.value).toEqual({ input: 50, output: 9 });
    expect(poll(3).from).toBe(18);

    const timeline = buildTimeline(s.messages.value, s.results.value, s.live.value);
    const calls = timeline.flatMap(item => item.kind === "turn" ? item.calls : []);
    expect(calls).toEqual([expect.objectContaining({
      callId: "call-assets", name: "mcp_yw_echo", running: false,
      result: expect.objectContaining({ text: "3 assets", details }),
    })]);
    expect(timeline.some(item => item.kind === "turn" && item.text === "You have 3 assets.")).toBe(true);
  });

  it("uses sequence zero as a valid resume cursor after receiving a snapshot", async () => {
    const s = stream();
    void s.connect("empty-session");
    poll(0).resolve({ events: [snapshot(0)] });
    await flush();
    expect(poll(1)).toMatchObject({ sid: "empty-session", from: 0 });
    expect(s.switching.value).toBe(false);
  });

  it.each(["success", "failure"] as const)("ignores an old session's late %s and finally without stopping the new watchdog", async outcome => {
    const s = stream();
    const oldConnection = s.connect("old-session");
    const oldPoll = poll(0);
    void s.connect("new-session");
    expect(oldPoll.signal?.aborted).toBe(true);
    expect(poll(1).from).toBeUndefined();
    poll(1).resolve({ events: [snapshot(20, "new conversation", "new-message")] });
    await flush();
    const currentPoll = poll(2);
    expect(currentPoll).toMatchObject({ sid: "new-session", from: 20 });

    if (outcome === "success") oldPoll.resolve({ events: [snapshot(999, "stale private data", "old-message")] });
    else oldPoll.reject(httpError(401));
    await oldConnection;
    await flush();
    expect(s.messages.value.map(message => message.id)).toEqual(["new-message"]);
    expect(s.seq.value).toBe(20);
    expect(s.connected.value).toBe(true);
    expect(s.switching.value).toBe(false);
    expect(s.streamError.value).toBeNull();
    expect(s.outage.value).toBeNull();
    expect(s.unreachable.value).toBe(false);
    expect(polls).toHaveLength(3);

    // A stale finally must not stop the current connection's watchdog. The
    // current request is intentionally pending and has delivered no keepalive.
    await vi.advanceTimersByTimeAsync(60_001);
    expect(currentPoll.signal?.aborted).toBe(true);
    expect(polls).toHaveLength(3);
  });

  it("rejects late results from a replaced connection even when the session ID is unchanged", async () => {
    const s = stream();
    void s.connect("same-session");
    poll(0).resolve({ events: [snapshot(5, "initial", "initial-message")] });
    await flush();
    const superseded = poll(1);
    void s.retryNow();
    expect(superseded.signal?.aborted).toBe(true);
    expect(poll(2).from).toBe(5);
    poll(2).resolve({ events: [snapshot(6, "current", "current-message")] });
    await flush();
    superseded.resolve({ events: [snapshot(500, "stale", "stale-message")] });
    await flush();
    expect(s.messages.value.map(message => message.id)).toEqual(["current-message"]);
    expect(s.seq.value).toBe(6);
    expect(s.connected.value).toBe(true);
    expect(polls).toHaveLength(4);
    expect(poll(3).from).toBe(6);
  });

  it.each(["success", "failure"] as const)("disconnect ignores a pending poll's late %s without applying or polling again", async outcome => {
    const s = stream();
    const connection = s.connect("alice-session");
    poll(0).resolve({ events: [snapshot(3, "saved", "saved-message")] });
    await flush();
    const pending = poll(1);
    s.disconnect();
    expect(pending.signal?.aborted).toBe(true);
    expect(s.connected.value).toBe(false);
    if (outcome === "success") pending.resolve({ events: [
      snapshot(400, "late", "late-message"), event("run_start", 401, { run_id: "late-run" }),
    ] });
    else pending.reject(httpError(403));
    await connection;
    await vi.advanceTimersByTimeAsync(60_001);
    expect(s.messages.value.map(message => message.id)).toEqual(["saved-message"]);
    expect(s.seq.value).toBe(3);
    expect(s.busy.value).toBe(false);
    expect(s.connected.value).toBe(false);
    expect(s.streamError.value).toBeNull();
    expect(s.outage.value).toBeNull();
    expect(polls).toHaveLength(2);
  });

  it.each([401, 403, 404])("stops automatic reconnect after HTTP %i", async status => {
    const s = stream();
    const connection = s.connect("alice-session");
    poll(0).reject(httpError(status));
    await connection;
    expect(s.connected.value).toBe(false);
    expect(s.switching.value).toBe(false);
    expect(s.unreachable.value).toBe(true);
    expect(s.outage.value).toMatchObject({ gaveUp: true, attempts: 1 });
    expect(s.streamError.value).not.toBeNull();
    await vi.advanceTimersByTimeAsync(60_001);
    expect(polls).toHaveLength(1);
  });

  it("resumes from the last event after a retryable failure and treats an empty response as recovery", async () => {
    const s = stream();
    void s.connect("alice-session");
    poll(0).resolve({ events: [snapshot(8)] });
    await flush();
    poll(1).reject(httpError(503));
    await flush();
    expect(s.outage.value).toMatchObject({ gaveUp: false, attempts: 1 });
    expect(s.connected.value).toBe(false);
    await vi.advanceTimersByTimeAsync(500);
    expect(poll(2)).toMatchObject({ sid: "alice-session", from: 8 });
    poll(2).resolve({ events: [] });
    await flush();
    expect(s.connected.value).toBe(true);
    expect(s.streamError.value).toBeNull();
    expect(s.outage.value).toBeNull();
    expect(poll(3).from).toBe(8);
  });

  it("does not let a scheduled retry replace a newer manually recovered connection", async () => {
    const s = stream();
    void s.connect("alice-session");
    poll(0).resolve({ events: [snapshot(8)] });
    await flush();
    poll(1).reject(httpError(503));
    await flush();
    void s.retryNow();
    poll(2).resolve({ events: [snapshot(9)] });
    await flush();
    const recovered = poll(3);
    await vi.advanceTimersByTimeAsync(500);
    expect(polls).toHaveLength(4);
    expect(recovered.signal?.aborted).toBe(false);
    expect(s.connected.value).toBe(true);
    expect(s.outage.value).toBeNull();
  });

  it("empty update batches refresh liveness while each next request waits for the server", async () => {
    const s = stream();
    void s.connect("quiet-session");
    poll(0).resolve({ events: [snapshot(2)] });
    await flush();
    for (let index = 1; index <= 3; index++) {
      await vi.advanceTimersByTimeAsync(30_000);
      expect(polls).toHaveLength(index + 1);
      expect(poll(index).signal?.aborted).toBe(false);
      poll(index).resolve({ events: [] });
      await flush();
      expect(poll(index + 1)).toMatchObject({ sid: "quiet-session", from: 2 });
      expect(s.connected.value).toBe(true);
      expect(s.outage.value).toBeNull();
    }
    expect(polls).toHaveLength(5);
    expect(s.seq.value).toBe(2);
  });
});
