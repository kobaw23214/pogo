import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const request = vi.fn(); let client: typeof import("./client");
beforeEach(async () => {
  vi.resetModules(); request.mockReset().mockImplementation(async () => new Response(JSON.stringify({ content: "skill", truncated: false }), { status: 200 }));
  vi.stubGlobal("location", { origin: "https://pogo.test", href: "https://pogo.test/", search: "", pathname: "/" }); vi.stubGlobal("sessionStorage", { getItem: () => "fixture-token", setItem() {} }); vi.stubGlobal("fetch", request); client = await import("./client");
});
afterEach(() => vi.unstubAllGlobals());
describe("Agent-owned Skill APIs", () => {
  it("keeps Agent, Skill, relative path and immutable revision scoped and separately encoded", async () => {
    await client.api.authoredSkills("agent/a"); await client.api.authoredSkillContent("agent/a", "skill/b", "references/例 子.md", "a".repeat(64));
    await client.api.authoredSkillFiles("agent/a", "skill/b", "references", "a".repeat(64)); await client.api.authoredSkillVersions("agent/a", "skill/b");
    expect(request.mock.calls.map(c => c[0])).toEqual(["/api/apps/agent%2Fa/authored-skills", `/api/apps/agent%2Fa/authored-skills/skill%2Fb/content?path=references%2F%E4%BE%8B+%E5%AD%90.md&revision=${"a".repeat(64)}`, `/api/apps/agent%2Fa/authored-skills/skill%2Fb/files?path=references&revision=${"a".repeat(64)}`, "/api/apps/agent%2Fa/authored-skills/skill%2Fb/versions"]);
    expect(request.mock.calls.every(c => c[1].headers.Authorization === "Bearer fixture-token")).toBe(true);
  });
  it("preserves whole-bundle save conflicts, without global API fallbacks", async () => {
    request.mockResolvedValueOnce(new Response(JSON.stringify({ error: "Revision changed" }), { status: 409 }));
    await expect(client.api.saveAuthoredSkillContent("ag_one", "as_one", "new", "a".repeat(64))).rejects.toMatchObject({ status: 409 });
    expect(request.mock.calls[0][1].body).toBe(JSON.stringify({ content: "new", expected_revision: "a".repeat(64) })); expect(request).toHaveBeenCalledTimes(1);
  });
  it("exports ZIP bytes through authentication and rejects an unexpected page", async () => {
    request.mockResolvedValueOnce(new Response("zip bytes", { status: 200, headers: { "Content-Type": "application/zip" } }));
    const blob = await client.api.exportAuthoredSkill("ag_one", "as_one", "rev"); expect(await blob.text()).toBe("zip bytes");
    request.mockResolvedValueOnce(new Response("<html>sign in</html>", { status: 200, headers: { "Content-Type": "text/html" } }));
    await expect(client.api.exportAuthoredSkill("ag_one", "as_one")).rejects.toThrow("ZIP");
  });
  it("refuses an empty scope before fetching a document", () => {
    expect(() => client.api.authoredSkillContent("", "as_one")).toThrow("scope"); expect(request).not.toHaveBeenCalled();
  });
});
