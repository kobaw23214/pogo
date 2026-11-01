import { describe, expect, it } from "vitest";
import { attachmentWorkspacePath, isEditableOfficeFile, reserveOfficeFile, sameWorkspaceFile, workspaceFilePath } from "./officeFiles";

describe("Office workspace file identity", () => {
  it("joins a relative attachment once and matches file tree paths", () => {
    const root = "users/alice/agents/a";
    expect(attachmentWorkspacePath(root, "uploads/报表.xlsx")).toBe(`${root}/uploads/报表.xlsx`);
    expect(attachmentWorkspacePath(root, `${root}/uploads/报表.xlsx`)).toBe(`${root}/uploads/报表.xlsx`);
    expect(sameWorkspaceFile(`${root}//uploads/./报表.xlsx`, `${root}/uploads/报表.xlsx`)).toBe(true);
    expect(workspaceFilePath("a/folder/../file.docx")).toBe("a/file.docx");
  });
  it("preserves distinct case, percent encoded text, absolute paths and backslashes", () => {
    expect(sameWorkspaceFile("A.docx", "a.docx")).toBe(false);
    expect(sameWorkspaceFile("a%20b.docx", "a b.docx")).toBe(false);
    expect(sameWorkspaceFile("/a.docx", "a.docx")).toBe(false);
    expect(sameWorkspaceFile("dir\\a.docx", "dir/a.docx")).toBe(false);
    expect(isEditableOfficeFile("a.DOCX")).toBe(true);
    for (const path of ["a.xlsm", "a.doc", "a.xls", "a.docm", "a.xlsx.backup"]) expect(isEditableOfficeFile(path)).toBe(false);
  });
  it("reserves before HTTP, scopes by conversation and releases only its own reservation", () => {
    const first = reserveOfficeFile("uploads/./race.xlsx", "one")!;
    expect(reserveOfficeFile("uploads/race.xlsx", "one")).toBeNull();
    const other = reserveOfficeFile("uploads/race.xlsx", "two")!;
    expect(other).toBeTypeOf("function");
    first();
    const next = reserveOfficeFile("uploads/race.xlsx", "one")!;
    first();
    expect(reserveOfficeFile("uploads/race.xlsx", "one")).toBeNull();
    next(); other();
  });
});
