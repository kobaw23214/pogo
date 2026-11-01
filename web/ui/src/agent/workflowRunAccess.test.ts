import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { nextTick, ref } from "vue";
import type { WorkflowRun } from "./workflowTypes";

const hooks = vi.hoisted(() => ({ cleanup: [] as (() => void)[] }));
const api = vi.hoisted(() => ({ get: vi.fn(), list: vi.fn(), start: vi.fn(), cancel: vi.fn(), retry: vi.fn(), streamUrl: vi.fn() }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount: (callback: () => void) => hooks.cleanup.push(callback) }));
vi.mock("@/api/platform", () => ({ platformApi: { workflowRuns: api } }));
import { useWorkflowRun } from "./useWorkflowRun";

function sample(id: string, live = false): WorkflowRun {
  return { id, workflow_id: "wf", revision: 1, status: live ? "working" : "completed", created_at: "2026-10-27T10:00:00Z",
    definition: { id: "wf", name: "Review", description: "", revision: 1, nodes: [], published: null, dirty: false, created_at: "", updated_at: "" },
    nodes: { review: { status: live ? "waiting_human" : "success", summary: "private result", human: { prompt: "private review material", actions: ["approve"], deadline: "2026-10-30T10:00:00Z", on_timeout: "reject" } } },
    capabilities: { can_read: true, can_cancel: true, can_retry: false, can_approve: live, can_read_artifacts: true, can_manage_participants: false } };
}
const denied = (status: number) => Object.assign(new Error("Run is unavailable"), { status });
async function settled() { await Promise.resolve(); await nextTick(); await Promise.resolve(); }

beforeEach(() => {
  vi.resetAllMocks();
  vi.useFakeTimers();
  vi.stubGlobal("window", { setTimeout: globalThis.setTimeout, clearTimeout: globalThis.clearTimeout });
  vi.stubGlobal("document", { hidden: false });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 503, body: null }));
  api.get.mockImplementation(async (_workflow: string, id: string) => sample(id));
  api.list.mockResolvedValue({ runs: [] });
  api.streamUrl.mockReturnValue("/mock/workflow-stream");
});
afterEach(() => {
  hooks.cleanup.splice(0).forEach(cleanup => cleanup());
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("workflow access revocation", () => {
  it.each([401, 403, 404] as const)("clears private current state and its invalid history after HTTP %i", async status => {
    const state = useWorkflowRun(ref("wf"));
    api.list.mockResolvedValue({ runs: [sample("old"), sample("other")] });
    await state.loadHistory();
    await state.loadRun("old");
    api.get.mockRejectedValueOnce(denied(status));
    await state.refresh();
    expect(state.run.value).toBeNull();
    expect(state.accessError.value).toBe(status);
    expect(state.history.value.map(item => item.id)).toEqual(status === 401 ? [] : ["other"]);
    expect(state.mode.value).toBe("idle");
  });

  it("stops the actual human-wait polling loop when permission is revoked", async () => {
    const state = useWorkflowRun(ref("wf"));
    api.get.mockResolvedValueOnce(sample("active", true));
    await state.loadRun("active");
    await settled();
    expect(state.mode.value).toBe("poll");
    api.get.mockRejectedValueOnce(denied(404));
    await vi.advanceTimersByTimeAsync(5000);
    expect(state.run.value).toBeNull();
    expect(state.accessError.value).toBe(404);
    expect(state.mode.value).toBe("idle");
    expect(vi.getTimerCount()).toBe(0);
  });

  it("does not clear a newly selected run when an older refresh fails late", async () => {
    const state = useWorkflowRun(ref("wf"));
    await state.loadRun("old");
    let reject!: (failure: Error) => void;
    api.get.mockReturnValueOnce(new Promise((_resolve, fail) => { reject = fail; }));
    const oldRefresh = state.refresh();
    await state.loadRun("new");
    reject(denied(404));
    await oldRefresh;
    expect(state.run.value?.id).toBe("new");
    expect(state.accessError.value).toBeNull();
    expect(state.error.value).toBe("");
  });

  it("does not apply a late forbidden SSE handshake to a newer selection", async () => {
    let resolve!: (response: Response) => void;
    vi.mocked(fetch).mockReturnValueOnce(new Promise<Response>(done => { resolve = done; }));
    const state = useWorkflowRun(ref("wf"));
    api.get.mockResolvedValueOnce(sample("old", true));
    await state.loadRun("old");
    await state.loadRun("new");
    resolve(new Response(null, { status: 403 }));
    await settled();
    expect(state.run.value?.id).toBe("new");
    expect(state.accessError.value).toBeNull();
  });

  it("cannot repopulate revoked history from an in-flight list", async () => {
    const state = useWorkflowRun(ref("wf"));
    await state.loadRun("old");
    let resolve!: (response: { runs: WorkflowRun[] }) => void;
    api.list.mockReturnValueOnce(new Promise(done => { resolve = done; }));
    const listing = state.loadHistory();
    api.get.mockRejectedValueOnce(denied(404));
    await state.refresh();
    resolve({ runs: [sample("old")] });
    await listing;
    expect(state.history.value).toEqual([]);
    expect(state.run.value).toBeNull();
    expect(state.historyLoading.value).toBe(false);
  });

  it("does not clear a new selection because an older history listing fails late", async () => {
    const state = useWorkflowRun(ref("wf"));
    await state.loadRun("old");
    let reject!: (failure: Error) => void;
    api.list.mockReturnValueOnce(new Promise((_resolve, fail) => { reject = fail; }));
    const oldListing = state.loadHistory();
    await state.loadRun("new");
    reject(denied(403));
    await oldListing;
    expect(state.run.value?.id).toBe("new");
    expect(state.accessError.value).toBeNull();
  });

  it("retains the current run for a transient server error", async () => {
    const state = useWorkflowRun(ref("wf"));
    await state.loadRun("old");
    api.get.mockRejectedValueOnce(denied(503));
    await state.refresh();
    expect(state.run.value?.id).toBe("old");
    expect(state.accessError.value).toBeNull();
  });
});
