import { afterEach, describe, expect, it, vi } from "vitest";
import { informationOfficePreview, isInformationOfficePreview, type InformationOfficePreview } from "./informationOffice";
vi.mock("./client", () => ({ authHeaders: () => ({ Authorization: "Bearer synthetic-preview" }) }));
const id = "inf_" + "a".repeat(32), sha = "b".repeat(64);
const preview = (): InformationOfficePreview => ({ entry_id: id, version: 1, sha256: sha, snapshot_sha256: "c".repeat(64), path: `information-assets/${sha}/report.xlsx`, title: "report.xlsx", editor: "cell", sheets: [{ name: "Sheet 1", rows: 1, columns: 1, cells: [{ row: 1, col: 1, text: "plain data" }] }], truncated: false });
afterEach(() => vi.unstubAllGlobals());
describe("Authenticated immutable Office preview API", () => {
  it("reads only the exact version with credentials and no edit request", async () => {
    const fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => preview() }); vi.stubGlobal("fetch", fetch);
    await expect(informationOfficePreview(id, 1)).resolves.toMatchObject({ version: 1 });
    expect(fetch).toHaveBeenCalledExactlyOnceWith(`/api/information/entries/${id}/office-preview?version=1`, expect.objectContaining({ credentials: "same-origin", cache: "no-store", headers: { Authorization: "Bearer synthetic-preview" } }));
  });
  it("rejects forged source IDs, version substitution, asset mismatches and unbounded grids", async () => {
    for (const value of [{ ...preview(), entry_id: "other" }, { ...preview(), path: "uploads/foreign.xlsx" }, { ...preview(), snapshot_sha256: "bad" }, { ...preview(), sheets: [{ name: "bad", rows: 10001, columns: 1, cells: [] }] }]) expect(isInformationOfficePreview(value)).toBe(false);
    const fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ ...preview(), version: 2 }) }); vi.stubGlobal("fetch", fetch);
    await expect(informationOfficePreview(id, 1)).rejects.toThrow("Invalid information Office preview response");
    await expect(informationOfficePreview("../../outside", 1)).rejects.toThrow(); await expect(informationOfficePreview(id, 0)).rejects.toThrow(); expect(fetch).toHaveBeenCalledTimes(1);
  });
  it("preserves readable protected API failures instead of pretending a preview exists", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 413, json: async () => ({ error: "preview limit" }) })); await expect(informationOfficePreview(id, 1)).rejects.toMatchObject({ status: 413, message: "preview limit" });
  });
});
