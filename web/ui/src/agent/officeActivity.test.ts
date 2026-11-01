import { describe, expect, it } from "vitest";
import { newerOfficeActivity, officeActivityItems } from "./officeActivity";
import type { OfficeActivityRecord, OfficeActivitySnapshot } from "./officeTransport";

const t = (key: string) => key;
const record = (patch: Partial<OfficeActivityRecord> = {}): OfficeActivityRecord => ({
  sequence: 1, operationId: "edit-a", operation: "sheet.apply", actor: "agent", phase: "writing", status: "succeeded",
  target: { kind: "cell", sheet: "明细", range: "C2" }, queuedAt: "2026-10-13T00:00:00Z", verified: true,
  changeCount: 1, changes: [{ address: "C2", before: "2", after: "5" }], ...patch,
});
const view = (...items: OfficeActivityRecord[]): OfficeActivitySnapshot => ({ version: 3, items });

describe("Office host receipt projection", () => {
  it("ignores stale and unchanged versions without replacing the current list", () => {
    const current = view(record());
    expect(newerOfficeActivity(current, view(record({ status: "running" })))).toBeNull();
    expect(newerOfficeActivity(current, { version: 2, items: [] })).toBeNull();
    expect(newerOfficeActivity(current, { version: 4, items: [] })).toEqual({ version: 4, items: [] });
  });
  it("keeps manual actions out of the agent record and uses actual target addresses", () => {
    const items = officeActivityItems(view(record(), record({ operationId: "human-save", actor: "user", phase: "saving" })), t);
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ status: "succeeded", location: "明细!C2", changeCount: 1, changes: [{ label: "C2", before: "2", after: "5" }] });
  });
  it("never treats pending, rejected or unconfirmed mutations as completed changes", () => {
    for (const status of ["queued", "running", "rejected", "unknown"] as const) {
      const [item] = officeActivityItems(view(record({ status })), t);
      expect(item?.changeCount).toBeUndefined(); expect(item?.changes).toBeUndefined();
    }
    const [uncertain] = officeActivityItems(view(record({ verified: false })), t);
    expect(uncertain?.status).toBe("unknown"); expect(uncertain?.changeCount).toBeUndefined();
  });
  it("requires workspace commit verification for saved records", () => {
    const saved = record({ operation: "host.export", phase: "saving", revision: 2 });
    expect(officeActivityItems(view(saved), t)[0]).toMatchObject({ status: "succeeded", summary: "officeActivity.savedVersion", changeCount: undefined });
    expect(officeActivityItems(view({ ...saved, revision: 0 }), t)[0]?.status).toBe("unknown");
  });
  it("distinguishes readback from verified range agreement and global history", () => {
    const check = record({ operation: "sheet.read", phase: "verifying" });
    expect(officeActivityItems(view(check), t)[0]?.summary).toBe("officeActivity.rangeConfirmed");
    expect(officeActivityItems(view({ ...check, verified: false }), t)[0]).toMatchObject({ phase: "reading", summary: "officeActivity.rangeReadOnly" });
    const history = record({ operation: "history.undo", phase: "history" });
    expect(officeActivityItems(view(history), t)[0]).toMatchObject({ summary: "officeEditor.undoNote", changeCount: undefined });
  });
  it("bounds retained records and diff text while maintaining chronological display", () => {
    const records = Array.from({ length: 50 }, (_, i) => record({ sequence: i, operationId: String(i), changes: Array.from({ length: 30 }, () => ({ before: "旧".repeat(10000), after: "新".repeat(10000) })) }));
    const items = officeActivityItems(view(...records), t);
    expect(items).toHaveLength(20); expect(items[0]?.id).toBe("19"); expect(items[19]?.id).toBe("0");
    expect(items[0]?.changes).toHaveLength(6); expect(items[0]?.changes?.[0]?.after?.length).toBe(161);
  });
});
