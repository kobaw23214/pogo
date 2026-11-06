import { describe, expect, it } from "vitest";
import type { BrowserDetails, BrowserPageSummary, ToolDetails } from "@/api/types";
import type { TimelineCall, TurnItem } from "./timeline";
import { buildActivityRuns, type ActivityRun } from "./activityRuns";
import { browserSummaryPageKey, historicalFailureGroups, pureSleepSeconds, summarizeActivityCalls } from "./runSummary";

const call = (id: string, name = "read", args: unknown = { path: `${id}.txt` }, details?: ToolDetails): TimelineCall => ({ callId: id, name, args, running: false, orphaned: false, result: { call_id: id, text: "ok", ...(details ? { details } : {}) } });
const page = (extra: Partial<BrowserPageSummary> = {}): BrowserPageSummary => ({ index: 0, ok: true, status: "observed", context_ref: "context", tab_id: "tab", generation: 1, document_version: 2, ...extra });
const browser = (id: string, action: string, pages?: BrowserPageSummary[]) => call(id, "browser", { action }, { action, ok: true, ...(pages ? { pages } : {}) });
const failure = (id: string, extra: Partial<TimelineCall> = {}) => ({ ...call(id, "browser", { action: "navigate", context_ref: "context", tab_id: "tab", control_epoch: 1, url: "https://example.test" }), result: { call_id: id, text: "BROWSER_RESOURCE_CAPACITY: unavailable", is_error: true }, ...extra });
const run = (calls: TimelineCall[]): ActivityRun => {
  const turn: TurnItem = { kind: "turn", id: "m1", index: 1, calls, thinking: "", text: "", streaming: false };
  return buildActivityRuns({ id: "u1", turns: [turn], finished: true, notices: [], stats: { calls: 0, failed: 0, files: 0, tools: {} } })[0] as ActivityRun;
};

describe("activity summaries report facts rather than inferred success", () => {
  it("counts successful categories in first-appearance order and distinct known file paths", () => {
    const a = call("a"), edit = call("edit", "edit", { path: "src/a.ts" });
    const result = summarizeActivityCalls([a, call("b", "grep"), call("a2", "read", { path: "a.txt" }), edit, call("edit2", "edit", { path: "src/a.ts" }), a]);
    expect(result.total).toBe(5);
    expect(result.clauses).toEqual([{ kind: "read", count: 1 }, { kind: "search", count: 1 }, { kind: "write", count: 1 }]);
  });

  it("preserves every observed failure after a corrects hint or unrelated browser success", () => {
    const a = failure("a"), b = failure("b"), success = browser("success", "click"); success.corrects = "b";
    const facts = summarizeActivityCalls([a, b, success, a]);
    expect(facts.failuresObserved).toBe(2); expect(facts.successful).toBe(1);
    expect(facts.environmentErrors).toEqual(["BROWSER_RESOURCE_CAPACITY"]);
    expect(facts.clauses).toEqual([{ kind: "browserClick", count: 1 }]);
    expect(facts).not.toHaveProperty("recoveriesConfirmed");
  });

  it("does not turn missing results, generated invalid args, approval or control states into successful calls", () => {
    const unknown = { ...call("unknown"), result: undefined };
    const invalid = { ...failure("invalid"), inputInvalid: true };
    const controlled = { ...failure("control"), result: { call_id: "control", text: "TAB_HUMAN_CONTROL: user owns tab", is_error: true } };
    const denied = { ...failure("denied"), result: { call_id: "denied", text: "Tool execution was blocked", is_error: true } };
    const timeout = { ...failure("timeout"), result: { call_id: "timeout", text: "The user did not approve this call within 1m0s", is_error: true } };
    const cancelled = { ...failure("cancelled"), result: { call_id: "cancelled", text: "Run was cancelled", is_error: true } };
    const gate = { ...call("gate"), gate: { gate_id: "g", call_id: "gate", tool: "read", deadline: 1000 } };
    expect(summarizeActivityCalls([unknown, invalid, controlled, denied, timeout, cancelled, gate])).toMatchObject({ total: 7, unknown: 1, notExecuted: 1, controlled: 1, denied: 1, timedOut: 1, cancelled: 1, gated: 1, failuresObserved: 0, successful: 0 });
  });

  it("does not reinterpret arbitrary execution permission errors as human denial", () => {
    const failed = call("a", "bash", { command: "cat protected" }, { command: "cat protected", exit_code: 1 });
    failed.result!.text = "permission denied";
    expect(summarizeActivityCalls([failed])).toMatchObject({ denied: 0, failuresObserved: 1 });
  });

  it("recognizes exact host control receipts without suppressing execution failures", () => {
    const rejected = failure("reject"); rejected.result!.text = "用户拒绝了这次调用";
    const takeover = failure("control"); takeover.result!.text = "browser tab failed: TAB_HUMAN_CONTROL: user owns tab";
    const wrapped = failure("wrapped"); wrapped.result!.text = "browser navigate failed: BROWSER_RESOURCE_CAPACITY: no capacity";
    expect(summarizeActivityCalls([rejected, takeover, wrapped])).toMatchObject({ denied: 1, controlled: 1, failuresObserved: 1, environmentErrors: ["BROWSER_RESOURCE_CAPACITY"] });
  });

  it("does not count requested multi-read paths as confirmed files", () => {
    const partial = call("partial", "read", { paths: ["a", "b"] }, { files: [{ path: "a", shown_lines: 3 }, { path: "b", error: "not found" }] });
    expect(summarizeActivityCalls([partial]).clauses).toEqual([{ kind: "read", count: 1 }]);
    expect(summarizeActivityCalls([call("legacy", "read", { paths: ["a", "b"] })]).clauses).toEqual([{ kind: "readOperations", count: 1 }]);
  });

  it("keeps running or orphaned background acknowledgements out of completed summaries", () => {
    const running = call("run", "bash", { command: "long" }, { command: "long", background: true, status: "running" });
    const old = call("old", "bash", { command: "long" }, { command: "long", background: true });
    expect(summarizeActivityCalls([running, old])).toMatchObject({ running: 1, incomplete: 1, successful: 0, clauses: [] });
  });

  it("does not count a pending asynchronous question acknowledgement as completed work", () => {
    const pending = call("question", "request_user_input_async", {}, { status: "pending", async: true, questions: [] });
    expect(summarizeActivityCalls([pending])).toMatchObject({ awaitingInput: 1, successful: 0, clauses: [] });
  });

  it("uses document identity for page counts, ignoring actions and control lease changes", () => {
    const first = page(), changedLease = page({ control_epoch: 99, version: "new snapshot", url: "https://different.test" });
    expect(browserSummaryPageKey(first)).toBe(browserSummaryPageKey(changedLease));
    const facts = summarizeActivityCalls([browser("a", "observe_pages", [first]), browser("b", "observe_pages", [changedLease]), browser("c", "observe_pages", [page({ tab_id: "other" })])]);
    expect(facts.clauses).toEqual([{ kind: "browserReadPages", count: 2 }]);
    expect(summarizeActivityCalls([browser("a", "observe_pages", [first]), browser("b", "observe_pages", [page({ document_version: 3 })])]).clauses).toEqual([{ kind: "browserReadPages", count: 2 }]);
  });

  it("falls back to operation counts if one page lacks reliable document identity", () => {
    const facts = summarizeActivityCalls([browser("a", "observe_pages", [page()]), browser("b", "observe_pages", [page({ document_version: undefined })])]);
    expect(facts.clauses).toEqual([{ kind: "browserRead", count: 2 }]);
    expect(summarizeActivityCalls([browser("a", "open_pages")]).clauses).toEqual([{ kind: "browserOpen", count: 1 }]);
  });

  it("counts a failed batch once and never infers success from requested actions", () => {
    const details: BrowserDetails = { action: "batch", ok: false, steps: 4, step_results: [{ action: "click", ok: false }, { action: "type", ok: false }], batch: { requested: 4, executed: 2, skipped: 2, stop_reason: "failed" } };
    expect(summarizeActivityCalls([call("batch", "browser", { action: "batch", actions: [1, 2, 3, 4] }, details)])).toMatchObject({ total: 1, failuresObserved: 1, successful: 0 });
    details.ok = true; details.batch!.stop_reason = "observation_pending";
    expect(summarizeActivityCalls([call("pending", "browser", { action: "batch" }, details)])).toMatchObject({ total: 1, unknown: 1, successful: 0 });
  });

  it("does not count tab management and waiting as business work, retaining honest fallback", () => {
    const tabs = browser("tabs", "tabs"), wait = call("wait", "bash", { command: "sleep 45" });
    expect(summarizeActivityCalls([tabs, wait]).clauses).toEqual([{ kind: "browserTabs", count: 1 }, { kind: "wait", count: 1 }]);
    expect(summarizeActivityCalls([tabs, wait, call("read")]).clauses).toEqual([{ kind: "read", count: 1 }]);
    expect(summarizeActivityCalls([wait])).not.toHaveProperty("durationMs");
  });

  it("only recognizes an unambiguous sleep command", () => {
    expect(pureSleepSeconds({ name: "bash", args: { command: " sleep 1.5m " } })).toBe(90);
    for (const command of ["echo sleep 45", "sleep 45 && rm file", "sleep $(read timeout)", "cd /tmp && sleep 3", "sleep 2; echo done"]) expect(pureSleepSeconds({ name: "bash", args: { command } })).toBeUndefined();
  });
});

describe("historical failure grouping requires reliable origin", () => {
  it("leaves identical errors independent when call run identity is absent", () => {
    expect(historicalFailureGroups(run([failure("a"), failure("b")]))).toEqual([]);
  });

  it("returns stable candidate groups without changing source calls", () => {
    const a = failure("a"), b = failure("b"), source = run([a, b]);
    const origins = new Map([a, b].map(call => [call.callId, { sessionId: "s", exchangeId: "u", runId: "r" }]));
    expect(historicalFailureGroups(source, origins)).toEqual([{ id: "retry:a", calls: [a, b], errorCode: "BROWSER_RESOURCE_CAPACITY" }]);
    expect(source.calls).toEqual([a, b]); expect(source.items).toHaveLength(2);
    source.closed = false; expect(historicalFailureGroups(source, origins)).toEqual([]);
  });

  it("does not cross sleep, different resources, run boundaries or live attempts", () => {
    const a = failure("a"), b = failure("b"), origins = new Map([a, b].map(call => [call.callId, { sessionId: "s", exchangeId: "u", runId: "r" }]));
    expect(historicalFailureGroups(run([a, call("wait", "bash", { command: "sleep 45" }), b]), origins)).toEqual([]);
    b.args = { ...(b.args as object), tab_id: "another" };
    expect(historicalFailureGroups(run([a, b]), origins)).toEqual([]);
    b.args = a.args; origins.get("b")!.runId = "different";
    expect(historicalFailureGroups(run([a, b]), origins)).toEqual([]);
    origins.get("b")!.runId = "r"; b.running = true;
    expect(historicalFailureGroups(run([a, b]), origins)).toEqual([]);
  });
});
