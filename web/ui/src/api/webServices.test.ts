import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const fetchResponse = vi.fn();
let client: typeof import("./client");
beforeEach(async () => {
  vi.resetModules(); fetchResponse.mockReset().mockImplementation(async () => new Response(JSON.stringify({ services: [] }), { status: 200 }));
  vi.stubGlobal("location", { origin: "http://pogo.test", href: "http://pogo.test/", search: "", pathname: "/" });
  vi.stubGlobal("sessionStorage", { getItem: () => "test-host-token", setItem() {} });
  vi.stubGlobal("fetch", fetchResponse); client = await import("./client");
});
afterEach(() => vi.unstubAllGlobals());
describe("owner managed website transport", () => {
  it("deletes one owned service through an encoded path and accepts an empty 204 receipt", async () => {
    const controller = new AbortController(); fetchResponse.mockResolvedValueOnce(new Response(null, { status: 204 }));
    await expect(client.api.deleteWebService("session/id", "service/id", controller.signal)).resolves.toBeUndefined();
    expect(fetchResponse).toHaveBeenCalledExactlyOnceWith("/api/sessions/session%2Fid/web-services/service%2Fid", expect.objectContaining({ method: "DELETE", body: undefined, signal: controller.signal, headers: expect.objectContaining({ Authorization: "Bearer test-host-token" }) }));
  });
  it("uses session-scoped authenticated paths and fixed bounded logs", async () => {
    await client.api.webServices("session/id"); await client.api.webServiceLogs("session/id", "service/id", "cursor + &");
    expect(fetchResponse.mock.calls[0]![0]).toBe("/api/sessions/session%2Fid/web-services");
    expect(fetchResponse.mock.calls[1]![0]).toBe("/api/sessions/session%2Fid/web-services/service%2Fid/logs?cursor=cursor+%2B+%26&limit_bytes=65536");
    expect(fetchResponse.mock.calls[0]![1].headers.Authorization).toBe("Bearer test-host-token");
  });
  it("keeps operation identity and never sends the host token in an open URL", async () => {
    await client.api.openWebService("s1", "ws_one", "original-request");
    expect(fetchResponse).toHaveBeenCalledExactlyOnceWith("/api/sessions/s1/web-services/ws_one/previews", expect.objectContaining({ method: "POST", body: JSON.stringify({ operation_id: "original-request" }) }));
    expect(fetchResponse.mock.calls[0]![0]).not.toContain("token");
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify({ error: "GRANT_EXPIRED: expired", code: "GRANT_EXPIRED" }), { status: 400 }));
    await expect(client.api.openWebService("s1", "ws_one", "original-request")).rejects.toMatchObject({ code: "GRANT_EXPIRED", status: 400 });
    expect(fetchResponse).toHaveBeenCalledTimes(2);
  });
  it("explicitly revokes one website address without stopping the website", async () => {
    await client.api.revokeWebService("s1", "ws_one", "grant_one");
    expect(fetchResponse).toHaveBeenCalledExactlyOnceWith("/api/sessions/s1/web-services/ws_one/previews/grant_one", expect.objectContaining({ method: "DELETE", keepalive: true, body: undefined }));
  });
  it("uses the platform lifetime default when starting through the owner collection", async () => {
    const spec = { name: "counter", project_root: "app", profile: "python-web" as const, start_argv: ["python3", "app.py"], health_path: "/" };
    await client.api.startWebService("session/id", spec, "fresh-lease");
    expect(fetchResponse).toHaveBeenCalledExactlyOnceWith("/api/sessions/session%2Fid/web-services", expect.objectContaining({ method: "POST", body: JSON.stringify({ ...spec, operation_id: "fresh-lease" }) }));
  });
  it("sends the selected renewal duration without a two-hour override", async () => {
    await client.api.webServiceAction("s1", "ws_one", "renew", "renew-days", undefined, 259200);
    expect(JSON.parse(fetchResponse.mock.calls[0]![1].body)).toEqual({ operation_id: "renew-days", lease_seconds: 259200 });
  });
  it("renames a website through its owner path without changing its internal slot", async () => {
    await client.api.renameWebService("s1", "ws_one", "我的工具网站", "rename-one");
    expect(fetchResponse.mock.calls[0]![0]).toBe("/api/sessions/s1/web-services/ws_one/rename");
    expect(JSON.parse(fetchResponse.mock.calls[0]![1].body)).toEqual({ display_name: "我的工具网站", operation_id: "rename-one" });
  });
  it("keeps quota scope and counts for a friendly capacity message", async () => {
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify({ error: "Conversation limit reached", code: "QUOTA_EXCEEDED", details: { scope: "session", current: 2, limit: 2 } }), { status: 409 }));
    await expect(client.api.startWebService("s1", { name: "x", profile: "node-web", project_root: ".", start_argv: ["node", "server.js"] }, "limited")).rejects.toMatchObject({ code: "QUOTA_EXCEEDED", details: { details: { scope: "session", current: 2, limit: 2 } } });
  });
});
