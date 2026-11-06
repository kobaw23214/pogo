import { afterEach, describe, expect, it } from "vitest";
import { setLocale } from "@/i18n";
import type { TimelineCall } from "./timeline";
import { presentToolCall, toolCallState, toolControlOutcome, toolGlyph } from "./toolPresentation";

const call = (name: string, extra: Partial<TimelineCall> = {}): TimelineCall => ({ callId: "c1", name: name as TimelineCall["name"], args: {}, running: false, orphaned: false, ...extra });
const gate = { gate_id: "g1", call_id: "c1", tool: "read" as const, deadline: 123 };
setLocale("zh-CN");
afterEach(() => setLocale("zh-CN"));

describe("tool presentation", () => {
  it("gives built-ins exact semantic identities before checking external families", () => {
    expect(presentToolCall(call("read", { args: { path: "src/main.go" } }))).toMatchObject({ name: "读取文件", glyph: "read", target: "src/main.go", protocolName: "read" });
    expect(presentToolCall(call("skill_read", { args: { path: "SKILL.md" } }))).toMatchObject({ name: "读取技能", glyph: "skill", target: "SKILL.md" });
    expect(presentToolCall(call("skill_search", { args: { query: "reports" } })).name).toBe("查找技能");
    expect(presentToolCall(call("skill_save")).name).toBe("保存技能");
    expect(toolGlyph("browser")).toBe("browser");
    expect(toolGlyph("subagent")).toBe("subagent");
    expect(presentToolCall(call("docs_office", { args: { action: "open", path: "report.docx" } }))).toMatchObject({ name: "在线 Office 文档", glyph: "office", target: "report.docx", protocolName: "docs_office" });
  });

  it("uses trusted result fields for external identities without decoding wire names", () => {
    const mcp = presentToolCall(call("mcp_hash_truncated", { result: { call_id: "c1", text: "ok", details: { server: "example", tool: "lookup", title: "Lookup record", duration_ms: 125 } } }));
    expect(mcp).toMatchObject({ glyph: "mcp", name: "Lookup record", secondary: "example · lookup", protocolName: "mcp_hash_truncated", meta: "125ms" });
    expect(presentToolCall(call("mcp_hash_truncated"))).toMatchObject({ glyph: "mcp", name: "mcp_hash_truncated" });
    expect(presentToolCall(call("skill_contract_hash", { result: { call_id: "c1", text: "ok", details: { capability: "reports.summary", callId: "s1" } } }))).toMatchObject({ glyph: "skill", name: "reports.summary", secondary: "技能调用" });
    expect(presentToolCall(call("vendor_action"))).toMatchObject({ glyph: "tool", name: "vendor_action" });
    expect(toolGlyph("vendor_action", { server: "example", tool: "lookup" })).toBe("mcp");
    expect(toolGlyph("mcp_display_only", { capability: "reports.summary", callId: "s1" })).toBe("skill");
  });

  it("normalizes shortcut action labels without changing their wire identity", () => {
    expect(presentToolCall(call("shortcut_manage", { args: { action: "update", title: "Reference", id: "s1" } }))).toMatchObject({ name: "更新快捷方式", glyph: "shortcut", protocolName: "shortcut_manage" });
  });

  it("keeps complete commands inspectable and does not guess a browser target", () => {
    const command = "for item in one two; do\n  printf '%s\\n' \"$item\"\ndone";
    expect(presentToolCall(call("bash", { args: { command } })).target).toBe(command);
    expect(presentToolCall(call("browser", { args: { unknown_action: "opaque", payload: { large: "data" } } })).target).toBeUndefined();
  });

  it("preserves state priority and treats a nonzero bash exit as a failure", () => {
    const all = call("bash", { inputInvalid: true, gate, running: true, orphaned: true, result: { call_id: "c1", text: "error", is_error: true } });
    expect(toolCallState(all)).toBe("error");
    expect(presentToolCall(all).statusLabel).toBe("未执行");
    expect(toolCallState({ ...all, inputInvalid: false })).toBe("gate");
    expect(toolCallState({ ...all, inputInvalid: false, gate: undefined })).toBe("running");
    expect(toolCallState({ ...all, inputInvalid: false, gate: undefined, running: false })).toBe("orphaned");
    expect(toolCallState(call("bash", { result: { call_id: "c1", text: "exit", details: { command: "false", exit_code: 1, duration_ms: 7 } } }))).toBe("error");
  });

  it("shows only recorded valid durations, including zero, with details taking precedence", () => {
    expect(presentToolCall(call("read")).meta).toBeUndefined();
    expect(presentToolCall(call("read", { result: { call_id: "c1", text: "", duration_ms: 0 } })).meta).toBe("0ms");
    expect(presentToolCall(call("bash", { result: { call_id: "c1", text: "", duration_ms: 9, details: { command: "pwd", exit_code: 0, duration_ms: 2 } } })).meta).toBe("2ms");
    expect(presentToolCall(call("read", { result: { call_id: "c1", text: "", duration_ms: -1 } })).meta).toBeUndefined();
    expect(presentToolCall(call("read", { result: { call_id: "c1", text: "", duration_ms: NaN } })).meta).toBeUndefined();
  });

  it("keeps background bash acknowledgements distinct from completed commands", () => {
    const dispatched = call("bash", { result: { call_id: "c1", text: "ACK", details: { command: "npm test", background: true, task_id: "shell_one", status: "running", duration_ms: 2 } } });
    expect(presentToolCall(dispatched)).toMatchObject({ state: "running", statusLabel: "后台运行中" });
    const unknown = call("bash", { result: { call_id: "c1", text: "ACK", details: { command: "npm test", task_id: "shell_one", background: true, duration_ms: 2 } } });
    expect(toolCallState(unknown)).toBe("orphaned");
    expect(presentToolCall(unknown)).toMatchObject({ state: "orphaned", statusLabel: "已交后台，状态未知" });
    const terminalWithoutDuration = call("bash", { result: { call_id: "c1", text: "done", duration_ms: 2, details: { command: "npm test", task_id: "shell_one", background: true, status: "completed", exit_code: 0, duration_ms: undefined } } });
    expect(presentToolCall(terminalWithoutDuration).state).toBe("done");
    expect(presentToolCall(terminalWithoutDuration).meta).toBeUndefined();
    expect(toolCallState({ ...dispatched, result: { ...dispatched.result!, details: { command: "npm test", background: true, task_id: "shell_one", status: "interrupted", duration_ms: 2 } } })).toBe("error");
  });

  it("uses background subagent state and task duration instead of a dispatch acknowledgement", () => {
    const background = call("subagent", { result: { call_id: "c1", text: "dispatched", duration_ms: 11 }, bgTask: { task_id: "bg1", mode: "read", task: "review", status: "running", started_at: 1000 } });
    expect(presentToolCall(background)).toMatchObject({ state: "running", statusLabel: "执行中" });
    expect(presentToolCall(background).meta).toBeUndefined();
    expect(toolCallState({ ...background, gate })).toBe("gate");
    expect(presentToolCall({ ...background, bgTask: { ...background.bgTask!, status: "failed", duration_ms: 3200 } })).toMatchObject({ state: "error", statusLabel: "失败", meta: "3.2s" });
    expect(presentToolCall({ ...background, bgTask: { ...background.bgTask!, status: "done", duration_ms: 0 } })).toMatchObject({ state: "done", meta: "0ms" });
    expect(presentToolCall({ ...background, bgTask: { ...background.bgTask!, status: "done" } }).meta).toBeUndefined();
  });

  it("localizes built-in labels while keeping third-party names unchanged", () => {
    setLocale("en");
    expect(presentToolCall(call("read")).name).toBe("Read file");
    expect(presentToolCall(call("read", { running: true })).statusLabel).toBe("Running");
    expect(presentToolCall(call("vendor_action")).name).toBe("vendor_action");
  });

  it("does not treat a background dispatch without task state as completed", () => {
    const dispatched = call("subagent", { result: { call_id: "c1", text: "ACK", duration_ms: 5, details: { id: "bg1", mode: "explore", background: true } } });
    expect(toolCallState(dispatched)).toBe("orphaned"); expect(presentToolCall(dispatched).meta).toBeUndefined();
    expect(toolCallState({ ...dispatched, taskNotice: { id: "bg1", brief: "task", failed: false, result: "actual result" } })).toBe("done");
    expect(toolCallState({ ...dispatched, childGates: [{ ...gate, origin: "bg1" }] })).toBe("gate");
  });

  it("keeps host refusals and cancellations distinct from execution failures without guessing arbitrary error text", () => {
    const receipt = (text: string) => call("read", { result: { call_id: "c1", text, is_error: true } });
    expect(toolControlOutcome(receipt("用户拒绝了这次调用"))).toBe("denied");
    expect(presentToolCall(receipt("The user did not approve this call within 1m0s"))).toMatchObject({ state: "done", statusLabel: "超时未批准" });
    expect(presentToolCall(receipt("Run was cancelled"))).toMatchObject({ state: "done", statusLabel: "已停止" });
    expect(toolControlOutcome(receipt("remote API denied access"))).toBeUndefined();
    expect(toolCallState(receipt("remote API denied access"))).toBe("error");
    expect(toolControlOutcome(call("browser", { result: { call_id: "c1", is_error: true, text: '{"ok":false,"error":{"code":"TAB_HUMAN_CONTROL"}}' } }))).toBe("human-control");
    expect(toolControlOutcome(call("browser", { result: { call_id: "c1", is_error: true, text: "browser snapshot failed: TAB_HUMAN_CONTROL: user owns tab" } }))).toBe("human-control");
  });

  it("reports observed result counts without turning missing metadata into zero or task success", () => {
    expect(presentToolCall(call("read", { result: { call_id: "c1", text: "content", details: { path: "a", first_line: 1, total_lines: 20, shown_lines: 10, truncated: true } } })).meta).toBe("10 行");
    expect(presentToolCall(call("read", { result: { call_id: "c1", text: "content" } })).meta).toBeUndefined();
    expect(presentToolCall(call("browser", { result: { call_id: "c1", text: "partial", details: { action: "batch", ok: false, step_results: [{ action: "click", ok: true }, { action: "fill", ok: false }] } } })).meta).toBe("1/2 步成功");
    expect(toolCallState(call("browser", { result: { call_id: "c1", text: "partial", details: { action: "batch", ok: false } } }))).toBe("error");
    expect(presentToolCall(call("shell_stop", { args: { task_id: "job-4" } }))).toMatchObject({ name: "停止后台命令", target: "job-4" });
  });
});
