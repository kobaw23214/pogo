import { afterEach, describe, expect, it, vi } from "vitest";
import { informationApi, InformationAPIError, isInformationEntry, isInformationTree, resolveInformationEntry, type InformationEntry } from "./information";
const id = "inf_" + "a".repeat(32), sha = "b".repeat(64), asset = `information-assets/${sha}/资料.txt`;
const entry: InformationEntry = { id, parent_id: "", kind: "file", name: "资料", version: 1, created_at: "2026-10-18T00:00:00Z", updated_at: "2026-10-18T00:00:00Z", file: { name: "资料.txt", mime: "text/plain", size: 3, sha256: sha, asset } };
const tree = { scope_id: "workspace-a", revision: 2, can_write: true, entries: [entry] };
afterEach(() => vi.unstubAllGlobals());
describe("Information library protocol", () => {
  it("rejects malformed immutable handles, versions and mixed shortcut payloads", () => {
    expect(isInformationTree(tree)).toBe(true);
    expect(isInformationEntry({ ...entry, version: 0 })).toBe(false);
    expect(isInformationEntry({ ...entry, file: { ...entry.file, asset: "information-assets/foreign.txt" } })).toBe(false);
    expect(isInformationTree({ ...tree, entries: [entry, entry] })).toBe(false);
    expect(isInformationEntry({ ...entry, kind: "shortcut", shortcut: { id: "bad", title: "Bad", content: "", parts: [{ type: "skill", name: "/not-a-skill" }] } })).toBe(false);
  });
  it("keeps revision conflicts typed with the current own workspace view", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ code: "INFORMATION_CONFLICT", error: "changed", current: tree }), { status: 409 })); vi.stubGlobal("fetch", fetcher);
    try { await informationApi.create({ revision: 1, parent_id: "", kind: "folder", name: "新目录" }); expect.fail("must reject"); } catch (error) { expect(error).toBeInstanceOf(InformationAPIError); expect(error).toMatchObject({ status: 409, code: "INFORMATION_CONFLICT", current: tree }); }
    const [url, options] = fetcher.mock.calls[0]; expect(url).toBe("/api/information/entries"); expect(options.credentials).toBe("same-origin"); expect(JSON.parse(options.body)).toEqual({ revision: 1, parent_id: "", kind: "folder", name: "新目录" });
  });
  it("resolves exactly the chosen version and rejects an unrelated response", async () => {
    const parts = [{ type: "file", path: "uploads/information/资料.txt", as_file: true }], attachments: unknown[] = [];
    const fetcher = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ parts, attachments, entry, input_layout: "inline" }))).mockResolvedValueOnce(new Response(JSON.stringify({ parts, attachments, entry: { ...entry, version: 2 } }))); vi.stubGlobal("fetch", fetcher);
    expect(await resolveInformationEntry("session a", id, 1)).toMatchObject({ parts, entry, input_layout: "inline" });
    expect(fetcher.mock.calls[0][0]).toBe("/api/sessions/session%20a/information/resolve"); expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({ id, version: 1 });
    await expect(informationApi.resolve("session a", id, 1)).rejects.toThrow("Invalid information reference response");
  });
  it("previews nested attachments through the entry/version-authorized endpoint", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response("资料", { headers: { "Content-Type": "text/plain" } })); vi.stubGlobal("fetch", fetcher);
    const result = await informationApi.assetContent(id, 1, asset); expect(await result.text()).toBe("资料");
    const url = new URL(fetcher.mock.calls[0][0], "https://local.invalid"); expect(url.pathname).toBe(`/api/information/entries/${id}/assets/content`); expect(url.searchParams.get("asset")).toBe(asset); expect(url.searchParams.get("version")).toBe("1");
    await expect(informationApi.assetContent(id, 1, "information-assets/foreign.txt")).rejects.toThrow("Invalid information asset"); expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
