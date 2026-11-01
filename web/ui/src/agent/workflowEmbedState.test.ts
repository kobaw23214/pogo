import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createWorkflowEmbedState } from "./useWorkflowEmbed";
import type { createWorkflowEmbedApi, WorkflowEmbedSession } from "@/api/workflowEmbed";
import type { WorkflowRun } from "./workflowTypes";

const session: WorkflowEmbedSession = { expires_at: "2026-11-01T00:00:00Z", subject: "alice", parent_origin: "https://host.example", can_invoke: true, publication: { id: "pub", workflow_id: "wf", version_id: "v1", name: "Review", enabled: true, input_fields: [{ key: "case", label: "Case", required: true }] }, inputs: { case: "case-1" } };
function sample(id = "run-old", status: WorkflowRun["status"] = "working"): WorkflowRun {
  return { id, workflow_id: "wf", status, created_at: "2026-10-27T00:00:00Z", revision: 1, definition: { id: "wf", name: "Review", description: "", nodes: [], published: null, dirty: false, revision: 1, created_at: "", updated_at: "" }, nodes: { review: { status: "waiting_human", human: { prompt: "Review", actions: ["approve", "reject"], deadline: "2026-11-01T00:00:00Z", on_timeout: "reject", fields: [{ key: "note", required: true }] } } }, capabilities: { can_read: true, can_cancel: true, can_retry: status === "failed", can_approve: true, can_approve_nodes: ["review"], can_read_artifacts: true, can_manage_participants: false } };
}
const states: ReturnType<typeof createWorkflowEmbedState>[] = [];
function boot() {
  const api = { exchange: vi.fn().mockResolvedValue(session), clear: vi.fn(), session: vi.fn().mockResolvedValue(session), list: vi.fn().mockResolvedValue({ runs: [sample()] }), get: vi.fn().mockImplementation(async (id: string) => sample(id)), start: vi.fn().mockResolvedValue({ id: "run-new" }), cancel: vi.fn().mockResolvedValue({ cancelled: true }), retry: vi.fn().mockResolvedValue({ id: "run-retry" }), resume: vi.fn().mockResolvedValue(sample()), artifact: vi.fn().mockResolvedValue("private artifact") };
  const selected = vi.fn(); const changed = vi.fn();
  const state = createWorkflowEmbedState(api as ReturnType<typeof createWorkflowEmbedApi>, selected, changed);
  states.push(state);
  return { state, api, selected, changed };
}
beforeEach(() => { vi.useFakeTimers(); });
afterEach(() => { states.splice(0).forEach(state => state.invalidate()); vi.clearAllTimers(); vi.useRealTimers(); });

describe("embedded workflow lifecycle", () => {
  it("treats prototype-looking input keys as ordinary form data", async () => {
    const { state, api } = boot();
    api.exchange.mockResolvedValueOnce({ ...session, inputs: JSON.parse('{"constructor":"case-2","__proto__":"case-3"}') });
    await state.authenticate("code", "https://host.example", null);
    expect(state.inputs.constructor).toBe("case-2"); expect(state.inputs.__proto__).toBe("case-3");
    const fieldKey: string = "toString";
    state.inputs[fieldKey] = "case-4";
    await state.start();
    expect(api.start.mock.calls[0]![0]).toEqual(JSON.parse('{"constructor":"case-2","__proto__":"case-3","toString":"case-4"}'));
  });
  it("retains a pending form through actual human-wait polling", async () => {
    const { state, api } = boot();
    await state.authenticate("code", "https://host.example", "run-old");
    state.approvalValues("review").note = "Keep this note";
    await vi.advanceTimersByTimeAsync(5000);
    expect(api.get).toHaveBeenCalledTimes(2);
    expect(state.approvalValues("review").note).toBe("Keep this note");
    await state.decide("review", "approve");
    expect(api.resume).toHaveBeenCalledWith("run-old", "review", "approve", { note: "Keep this note" });
  });
  it.each([401, 403, 404])("clears private data and stops polling after %i", async status => {
    const { state, api } = boot();
    await state.authenticate("code", "https://host.example", "run-old");
    state.approvalValues("review").note = "private draft";
    await state.readArtifact("review");
    api.session.mockRejectedValueOnce(Object.assign(new Error("gone"), { status }));
    await vi.advanceTimersByTimeAsync(5000);
    expect(state.session.value).toBeNull(); expect(state.run.value).toBeNull(); expect(state.history.value).toEqual([]); expect(state.approvals).toEqual({}); expect(state.artifacts).toEqual({}); expect(state.unavailable.value).toBe(true); expect(vi.getTimerCount()).toBe(0);
  });
  it("keeps the selected result on a transient backend outage", async () => {
    const { state, api } = boot();
    await state.authenticate("code", "https://host.example", "run-old");
    api.session.mockRejectedValueOnce(Object.assign(new Error("temporary"), { status: 503 }));
    await state.refresh(); expect(state.run.value?.id).toBe("run-old"); expect(state.error.value).toBe("temporary");
    await vi.advanceTimersByTimeAsync(5000); expect(state.error.value).toBe("");
  });
  it("reuses an unresolved start intent but generates a new run selection on success", async () => {
    const { state, api, selected } = boot();
    await state.authenticate("code", "https://host.example", null);
    api.start.mockRejectedValueOnce(Object.assign(new Error("network uncertain"), { status: 503 }));
    await state.start(); await state.start();
    expect(api.start.mock.calls[0]![1]).toBe(api.start.mock.calls[1]![1]);
    expect(api.start.mock.calls[1]![0]).toEqual({ case: "case-1" }); expect(selected).toHaveBeenLastCalledWith("run-new");
  });
  it("follows a retry to its new Run without replacing the original history", async () => {
    const { state, api, selected } = boot();
    api.get.mockResolvedValueOnce(sample("run-old", "failed"));
    await state.authenticate("code", "https://host.example", "run-old");
    await state.retry(); expect(selected).toHaveBeenLastCalledWith("run-retry"); expect(state.history.value.map(run => run.id)).toContain("run-old");
  });
  it("reuses a retry intent when its receipt was lost", async () => {
    const { state, api } = boot();
    api.get.mockResolvedValueOnce(sample("run-old", "failed"));
    await state.authenticate("code", "https://host.example", "run-old");
    api.retry.mockRejectedValueOnce(Object.assign(new Error("receipt lost"), { status: 503 }));
    await state.retry(); await state.retry();
    expect(api.retry).toHaveBeenCalledTimes(2);
    expect(api.retry.mock.calls[0]![0]).toBe("run-old");
    expect(api.retry.mock.calls[0]![1]).toBeTruthy();
    expect(api.retry.mock.calls[0]![1]).toBe(api.retry.mock.calls[1]![1]);
    expect(state.run.value?.id).toBe("run-retry");
  });
  it("does not let late action responses restore a previous selection", async () => {
    const { state, api } = boot();
    await state.authenticate("code", "https://host.example", null);
    let resolve!: (value: { id: string }) => void;
    api.start.mockReturnValueOnce(new Promise(done => { resolve = done; }));
    const pending = state.start(); await state.select("chosen-later");
    resolve({ id: "late-run" }); await pending;
    expect(state.run.value?.id).toBe("chosen-later");
  });
  it("uses explicit capabilities for approval, cancel and retry", async () => {
    const { state, api } = boot();
    api.get.mockResolvedValueOnce({ ...sample(), capabilities: { can_read: true, can_cancel: false, can_retry: false, can_approve: false, can_read_artifacts: false, can_manage_participants: false } });
    await state.authenticate("code", "https://host.example", "run-old");
    await state.decide("review", "approve"); await state.cancel(); await state.retry(); await state.readArtifact("review");
    expect(api.resume).not.toHaveBeenCalled(); expect(api.cancel).not.toHaveBeenCalled(); expect(api.retry).not.toHaveBeenCalled(); expect(api.artifact).not.toHaveBeenCalled();
  });
});
