import { describe, expect, it } from "vitest";
import { officeHasNewVersion, officeReplacementMatches, officeSessionClean, officeStatusIsOlder } from "./officeVersionSync";
import type { OfficeSession } from "./officeTransport";

const a = "a".repeat(64), b = "b".repeat(64);
const session = (): OfficeSession => ({ id: "old", documentId: "old-document", conversationId: "chat", path: "uploads/a.docx", fileRef: "owned-file", fileType: "docx", editor: "word", title: "a", generation: 1, ready: true, closed: false, dirty: false, dirtyEpoch: 2, persistedEpoch: 2, saveState: "saved", revision: 1, pending: false, blocked: false, canvasVersion: a, diskVersion: a, syncRevision: 2 });

describe("Office version synchronization boundaries", () => {
  it("requires an actual host observation with known versions, not a save counter", () => {
    expect(officeHasNewVersion({ ...session(), externalChange: true, diskVersion: b })).toBe(true);
    for (const value of [session(), { ...session(), revision: 500 }, { ...session(), externalChange: true, canvasVersion: undefined }, { ...session(), externalChange: true, diskVersion: "unknown" }]) expect(officeHasNewVersion(value)).toBe(false);
  });
  it("rebinds a host-observed atomic replacement even when its content hash is identical", () => {
    expect(officeHasNewVersion({ ...session(), externalChange: true })).toBe(true);
    const sameBytesNewIdentity = { ...session(), id: "new", documentId: "new-document", generation: 2 };
    expect(officeReplacementMatches(session(), sameBytesNewIdentity, "new")).toBe(true);
    expect(officeReplacementMatches(session(), session(), "old")).toBe(false);
  });
  it("rejects stale freeze/cancellation snapshots even when save and dirty counters did not change", () => {
    expect(officeStatusIsOlder(session(), { ...session(), batch: null, syncRevision: 1 })).toBe(true);
    expect(officeStatusIsOlder(session(), { ...session(), syncRevision: undefined })).toBe(true);
    expect(officeStatusIsOlder(session(), { ...session(), batch: null, syncRevision: 3 })).toBe(false);
    expect(officeStatusIsOlder(null, session())).toBe(false);
  });
  it("cannot reload a dirty, queued, saving or outcome-unknown canvas", () => {
    expect(officeSessionClean(session())).toBe(true);
    for (const delta of [{ dirty: true }, { dirtyEpoch: 3 }, { pending: true }, { pending: undefined }, { blocked: true }, { closed: true }, { saveState: "saving" as const }, { saveState: "error" as const }, { savePhase: "committing" as const }]) expect(officeSessionClean({ ...session(), ...delta }), JSON.stringify(delta)).toBe(false);
  });
  it("requires the exact newer document generation and observed disk content for replacement", () => {
    const previous = { ...session(), diskVersion: b }, next = { ...session(), id: "new", documentId: "new-document", generation: 2, canvasVersion: b, diskVersion: b };
    expect(officeReplacementMatches(previous, next, "new")).toBe(true);
    for (const delta of [{ id: "other" }, { documentId: "old-document" }, { generation: 1 }, { conversationId: "other" }, { fileRef: "foreign" }, { path: "other/a.docx" }, { canvasVersion: a }, { diskVersion: a }, { closed: true }]) expect(officeReplacementMatches(previous, { ...next, ...delta }, "new"), JSON.stringify(delta)).toBe(false);
  });
});
