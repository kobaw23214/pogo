import { afterEach, describe, expect, it, vi } from "vitest";
import { workflowPublications } from "./workflowPublication";

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });
describe("callback management transport", () => {
  it("uses the admin transport and explicitly acknowledges the current target for a manual retry", async () => {
    const fetcher = vi.fn().mockImplementation(async () => new Response('{"deliveries":[]}'));
    vi.stubGlobal("fetch", fetcher);
    await workflowPublications.deliveries("wf A", "pub A");
    await workflowPublications.retryDelivery("wf A", "pub A", "event A");
    await workflowPublications.rotateCallback("wf A", "pub A");
    expect(fetcher.mock.calls[0]![0]).toBe("/api/manage/workflows/wf%20A/publications/pub%20A/deliveries");
    expect(fetcher.mock.calls[0]![1]).toMatchObject({ method: "GET", credentials: "same-origin", cache: "no-store" });
    expect(fetcher.mock.calls[1]![0]).toBe("/api/manage/workflows/wf%20A/publications/pub%20A/deliveries/event%20A/retry");
    expect(JSON.parse(fetcher.mock.calls[1]![1].body)).toEqual({ use_current_target: true });
    expect(fetcher.mock.calls[2]![0]).toBe("/api/manage/workflows/wf%20A/publications/pub%20A/rotate-callback-secret");
  });
});
