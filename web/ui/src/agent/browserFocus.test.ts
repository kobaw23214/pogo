import { afterEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type AgentEvent } from "@/api/types";
import type { DockTab } from "@/components/dockSheets";
import type { NativeBrowserState } from "./nativeBrowser";
import { BrowserFocusPolicy, createBrowserFocusObserver, setBrowserFocusHook, type BrowserFocusRequest } from "./browserFocus";

const contextRef = "ctx_012345678901234567890123";
const otherContext = "ctx_999999999999999999999999";
const event = (type: AgentEvent["type"], seq: number, extra: Partial<AgentEvent> = {}): AgentEvent => ({ type, seq, ts: seq * 100, ...extra });
const result = (extra: Record<string, unknown> = {}): AgentEvent["details"] => ({ action: "tab", ok: true, backend: "native",
  context_ref: contextRef, active_tab: "t2", version: "1:t2:1:0:revision:0:1280x800", ...extra }) as AgentEvent["details"];
const start = (seq = 2, callId = "call1", args: unknown = { action: "tab", tab_action: "new" }) => event("tool_start", seq, { name: "browser", call_id: callId, args });
const end = (seq = 3, callId = "call1", details = result()) => event("tool_end", seq, { name: "browser", call_id: callId, details });
const snapshot = (seq = 10, runId = "r1") => event("snapshot", seq, { snapshot: {
  seq, messages: [], results: {}, run: { active: true, run_id: runId }, live: { ...emptyLive(), active: true, run_id: runId,
    pending_tools: [{ call_id: "pending", name: "browser", args: { action: "tab", tab_action: "new" }, started_at: 1 }] },
  policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0,
} });
function fixture() {
  const observer = createBrowserFocusObserver();
  const hook = vi.fn();
  setBrowserFocusHook(hook);
  observer.selectSession("s1");
  const apply = (e: AgentEvent, replay = false, sessionId = "s1") => observer.observe(e, { sessionId, replay });
  const focused = () => hook.mock.calls.map(([request]) => request as BrowserFocusRequest).filter(request => ["start", "complete"].includes(request.phase));
  return { observer, hook, apply, focused };
}
afterEach(() => setBrowserFocusHook(null));

describe("completed Agent tab focus observation", () => {
  it("emits the main live call identity only after its matching successful completion", () => {
    const { apply, focused } = fixture();
    apply(event("run_start", 1, { run_id: "r1" }));
    apply(start());
    expect(focused()).toEqual([{ phase: "start", sessionId: "s1", runId: "r1", callId: "call1", seq: 2 }]);
    apply(end());
    expect(focused()[1]).toEqual({ phase: "complete", sessionId: "s1", runId: "r1", callId: "call1", seq: 3,
      contextRef, tabId: "t2", generation: 1 });
  });
  it("accepts explicit and implicit existing-tab switches", () => {
    const { apply, focused } = fixture();
    apply(event("run_start", 1, { run_id: "r1" }));
    apply(start(2, "explicit", { action: "tab", tab_action: "switch", target: "t2" }));
    apply(end(3, "explicit"));
    apply(start(4, "implicit", { action: "tab", target: "t2" }));
    apply(end(5, "implicit"));
    expect(focused().filter(request => request.phase === "complete")).toHaveLength(2);
  });
  it("follows a successful standalone Agent claim for the explicitly observed human Page", () => {
    const { apply, focused } = fixture();
    apply(event("run_start", 1, { run_id: "r1" }));
    apply(start(2, "claim", { action: "tab", tab_action: "claim", target: "t2", context_ref: contextRef, control_epoch: 4 }));
    apply(end(3, "claim", result({ control: "agent", control_epoch: 5 })));
    expect(focused().map(request => request.phase)).toEqual(["start", "complete"]);
    expect(focused()[1]).toMatchObject({ contextRef, tabId: "t2" });
  });
  it("rejects claims without an observed identity and completions that do not prove that handoff", () => {
    const { apply, focused } = fixture();
    apply(event("run_start", 1, { run_id: "r1" }));
    const valid = { action: "tab", tab_action: "claim", target: "t2", context_ref: contextRef, control_epoch: 4 };
    const invalidArgs = [{ ...valid, context_ref: undefined }, { ...valid, control_epoch: undefined }, { ...valid, control_epoch: -1 },
      { ...valid, target: "../t2" }, { action: "batch", actions: [valid] }];
    invalidArgs.forEach((args, i) => { apply(start(2 + i * 2, `bad-args-${i}`, args)); apply(end(3 + i * 2, `bad-args-${i}`, result({ control: "agent", control_epoch: 5 }))); });
    expect(focused()).toEqual([]);
    const invalidResults = [{ control: "human", control_epoch: 5 }, { control: "agent", control_epoch: 4 },
      { control: "agent", control_epoch: 5, context_ref: otherContext }, { control: "agent", control_epoch: 5, active_tab: "t1", version: "1:t1:1:0:x" }];
    invalidResults.forEach((details, i) => { apply(start(20 + i * 2, `bad-result-${i}`, valid)); apply(end(21 + i * 2, `bad-result-${i}`, result(details))); });
    expect(focused().filter(request => request.phase === "complete")).toEqual([]);
  });
  it("does not focus normal navigation, observations, metadata or invalid tab requests", () => {
    const { apply, focused } = fixture();
    apply(event("run_start", 1, { run_id: "r1" }));
    const args = [{ action: "bookmarks" }, { action: "tabs" }, { action: "navigate", url: "https://site.invalid/" },
      { action: "snapshot" }, { action: "tab", tab_action: "close", target: "t2" }, { action: "tab", tab_action: "switch" },
      { action: "tab", tab_action: "new", target: "t1" }, { action: "tab", target: "../t2" }];
    args.forEach((arg, i) => { apply(start(2 + i * 2, `c${i}`, arg)); apply(end(3 + i * 2, `c${i}`)); });
    expect(focused()).toEqual([]);
  });
  it("never focuses replayed actions, missing starts or snapshot pending calls", () => {
    const { apply, focused } = fixture();
    apply(event("run_start", 1, { run_id: "r1" }), true);
    apply(start(), true);
    apply(end());
    apply(snapshot(), true);
    apply(end(11, "pending"));
    apply(end(12, "missing"));
    expect(focused()).toEqual([]);
    apply(start(13, "fresh"));
    apply(end(14, "fresh"));
    expect(focused()).toHaveLength(2);
  });
  it("cancels a live start on reconnect instead of completing it from replay", () => {
    const { apply, focused } = fixture();
    apply(event("run_start", 1, { run_id: "r1" }));
    apply(start());
    apply(snapshot(1), true);
    apply(start(2), true);
    apply(end(3));
    expect(focused().filter(request => request.phase === "complete")).toEqual([]);
  });
  it("deduplicates old sequence and call identities without replaying focus", () => {
    const { apply, focused } = fixture();
    apply(event("run_start", 1, { run_id: "r1" }));
    apply(start()); apply(end()); apply(end());
    apply(start(4)); apply(end(5));
    apply(event("run_start", 1, { run_id: "old" }));
    expect(focused()).toHaveLength(2);
  });
  it("only accepts the selected session and rejects child frames or old-run identities", () => {
    const { observer, apply, focused } = fixture();
    apply(event("run_start", 1, { run_id: "r1" }));
    apply({ ...start(2, "child"), task_id: "child-task" });
    apply(end(3, "child"));
    apply({ ...start(4, "old-run"), run_id: "old" });
    apply(end(5, "old-run"));
    apply(event("subagent_frame", 6, { frame: { type: "tool_start", name: "browser", call_id: "embedded", args: { action: "tab", tab_action: "new" } } }));
    observer.selectSession("s2");
    apply(start(7, "old-stream")); apply(end(8, "old-stream"));
    apply(event("run_start", 1, { run_id: "r2" }), false, "s2");
    apply(start(), false, "s2"); apply(end(), false, "s2");
    expect(focused()).toHaveLength(2);
    expect(focused()[1].sessionId).toBe("s2");
  });
  it("rejects errors, missing context, wrong page/version, malformed generation and metadata provenance", () => {
    const { apply, focused } = fixture();
    apply(event("run_start", 1, { run_id: "r1" }));
    const invalid = [{ ok: false }, { backend: "legacy" }, { action: "navigate" }, { context_ref: undefined },
      { context_ref: "ctx_bad" }, { active_tab: "../t2" }, { version: "1:t1:1:0:x" }, { version: "0:t2:1:0:x" },
      { version: "9007199254740992:t2:1:0:x" }, { content_source: "pogo_bookmarks" }];
    invalid.forEach((details, i) => { apply(start(2 + i * 2, `c${i}`)); apply(end(3 + i * 2, `c${i}`, result(details))); });
    apply(start(30, "error")); apply({ ...end(31, "error"), is_error: true });
    expect(focused().filter(request => request.phase === "complete")).toEqual([]);
  });
  it("focuses only when an executed batch prefix contains a tab action", () => {
    const { apply, focused } = fixture();
    apply(event("run_start", 1, { run_id: "r1" }));
    const args = { action: "batch", actions: [{ action: "navigate", url: "https://site.invalid/" }, { action: "tab", tab_action: "new" }, { action: "snapshot" }] };
    for (const [i, steps] of [1, 0, 1.5, 4, undefined].entries()) {
      apply(start(2 + i * 2, `bad${i}`, args)); apply(end(3 + i * 2, `bad${i}`, result({ action: "batch", steps })));
    }
    apply(start(12, "good", args)); apply(end(13, "good", result({ action: "batch", steps: 2 })));
    apply(start(14, "first", { action: "batch", actions: [{ action: "tab", target: "t2" }, { action: "snapshot" }] }));
    apply(end(15, "first", result({ action: "batch", steps: 1 })));
    expect(focused().filter(request => request.phase === "complete").map(request => request.callId)).toEqual(["good", "first"]);
  });
  it("does not complete an old call after a new run or run_end", () => {
    const { apply, focused } = fixture();
    apply(event("run_start", 1, { run_id: "r1" })); apply(start());
    apply(event("run_start", 3, { run_id: "r2" })); apply(end(4));
    apply(start(5, "later")); apply(event("run_end", 6)); apply(end(7, "later"));
    expect(focused().filter(request => request.phase === "complete")).toEqual([]);
  });
  it("only unregisters the hook that owns its cleanup", () => {
    const old = vi.fn(), current = vi.fn();
    const cleanup = setBrowserFocusHook(old);
    setBrowserFocusHook(current); cleanup();
    const observer = createBrowserFocusObserver(); observer.selectSession("s1");
    observer.observe(event("run_start", 1, { run_id: "r1" }), { sessionId: "s1", replay: false });
    expect(old).not.toHaveBeenCalled(); expect(current).toHaveBeenCalled();
  });
});

const state: NativeBrowserState = { backend: "native", control: "agent", context_ref: contextRef, generation: 1,
  viewed_tab: "t1", active_tab: "t2", url: "https://a.invalid/", title: "A", viewport: { width: 1280, height: 800 },
  tabs: [{ id: "t1", url: "https://a.invalid/", title: "A" }, { id: "t2", url: "https://b.invalid/", title: "B" }] };
const sheet = (tabId: string, extra: Partial<DockTab> = {}): DockTab => ({ id: `sheet:${tabId}`, kind: "browser", label: tabId,
  closable: true, browserBinding: { sessionId: "s1", context_ref: contextRef, tab_id: tabId }, ...extra });
const request = (phase: BrowserFocusRequest["phase"], seq: number, extra: Partial<BrowserFocusRequest> = {}): BrowserFocusRequest => ({
  phase, sessionId: "s1", runId: "r1", callId: "call1", seq, ...extra,
});
function pendingPolicy() {
  const policy = new BrowserFocusPolicy();
  policy.observe(request("run", 1), 100);
  policy.observe(request("start", 2), 100);
  policy.observe(request("complete", 3, { contextRef, tabId: "t2", generation: 1 }), 100);
  return policy;
}
describe("one-time Sheet focus policy", () => {
  it("selects the exact live binding once without using viewed_tab", () => {
    const policy = pendingPolicy();
    expect(policy.selection([sheet("t1"), sheet("t2")], "s1", state, 101)).toBe("sheet:t2");
    expect(policy.selection([sheet("t1"), sheet("t2")], "s1", state, 102)).toBeUndefined();
    policy.observe(request("complete", 4, { contextRef, tabId: "t2", generation: 1 }), 103);
    expect(policy.selection([sheet("t2")], "s1", state, 104)).toBeUndefined();
  });
  it("normal polling and popup discovery never create a focus request", () => {
    const policy = new BrowserFocusPolicy();
    expect(policy.selection([sheet("t1"), sheet("t2")], "s1", state, 100)).toBeUndefined();
    policy.observe(request("run", 1), 100);
    expect(policy.selection([sheet("t1"), sheet("t2")], "s1", state, 100)).toBeUndefined();
  });
  it("waits for late discovery even after run_end and expires after 15 seconds", () => {
    const policy = pendingPolicy();
    expect(policy.selection([sheet("t1")], "s1", state, 101)).toBeUndefined();
    expect(policy.selection([sheet("t2")], "s1", state, 15_099)).toBe("sheet:t2");
    const expired = pendingPolicy();
    expect(expired.selection([sheet("t2")], "s1", state, 15_100)).toBeUndefined();
    expect(expired.selection([sheet("t2")], "s1", state, 15_101)).toBeUndefined();
  });
  it("allows completed requests to discover a Sheet after the observed run has ended", () => {
    const policy = new BrowserFocusPolicy();
    const observer = createBrowserFocusObserver();
    setBrowserFocusHook(request => policy.observe(request, 100));
    observer.selectSession("s1");
    for (const e of [event("run_start", 1, { run_id: "r1" }), start(), end(), event("run_end", 4)]) {
      observer.observe(e, { sessionId: "s1", replay: false });
    }
    expect(policy.selection([], "s1", state, 101)).toBeUndefined();
    expect(policy.selection([sheet("t2")], "s1", state, 102)).toBe("sheet:t2");
  });
  it("restores a replayed run identity without focusing its old calls", () => {
    const policy = new BrowserFocusPolicy();
    const observer = createBrowserFocusObserver();
    setBrowserFocusHook(request => policy.observe(request, 100));
    observer.selectSession("s1");
    observer.observe(event("run_start", 1, { run_id: "r1" }), { sessionId: "s1", replay: true });
    observer.observe(start(), { sessionId: "s1", replay: true });
    observer.observe(end(), { sessionId: "s1", replay: false });
    expect(policy.selection([sheet("t2")], "s1", state, 101)).toBeUndefined();
    observer.observe(start(4, "fresh"), { sessionId: "s1", replay: false });
    observer.observe(end(5, "fresh"), { sessionId: "s1", replay: false });
    expect(policy.selection([sheet("t2")], "s1", state, 102)).toBe("sheet:t2");
  });
  it("manual selection during an action cancels its later completion, but a later action can follow", () => {
    const policy = new BrowserFocusPolicy();
    policy.observe(request("run", 1), 100); policy.observe(request("start", 2), 100);
    policy.manual();
    policy.observe(request("complete", 3, { contextRef, tabId: "t2", generation: 1 }), 101);
    expect(policy.selection([sheet("t2")], "s1", state, 102)).toBeUndefined();
    policy.observe(request("start", 4, { callId: "later" }), 103);
    policy.observe(request("complete", 5, { callId: "later", contextRef, tabId: "t2", generation: 1 }), 104);
    expect(policy.selection([sheet("t2")], "s1", state, 105)).toBe("sheet:t2");
  });
  it("manual selection cancels an already-completed pending focus", () => {
    const policy = pendingPolicy(); policy.manual();
    expect(policy.selection([sheet("t2")], "s1", state, 101)).toBeUndefined();
  });
  it("a new run or session reset retires unfinished requests", () => {
    const policy = pendingPolicy();
    policy.observe(request("run", 4, { runId: "r2" }), 101);
    expect(policy.selection([sheet("t2")], "s1", state, 102)).toBeUndefined();
    policy.observe(request("complete", 5, { contextRef, tabId: "t2", generation: 1 }), 103);
    expect(policy.selection([sheet("t2")], "s1", state, 104)).toBeUndefined();
    const reset = pendingPolicy(); reset.reset();
    expect(reset.selection([sheet("t2")], "s1", state, 101)).toBeUndefined();
  });
  it("fails closed when a context or generation reincarnates with the same t2 name", () => {
    for (const changed of [{ ...state, context_ref: otherContext }, { ...state, generation: 2 }, { ...state, reset: true }]) {
      const policy = pendingPolicy();
      expect(policy.selection([sheet("t2")], "s1", changed, 101)).toBeUndefined();
      expect(policy.selection([sheet("t2")], "s1", state, 102)).toBeUndefined();
    }
  });
  it("cannot focus another account/session, a recovery record or an unbound Sheet", () => {
    const policy = pendingPolicy();
    const wrong = [sheet("t2", { browserUnavailable: true }), sheet("t2", { browserBinding: undefined }),
      sheet("t2", { browserBinding: { sessionId: "s2", context_ref: contextRef, tab_id: "t2" } }),
      sheet("t2", { browserBinding: { sessionId: "s1", context_ref: otherContext, tab_id: "t2" } })];
    expect(policy.selection(wrong, "s1", state, 101)).toBeUndefined();
    expect(policy.selection([sheet("t2")], "s2", state, 102)).toBeUndefined();
    expect(policy.selection([sheet("t2")], "s1", state, 103)).toBeUndefined();
  });
  it("waits for a complete discovery snapshot of the completed active tab", () => {
    const policy = pendingPolicy();
    expect(policy.selection([sheet("t2")], "s1", { ...state, active_tab: "t1" }, 101)).toBeUndefined();
    expect(policy.selection([sheet("t2")], "s1", { ...state, tabs: [state.tabs[0]] }, 102)).toBeUndefined();
    expect(policy.selection([sheet("t2")], "s1", state, 103)).toBe("sheet:t2");
  });
  it("rejects completion without a current start, a matching run or a valid context", () => {
    const policy = new BrowserFocusPolicy();
    policy.observe(request("run", 1), 100);
    policy.observe(request("complete", 2, { contextRef, tabId: "t2", generation: 1 }), 100);
    policy.observe(request("start", 3), 100);
    policy.observe(request("complete", 4, { runId: "old", contextRef, tabId: "t2", generation: 1 }), 100);
    policy.observe(request("complete", 5, { tabId: "t2", generation: 1 }), 100);
    expect(policy.selection([sheet("t2")], "s1", state, 101)).toBeUndefined();
  });
});
