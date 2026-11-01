import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const fetchResponse = vi.fn();
let client: typeof import("./client");
beforeEach(async () => {
  vi.resetModules(); fetchResponse.mockReset().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ calls: [], call: { id: "call-a" } }), { status: 200 })));
  vi.stubGlobal("location", { origin: "https://pogo.test", href: "https://pogo.test/", search: "", pathname: "/" });
  vi.stubGlobal("sessionStorage", { getItem: () => "fixture-account-token", setItem() {} });
  vi.stubGlobal("fetch", fetchResponse); client = await import("./client");
});
afterEach(() => vi.unstubAllGlobals());
describe("Agent MCP history authenticated transport", () => {
  it("uses only the Agent calls/stats/detail routes and preserves the exact query", async () => {
    await client.api.mcpAgentCalls("ag_own", "from=10&to=20&server=demo&before=next"); await client.api.mcpAgentCallStats("ag_own", "from=10&to=20&server=demo"); await client.api.mcpAgentCall("ag_own", "id/encoded");
    expect(fetchResponse.mock.calls.map(row => row[0])).toEqual(["/api/apps/ag_own/mcp/calls?from=10&to=20&server=demo&before=next", "/api/apps/ag_own/mcp/calls/stats?from=10&to=20&server=demo", "/api/apps/ag_own/mcp/calls/id%2Fencoded"]);
    for (const row of fetchResponse.mock.calls) { expect(row[1].method).toBe("GET"); expect(row[1].headers.Authorization).toBe("Bearer fixture-account-token"); expect(row[1].body).toBeUndefined(); }
  });
  it.each(["", "../other", " ", "ag?all=1"])("rejects invalid or absent Agent %s before issuing any global request", id => {
    expect(() => client.api.mcpAgentCalls(id, "")).toThrow(); expect(() => client.api.mcpAgentCallStats(id, "")).toThrow(); expect(() => client.api.mcpAgentCall(id, "call-a")).toThrow(); expect(fetchResponse).not.toHaveBeenCalled();
  });
  it("keeps permission and cross-scope detail failures as errors without retry or global fallback", async () => {
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify({ error: "Call not found" }), { status: 404 }));
    await expect(client.api.mcpAgentCall("ag_own", "foreign")).rejects.toMatchObject({ status: 404 }); expect(fetchResponse).toHaveBeenCalledTimes(1);
    expect(fetchResponse.mock.calls[0]![0]).toBe("/api/apps/ag_own/mcp/calls/foreign");
  });
});
