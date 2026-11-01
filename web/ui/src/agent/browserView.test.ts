import { afterEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type AgentEvent } from "@/api/types";
import { BrowserImmersionPolicy, browserStreamBacklogThrough, createBrowserReveal, setEnsurePaneHook, type BrowserRevealRequest } from "./browserView";

afterEach(() => setEnsurePaneHook(null));
const event = (type: AgentEvent["type"], seq: number, extra: Partial<AgentEvent> = {}): AgentEvent => ({ type, seq, ts: seq * 100, ...extra });
function fixture() {
  const reveal = createBrowserReveal(), show = vi.fn();
  setEnsurePaneHook(show);
  reveal.selectSession("s1");
  const apply = (e: AgentEvent, replay = false, sessionId = "s1") => reveal.observe(e, { sessionId, replay });
  return { reveal, show, apply };
}
function snapshot(seq: number, runId: string, browserPending = false): AgentEvent {
  return event("snapshot", seq, { snapshot: {
    seq, messages: [], results: {}, run: { active: true, run_id: runId },
    live: { ...emptyLive(), active: true, run_id: runId, pending_tools: browserPending ? [{ call_id: "pending", name: "browser", started_at: 1 }] : [] },
    policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0,
  } });
}

describe("live browser reveal identities", () => {
  it("account bookmark reads leave the panel alone and do not consume the first page reveal", () => {
    const { apply, show } = fixture();
    apply(event("run_start", 1, { run_id: "r1" }));
    apply(event("tool_start", 2, { name: "browser", args: { action: "bookmarks" } }));
    expect(show).not.toHaveBeenCalled();
    apply(event("tool_start", 3, { name: "browser", args: { action: "tab", tab_action: "new" } }));
    expect(show).toHaveBeenCalledTimes(1);
    const pending = snapshot(4, "r2", true);
    pending.snapshot!.live!.pending_tools[0].args = { action: "bookmarks" };
    apply(pending, true);
    apply(event("tool_start", 5, { name: "browser", args: { action: "navigate", url: "https://example.test/" } }));
    expect(show).toHaveBeenCalledTimes(2);
  });
  it("only clears the hook registration whose owner is unmounting", () => {
    const old = vi.fn(), current = vi.fn();
    const cleanupOld = setEnsurePaneHook(old);
    const cleanupCurrent = setEnsurePaneHook(current);
    cleanupOld();
    const reveal = createBrowserReveal();
    reveal.selectSession("s1");
    reveal.observe(event("run_start", 1, { run_id: "r1" }), { sessionId: "s1", replay: false });
    reveal.observe(event("tool_start", 2, { name: "browser" }), { sessionId: "s1", replay: false });
    expect(current).toHaveBeenCalledTimes(1);
    expect(old).not.toHaveBeenCalled();
    cleanupCurrent();
    reveal.observe(event("run_start", 3, { run_id: "r2" }), { sessionId: "s1", replay: false });
    reveal.observe(event("tool_start", 4, { name: "browser" }), { sessionId: "s1", replay: false });
    expect(current).toHaveBeenCalledTimes(1);
  });
  it("reveals the first browser call once per run and keeps its session/call identity", () => {
    const { apply, show } = fixture();
    apply(event("run_start", 1, { run_id: "r1" }));
    apply(event("tool_start", 2, { name: "bash", call_id: "shell" }));
    apply(event("tool_start", 3, { name: "browser", call_id: "browser-first" }));
    apply(event("tool_start", 4, { name: "browser", call_id: "browser-next" }));
    expect(show).toHaveBeenCalledExactlyOnceWith({ sessionId: "s1", runId: "r1", callId: "browser-first", seq: 3 });
    apply(event("run_end", 5));
    apply(event("run_start", 6, { run_id: "r2" }));
    apply(event("tool_start", 7, { name: "browser" }));
    expect(show).toHaveBeenCalledTimes(2);
    expect(show.mock.calls[1][0].runId).toBe("r2");
  });

  it("never reveals historical replay or a later step of its already-started browser run", () => {
    const { apply, show } = fixture();
    apply(event("run_start", 1, { run_id: "old" }), true);
    apply(event("tool_start", 2, { name: "browser" }), true);
    apply(event("tool_start", 3, { name: "browser" }));
    apply(event("run_end", 4), true);
    expect(show).not.toHaveBeenCalled();
    apply(event("run_start", 5, { run_id: "fresh" }));
    apply(event("tool_start", 6, { name: "browser" }));
    expect(show).toHaveBeenCalledTimes(1);
  });

  it("restores a run from a snapshot without replaying its pending browser action", () => {
    const { apply, show } = fixture();
    apply(snapshot(10, "restored", true), true);
    apply(event("tool_start", 11, { name: "browser" }));
    expect(show).not.toHaveBeenCalled();
  });

  it("can reveal a first live browser action following a non-browser snapshot", () => {
    const { apply, show } = fixture();
    apply(snapshot(10, "restored"), true);
    expect(show).not.toHaveBeenCalled();
    apply(event("tool_start", 11, { name: "browser", call_id: "new-browser" }));
    expect(show).toHaveBeenCalledExactlyOnceWith({ sessionId: "s1", runId: "restored", callId: "new-browser", seq: 11 });
  });

  it("isolates selected sessions and ignores a discarded stream's late calls", () => {
    const { reveal, apply, show } = fixture();
    apply(event("run_start", 1, { run_id: "same-run-id" }));
    reveal.selectSession("s2");
    apply(event("tool_start", 2, { name: "browser" }));
    apply(event("run_start", 1, { run_id: "same-run-id" }), false, "s2");
    apply(event("tool_start", 2, { name: "browser" }), false, "s2");
    expect(show).toHaveBeenCalledTimes(1);
    expect(show.mock.calls[0][0].sessionId).toBe("s2");
    reveal.selectSession("s1");
    apply(event("tool_start", 3, { name: "browser" }));
    expect(show).toHaveBeenCalledTimes(2);
    reveal.selectSession(null);
    apply(event("run_start", 4, { run_id: "stale" }));
    apply(event("tool_start", 5, { name: "browser" }));
    expect(show).toHaveBeenCalledTimes(2);
  });

  it("duplicate or older lifecycle events cannot reset the current run's reveal", () => {
    const { apply, show } = fixture();
    apply(event("run_start", 1, { run_id: "r1" }));
    apply(event("tool_start", 2, { name: "browser" }));
    apply(event("run_start", 1, { run_id: "r1" }), true);
    apply(event("tool_start", 3, { name: "browser" }));
    apply(event("run_end", 4));
    apply(event("tool_start", 5, { name: "browser" }));
    expect(show).toHaveBeenCalledTimes(1);
  });

  it("adopts a restarted server's snapshot cursor without forgetting run deduplication", () => {
    const { apply, show } = fixture();
    apply(event("run_start", 20, { run_id: "r1" }));
    apply(event("tool_start", 21, { name: "browser" }));
    apply(snapshot(1, "r1"), true);
    apply(event("tool_start", 2, { name: "browser" }));
    expect(show).toHaveBeenCalledTimes(1);
    apply(event("run_start", 3, { run_id: "r2" }));
    apply(event("tool_start", 4, { name: "browser" }));
    expect(show).toHaveBeenCalledTimes(2);
  });
});

describe("delayed automatic immersion", () => {
  const request: BrowserRevealRequest = { sessionId: "s1", runId: "r1", seq: 2 };
  it("accepts a run once and waits for that same live run/session", () => {
    const policy = new BrowserImmersionPolicy();
    expect(policy.isAllowed(request, "s1", "r1")).toBe(false);
    expect(policy.request(request)).toBe(true);
    expect(policy.request({ ...request, seq: 4 })).toBe(false);
    expect(policy.isAllowed(request, "s1", "r1")).toBe(true);
    expect(policy.isAllowed(request, "s2", "r1")).toBe(false);
    expect(policy.isAllowed(request, "s1", undefined)).toBe(false);
    expect(policy.isAllowed(request, "s1", "r2")).toBe(false);
  });
  it("manual exit suppresses pending entry and later browser steps only for that run", () => {
    const policy = new BrowserImmersionPolicy();
    policy.request(request);
    policy.manualExit("s1", "r1");
    expect(policy.isAllowed(request, "s1", "r1")).toBe(false);
    expect(policy.request({ ...request, seq: 9 })).toBe(false);
    expect(policy.request({ ...request, runId: "r2" })).toBe(true);
    expect(policy.isAllowed({ ...request, runId: "r2" }, "s1", "r2")).toBe(true);
    expect(policy.request({ ...request, sessionId: "s2" })).toBe(true);
    expect(policy.isAllowed({ ...request, sessionId: "s2" }, "s2", "r1")).toBe(true);
  });
  it("manual exit before a delayed first request is also respected", () => {
    const policy = new BrowserImmersionPolicy();
    policy.manualExit("s1", "r1");
    expect(policy.request(request)).toBe(false);
    policy.manualExit(null, undefined);
    expect(policy.request({ ...request, runId: "r2" })).toBe(true);
  });
});

it("requires a valid server backlog boundary instead of guessing from event timestamps", () => {
  expect(browserStreamBacklogThrough("0")).toBe(0);
  expect(browserStreamBacklogThrough("42")).toBe(42);
  for (const value of [null, "", "-1", "1.5", "Infinity", "9007199254740992"]) expect(browserStreamBacklogThrough(value)).toBe(Infinity);
});
