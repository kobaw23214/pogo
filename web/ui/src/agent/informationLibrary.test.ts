import { setLocale } from "@/i18n";
import { beforeEach, describe, expect, it } from "vitest";
import type { InformationEntry } from "@/api/information";
import { informationDescendants, informationPath, informationVisible, restoreInformationView } from "./informationLibrary";
const entry = (id: string, parent_id: string, name: string, kind: InformationEntry["kind"] = "folder"): InformationEntry => ({ id, parent_id, name, kind, version: 1, created_at: "2026-10-18T00:00:00Z", updated_at: "2026-10-18T00:00:00Z" });
const entries = [entry("a", "", "报表"), entry("b", "a", "2026"), entry("c", "b", "数据库说明", "shortcut"), entry("d", "", "数据库模板", "file"), entry("e", "a", "指标口径", "shortcut")];
describe("Information library browsing", () => {
  it("shows one folder or an all-library flat view without mutating metadata", () => {
    expect(informationVisible(entries, "a", "", false).map(value => value.id)).toEqual(["b", "e"]);
    expect(informationVisible(entries, "a", "", true).map(value => value.id)).toEqual(["d", "c", "e"]);
    expect(entries.map(value => value.id)).toEqual(["a", "b", "c", "d", "e"]);
  });
  it("omits folders in flat mode while keeping matching folders in ordinary name search", () => {
    expect(informationVisible(entries, "", "报表", false).map(value => value.id)).toEqual(["a"]);
    expect(informationVisible(entries, "", "报表", true)).toEqual([]);
    expect(informationVisible(entries, "b", "数据库", true).map(value => value.id)).toEqual(["d", "c"]);
  });
  it("searches names across every folder and leaves the original directory untouched", () => {
    expect(informationVisible(entries, "a", "数据库", false).map(value => value.id)).toEqual(["d", "c"]);
    expect(informationVisible(entries, "a", "", false).map(value => value.id)).toEqual(["b", "e"]);
    expect(informationVisible(entries, "", "指标", false)).toEqual([entries[4]]);
  });
  it("builds source paths and recursive deletion counts without following cycles forever", () => {
    expect(informationPath(entries, "c").map(value => value.name)).toEqual(["报表", "2026", "数据库说明"]);
    expect([...informationDescendants(entries, "a")].sort()).toEqual(["a", "b", "c", "e"]);
    const malformed = [entry("a", "b", "A"), entry("b", "a", "B")];
    expect(informationPath(malformed, "a")).toHaveLength(2);
    expect(informationDescendants(malformed, "a").size).toBe(2);
  });
  it("restores only valid folder, selection and expanded IDs after metadata changes", () => {
    const state = restoreInformationView(JSON.stringify({ folder: "b", selected: "c", expanded: ["a", "b", "c", "foreign"], mode: "grid", flat: true }), entries);
    expect(state).toEqual({ folder: "b", selected: "c", expanded: ["a", "b"], mode: "grid", flat: true });
    expect(restoreInformationView(JSON.stringify(state), entries.filter(value => !["b", "c"].includes(value.id)))).toMatchObject({ folder: "", selected: "", expanded: ["a"] });
    expect(restoreInformationView("bad JSON", entries)).toEqual({ folder: "", selected: "", expanded: [], mode: "list", flat: false });
  });
});

// These fixtures assert Chinese labels/order; the interface now follows an explicit locale.
beforeEach(() => setLocale("zh-CN"));
