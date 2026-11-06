import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const fetchResponse = vi.fn();
let client: typeof import("./client");
beforeEach(async () => {
  vi.resetModules();
  fetchResponse.mockReset();
  vi.stubGlobal("location", { origin: "https://pogo.test", href: "https://pogo.test/", search: "", pathname: "/" });
  vi.stubGlobal("sessionStorage", { getItem: () => "fixture-token", setItem() {} });
  vi.stubGlobal("fetch", fetchResponse);
  client = await import("./client");
});
afterEach(() => vi.unstubAllGlobals());

describe("MCP invocation catalog scope", () => {
  it("uses the effective conversation catalog when a draft Agent is also provided", async () => {
    const result = { tools: [], mcp: [], mcp_methods: [{ server: "docs", name: "search", wire_name: "mcp__docs__search", description: "Search documents" }] };
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify(result), { status: 200 }));
    expect(await client.api.tools("session/id", "agent/id")).toEqual(result);
    expect(fetchResponse.mock.calls[0]![0]).toBe("/api/tools?session_id=session%2Fid");
    expect(fetchResponse.mock.calls[0]![1]).toMatchObject({ method: "GET", headers: { Authorization: "Bearer fixture-token" } });
  });

  it("requests an Agent-scoped draft catalog without creating a conversation", async () => {
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify({ tools: [], mcp: [], mcp_methods: [] }), { status: 200 }));
    await client.api.tools(undefined, "agent/id");
    expect(fetchResponse).toHaveBeenCalledTimes(1);
    expect(fetchResponse.mock.calls[0]![0]).toBe("/api/tools?agent_id=agent%2Fid");
    expect(fetchResponse.mock.calls[0]![1]).toMatchObject({ method: "GET" });
  });

  it("keeps a denied scoped catalog as an error without fetching broader methods", async () => {
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify({ error: "not found" }), { status: 404 }));
    await expect(client.api.tools("foreign-session", "agent-a")).rejects.toMatchObject({ status: 404 });
    expect(fetchResponse).toHaveBeenCalledTimes(1);
  });
});
