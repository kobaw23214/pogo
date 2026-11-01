import { afterEach, describe, expect, it, vi } from "vitest";
import { agentWorkflowRequest } from "./client";
afterEach(() => vi.unstubAllGlobals());
describe("internal workflow request boundary", () => {
  it("never sends unknown paths or unsupported methods", () => {
    const fetch = vi.fn(); vi.stubGlobal("fetch", fetch);
    expect(() => agentWorkflowRequest("GET", "/api/manage/workflows")).toThrow();
    expect(() => agentWorkflowRequest("GET", "/api/apps/ag_1/agent-workflows/confirm")).toThrow();
    expect(() => agentWorkflowRequest("POST", "/api/apps/ag_1/agent-workflows/other", {})).toThrow();
    expect(() => agentWorkflowRequest("DELETE" as "POST", "/api/apps/ag_1/agent-workflows/edit", {})).toThrow();
    expect(() => agentWorkflowRequest("PUT", "/api/apps/ag_1/agent-workflows/edit", {})).toThrow();
    expect(() => agentWorkflowRequest("POST", "/api/apps/ag_1/agent-workflows/aw_1/layout", {})).toThrow();
    expect(() => agentWorkflowRequest("PUT", "/api/apps/ag_1/agent-workflows/aw_1/layout/extra", {})).toThrow();
    expect(fetch).not.toHaveBeenCalled();
  });
  it("retains server conflict identity and message without retrying a mutation", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: "revision_conflict", message: "草稿已变化" } }), { status: 409 })); vi.stubGlobal("fetch", fetch);
    await expect(agentWorkflowRequest("POST", "/api/apps/ag_1/agent-workflows/enable", { request_id: "one", expected_revision: 2 })).rejects.toMatchObject({ status: 409, code: "revision_conflict", message: "草稿已变化" });
    expect(fetch).toHaveBeenCalledTimes(1);
  });
  it("allows bounded layout GET and PUT with the shared authenticated transport", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ok: true, layout: { layout_revision: 1 } }), { status: 200 })); vi.stubGlobal("fetch", fetch);
    await agentWorkflowRequest("PUT", "/api/apps/ag_1/agent-workflows/aw_1/layout", { request_id: "layout-one" });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch.mock.calls[0][0]).toBe("/api/apps/ag_1/agent-workflows/aw_1/layout");
    expect(fetch.mock.calls[0][1].method).toBe("PUT");
  });
});
