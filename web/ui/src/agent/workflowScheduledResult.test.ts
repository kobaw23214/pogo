import { beforeEach, describe, expect, it, vi } from "vitest";
import { ref } from "vue";
import type { WorkflowRun } from "./workflowTypes";

const api = vi.hoisted(() => ({ get: vi.fn(), list: vi.fn(), start: vi.fn(), cancel: vi.fn() }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount: vi.fn() }));
vi.mock("@/api/platform", () => ({ platformApi: { workflowRuns: api } }));
import { useWorkflowRun } from "./useWorkflowRun";

function finished(id: string): WorkflowRun {
  return {
    id, workflow_id: "workflow", revision: 1, status: "completed", nodes: {}, created_at: "2026-10-18T12:00:00Z",
    definition: { id: "workflow", name: "Report", description: "", revision: 1, nodes: [], published: null, dirty: false, created_at: "", updated_at: "" },
  };
}

beforeEach(() => { vi.clearAllMocks(); });
describe("scheduled workflow result selection", () => {
  it("opens the requested historical result without substituting the latest run", async () => {
    api.get.mockResolvedValue(finished("historical"));
    const state = useWorkflowRun(ref("workflow"));
    expect(await state.loadRun("historical")).toBe(true);
    expect(api.get).toHaveBeenCalledWith("workflow", "historical");
    expect(api.list).not.toHaveBeenCalled();
    expect(state.run.value?.id).toBe("historical");
  });

  it("shows a missing-result error and does not silently open an unrelated result", async () => {
    api.get.mockRejectedValue(new Error("Run no longer exists"));
    const state = useWorkflowRun(ref("workflow"));
    expect(await state.loadRun("missing")).toBe(false);
    expect(state.error.value).toBe("Run no longer exists");
    expect(state.run.value).toBeNull();
    expect(api.list).not.toHaveBeenCalled();
  });

  it("ignores a late latest-list response after selecting a specific history entry", async () => {
    let complete!: (value: { runs: WorkflowRun[] }) => void;
    api.list.mockReturnValue(new Promise(resolve => { complete = resolve; }));
    api.get.mockResolvedValue(finished("chosen"));
    const state = useWorkflowRun(ref("workflow"));
    const latest = state.loadLatest();
    await state.loadRun("chosen");
    complete({ runs: [finished("newer-but-unselected")] });
    await latest;
    expect(state.run.value?.id).toBe("chosen");
  });

  it("refreshes history without replacing the explicitly selected run", async () => {
    api.get.mockResolvedValue(finished("chosen"));
    api.list.mockResolvedValue({ runs: [finished("other")] });
    const state = useWorkflowRun(ref("workflow"));
    await state.loadRun("chosen");
    await state.loadHistory();
    expect(state.run.value?.id).toBe("chosen");
    expect(state.history.value.map(item => item.id)).toEqual(["other"]);
  });
});
