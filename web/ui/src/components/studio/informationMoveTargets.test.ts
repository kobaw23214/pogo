import { describe, expect, it } from "vitest";
import type { InformationEntry } from "@/api/information";
import { informationCanMoveTo, informationMoveInitialExpansion, informationMoveTargetRows } from "./informationMoveTargets";
const entry = (id: string, parent_id = "", kind: InformationEntry["kind"] = "folder"): InformationEntry => ({ id, name: id, parent_id, kind, version: 1, created_at: "2026-10-18", updated_at: "2026-10-18" });
const entries = [entry("project"), entry("child", "project"), entry("grandchild", "child"), entry("other"), entry("document", "child", "file"), entry("shortcut", "", "shortcut")];
describe("Information move destination hierarchy", () => {
  it("offers the real root, folders and ancestor folders for a file but never files or shortcuts", () => {
    for (const id of ["", "project", "child", "grandchild", "other"]) expect(informationCanMoveTo(entries, "document", id)).toBe(true);
    for (const id of ["document", "shortcut", "missing"]) expect(informationCanMoveTo(entries, "document", id)).toBe(false);
    expect(informationMoveTargetRows(entries, "document", ["project", "child"]).map(row => row.entry.id)).toEqual(["other", "project", "child", "grandchild"]);
  });
  it("keeps a moving folder and its descendants visible but unselectable", () => {
    const rows = informationMoveTargetRows(entries, "project", ["project", "child"]);
    expect(rows.filter(row => row.blocked).map(row => row.entry.id)).toEqual(["project", "child", "grandchild"]);
    for (const id of ["project", "child", "grandchild"]) expect(informationCanMoveTo(entries, "project", id)).toBe(false);
    expect(informationCanMoveTo(entries, "project", "other")).toBe(true);
    expect(informationCanMoveTo(entries, "project", "")).toBe(true);
  });
  it("expands the current folder ancestry initially and preserves hierarchy when collapsed", () => {
    expect(informationMoveInitialExpansion(entries, "child")).toEqual(["project", "child"]);
    expect(informationMoveTargetRows(entries, "document", []).map(row => [row.entry.id, row.depth, row.expandable])).toEqual([["other", 0, false], ["project", 0, true]]);
    expect(informationMoveTargetRows(entries, "document", ["project"]).map(row => [row.entry.id, row.depth])).toEqual([["other", 0], ["project", 0], ["child", 1]]);
  });
  it("rejects removed items, stale destinations and malformed folder ancestry", () => {
    expect(informationCanMoveTo(entries, "removed", "")).toBe(false);
    expect(informationCanMoveTo(entries.filter(item => item.id !== "other"), "document", "other")).toBe(false);
    const broken = [...entries, entry("orphan", "gone"), entry("cycle-a", "cycle-b"), entry("cycle-b", "cycle-a"), entry("file-child", "document")];
    for (const id of ["orphan", "cycle-a", "cycle-b", "file-child"]) expect(informationCanMoveTo(broken, "document", id)).toBe(false);
    expect(informationMoveTargetRows(broken, "document", broken.map(item => item.id)).map(row => row.entry.id)).toEqual(["other", "project", "child", "grandchild"]);
  });
});
