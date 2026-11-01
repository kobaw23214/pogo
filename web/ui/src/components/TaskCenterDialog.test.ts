import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import type { BackgroundTask, BackgroundTaskCenter, BackgroundTaskRun } from "@/api/taskCenterApi";
import type { ScheduleDraft } from "@/agent/schedulePresentation";
import type { ScheduleJob } from "@/api/types";
const host = vi.hoisted(() => ({ list: vi.fn(), enabled: vi.fn(), cancel: vi.fn(), retry: vi.fn(), detail: vi.fn(), history: vi.fn(), runNow: vi.fn(), remove: vi.fn(), cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount: (fn: () => void) => host.cleanup.push(fn), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async () => { const { ref } = await import("vue"); return { useI18n: () => ({ t: (key: string) => key, locale: ref("zh-CN") }) }; });
vi.mock("@/api/taskCenterApi", () => ({ taskCenterApi: { list: host.list, setEnabled: host.enabled, cancel: host.cancel, retry: host.retry } }));
vi.mock("@/api/client", () => ({ api: { scheduleJob: host.detail, scheduleRuns: host.history, scheduleRunNow: host.runNow, scheduleDeleteJob: host.remove } }));
vi.mock("./ScheduleEditorDialog.vue", () => ({ default: {} })); vi.mock("./OrderedMessageContent.vue", () => ({ default: {} })); vi.mock("./ConfirmDialog.vue", () => ({ default: {} }));
import TaskCenterDialog from "./TaskCenterDialog.vue";
interface Controller {
  tasks: Ref<BackgroundTask[]>; runs: Ref<BackgroundTaskRun[]>; view: Ref<"tasks" | "queue" | "history">; filter: Ref<string>; visibleRuns: Ref<BackgroundTaskRun[]>; queuedRuns: Ref<BackgroundTaskRun[]>; historyRuns: Ref<BackgroundTaskRun[]>; queueError: Ref<string>; loadError: Ref<string>; loading: Ref<boolean>; attention: Ref<boolean>;
  selectedTaskId: Ref<string>; selectedRunId: Ref<string>; selectedInterruptedRun: Ref<BackgroundTaskRun | null>; selectedSchedule: Ref<ScheduleJob | null>; detailError: Ref<string>; editorOpen: Ref<boolean>; editing: Ref<ScheduleJob | null>; editorDraft: Ref<ScheduleDraft | null>; editorVersion: Ref<number>;
  editor: Ref<{ confirmLeave(): Promise<boolean> } | null>; confirm: Ref<{ ask(options: unknown): Promise<boolean> } | null>;
  load(silent?: boolean): Promise<void>; loadTaskDetail(task: BackgroundTask): Promise<void>; switchView(view: "tasks" | "queue" | "history"): void; openCreate(draft?: ScheduleDraft | null): Promise<void>; openEdit(task: BackgroundTask): Promise<void>; close(): Promise<void>; saved(job: ScheduleJob): Promise<void>; requestDraft(): Promise<void>;
  mutateTask(task: BackgroundTask, action: "pause" | "resume" | "run" | "remove"): Promise<void>; mutateRun(run: BackgroundTaskRun, action: "cancel" | "retry"): Promise<void>; openRunResult(run: BackgroundTaskRun): void; openTaskResult(task: BackgroundTask): void; canOpenRunResult(run: BackgroundTaskRun): boolean; canOpenTaskResult(task: BackgroundTask): boolean; showRelatedRun(run: BackgroundTaskRun): void; editable(task: BackgroundTask): boolean; canRunNow(task: BackgroundTask): boolean; canToggle(task: BackgroundTask): boolean;
}
const scopes: ReturnType<typeof effectScope>[] = [];
const task = (extra: Partial<BackgroundTask> = {}): BackgroundTask => ({ id: "schedule:j1", source: "schedule", source_id: "j1", title: "A 晨报", description: "整理新闻", agent_id: "a", trigger: "time", executor: "agent", enabled: true, state: "ready", queued_count: 0, running_count: 0, pending_events: 0, attention: false, can_manage: true, ...extra });
const watchTask = (): BackgroundTask => task({ id: "sentinel:w1", source: "sentinel", source_id: "w1", title: "B 业务监听", trigger: "condition", state: "watching" });
const run = (extra: Partial<BackgroundTaskRun> = {}): BackgroundTaskRun => ({ id: "r1", task_id: "schedule:j1", source: "schedule", source_id: "j1", title: "A 晨报", agent_id: "a", trigger: "manual", status: "queued", revision: 3, attempt: 1, cancel_requested: false, created_at: 100, can_cancel: true, can_retry: false, waiting_human: false, ...extra });
const job = (extra: Partial<ScheduleJob> = {}): ScheduleJob => ({ id: "j1", title: "晨报", prompt: "整理新闻", agent_id: "a", workdir: "", kind: "every", every_sec: 60, enabled: true, created_at: "2026-10-23T10:00:00Z", next_run: null, ...extra });
const info = (extra: Partial<BackgroundTaskCenter> = {}): BackgroundTaskCenter => ({ tasks: [task(), watchTask()], runs: [], ...extra });
function pending<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done; }); return { promise, resolve }; }
async function flush() { for (let n = 0; n < 8; n++) await Promise.resolve(); await nextTick(); }
function mount(extra: Record<string, unknown> = {}) {
  const props = reactive({ modelValue: true, agentId: "a", agentName: "助理", sessionId: "s1", agents: [], models: [], initialDraft: null as ScheduleDraft | null, isAdmin: false, draftBusy: false, ...extra });
  const emit = vi.fn(), scope = effectScope(); scopes.push(scope);
  const c = scope.run(() => (TaskCenterDialog as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  return { c, props, emit };
}
beforeEach(() => {
  vi.useFakeTimers(); host.list.mockReset().mockResolvedValue(info()); host.enabled.mockReset().mockResolvedValue({}); host.cancel.mockReset().mockResolvedValue({}); host.retry.mockReset().mockResolvedValue({}); host.detail.mockReset().mockResolvedValue({ job: job() }); host.history.mockReset().mockResolvedValue({ runs: [], log_tail: [], log_path: "" }); host.runNow.mockReset().mockResolvedValue({}); host.remove.mockReset().mockResolvedValue({});
});
afterEach(() => { for (const cleanup of host.cleanup.splice(0)) cleanup(); for (const scope of scopes.splice(0)) scope.stop(); vi.useRealTimers(); });
describe("Background task center", () => {
  it("blocks immediate running after the latest execution was interrupted and directs review to that history", async () => {
    const interrupted = run({ id: "unreviewed", status: "interrupted", can_retry: true });
    host.list.mockResolvedValueOnce(info({ runs: [interrupted] }));
    const { c } = mount(); await flush();
    expect(c.canRunNow(task())).toBe(false);
    expect(c.canRunNow(task({ id: "schedule:other", source_id: "other" }))).toBe(true);
    expect(c.selectedInterruptedRun.value?.id).toBe(interrupted.id);
    await c.mutateTask(task(), "run"); expect(host.runNow).not.toHaveBeenCalled();
    c.showRelatedRun(c.selectedInterruptedRun.value!);
    expect(c.view.value).toBe("history"); expect(c.selectedRunId.value).toBe(interrupted.id);
  });
  it("does not permanently block a plan whose newer explicit retry has completed", async () => {
    const interrupted = run({ id: "old", status: "interrupted", created_at: 1 });
    const completed = run({ id: "retry", status: "completed", trigger: "retry", created_at: 2, attempt: 2 });
    host.list.mockResolvedValueOnce(info({ runs: [completed, interrupted] }));
    const { c } = mount(); await flush();
    expect(c.canRunNow(task())).toBe(true); expect(c.selectedInterruptedRun.value).toBeNull();
    await c.mutateTask(task(), "run"); expect(host.runNow).toHaveBeenCalledWith("j1");
  });
  it("requires an Agent and never expands administrators into a global task view", async () => { const { c } = mount({ agentId: "", isAdmin: true }); await flush(); expect(host.list).not.toHaveBeenCalled(); await c.openCreate(); expect(c.editorOpen.value).toBe(false); });
  it("lists both schedules and listeners within the current Agent only", async () => { host.list.mockResolvedValueOnce(info({ tasks: [task(), watchTask(), task({ id: "schedule:foreign", agent_id: "b" })], runs: [run({ id: "foreign", agent_id: "b" })] })); const { c } = mount({ isAdmin: true }); await flush(); expect(c.tasks.value.map(item => item.id)).toEqual(["schedule:j1", "sentinel:w1"]); expect(c.runs.value).toEqual([]); expect(host.list).toHaveBeenCalledWith("a", expect.any(AbortSignal)); });
  it("rejects a late list after Agent switch and aborts the previous transport", async () => { const result = pending<BackgroundTaskCenter>(); host.list.mockReturnValueOnce(result.promise); const { c, props } = mount(); const signal = host.list.mock.calls[0][1] as AbortSignal; host.list.mockResolvedValueOnce(info({ tasks: [task({ id: "schedule:b", agent_id: "b" })] })); props.agentId = "b"; await flush(); expect(signal.aborted).toBe(true); result.resolve(info()); await flush(); expect(c.tasks.value.map(item => item.agent_id)).toEqual(["b"]); });
  it("does not disguise queue load failure as an empty successful state", async () => { host.list.mockResolvedValueOnce(info({ queue_error: "database unavailable" })); const { c, emit } = mount(); await flush(); expect(c.tasks.value).toHaveLength(2); expect(c.queueError.value).toBe("database unavailable"); expect(c.attention.value).toBe(true); expect(emit).toHaveBeenCalledWith("attention", true); });
  it("keeps human waits in the execution view and terminal results in history", async () => { host.list.mockResolvedValueOnce(info({ runs: [run(), run({ id: "waiting", status: "completed", waiting_human: true }), run({ id: "done", status: "completed" })] })); const { c } = mount(); await flush(); expect(c.queuedRuns.value.map(item => item.id)).toEqual(["r1", "waiting"]); expect(c.historyRuns.value.map(item => item.id)).toEqual(["done"]); });
  it("blocks editing, deletion and another run while a human wait has no active execution counter", async () => { host.list.mockResolvedValueOnce(info({ runs: [run({ status: "completed", waiting_human: true })] })); const { c } = mount(); await flush(); host.detail.mockClear(); c.confirm.value = { ask: vi.fn().mockResolvedValue(true) }; expect(c.editable(task())).toBe(false); expect(c.canRunNow(task())).toBe(false); expect(c.editable(task({ id: "schedule:other", source_id: "other" }))).toBe(true); await c.openEdit(task()); await c.mutateTask(task(), "run"); await c.mutateTask(task(), "remove"); expect(host.detail).not.toHaveBeenCalled(); expect(host.runNow).not.toHaveBeenCalled(); expect(host.remove).not.toHaveBeenCalled(); expect(c.editorOpen.value).toBe(false); });
  it("keeps cancelled sentinel reconciliation in history attention without treating normal cancellation as an error", async () => { host.list.mockResolvedValueOnce(info({ runs: [run({ id: "unresolved", source: "sentinel", task_id: "sentinel:w1", status: "cancelled", can_retry: true }), run({ id: "settled", source: "sentinel", task_id: "sentinel:w2", status: "cancelled" })] })); const { c } = mount(); await flush(); c.switchView("history"); c.filter.value = "attention"; expect(c.visibleRuns.value.map(item => item.id)).toEqual(["unresolved"]); expect(c.queuedRuns.value).toHaveLength(0); });
  it("pauses the standing task through its own API, leaving the current run alone", async () => { const { c } = mount(); await flush(); await c.mutateTask(watchTask(), "pause"); expect(host.enabled).toHaveBeenCalledWith("sentinel", "w1", false); expect(host.cancel).not.toHaveBeenCalled(); expect(host.runNow).not.toHaveBeenCalled(); });
  it("cancels only the chosen run using its revision, without pausing its definition", async () => { host.list.mockResolvedValue(info({ runs: [run({ status: "running", cancel_requested: true })] })); const { c } = mount(); await flush(); c.confirm.value = { ask: vi.fn().mockResolvedValue(true) }; await c.mutateRun(run({ status: "running" }), "cancel"); expect(host.cancel).toHaveBeenCalledWith("r1", 3); expect(host.enabled).not.toHaveBeenCalled(); expect(c.runs.value[0].status).toBe("running"); expect(c.runs.value[0].cancel_requested).toBe(true); });
  it("requires an explicit retry confirmation and preserves the old run", async () => { const failed = run({ status: "interrupted", can_retry: true }); host.list.mockResolvedValue(info({ runs: [failed] })); const { c } = mount(); await flush(); c.confirm.value = { ask: vi.fn().mockResolvedValue(false) }; await c.mutateRun(failed, "retry"); expect(host.retry).not.toHaveBeenCalled(); c.confirm.value = { ask: vi.fn().mockResolvedValue(true) }; await c.mutateRun(failed, "retry"); expect(host.retry).toHaveBeenCalledWith("r1", 3); expect(c.runs.value[0].status).toBe("interrupted"); expect(c.view.value).toBe("queue"); });
  it("does not execute a run action after confirmation outlives its Agent", async () => { const answer = pending<boolean>(); const { c, props } = mount(); await flush(); c.confirm.value = { ask: () => answer.promise }; const action = c.mutateRun(run(), "cancel"); props.agentId = "b"; await flush(); answer.resolve(true); await action; expect(host.cancel).not.toHaveBeenCalled(); });
  it("blocks direct foreign task and run actions", async () => { const { c, emit } = mount(); await flush(); c.confirm.value = { ask: vi.fn().mockResolvedValue(true) }; await c.mutateTask(task({ agent_id: "b" }), "run"); await c.mutateRun(run({ agent_id: "b" }), "cancel"); c.openRunResult(run({ agent_id: "b", session_id: "foreign" })); expect(host.runNow).not.toHaveBeenCalled(); expect(host.cancel).not.toHaveBeenCalled(); expect(emit).not.toHaveBeenCalledWith("open-session", expect.anything(), expect.anything(), expect.anything()); });
  it("preserves the dirty schedule editor when close/create/edit is declined", async () => { const { c, emit } = mount(); await flush(); c.editorOpen.value = true; c.editing.value = job(); c.editor.value = { confirmLeave: vi.fn().mockResolvedValue(false) }; await c.openCreate(); await c.openEdit(task()); await c.close(); expect(c.editorOpen.value).toBe(true); expect(c.editing.value?.id).toBe("j1"); expect(emit).not.toHaveBeenCalledWith("update:modelValue", false); });
  it("rejects late or mismatched schedule detail responses", async () => { const { c, props } = mount(); await flush(); const result = pending<{ job: ScheduleJob }>(); host.detail.mockReturnValueOnce(result.promise); const edit = c.openEdit(task()); props.modelValue = false; await flush(); result.resolve({ job: job() }); await edit; expect(c.editorOpen.value).toBe(false); props.modelValue = true; await flush(); host.detail.mockResolvedValueOnce({ job: job({ id: "other" }) }); await c.openEdit(task()); expect(c.editorOpen.value).toBe(false); });
  it("routes native results correctly and rejects unsafe result links", async () => { const { c, emit } = mount(); await flush(); c.openRunResult(run({ session_id: "session-result" })); expect(emit).toHaveBeenCalledWith("open-task", "schedule:j1", "A 晨报", "a", "session-result"); c.openRunResult(run({ workflow_id: "wf", session_id: "wf-run" })); expect(emit).toHaveBeenCalledWith("open-workflow-run", "wf-run", "wf"); c.openRunResult(run({ result_url: "https://external.example/" })); expect(emit.mock.calls.filter(call => call[0] === "open-task")).toHaveLength(1); });
  it("opens a sentinel sheet before its first event creates a worker conversation", async () => {
    const { c, emit } = mount(); await flush();
    const watch = { ...watchTask(), parent_session_id: "main" };
    expect(watch.result_url).toBeUndefined(); expect(watch.session_id).toBeUndefined();
    expect(c.canOpenTaskResult(watch)).toBe(true);
    c.openTaskResult(watch);
    expect(emit).toHaveBeenCalledWith("open-sentinel", "w1", "B 业务监听", "a", "main");
    expect(emit.mock.calls.some(call => call[0] === "open-session" || call[0] === "open-workflow-run")).toBe(false);
  });
  it("keeps an existing sentinel worker out of ordinary chat navigation", async () => {
    const { c, emit } = mount(); await flush();
    c.openTaskResult({ ...watchTask(), sentinel_id: "w1", parent_session_id: "main", session_id: "worker", result_url: "/manage/agents/a/chat?c=worker" });
    expect(emit).toHaveBeenCalledWith("open-sentinel", "w1", "B 业务监听", "a", "main");
    expect(emit.mock.calls.some(call => call[0] === "open-session")).toBe(false);
  });
  it("opens sentinel approval and progress in the same sheet, preserving its parent", async () => {
    host.list.mockResolvedValueOnce(info({ tasks: [task(), { ...watchTask(), parent_session_id: "main-from-task" }] }));
    const { c, emit } = mount(); await flush();
    const waiting = run({ source: "sentinel", source_id: "w1", task_id: "sentinel:w1", session_id: "worker", status: "running", waiting_human: true });
    expect(c.canOpenRunResult(waiting)).toBe(true);
    c.openRunResult(waiting);
    expect(emit).toHaveBeenCalledWith("open-sentinel", "w1", "A 晨报", "a", "main-from-task");
    c.openRunResult({ ...waiting, parent_session_id: "main-from-run" });
    expect(emit).toHaveBeenLastCalledWith("open-sentinel", "w1", "A 晨报", "a", "main-from-run");
    expect(emit.mock.calls.some(call => call[0] === "open-session" || call[0] === "open-workflow-run")).toBe(false);
  });
  it("rejects foreign, mismatched and stale sentinel result requests", async () => {
    const { c, emit, props } = mount(); await flush();
    const foreign = { ...watchTask(), agent_id: "b" };
    expect(c.canOpenTaskResult(foreign)).toBe(false); c.openTaskResult(foreign);
    const mismatched = { ...watchTask(), sentinel_id: "other" };
    expect(c.canOpenTaskResult(mismatched)).toBe(false); c.openTaskResult(mismatched);
    const badRun = run({ source: "sentinel", source_id: "../other", session_id: "worker" });
    expect(c.canOpenRunResult(badRun)).toBe(false); c.openRunResult(badRun);
    props.modelValue = false; await flush();
    c.openTaskResult(watchTask()); c.openRunResult(run({ source: "sentinel", source_id: "w1", session_id: "worker" }));
    expect(emit.mock.calls.some(call => ["open-sentinel", "open-session", "open-workflow-run"].includes(call[0]))).toBe(false);
  });
  it("retains schedule result conversations and workflow viewers", async () => {
    host.detail.mockResolvedValueOnce({ job: job({ session_id: "schedule-result" }) });
    const { c, emit } = mount(); await flush();
    c.openTaskResult(task());
    expect(emit).toHaveBeenCalledWith("open-task", "schedule:j1", "A 晨报", "a", "schedule-result");
    c.openTaskResult(task({ id: "schedule:wf-trigger", source_id: "wf-trigger", workflow_id: "wf", executor: "workflow", result_url: "/manage/workflows/wf?run=workflow-result" }));
    expect(emit).toHaveBeenCalledWith("open-workflow-run", "workflow-result", "wf");
    expect(emit.mock.calls.some(call => call[0] === "open-sentinel")).toBe(false);
  });
  it("keeps expired tasks visible without offering a resume or manual run", async () => { const { c } = mount(); await flush(); const expired = task({ enabled: false, state: "expired", expires_at: Date.now() - 1000 }); expect(c.canToggle(expired)).toBe(false); expect(c.canRunNow(expired)).toBe(false); await c.mutateTask(expired, "resume"); await c.mutateTask(expired, "run"); expect(host.enabled).not.toHaveBeenCalled(); expect(host.runNow).not.toHaveBeenCalled(); });
  it("starts a paused schedule once without enabling it and then opens the queue", async () => { const { c } = mount(); await flush(); const paused = task({ enabled: false, state: "paused" }); expect(c.canRunNow(paused)).toBe(true); await c.mutateTask(paused, "run"); expect(host.runNow).toHaveBeenCalledWith("j1"); expect(host.enabled).not.toHaveBeenCalled(); expect(c.view.value).toBe("queue"); expect(c.canRunNow(task({ queued_count: 1 }))).toBe(false); });
  it("retains historical schedule lookup without changing composer message delivery", async () => { const { c } = mount(); await flush(); expect(host.history).toHaveBeenCalledWith("j1"); expect(c.selectedSchedule.value?.prompt).toBe("整理新闻"); await c.saved(job({ agent_id: "b" })); expect(c.selectedTaskId.value).toBe("schedule:j1"); });
});
