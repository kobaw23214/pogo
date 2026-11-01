import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const fetchResponse = vi.fn();
let client: typeof import("./client");
beforeEach(async () => {
  vi.resetModules(); fetchResponse.mockReset().mockImplementation(async () => new Response("{}", { status: 200 }));
  vi.stubGlobal("location", { origin: "http://pogo.test:8080", href: "http://pogo.test:8080/", search: "", pathname: "/" });
  vi.stubGlobal("sessionStorage", { getItem: () => "fixture-token", setItem() {} });
  vi.stubGlobal("fetch", fetchResponse); client = await import("./client");
});
afterEach(() => vi.unstubAllGlobals());
describe("Spill read-only directory and file requests", () => {
  it("uses only authenticated bounded GET endpoints and encodes relative paths as selectors", async () => {
    const abort = new AbortController();
    await client.api.spillEntries("scopes/中文 + &/results", 100, abort.signal);
    await client.api.spillFile("scopes/中文 + &/results/a.json", 32768, abort.signal);
    const [entries, file] = fetchResponse.mock.calls;
    const first = new URL(entries![0], "http://pogo.test");
    expect(first.pathname).toBe("/api/prefs/spill/entries");
    expect(Object.fromEntries(first.searchParams)).toEqual({ path: "scopes/中文 + &/results", offset: "100", limit: "100" });
    const second = new URL(file![0], "http://pogo.test");
    expect(second.pathname).toBe("/api/prefs/spill/file");
    expect(Object.fromEntries(second.searchParams)).toEqual({ path: "scopes/中文 + &/results/a.json", offset: "32768", limit: "32768" });
    for (const call of fetchResponse.mock.calls) {
      expect(call[1]).toMatchObject({ method: "GET", headers: { Authorization: "Bearer fixture-token" }, signal: abort.signal });
      expect(call[1].body).toBeUndefined();
    }
  });
  it("preserves permission and expiry errors without falling back to arbitrary file APIs", async () => {
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify({ error: "Administrator required" }), { status: 403 }));
    await expect(client.api.spillEntries()).rejects.toMatchObject({ status: 403 });
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify({ error: "File not found" }), { status: 404 }));
    await expect(client.api.spillFile("expired.txt")).rejects.toMatchObject({ status: 404 });
    expect(fetchResponse).toHaveBeenCalledTimes(2);
  });
});
