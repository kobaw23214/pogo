import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const fetchResponse = vi.fn();
let client: typeof import("./client");
beforeEach(async () => {
  vi.resetModules(); fetchResponse.mockReset().mockResolvedValue(new Response(JSON.stringify({ content: "preview", truncated: false }), { status: 200 }));
  vi.stubGlobal("location", { origin: "https://pogo.test", href: "https://pogo.test/", search: "", pathname: "/" });
  vi.stubGlobal("sessionStorage", { getItem: () => "fixture-token", setItem() {} }); vi.stubGlobal("fetch", fetchResponse); client = await import("./client");
});
afterEach(() => vi.unstubAllGlobals());
describe("Skill package document content requests", () => {
  it("keeps existing root requests compatible and URL-encodes package paths separately from Skill identity", async () => {
    // A fresh response is needed for each request because bodies are consumed.
    fetchResponse.mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ content: "preview", truncated: false }), { status: 200 })));
    await client.api.managedSkillContent("skill/id"); await client.api.managedSkillContent("skill/id", "references/说明.txt");
    await client.api.skillContent("yx", "session-a"); await client.api.skillContent("yx", "session-a", "references/sql/workflow.md");
    expect(fetchResponse.mock.calls.map(row => row[0])).toEqual(["/api/manage/skills/skill%2Fid/content", "/api/manage/skills/skill%2Fid/content?path=references%2F%E8%AF%B4%E6%98%8E.txt", "/api/skills/content?name=yx&session_id=session-a", "/api/skills/content?name=yx&session_id=session-a&path=references%2Fsql%2Fworkflow.md"]);
    for (const row of fetchResponse.mock.calls) { expect(row[1].method).toBe("GET"); expect(row[1].headers.Authorization).toBe("Bearer fixture-token"); }
  });
  it("preserves authorization/not-found errors without retrying through global or physical file routes", async () => {
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify({ error: "Document not found" }), { status: 404 }));
    await expect(client.api.skillContent("yx", "session-a", "references/no.md")).rejects.toMatchObject({ status: 404 }); expect(fetchResponse).toHaveBeenCalledTimes(1);
  });
  it("loads only the requested package directory through the authenticated management route", async () => {
    fetchResponse.mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ relative_path: "", entries: [], truncated: false, limits: { max_entries: 1000, max_depth: 16 } }), { status: 200 })));
    await client.api.managedSkillFiles("skill/id"); await client.api.managedSkillFiles("skill/id", "references/示例");
    expect(fetchResponse.mock.calls.map(row => row[0])).toEqual(["/api/manage/skills/skill%2Fid/files", "/api/manage/skills/skill%2Fid/files?path=references%2F%E7%A4%BA%E4%BE%8B"]);
    expect(fetchResponse.mock.calls[1]![1].headers.Authorization).toBe("Bearer fixture-token");
  });
  it("writes only the managed root document with its expected revision and preserves server conflicts", async () => {
    const content = "---\nname: yx-business\ndescription: 业务助手\n---\n# 新说明\n";
    await client.api.saveManagedSkillContent("skill/id", content, "revision-a");
    expect(fetchResponse).toHaveBeenCalledWith("/api/manage/skills/skill%2Fid/content", expect.objectContaining({ method: "PUT", body: JSON.stringify({ content, expected_revision: "revision-a" }), headers: expect.objectContaining({ Authorization: "Bearer fixture-token" }) }));
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify({ error: "Document changed" }), { status: 409 }));
    await expect(client.api.saveManagedSkillContent("skill/id", content, "revision-a")).rejects.toMatchObject({ status: 409 });
    expect(fetchResponse).toHaveBeenCalledTimes(2);
  });
});
