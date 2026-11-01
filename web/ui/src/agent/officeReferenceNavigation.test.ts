import { describe, expect, it } from "vitest";
import type { OfficeReferenceSnapshot } from "@/api/types";
import { officeReferenceTarget } from "./officeReferenceNavigation";

const scope = { conversationId: "source-session", cwd: "/home/pogo/users/owner", root: "/home/pogo", workspace: "users/owner" };
const office: OfficeReferenceSnapshot = { path: "users/owner/uploads/报告.xlsx", title: "报告.xlsx", editor: "cell", text: "选区", canvas_version: "a".repeat(64), source_session: "source-session" };
describe("Office reference source navigation", () => {
  it("resolves canonical paths once without duplicating the workspace prefix", () => {
    expect(officeReferenceTarget(office, scope)?.path).toBe("uploads/报告.xlsx");
    expect(officeReferenceTarget({ ...office, path: "uploads/报告.xlsx" }, scope)?.path).toBe("uploads/报告.xlsx");
  });
  it("does not retarget a frozen reference to another conversation or account", () => {
    expect(officeReferenceTarget(office, { ...scope, conversationId: "another" })).toBeNull();
    expect(officeReferenceTarget({ ...office, path: "users/other/uploads/报告.xlsx" }, scope)).toBeNull();
    expect(officeReferenceTarget({ ...office, path: "../other/报告.xlsx" }, scope)).toBeNull();
  });
  it("never treats a saved information asset as a writable workspace document", () => {
    const path = `information-assets/${"a".repeat(64)}/报告.xlsx`;
    expect(officeReferenceTarget({ ...office, path }, scope)).toBeNull();
    expect(officeReferenceTarget({ ...office, information: { entry_id: `inf_${"1".repeat(32)}`, version: 2, snapshot_sha256: "b".repeat(64) } }, scope)).toBeNull();
  });
});
