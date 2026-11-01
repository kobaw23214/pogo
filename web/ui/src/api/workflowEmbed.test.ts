import { afterEach, describe, expect, it, vi } from "vitest";
import { createWorkflowEmbedApi } from "./workflowEmbed";

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); vi.resetModules(); });
describe("workflow embed transport isolation", () => {
  it("exchanges a code without cookies and sends only its session bearer", async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ session_token: "wes_short_session" }))).mockImplementation(async () => new Response(JSON.stringify({ runs: [] })));
    vi.stubGlobal("fetch", fetcher);
    const api = createWorkflowEmbedApi("pub_123");
    await api.exchange("single-code", "https://host.example");
    await api.list();
    const [path, request] = fetcher.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/api/workflow-embed/publications/pub_123/session");
    expect(request.credentials).toBe("omit");
    expect(request.headers).not.toHaveProperty("Authorization");
    expect(JSON.parse(request.body as string)).toEqual({ code: "single-code", parent_origin: "https://host.example" });
    expect(fetcher.mock.calls[1]![1]).toMatchObject({ credentials: "omit", headers: { Authorization: "Bearer wes_short_session" } });
    expect(fetcher.mock.calls.every(([url]) => !String(url).includes("single-code"))).toBe(true);
    await api.retry("failed-run", "stable-retry-intent");
    expect(fetcher.mock.calls[2]![1]).toMatchObject({ credentials: "omit", headers: { "Idempotency-Key": "stable-retry-intent" } });
    api.clear();
    await expect(api.list()).rejects.toMatchObject({ status: 401 });
    expect(fetcher).toHaveBeenCalledTimes(3);
  });
  it("does not read or write account token storage on the iframe mount", async () => {
    vi.resetModules();
    const storage = { getItem: vi.fn(() => "account-token"), setItem: vi.fn(), removeItem: vi.fn() };
    const history = { replaceState: vi.fn() };
    vi.stubGlobal("location", { pathname: "/embed/workflows/pub_123", search: "?token=account-token", href: "https://pogo.example/embed/workflows/pub_123?token=account-token" });
    vi.stubGlobal("sessionStorage", storage);
    vi.stubGlobal("history", history);
    const client = await import("./client");
    expect(client.token).toBe(""); expect(client.authHeaders()).toEqual({});
    client.setToken("should-not-be-written");
    expect(storage.getItem).not.toHaveBeenCalled(); expect(storage.setItem).not.toHaveBeenCalled(); expect(history.replaceState).not.toHaveBeenCalled();
  });
  it("keeps authentication failures explicit instead of using a local session", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response('{"error":"expired"}', { status: 401 })));
    const api = createWorkflowEmbedApi("pub");
    await expect(api.exchange("old", "https://host.example")).rejects.toMatchObject({ status: 401, message: "expired" });
    await expect(api.get("other-run")).rejects.toMatchObject({ status: 401 });
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});
