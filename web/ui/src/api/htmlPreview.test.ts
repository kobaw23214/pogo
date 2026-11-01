import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const fetchResponse = vi.fn();
let client: typeof import("./client");
beforeEach(async () => {
  vi.resetModules(); fetchResponse.mockReset().mockImplementation(async () => new Response(JSON.stringify({ ok: true }), { status: 200 }));
  vi.stubGlobal("location", { origin: "http://pogo.test:8080", href: "http://pogo.test:8080/", search: "", pathname: "/" });
  vi.stubGlobal("sessionStorage", { getItem: () => "fixture-host-token", setItem() {} });
  vi.stubGlobal("fetch", fetchResponse); client = await import("./client");
});
afterEach(() => vi.unstubAllGlobals());
describe("HTML preview capabilities", () => {
  it("issues a session-authorized capability with the exact workspace selector and no URL token", async () => {
    await client.api.createHTMLPreview("session/id", "reports/中文 + &/index.htm");
    expect(fetchResponse).toHaveBeenCalledExactlyOnceWith("/api/sessions/session%2Fid/html-previews", expect.objectContaining({
      method: "POST", headers: expect.objectContaining({ Authorization: "Bearer fixture-host-token" }), body: JSON.stringify({ path: "reports/中文 + &/index.htm" }),
    }));
    expect(fetchResponse.mock.calls[0]![0]).not.toContain("token=");
  });
  it("revokes only the exact session/capability pair and preserves errors", async () => {
    await client.api.revokeHTMLPreview("session/id", "preview/id");
    expect(fetchResponse).toHaveBeenCalledExactlyOnceWith("/api/sessions/session%2Fid/html-previews/preview%2Fid", expect.objectContaining({ method: "DELETE" }));
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify({ error: "Access denied" }), { status: 404 }));
    await expect(client.api.createHTMLPreview("session/id", "other/index.html")).rejects.toMatchObject({ status: 404 });
    expect(fetchResponse).toHaveBeenCalledTimes(2);
  });
});
