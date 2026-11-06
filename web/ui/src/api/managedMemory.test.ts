import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const fetchResponse = vi.fn();
let client: typeof import("./client");
beforeEach(async () => {
  vi.resetModules(); fetchResponse.mockReset();
  vi.stubGlobal("location", { origin: "http://pogo.test:8080", href: "http://pogo.test:8080/", search: "", pathname: "/" });
  vi.stubGlobal("sessionStorage", { getItem: () => "fixture-token", setItem() {} });
  vi.stubGlobal("fetch", fetchResponse);
  client = await import("./client");
});
afterEach(() => vi.unstubAllGlobals());

describe("managed memory API scope", () => {
  it("loads management independently of any active session", async () => {
    const reply = { scopes: [], auto_generate: false, auto_generate_editable: false };
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify(reply), { status: 200 }));
    expect(await client.api.managedMemoryCatalog()).toEqual(reply);
    expect(fetchResponse.mock.calls[0]![0]).toBe("/api/memory/catalog");
    expect(fetchResponse.mock.calls[0]![1]).toMatchObject({ method: "GET", headers: { Authorization: "Bearer fixture-token" } });
  });

  it("passes an encoded host scope ID and editable note fields for a save", async () => {
    fetchResponse.mockResolvedValueOnce(new Response("{}", { status: 200 }));
    const note = { path: "preferences.md", type: "user" as const, content: "Keep replies concise.", expected_modified: "2026-11-05T01:02:03.123456789Z" };
    await client.api.saveManagedMemoryNote("agent:ag_a&scope_id=user", note);
    const [path, options] = fetchResponse.mock.calls[0]!;
    expect(path).toBe("/api/memory/notes?scope_id=agent%3Aag_a%26scope_id%3Duser");
    expect(options.method).toBe("PUT");
    expect(JSON.parse(options.body)).toEqual(note);
  });

  it("keeps delete path and scope as separate encoded query values", async () => {
    fetchResponse.mockResolvedValueOnce(new Response('{"deleted":true}', { status: 200 }));
    await client.api.deleteManagedMemoryNote("agent:ag_a", "preferences.md&scope_id=user", "2026-11-05T01:02:03.123+08:00");
    expect(fetchResponse.mock.calls[0]![0]).toBe("/api/memory/notes?scope_id=agent%3Aag_a&path=preferences.md%26scope_id%3Duser&expected_modified=2026-11-05T01%3A02%3A03.123%2B08%3A00");
    expect(fetchResponse.mock.calls[0]![1].method).toBe("DELETE");
  });

  it("does not retry denied writes through an existing session API", async () => {
    fetchResponse.mockResolvedValueOnce(new Response('{"error":"not found"}', { status: 404 }));
    await expect(client.api.deleteManagedMemoryNote("foreign-scope", "fact.md")).rejects.toMatchObject({ status: 404 });
    expect(fetchResponse).toHaveBeenCalledOnce();
  });

  it("carries create-only intent without adding an edit token", async () => {
    fetchResponse.mockResolvedValueOnce(new Response("{}", { status: 200 }));
    await client.api.saveManagedMemoryNote("user", { path: "new-memory.md", type: "user", content: "A new fact", create_only: true });
    expect(JSON.parse(fetchResponse.mock.calls[0]![1].body)).toEqual({ path: "new-memory.md", type: "user", content: "A new fact", create_only: true });
  });

  it("preserves a memory setting changed since the storage page was opened", async () => {
    const latest = { prefs: { spill_retention_days: 7, memory_auto: true, max_turns: 80, sandbox_mode: "enforce" } };
    fetchResponse.mockResolvedValueOnce(new Response(JSON.stringify(latest), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(latest), { status: 200 }));
    await client.api.saveSpillRetentionDays(30);
    expect(fetchResponse.mock.calls[0]![0]).toBe("/api/prefs");
    expect(fetchResponse.mock.calls[0]![1].method).toBe("GET");
    expect(fetchResponse.mock.calls[1]![1].method).toBe("PUT");
    expect(JSON.parse(fetchResponse.mock.calls[1]![1].body)).toEqual({ ...latest.prefs, spill_retention_days: 30 });
  });

  it("does not overwrite memory preferences when the latest settings cannot be read", async () => {
    fetchResponse.mockResolvedValueOnce(new Response('{"error":"not available"}', { status: 503 }));
    await expect(client.api.saveSpillRetentionDays(30)).rejects.toMatchObject({ status: 503 });
    expect(fetchResponse).toHaveBeenCalledOnce();
  });
});
