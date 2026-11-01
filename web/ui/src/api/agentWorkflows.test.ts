import { beforeEach, describe, expect, it, vi } from "vitest";
const transport = vi.hoisted(() => vi.fn());
vi.mock("./client", () => ({ agentWorkflowRequest: transport }));
import { agentWorkflowsApi } from "./agentWorkflows";
beforeEach(() => transport.mockReset().mockResolvedValue({ ok: true }));
describe("Agent internal workflow API scope", () => {
  it("uses a bound Agent route and keeps human confirmation a distinct operation", async () => {
    const controller = new AbortController();
    await agentWorkflowsApi.list("ag_one", controller.signal);
    await agentWorkflowsApi.call("ag_one", "confirm", { workflow_id: "af_one", run_id: "afr_one", step_id: "review", request_id: "confirm-once", feedback: "补充依据" });
    expect(transport.mock.calls).toEqual([
      ["GET", "/api/apps/ag_one/agent-workflows", undefined, controller.signal],
      ["POST", "/api/apps/ag_one/agent-workflows/confirm", { workflow_id: "af_one", run_id: "afr_one", step_id: "review", request_id: "confirm-once", feedback: "补充依据" }, undefined],
    ]);
  });
  it("rejects unbound and path-like Agent identities before making a request", () => {
    expect(() => agentWorkflowsApi.list("")).toThrow();
    expect(() => agentWorkflowsApi.list("../other")).toThrow();
    expect(transport).not.toHaveBeenCalled();
  });
  it("stores layout through a separate owner-bound route without semantic edits", async () => {
    const signal = new AbortController().signal;
    const body = { request_id: "layout-one", expected_layout_revision: 0, expected_definition_revision: 2, positions: { collect: { x: 10, y: 20 } } };
    await agentWorkflowsApi.getLayout("ag_one", "aw_one", signal);
    await agentWorkflowsApi.saveLayout("ag_one", "aw_one", body, signal);
    expect(transport.mock.calls).toEqual([
      ["GET", "/api/apps/ag_one/agent-workflows/aw_one/layout", undefined, signal],
      ["PUT", "/api/apps/ag_one/agent-workflows/aw_one/layout", body, signal],
    ]);
    expect(() => agentWorkflowsApi.getLayout("ag_one", "../other")).toThrow();
  });
});
