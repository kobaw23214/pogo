import { describe, expect, it } from "vitest";
import type { OfficeSession } from "./officeTransport";
import { bindOfficeSelection, officeSelectionArgs, officeSelectionSnapshot, officeSelectionStillCurrent } from "./officeSelection";

const session = (): OfficeSession => ({ id: "office-session-a", documentId: "document-a", conversationId: "original-conversation", path: "uploads/选区.docx", title: "选区.docx", fileType: "docx", editor: "word", generation: 2, ready: true, closed: false, dirty: true, dirtyEpoch: 7, persistedEpoch: 2, saveState: "dirty", revision: 3, blocked: false, canvasVersion: "a".repeat(64) });
const bind = (s = session()) => bindOfficeSelection(s, "instance-a", 9, 4, "target-conversation");

describe("live Office selection snapshots", () => {
  it("preserves unsaved selected text and a read-only exact locator without referencing a saved-file read", () => {
    const s = session(), binding = bind(s);
    const result = officeSelectionSnapshot(binding, s, { kind: "word", text: "当前未保存的选中内容" }, "2026-10-16T01:02:03Z");
    expect(result).toEqual({ path: s.path, title: s.title, editor: "word", text: "当前未保存的选中内容", canvas_version: s.canvasVersion, dirty: true, dirty_epoch: 7, captured_at: "2026-10-16T01:02:03Z", source_session: "target-conversation", locator: { kind: "text", exact: "当前未保存的选中内容" } });
    expect(officeSelectionArgs(binding)).toEqual({ documentId: s.documentId, instanceId: "instance-a", canvasVersion: s.canvasVersion, dirtyEpoch: 7 });
    expect(s.dirtyEpoch).toBe(7);
  });
  it("rejects late results after any document, executor, canvas, dirty, local-edit, lifecycle or target-conversation change", () => {
    const s = session(), b = bind(s);
    expect(officeSelectionStillCurrent(b, s, "instance-a", 9, 4, "target-conversation")).toBe(true);
    for (const change of [{ id: "new" }, { documentId: "other" }, { generation: 3 }, { canvasVersion: "b".repeat(64) }, { dirtyEpoch: 8 }, { path: "other.docx" }, { conversationId: "other" }, { editor: "cell" as const }, { closed: true }]) expect(officeSelectionStillCurrent(b, { ...s, ...change }, "instance-a", 9, 4, "target-conversation")).toBe(false);
    expect(officeSelectionStillCurrent(b, s, "new-instance", 9, 4, "target-conversation")).toBe(false);
    expect(officeSelectionStillCurrent(b, s, "instance-a", 10, 4, "target-conversation")).toBe(false);
    expect(officeSelectionStillCurrent(b, s, "instance-a", 9, 5, "target-conversation")).toBe(false);
    expect(officeSelectionStillCurrent(b, s, "instance-a", 9, 4, "new-target")).toBe(false);
  });
  it("captures current worksheet name and bounded A1 range with display text in row order", () => {
    const s = { ...session(), editor: "cell" as const, fileType: "xlsx" as const }, binding = bind(s);
    const result = officeSelectionSnapshot(binding, s, { kind: "cell", sheet: "未保存的名称", range: "B2:C3", text: "城市\t国家\n北京\t中国", inputs: [["must not leak native handles"]] });
    expect(result.sheet).toBe("未保存的名称"); expect(result.range).toBe("B2:C3"); expect(result.text).toBe("城市\t国家\n北京\t中国");
    expect(result).not.toHaveProperty("inputs"); expect(result).not.toHaveProperty("instanceId");
  });
  it("rejects empty, oversized, malformed and cross-editor selection receipts", () => {
    const s = session(), binding = bind(s);
    for (const raw of [null, { kind: "word", text: " " }, { kind: "word", text: "x".repeat(32001) }, { kind: "word", text: "中".repeat(23000) }, { kind: "word", text: "bad\u0000" }, { kind: "cell", text: "wrong" }]) expect(() => officeSelectionSnapshot(binding, s, raw)).toThrow();
    const cell = { ...s, editor: "cell" as const }, b = bind(cell);
    for (const range of ["A:A", "B2:A1", "A1:U20", "XFE1", "A1048577", "A1,B2"]) expect(() => officeSelectionSnapshot(b, cell, { kind: "cell", sheet: "明细", range, text: "value" })).toThrow();
    expect(officeSelectionSnapshot(b, cell, { kind: "cell", sheet: "明细", range: "A1:T20", text: "value" }).range).toBe("A1:T20");
  });
});
