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
describe("conversation Skill configuration synchronization", () => {
  it("scopes the write to the exact Agent and session and preserves deferred state", async () => {
    const result = { changed: false, deferred: true, revision: 2, desired_revision: 3 };
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify(result), { status: 200 }));
    expect(await client.api.syncStudioSkillConfig("agent/id", "session/id")).toEqual(result);
    expect(fetchResponse.mock.calls[0]![0]).toBe("/api/apps/agent%2Fid/sessions/session%2Fid/skill-config/sync");
    expect(fetchResponse.mock.calls[0]![1]).toMatchObject({ method: "POST", body: "{}", headers: { Authorization: "Bearer fixture-token" } });
  });
  it("does not hide a scoped denial by retrying a global catalog", async () => {
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify({ error: "not found" }), { status: 404 }));
    await expect(client.api.syncStudioSkillConfig("agent-a", "foreign-session")).rejects.toMatchObject({ status: 404 });
    expect(fetchResponse).toHaveBeenCalledTimes(1);
  });
});
