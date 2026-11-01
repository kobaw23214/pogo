import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("./client", () => ({ authHeaders: () => ({ Authorization: "Bearer account" }) }));
import { platformApi } from "./platform";

afterEach(() => vi.unstubAllGlobals());

describe("workflow access requests", () => {
  it("keeps explicit denial distinct from restoring inherited access", async () => {
    const fetcher = vi.fn().mockImplementation(async () => new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetcher);
    await platformApi.workflows.setAccess("workflow", { principal_id: "user-a", actions: [] });
    await platformApi.workflows.setAccess("workflow", { principal_id: "user-a", inherit: true });
    const first = fetcher.mock.calls[0] as [string, RequestInit];
    const second = fetcher.mock.calls[1] as [string, RequestInit];
    expect(first[0]).toBe("/api/manage/workflows/workflow/access");
    expect(first[1].method).toBe("PUT");
    expect(JSON.parse(first[1].body as string)).toEqual({ principal_id: "user-a", actions: [] });
    expect(JSON.parse(second[1].body as string)).toEqual({ principal_id: "user-a", inherit: true });
  });

  it("revokes only the specified participant scope without sending an owner identity", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetcher);
    await platformApi.workflowRuns.setParticipant("workflow", "run-a", { principal_id: "reviewer", actions: [], node_id: "review" });
    const [path, request] = fetcher.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/api/workflows/workflow/runs/run-a/participants");
    expect(JSON.parse(request.body as string)).toEqual({ principal_id: "reviewer", actions: [], node_id: "review" });
    expect(request.headers).toMatchObject({ Authorization: "Bearer account" });
  });
});
