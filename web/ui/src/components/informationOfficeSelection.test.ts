import { describe, expect, it } from "vitest";
import type { InformationOfficePreview, InformationOfficeSheet } from "@/api/informationOffice";
import { cellRangeName, columnName, informationOfficeSnapshot, parseCellRange, selectedCellText } from "./informationOfficeSelection";
const sheet: InformationOfficeSheet = { name: "明细", rows: 3, columns: 4, cells: [{ row: 1, col: 1, text: "国家" }, { row: 1, col: 2, text: "数量" }, { row: 2, col: 1, text: "中国" }, { row: 2, col: 2, text: "12" }, { row: 3, col: 2, text: "包含\t制表\n换行" }] };
const preview = (editor: "word" | "cell"): InformationOfficePreview => ({ entry_id: "inf_" + "a".repeat(32), version: 4, sha256: "b".repeat(64), snapshot_sha256: "c".repeat(64), path: `information-assets/${"b".repeat(64)}/分析.${editor === "word" ? "docx" : "xlsx"}`, title: "分析", editor, truncated: false });
describe("Read-only library Office selections", () => {
  it("parses normalized bounded A1 rectangles and correct Excel column names", () => {
    expect(columnName(1)).toBe("A"); expect(columnName(26)).toBe("Z"); expect(columnName(27)).toBe("AA"); expect(columnName(256)).toBe("IV"); expect(columnName(0)).toBe("");
    const range = parseCellRange("$B$2:$A$1", sheet)!; expect(range).toEqual({ start: { row: 1, col: 1 }, end: { row: 2, col: 2 } }); expect(cellRangeName(range)).toBe("A1:B2"); expect(cellRangeName(parseCellRange("a3", sheet)!)).toBe("A3");
    for (const invalid of ["A0", "E1", "A4", "A1:B2:C3", "A1;fetch()", "https://example.invalid", "A1:", "", "XFD1"]) expect(parseCellRange(invalid, sheet)).toBeNull();
  });
  it("captures ordered cached values and preserves selected blank cells", () => {
    expect(selectedCellText(sheet, parseCellRange("A1:B2", sheet)!)).toBe("国家\t数量\n中国\t12");
    expect(selectedCellText(sheet, parseCellRange("A3:C3", sheet)!)).toBe("\t包含 制表 ⏎ 换行\t");
    const snapshot = informationOfficeSnapshot(preview("cell"), { text: "\t12\t", sheet: "明细", range: "A2:C2" }); expect(snapshot.text).toBe("\t12\t");
  });
  it("rejects oversized, blank or out-of-bounds selections before insertion", () => {
    expect(() => informationOfficeSnapshot(preview("word"), { text: "界".repeat(22000) })).toThrow();
    expect(() => informationOfficeSnapshot(preview("word"), { text: "\t\n" })).toThrow();
    expect(() => selectedCellText(sheet, { start: { row: 1, col: 1 }, end: { row: 100, col: 4 } })).toThrow();
    expect(() => selectedCellText({ ...sheet, cells: [{ row: 1, col: 1, text: "a".repeat(65537) }] }, parseCellRange("A1", sheet)!)).toThrow();
  });
  it("uses immutable information provenance without a live canvas or source session", () => {
    const snapshot = informationOfficeSnapshot(preview("cell"), { text: "中国\t12", sheet: "明细", range: "A2:B2" }, "capture-time");
    expect(snapshot).toMatchObject({ editor: "cell", canvas_version: `disk:${"b".repeat(64)}`, dirty: false, captured_at: "capture-time", information: { entry_id: "inf_" + "a".repeat(32), version: 4, snapshot_sha256: "c".repeat(64) } }); expect(snapshot.source_session).toBeUndefined();
    const word = informationOfficeSnapshot(preview("word"), { text: "原文，不执行<script>", prefix: "前".repeat(1500), suffix: "后".repeat(1500) }); expect(word.locator?.exact).toBe(word.text); expect(word.locator?.prefix).toHaveLength(1024); expect(word.locator?.suffix).toHaveLength(1024); expect(word.range).toBeUndefined();
  });
});
