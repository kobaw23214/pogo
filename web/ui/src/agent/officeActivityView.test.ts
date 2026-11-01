import { describe, expect, it } from "vitest";
import { officeActivityChangedCount, officeActivityHeadline, officeActivityShownChanges, officeActivityText, officeActivityTotalChanges, officeActivityWindow, type OfficeActivityItem } from "./officeActivityView";

const record = (id: string, status: OfficeActivityItem["status"] = "succeeded", phase: OfficeActivityItem["phase"] = "writing", changeCount = 1): OfficeActivityItem => ({ id, status, phase, changeCount, operation: "sheet.apply" });

describe("Office receipt presentation", () => {
  it("never counts pending, rejected, unknown, read or save receipts as confirmed changes", () => {
    expect(officeActivityChangedCount([record("1"), record("2", "pending"), record("3", "unknown"), record("4", "rejected"), record("5", "succeeded", "reading", 5), record("6", "succeeded", "saving", 3)])).toBe(1);
    expect(officeActivityChangedCount([record("7", "succeeded", "writing", NaN), record("8", "succeeded", "writing", -1)])).toBe(0);
  });
  it("keeps actual work visible while an unrelated receipt finishes", () => {
    expect(officeActivityHeadline([record("write", "running"), record("read", "succeeded", "reading")])?.id).toBe("write");
    expect(officeActivityHeadline([record("write", "running"), record("newer-queued", "pending")])?.id).toBe("write");
    expect(officeActivityHeadline([record("write"), record("uncertain", "unknown")])?.status).toBe("unknown");
    expect(officeActivityHeadline([record("uncertain", "unknown"), record("later-read", "succeeded", "reading")])?.status).toBe("unknown");
  });
  it("renders at most twenty recent records without reordering source state", () => {
    const records = Array.from({ length: 40 }, (_, index) => record(String(index)));
    const latest = officeActivityWindow(records);
    expect(latest).toHaveLength(20);
    expect(latest[0]?.id).toBe("39");
    expect(latest[latest.length - 1]?.id).toBe("20");
    expect(records[0]?.id).toBe("0");
  });
  it("reports the confirmed total rather than the length of an excerpted change list", () => {
    const item = { ...record("batch", "succeeded", "writing", 10), changes: Array.from({ length: 6 }, () => ({ before: "2", after: "5" })), truncated: true };
    expect(officeActivityTotalChanges(item)).toBe(10);
    expect(officeActivityShownChanges(item)).toBe(4);
  });
  it("filters before limiting so later reads do not hide a retained edit", () => {
    const items = [record("earlier-edit"), ...Array.from({ length: 39 }, (_, index) => record(`read-${index}`, "succeeded", "reading", 0))];
    expect(officeActivityWindow(items).some(item => item.id === "earlier-edit")).toBe(false);
    expect(officeActivityWindow(items, true).map(item => item.id)).toEqual(["earlier-edit"]);
  });
  it("bounds text by characters without breaking Chinese, emoji, multiline or literal HTML", () => {
    expect(officeActivityText("中文🙂第二行", 3)).toBe("中文🙂…");
    expect(officeActivityText("前\u0000后\n<script>" )).toBe("前后\n<script>");
    expect(officeActivityText(undefined)).toBe("");
  });
});
