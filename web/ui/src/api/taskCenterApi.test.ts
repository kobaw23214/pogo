import { beforeEach, describe, expect, it, vi } from "vitest";
const transport = vi.hoisted(() => vi.fn());
vi.mock("./client", () => ({ taskCenterRequest: transport }));
import { taskCenterApi } from "./taskCenterApi";
beforeEach(() => transport.mockReset().mockResolvedValue({}));
describe("task center transport", () => {
  it("always queries an explicit Agent using the shared transport", async () => { const controller = new AbortController(); await taskCenterApi.list("agent a", controller.signal); expect(transport).toHaveBeenCalledWith("GET", "/api/tasks?agent_id=agent+a", undefined, controller.signal); expect(() => taskCenterApi.list("")).toThrow(); });
  it("keeps definition controls distinct from per-run controls", async () => { await taskCenterApi.setEnabled("sentinel", "source/a", false); await taskCenterApi.cancel("run/a", 4); await taskCenterApi.retry("run/a", 4); expect(transport.mock.calls).toEqual([["POST", "/api/tasks/sentinel/source%2Fa/pause"], ["POST", "/api/task-runs/run%2Fa/cancel", { revision: 4 }], ["POST", "/api/task-runs/run%2Fa/retry", { revision: 4 }]]); });
  it("never sends a revisionless or unknown control", () => { expect(() => taskCenterApi.cancel("run", 0)).toThrow(); expect(() => taskCenterApi.retry("run", Number.NaN)).toThrow(); expect(() => taskCenterApi.setEnabled("anything" as "schedule", "source", true)).toThrow(); expect(transport).not.toHaveBeenCalled(); });
});
