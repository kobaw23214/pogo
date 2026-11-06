import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive } from "vue";
import type { WebServiceChange, WebServiceRecord } from "@/api/webServices";
const host = vi.hoisted(() => ({ list: vi.fn(), status: vi.fn(), logs: vi.fn(), action: vi.fn(), start: vi.fn(), rename: vi.fn(), remove: vi.fn() }));
vi.mock("@/api/client", () => ({ api: { webServices: host.list, webService: host.status, webServiceLogs: host.logs, webServiceAction: host.action, startWebService: host.start, renameWebService: host.rename, deleteWebService: host.remove } }));
vi.mock("@/i18n", () => ({ gt: (key: string) => key }));
import { useWebServices } from "./useWebServices";
const record = (generation = 1, revision = generation): WebServiceRecord => ({ service_id: "ws_one", name: "counter", generation, revision, state: "ready", profile: "python-web", project_root: "app", lease_expires_at: new Date(Date.now() + 7200000).toISOString() });
const expiredRecord = (): WebServiceRecord => ({ ...record(), state: "expired", lease_expires_at: new Date(Date.now() - 1000).toISOString(), spec: { name: "counter", project_root: "app", profile: "python-web", start_argv: ["python3", "app.py"], health_path: "/", lease_seconds: 10 } });
const scopes: ReturnType<typeof effectScope>[] = [];
function deferred<T>() { let resolve!: (value: T) => void, reject!: (reason: unknown) => void; const promise = new Promise<T>((r, j) => { resolve = r; reject = j; }); return { promise, resolve, reject }; }
async function flush() { for (let i = 0; i < 12; i++) await Promise.resolve(); await nextTick(); }
function mount(onDeleted = vi.fn()) {
  const context = reactive({ sessionId: "first", enabled: true, connected: true, change: null as WebServiceChange | null });
  const scope = effectScope(); scopes.push(scope); const state = scope.run(() => useWebServices(() => context, onDeleted))!;
  return { state, context, onDeleted };
}
beforeEach(() => {
  vi.useFakeTimers(); host.list.mockReset().mockResolvedValue({ services: [record()] }); host.status.mockReset().mockResolvedValue(record());
  host.logs.mockReset().mockResolvedValue({ generation: 1, from_cursor: "", next_cursor: "next", text: "first log", truncated: false, eof: false });
  host.action.mockReset().mockResolvedValue({ ...record(), operation_status: "accepted" });
  host.rename.mockReset().mockResolvedValue({ ...record(), operation_status: "completed" });
  host.start.mockReset().mockResolvedValue({ ...record(2), operation_status: "accepted" });
  host.remove.mockReset().mockResolvedValue(undefined);
});
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); vi.useRealTimers(); });
describe("current conversation websites", () => {
  it("removes a confirmed website and its log without reviving it from old lists or events", async () => {
    const { state, context, onDeleted } = mount(); await flush(); await state.readLog(record());
    expect(await state.remove(record())).toBe(true); await flush();
    expect(host.remove).toHaveBeenCalledExactlyOnceWith("first", "ws_one", expect.any(AbortSignal));
    expect(state.records.value).toEqual([]); expect(state.selected.value).toBeNull(); expect(state.log.value).toBeNull();
    state.accept(record(5, 100)); context.change = { sessionId: "first", record: record(6, 200) }; await flush(); await state.refresh();
    expect(state.records.value).toEqual([]); expect(onDeleted).toHaveBeenCalledExactlyOnceWith({ sessionId: "first", serviceId: "ws_one" });
  });
  it("blocks duplicate deletion and other controls until the deletion request settles", async () => {
    const { state } = mount(); await flush(); const pending = deferred<void>(); host.remove.mockReturnValueOnce(pending.promise);
    const removing = state.remove(record()); expect(state.busy.value.ws_one).toBe("delete");
    expect(await state.remove(record())).toBe(false); await state.action(record(), "restart"); await state.readLog(record());
    expect(host.remove).toHaveBeenCalledOnce(); expect(host.action).not.toHaveBeenCalled(); expect(host.logs).not.toHaveBeenCalled();
    pending.resolve(); expect(await removing).toBe(true); expect(state.busy.value.ws_one).toBeUndefined();
  });
  it("keeps a lost deletion visible and retries only that service", async () => {
    const { state, onDeleted } = mount(); await flush(); host.remove.mockRejectedValueOnce(new Error("Connection lost"));
    expect(await state.remove(record())).toBe(false); await flush();
    expect(state.records.value).toHaveLength(1); expect(state.deleting.value.ws_one).toBe(true); expect(state.deleteErrors.value.ws_one).toBe("webServices.deleteUnknown");
    await state.action(record(), "renew", 7200); expect(host.action).not.toHaveBeenCalled(); expect(onDeleted).not.toHaveBeenCalled();
    expect(await state.remove(record())).toBe(true); expect(host.remove.mock.calls.map(call => call.slice(0, 2))).toEqual([["first", "ws_one"], ["first", "ws_one"]]);
    expect(state.records.value).toEqual([]); expect(state.deleteErrors.value.ws_one).toBeUndefined();
  });
  it("does not mistake an unsupported DELETE endpoint for successful deletion", async () => {
    const { state, onDeleted } = mount(); await flush(); host.remove.mockRejectedValueOnce(Object.assign(new Error("HTTP 404"), { status: 404 }));
    expect(await state.remove(record())).toBe(false); await flush();
    expect(state.records.value).toHaveLength(1); expect(state.deleteErrors.value.ws_one).toBe("HTTP 404"); expect(state.deleting.value.ws_one).toBeUndefined(); expect(onDeleted).not.toHaveBeenCalled();
  });
  it("shows a friendly cleanup message without declaring a pending deletion complete", async () => {
    const { state, onDeleted } = mount(); await flush();
    host.remove.mockRejectedValueOnce(Object.assign(new Error("SERVICE_DELETE_PENDING: cleanup incomplete"), { status: 503, code: "SERVICE_DELETE_PENDING" }));
    await state.remove(record()); await flush();
    expect(state.records.value).toHaveLength(1); expect(state.deleting.value.ws_one).toBe(true); expect(state.deleteErrors.value.ws_one).toBe("webServices.deletePending"); expect(onDeleted).not.toHaveBeenCalled();
  });
  it("confirms a lost deletion when a list started afterward no longer contains the service", async () => {
    const { state, onDeleted } = mount(); await flush();
    host.remove.mockRejectedValueOnce(Object.assign(new Error("Cleanup pending"), { status: 503, code: "SERVICE_DELETE_PENDING" })); host.list.mockResolvedValue({ services: [] });
    await state.remove(record()); await flush();
    expect(state.records.value).toEqual([]); expect(state.deleting.value.ws_one).toBeUndefined(); expect(state.deleteErrors.value.ws_one).toBeUndefined(); expect(onDeleted).toHaveBeenCalledOnce();
  });
  it("does not confirm a lost deletion using a list started before the request", async () => {
    const { state, onDeleted } = mount(); await flush(); const oldList = deferred<{ services: WebServiceRecord[] }>();
    host.list.mockReturnValueOnce(oldList.promise); const refreshing = state.refresh(); host.remove.mockRejectedValueOnce(new Error("lost"));
    await state.remove(record()); oldList.resolve({ services: [] }); await refreshing; await flush();
    expect(state.records.value).toHaveLength(1); expect(state.deleting.value.ws_one).toBe(true); expect(onDeleted).not.toHaveBeenCalled();
    host.list.mockResolvedValue({ services: [] }); await state.refresh(); expect(state.records.value).toEqual([]); expect(onDeleted).toHaveBeenCalledOnce();
  });
  it("ignores a late action reply after an authoritative removal", async () => {
    const { state, onDeleted } = mount(); await flush(); const actionReply = deferred<WebServiceRecord>(); host.action.mockReturnValueOnce(actionReply.promise);
    const acting = state.action(record(), "restart"); host.list.mockResolvedValue({ services: [] }); await state.refresh();
    expect(state.records.value).toEqual([]); actionReply.resolve({ ...record(3), operation_status: "completed" }); await acting; await flush();
    expect(state.records.value).toEqual([]); expect(onDeleted).toHaveBeenCalledOnce();
  });
  it("does not report an old conversation's late deletion in the new one", async () => {
    const { state, context, onDeleted } = mount(); await flush(); const reply = deferred<void>(); host.remove.mockReturnValueOnce(reply.promise);
    const removing = state.remove(record()); context.sessionId = "second"; await flush(); reply.resolve(); expect(await removing).toBe(false);
    expect(state.records.value).toHaveLength(1); expect(state.deleting.value).toEqual({}); expect(onDeleted).not.toHaveBeenCalled();
  });
  it("blocks controls for a persisted deletion intent while permitting deletion retry", async () => {
    const deleting = { ...record(), state: "stopping" as const, phase: "deleting" }; host.list.mockResolvedValue({ services: [deleting] });
    const { state } = mount(); await flush(); await state.action(deleting, "restart"); await state.readLog(deleting);
    expect(host.action).not.toHaveBeenCalled(); expect(host.logs).not.toHaveBeenCalled(); expect(await state.remove(deleting)).toBe(true);
  });
  it("keeps a cold view deleted when a terminal deletion event precedes an old ready event", async () => {
    host.list.mockResolvedValue({ services: [] }); const { state, context, onDeleted } = mount(); await flush();
    context.change = { sessionId: "first", record: { ...record(2, 9), state: "stopped", phase: "deleted" } }; await flush();
    context.change = { sessionId: "first", record: record(2, 8) }; await flush();
    expect(state.records.value).toEqual([]); expect(onDeleted).toHaveBeenCalledExactlyOnceWith({ sessionId: "first", serviceId: "ws_one" });
  });
  it("ignores an in-flight operation status failure after a terminal deletion event", async () => {
    const { state, context } = mount(); await flush(); const status = deferred<WebServiceRecord>(); host.status.mockReturnValueOnce(status.promise);
    await state.action(record(), "restart"); await flush();
    context.change = { sessionId: "first", record: { ...record(2, 9), state: "stopped", phase: "deleted" } }; await flush();
    status.reject(Object.assign(new Error("gone"), { status: 404 })); await state.refresh();
    expect(state.records.value).toEqual([]); expect(state.error.value).toBe(""); expect(state.stale.value).toBe(false);
  });
  it("loads platform default duration and scoped capacity for the website card", async () => {
    const quota = { session: { current: 2, limit: 2 }, user: { current: 3, limit: 4 }, total: { current: 6, limit: 16 } };
    host.list.mockResolvedValue({ services: [record()], quota, default_lease_seconds: 86400 });
    const { state } = mount(); await flush();
    expect(state.quota.value).toEqual(quota); expect(state.defaultLeaseSeconds.value).toBe(86400);
  });
  it("retries a lost custom renewal with its original duration and identity", async () => {
    const { state } = mount(); await flush();
    host.action.mockRejectedValueOnce(new Error("lost")); await state.action(record(), "renew", 259200);
    await state.action(record(), "renew", 3600);
    expect(host.action.mock.calls[0]![3]).toBe(host.action.mock.calls[1]![3]);
    expect(host.action.mock.calls[1]![5]).toBe(259200);
  });
  it("preserves a renamed Chinese display label when starting the same expired slot", async () => {
    const expired = { ...expiredRecord(), name: "我的网站", slot_name: "counter" };
    const { state } = mount(); await flush(); await state.action(expired, "start");
    expect(host.start.mock.calls[0]![1]).toMatchObject({ name: "counter", display_name: "我的网站" });
  });
  it("renames without invoking restart and captures the original name after a lost response", async () => {
    const { state } = mount(); await flush();
    host.rename.mockRejectedValueOnce(new Error("lost")); await state.action(record(), "rename", "中文网站");
    await state.action(record(), "rename", "另一个名字");
    expect(host.rename.mock.calls[0]![3]).toBe(host.rename.mock.calls[1]![3]);
    expect(host.rename.mock.calls[1]![2]).toBe("中文网站"); expect(host.action).not.toHaveBeenCalled();
  });
  it("loads service truth and ignores late lower-generation and lower-revision events", async () => {
    const { state } = mount(); await flush(); state.accept(record(2, 8)); state.accept(record(1, 99)); state.accept(record(2, 7));
    expect(state.records.value[0]?.generation).toBe(2); expect(state.records.value[0]?.revision).toBe(8);
  });
  it("does not install a late list from the conversation switched away from", async () => {
    const late = deferred<{ services: WebServiceRecord[] }>(); host.list.mockReturnValueOnce(late.promise).mockResolvedValue({ services: [] });
    const { state, context } = mount(); context.sessionId = "second"; await flush();
    late.resolve({ services: [record()] }); await flush(); expect(state.records.value).toEqual([]);
  });
  it("waits for the in-flight owner list when revealing a deep link", async () => {
    const late = deferred<{ services: WebServiceRecord[] }>(); host.list.mockReturnValueOnce(late.promise);
    const { state } = mount(); const reveal = state.reveal("ws_one"); late.resolve({ services: [record()] });
    expect((await reveal)?.service_id).toBe("ws_one"); expect(host.list).toHaveBeenCalledOnce();
  });
  it("clears a previous-generation log and discards its delayed page", async () => {
    const { state } = mount(); await flush(); await state.readLog(record()); expect(state.log.value?.text).toBe("first log");
    const late = deferred<unknown>(); host.logs.mockReturnValueOnce(late.promise);
    const reading = state.readLog(record(), true); state.accept(record(2)); expect(state.log.value).toBeNull();
    late.resolve({ generation: 1, text: "old", next_cursor: "next", eof: true }); await reading; expect(state.log.value).toBeNull();
  });
  it("reuses an uncertain operation after a lost response but does not replay a definite 4xx failure forever", async () => {
    const { state } = mount(); await flush();
    host.action.mockRejectedValueOnce(new Error("lost response")); await state.action(record(), "restart");
    host.action.mockRejectedValueOnce(Object.assign(new Error("conflict"), { status: 409 })); await state.action(record(), "restart");
    await state.action(record(), "restart");
    expect(host.action.mock.calls[0]![3]).toBe(host.action.mock.calls[1]![3]); expect(host.action.mock.calls[2]![3]).not.toBe(host.action.mock.calls[1]![3]);
  });
  it("keeps the entry absent when the feature is disabled and does not keep polling it", async () => {
    host.list.mockRejectedValue(Object.assign(new Error("disabled"), { code: "FEATURE_DISABLED", status: 503 }));
    const { state } = mount(); await flush(); await vi.advanceTimersByTimeAsync(30000);
    expect(state.visible.value).toBe(false); expect(host.list).toHaveBeenCalledOnce();
  });
  it("accepts a scoped live update even when the Agent's tool selection is no longer involved", async () => {
    host.list.mockResolvedValue({ services: [] }); const { state, context } = mount(); await flush();
    context.change = { sessionId: "other", record: record() }; await nextTick(); expect(state.records.value).toEqual([]);
    context.change = { sessionId: "first", record: record() }; await nextTick(); expect(state.visible.value).toBe(true);
  });
  it("does not let a delayed old operation status clear a newer stop request", async () => {
    const { state } = mount(); await flush();
    const late = deferred<WebServiceRecord>(); host.status.mockReturnValueOnce(late.promise);
    await state.action(record(), "restart"); await flush();
    const oldId = state.operations.value.ws_one!.id;
    host.action.mockResolvedValueOnce({ ...record(1, 3), state: "stopping", operation_status: "accepted" });
    await state.action(record(), "stop"); const newId = state.operations.value.ws_one!.id;
    late.resolve({ ...record(), operation_status: "completed", operation_id: oldId } as WebServiceRecord); await flush();
    expect(state.operations.value.ws_one?.id).toBe(newId); expect(state.records.value[0]?.state).toBe("stopping");
  });
  it("starts a new lease using the retained slot/spec instead of renewing or restarting an expired runtime", async () => {
    const expired = expiredRecord(); host.list.mockResolvedValue({ services: [expired] }); const { state } = mount(); await flush();
    await state.action(expired, "start");
    expect(host.start).toHaveBeenCalledExactlyOnceWith("first", { name: "counter", display_name: "counter", project_root: "app", profile: "python-web", start_argv: ["python3", "app.py"], health_path: "/" }, expect.any(String), expect.any(AbortSignal));
    expect(host.action).not.toHaveBeenCalled(); expect(state.records.value[0]?.generation).toBe(2);
  });
  it("does not start while stopping is unconfirmed or reusable startup settings are absent", async () => {
    const { state } = mount(); await flush(); const expired = expiredRecord();
    await state.action({ ...expired, state: "stopping" }, "start"); expect(state.actionErrors.value.ws_one).toBe("webServices.waitForStop");
    await state.action({ ...expired, spec: undefined }, "start"); expect(state.actionErrors.value.ws_one).toBe("webServices.missingSpec");
    expect(host.start).not.toHaveBeenCalled(); expect(host.action).not.toHaveBeenCalled();
  });
  it("retries a lost fresh-start receipt with the same operation and captured spec, even after a newer state arrives", async () => {
    const expired = expiredRecord(); const { state } = mount(); await flush();
    host.start.mockRejectedValueOnce(new Error("lost response")); await state.action(expired, "start");
    (expired.spec as { start_argv: string[] }).start_argv[1] = "changed.py";
    const newer = { ...record(2), spec: undefined }; state.accept(newer); await state.action(newer, "start");
    expect(host.start.mock.calls[0]![2]).toBe(host.start.mock.calls[1]![2]); expect(host.start.mock.calls[1]![1].start_argv).toEqual(["python3", "app.py"]);
    expect(host.action).not.toHaveBeenCalled();
  });
  it("uses a new operation when a definite fresh-start rejection is corrected", async () => {
    const expired = expiredRecord(); const { state } = mount(); await flush();
    host.start.mockRejectedValueOnce(Object.assign(new Error("quota"), { status: 409 })); await state.action(expired, "start");
    expect(state.operations.value.ws_one).toBeUndefined(); await state.action(expired, "start");
    expect(host.start.mock.calls[0]![2]).not.toBe(host.start.mock.calls[1]![2]);
  });
});
