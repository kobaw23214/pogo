import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, reactive } from "vue";
import { emptyLive, type AgentEvent, type Snapshot } from "@/api/types";
import { buildTimeline } from "./timeline";
import { buildExchanges } from "./exchanges";
import { projectTaskSessionSnapshot, reduceTaskSessionEvent } from "./taskSessionSnapshot";
import { createTaskContextUsageReader } from './taskContextUsage';

const host = vi.hoisted(() => ({ session: vi.fn(), fetch: vi.fn() }));
vi.mock("@/api/client", () => ({ api: { session: host.session }, authHeaders: () => ({ Authorization: "Bearer test" }), streamURL: (id: string) => `/api/sessions/${id}/stream` }));
import { useTaskSessionStream, type TaskSessionStreamScope } from "./useTaskSessionStream";

const scopes: ReturnType<typeof effectScope>[] = [];
const flush = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); };
const event = (seq: number, type: AgentEvent["type"], fields: Partial<AgentEvent> = {}): AgentEvent => ({ seq, type, ts: 1000 + seq, ...fields });
const snapshot = (fields: Partial<Snapshot> = {}): Snapshot => ({ seq: 10, messages: [], results: {}, live: emptyLive(), run: { active: false }, policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0, ...fields });
const identity = (sid = "node-one", value = snapshot()) => ({ session_id: sid, agent_workflow: { owner_id: "owner-a", agent_id: "agent-a", workflow_id: "flow-one", run_id: "workflow-run" }, snapshot: value });
function channel() {
  let controller!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({ start(value) { controller = value; } });
  return { response: new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }),
    send(value: AgentEvent) { controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(value)}\n\n`)); },
    close() { controller.close(); }, fail() { controller.error(new Error("lost")); } };
}
function mount(fields: Partial<TaskSessionStreamScope> = {}) {
  const props = reactive<TaskSessionStreamScope>({ agentId: "agent-a", sessionId: "node-one", visible: true, workflowId: "flow-one", workflowRunId: "workflow-run", ...fields });
  const scope = effectScope(); scopes.push(scope);
  return { props, stream: scope.run(() => useTaskSessionStream(() => props))!, scope };
}
beforeEach(() => {
  vi.useFakeTimers(); vi.stubGlobal("window", new EventTarget()); vi.stubGlobal("fetch", host.fetch);
  host.session.mockReset().mockImplementation((sid: string) => Promise.resolve(identity(sid)));
  host.fetch.mockReset();
});
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); vi.clearAllTimers(); vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("read-only task session stream", () => {
  it("uses full shared prompt history only for the latest authenticated node while keeping its process transcript isolated", async () => {
    const value = snapshot({ context_tokens: 0, overhead_tokens: 1000, run: { active: false, model: 'node-model', context_window: 100000 }, messages: [
      { id: 'old', role: 'user', run_id: 'engine-a', content: [{ type: 'text', text: 'a'.repeat(4000) }] },
      { id: 'old-answer', role: 'assistant', content: [{ type: 'text', text: 'prior' }] },
      { id: 'current', role: 'user', run_id: 'engine-b', content: [{ type: 'text', text: 'b'.repeat(40) }] },
    ] });
    host.session.mockResolvedValue(identity('node-one', value));
    const first = channel(), second = channel(); host.fetch.mockResolvedValueOnce(first.response).mockResolvedValueOnce(second.response);
    const { props, stream } = mount({ activeRunId: 'engine-b' }); await flush();
    first.send(event(10, 'snapshot', { snapshot: value })); await flush();
    expect(stream.snapshot.value?.messages.map(message => message.id)).toEqual(['current']);
    expect(stream.contextSnapshot.value?.messages).toHaveLength(3);
    expect(createTaskContextUsageReader()('node-one', stream.contextSnapshot.value, 'engine-b')).toMatchObject({ source: 'estimated' });
    expect(createTaskContextUsageReader()('node-one', stream.contextSnapshot.value, 'engine-b')!.used).toBeGreaterThan(2000);
    props.activeRunId = 'engine-a'; expect(stream.contextSnapshot.value).toBeNull(); await flush();
    second.send(event(10, 'snapshot', { snapshot: value })); await flush();
    expect(stream.snapshot.value?.messages.map(message => message.id)).toEqual(['old', 'old-answer']);
    expect(stream.contextSnapshot.value).toBeNull();
  });

  it("verifies scope before subscribing and folds multiple turns, live tools and gate decisions without waiting for completion", async () => {
    const wire = channel(); host.fetch.mockResolvedValue(wire.response);
    const { stream } = mount(); expect(host.fetch).not.toHaveBeenCalled(); await flush();
    expect(host.session).toHaveBeenCalledWith("node-one", expect.any(AbortSignal));
    expect(host.fetch).toHaveBeenCalledWith("/api/sessions/node-one/stream", expect.objectContaining({ headers: { Authorization: "Bearer test" } }));
    expect(stream.canDecide.value).toBe(false);
    wire.send(event(10, "snapshot", { snapshot: snapshot() }));
    wire.send(event(11, "run_start", { run_id: "engine-one" }));
    wire.send(event(12, "user_message", { message_id: "u1", run_id: "engine-one", text: "整理资料" }));
    wire.send(event(13, "turn_start", { message_id: "m1", turn: 1 }));
    wire.send(event(14, "thinking", { text: "正在核对资料" }));
    wire.send(event(15, "token", { text: "先读来源。" })); await flush();
    expect(stream.connection.value).toBe("live"); expect(stream.canDecide.value).toBe(true);
    expect(stream.snapshot.value?.live).toMatchObject({ turn: 1, thinking: "正在核对资料", text: "先读来源。" });
    wire.send(event(16, "message", { message_id: "m1", content: [{ type: "thinking", text: "正在核对资料" }, { type: "tool_use", id: "c1", name: "bash", input: { command: "read" } }] }));
    wire.send(event(17, "tool_start", { call_id: "c1", name: "bash", args: { command: "read" } }));
    wire.send(event(18, "tool_partial", { call_id: "c1", text: "读取中…" }));
    wire.send(event(19, "gate_request", { call_id: "c1", gate_id: "gate-one", name: "bash", deadline: 5000 }));
    wire.send(event(20, "gate_deadline", { gate_id: "gate-one", frozen: true, remaining_ms: 1000 })); await flush();
    const timeline = buildTimeline(stream.snapshot.value!.messages, stream.snapshot.value!.results, stream.snapshot.value!.live);
    const turn = timeline.find(item => item.kind === "turn");
    expect(turn?.kind === "turn" && turn.calls[0]).toMatchObject({ running: true, liveOutput: "读取中…", gate: { frozen: true } });
    expect(stream.snapshot.value?.live.text).toBe("");
    wire.send(event(21, "gate_resolved", { gate_id: "gate-one" }));
    wire.send(event(22, "tool_end", { call_id: "c1", name: "bash", text: "资料已读", duration_ms: 5 }));
    wire.send(event(23, "turn_start", { message_id: "m2", turn: 2 }));
    wire.send(event(24, "token", { text: "报告完成" }));
    wire.send(event(25, "message", { message_id: "m2", content: [{ type: "text", text: "报告完成" }] }));
    wire.send(event(26, "run_end", { end_reason: "completed", usage: { input: 2, output: 3 } })); await flush();
    expect(stream.snapshot.value?.live.pending_gates).toEqual([]);
    expect(stream.snapshot.value?.results.c1?.text).toBe("资料已读");
    expect(stream.snapshot.value?.run.active).toBe(false);
    expect(buildExchanges(buildTimeline(stream.snapshot.value!.messages, stream.snapshot.value!.results, stream.snapshot.value!.live), false)[0]?.answer?.text).toBe("报告完成");
  });

  it("revalidates after outages, disables approvals, and requests a new snapshot rather than replaying partial deltas", async () => {
    const first = channel(), second = channel(); host.fetch.mockResolvedValueOnce(first.response).mockResolvedValueOnce(second.response);
    const { stream } = mount(); await flush();
    first.send(event(10, "snapshot", { snapshot: snapshot({ live: { ...emptyLive(), active: true, text: "旧片段" }, run: { active: true } }) })); await flush();
    expect(stream.canDecide.value).toBe(true); first.fail(); await flush();
    expect(stream.connection.value).toBe("reconnecting"); expect(stream.canDecide.value).toBe(false); expect(stream.verifiedSession.value).toBeNull();
    host.session.mockResolvedValueOnce(identity("node-one", snapshot({ seq: 30, live: { ...emptyLive(), active: true, text: "恢复快照" }, run: { active: true } })));
    await vi.advanceTimersByTimeAsync(500); await flush();
    expect(host.session).toHaveBeenCalledTimes(2); expect(stream.canDecide.value).toBe(false);
    expect(host.fetch.mock.calls.every(([url]) => url === "/api/sessions/node-one/stream")).toBe(true);
    second.send(event(31, "token", { text: "不可应用的早到增量" })); await flush();
    expect(stream.snapshot.value?.live.text).toBe("恢复快照");
    second.send(event(32, "snapshot", { snapshot: snapshot({ seq: 32, live: { ...emptyLive(), active: true, text: "完整当前内容" }, run: { active: true } }) }));
    second.send(event(33, "token", { text: "，继续" })); second.send(event(33, "token", { text: "不能重复" })); await flush();
    expect(stream.snapshot.value?.live.text).toBe("完整当前内容，继续"); expect(stream.canDecide.value).toBe(true);
  });

  it("rejects foreign records before opening a stream and clears a revoked record on reconnect", async () => {
    host.session.mockResolvedValueOnce({ ...identity(), agent_workflow: { ...identity().agent_workflow, agent_id: "agent-b" } });
    const bad = mount(); await flush(); expect(host.fetch).not.toHaveBeenCalled();
    expect(bad.stream.error.value).toBe("scopeChanged"); expect(bad.stream.snapshot.value).toBeNull(); bad.scope.stop();
    const wire = channel(); host.fetch.mockResolvedValue(wire.response); const { stream } = mount(); await flush();
    wire.send(event(10, "snapshot", { snapshot: snapshot({ messages: [{ id: "private", role: "assistant", content: [{ type: "text", text: "private" }] }] }) })); await flush();
    wire.close(); await flush();
    host.session.mockRejectedValueOnce(Object.assign(new Error("Denied"), { status: 403 }));
    await vi.advanceTimersByTimeAsync(500); await flush();
    expect(stream.connection.value).toBe("error"); expect(stream.snapshot.value).toBeNull(); expect(stream.canDecide.value).toBe(false);
    const attempts = host.session.mock.calls.length; await vi.advanceTimersByTimeAsync(60000); expect(host.session).toHaveBeenCalledTimes(attempts);
  });

  it("aborts old node/session reads on selection, hiding and authentication changes", async () => {
    let resolve!: (value: ReturnType<typeof identity>) => void;
    host.session.mockReturnValueOnce(new Promise<ReturnType<typeof identity>>(done => { resolve = done; }));
    const wire = channel(); host.fetch.mockResolvedValue(wire.response);
    const { props, stream } = mount(); const oldSignal = host.session.mock.calls[0]![1] as AbortSignal;
    props.sessionId = "node-two"; await flush(); expect(oldSignal.aborted).toBe(true);
    resolve(identity("node-one", snapshot({ messages: [{ id: "old", role: "assistant", content: [{ type: "text", text: "old private" }] }] }))); await flush();
    expect(host.fetch).toHaveBeenCalledTimes(1); expect(stream.verifiedSession.value).toBe("node-two"); expect(stream.snapshot.value?.messages).toEqual([]);
    wire.send(event(10, "snapshot", { snapshot: snapshot() })); await flush();
    const signal = host.fetch.mock.calls[0]![1].signal as AbortSignal;
    props.visible = false; expect(signal.aborted).toBe(true); expect(stream.snapshot.value).toBeNull(); expect(stream.canDecide.value).toBe(false);
    props.visible = true; const next = channel(); host.fetch.mockResolvedValue(next.response); await flush();
    const before = host.session.mock.calls.length; window.dispatchEvent(new Event("pogo:auth-changed")); await flush();
    expect(host.session.mock.calls.length).toBe(before + 1); expect(stream.canDecide.value).toBe(false);
  });

  it("continues to support authenticated background task sessions", async () => {
    host.session.mockResolvedValue({ session_id: "task-one", background_task: { source: "schedule", task_id: "schedule:job-one", source_id: "job-one", agent_id: "agent-a" }, snapshot: snapshot() });
    const wire = channel(); host.fetch.mockResolvedValue(wire.response);
    const { stream } = mount({ sessionId: "task-one", workflowId: undefined, workflowRunId: undefined }); await flush();
    wire.send(event(10, "snapshot", { snapshot: snapshot() })); await flush();
    expect(stream.verifiedSession.value).toBe("task-one"); expect(stream.connection.value).toBe("live");
  });

  it("rechecks long-lived permissions without overwriting live deltas and closes when access is revoked", async () => {
    const wire = channel(); host.fetch.mockResolvedValue(wire.response);
    const { stream } = mount(); await flush();
    wire.send(event(10, "snapshot", { snapshot: snapshot() }));
    wire.send(event(11, "run_start", { run_id: "engine-one" }));
    wire.send(event(12, "token", { text: "最新实时内容" })); await flush();
    host.session.mockResolvedValueOnce(identity("node-one", snapshot({ seq: 1 })));
    await vi.advanceTimersByTimeAsync(15000); await flush();
    expect(host.session).toHaveBeenCalledTimes(2); expect(stream.snapshot.value?.live.text).toBe("最新实时内容"); expect(stream.canDecide.value).toBe(true);
    host.session.mockRejectedValueOnce(Object.assign(new Error("Denied"), { status: 403 }));
    await vi.advanceTimersByTimeAsync(15000); await flush();
    expect(stream.connection.value).toBe("error"); expect(stream.verifiedSession.value).toBeNull(); expect(stream.snapshot.value).toBeNull(); expect(stream.canDecide.value).toBe(false);
    expect((host.fetch.mock.calls[0]![1].signal as AbortSignal).aborted).toBe(true);
  });
});

describe("task snapshot projection", () => {
  it("isolates sequential node messages, tool results, live activity and approvals by engine run", () => {
    const value = snapshot({ messages: [
      { id: "u1", role: "user", run_id: "engine-a", content: [{ type: "text", text: "A" }] },
      { id: "m1", role: "assistant", content: [{ type: "tool_use", id: "call-a", name: "read", input: {} }] },
      { id: "u2", role: "user", run_id: "engine-b", content: [{ type: "text", text: "B" }] },
      { id: "m2", role: "assistant", content: [{ type: "tool_use", id: "call-b", name: "read", input: {} }] },
    ], results: { "call-a": { call_id: "call-a", text: "A result" }, "call-b": { call_id: "call-b", text: "B result" } },
      run: { active: true, run_id: "engine-b" }, live: { ...emptyLive(), active: true, run_id: "engine-b", thinking: "B thinking", pending_tools: [{ call_id: "call-b", name: "read", started_at: 1 }], pending_gates: [{ gate_id: "g-b", call_id: "call-b", tool: "read", deadline: 10 }] } });
    const a = projectTaskSessionSnapshot(value, "engine-a").snapshot!, b = projectTaskSessionSnapshot(value, "engine-b").snapshot!;
    expect(a.messages.map(message => message.id)).toEqual(["u1", "m1"]); expect(Object.keys(a.results)).toEqual(["call-a"]);
    expect(a.live.thinking).toBeUndefined(); expect(a.live.pending_gates).toEqual([]); expect(a.run.active).toBe(false);
    expect(b.messages.map(message => message.id)).toEqual(["u2", "m2"]); expect(b.live.thinking).toBe("B thinking"); expect(b.live.pending_gates[0]?.gate_id).toBe("g-b");
    expect(value.messages).toHaveLength(4);
    const legacy = projectTaskSessionSnapshot(snapshot({ messages: [{ id: "u", role: "user", content: [{ type: "text", text: "old" }] }] }), "engine-a");
    expect(legacy.isolationMissing).toBe(true); expect(legacy.snapshot?.messages).toEqual([]);
  });

  it("preserves bounded argument previews, child progress and settled frames with no browser or attachment effects", () => {
    let value: Snapshot | null = snapshot();
    const fold = (type: AgentEvent["type"], fields: Partial<AgentEvent>) => { value = reduceTaskSessionEvent(value, event(value!.seq + 1, type, fields)); };
    fold("tool_args", { call_id: "c", name: "write", text: "x".repeat(20000) });
    expect(value!.live.incoming?.[0]?.head).toHaveLength(4096); expect(value!.live.incoming?.[0]?.tail).toHaveLength(8192);
    fold("tool_start", { call_id: "c", name: "write" }); expect(value!.live.incoming).toEqual([]);
    fold("tool_partial", { call_id: "c", text: "y".repeat(50000), frame: { type: "token", text: "child" } });
    expect(value!.live.pending_tools[0]?.output).toHaveLength(32768);
    fold("tool_end", { call_id: "c", name: "write", text: "done" }); expect(value!.results.c?.frames?.[0]?.text).toBe("child");
    fold("subagent_start", { task_id: "child", name: "subagent", text: "review data" });
    fold("subagent_frame", { task_id: "child", frame: { type: "thinking", text: "working" } });
    fold("subagent_end", { task_id: "child", frames: [{ type: "thinking", text: "working" }, { type: "token", text: "done" }] });
    expect(value!.bg_tasks?.[0]).toMatchObject({ status: "done", frames: [{ text: "working" }, { text: "done" }] });
    fold("office_open", { path: "report.docx" }); fold("attachment_uploaded", {});
    expect(value!.messages).toEqual([]); expect(value!.live.pending_tools).toEqual([]);
  });
});
