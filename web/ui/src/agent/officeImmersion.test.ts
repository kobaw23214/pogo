import { describe, expect, it } from "vitest";
import { immersiveWorkspaceAvailable, officeRevealStillSelected, type OfficeRevealRequest } from "./officeImmersion";
import type { DockTab } from "@/components/dockSheets";

const office = { path: "uploads/test.docx", conversationId: "conversation-a" };
const preview: DockTab = { id: "preview:uploads/test.docx", path: office.path, label: "test.docx", kind: "preview", closable: true };
const request: OfficeRevealRequest = { ...office, sheet: "hub", tabId: preview.id };
const browser: DockTab = { id: "browser:session", kind: "browser", label: "Browser", closable: true };

describe("Office immersive entry boundaries", () => {
  it("opens the selected new Office editor in its existing hub preview", () => {
    expect(officeRevealStillSelected(request, office.conversationId, "hub", preview.id, [preview, browser], null)).toBe(true);
    expect(immersiveWorkspaceAvailable("hub", [preview], preview.id, null, office.conversationId)).toBe(true);
  });

  it("also permits the existing editor inside files without moving it to the hub", () => {
    const filesRequest: OfficeRevealRequest = { ...office, sheet: "files" };
    expect(officeRevealStillSelected(filesRequest, office.conversationId, "files", browser.id, [browser], office)).toBe(true);
    expect(immersiveWorkspaceAvailable("files", [browser], browser.id, office, office.conversationId)).toBe(true);
  });

  it("never opens immersion for an ordinary file preview or another conversation's retained editor", () => {
    expect(immersiveWorkspaceAvailable("files", [], null, null, office.conversationId)).toBe(false);
    expect(immersiveWorkspaceAvailable("files", [], null, office, "conversation-b")).toBe(false);
    expect(officeRevealStillSelected({ ...office, sheet: "files" }, office.conversationId, "files", null, [], null)).toBe(false);
    expect(officeRevealStillSelected(request, "conversation-b", "hub", preview.id, [preview], null)).toBe(false);
  });

  it("drops delayed entry after the user chooses another sheet, file or tab", () => {
    expect(officeRevealStillSelected(request, office.conversationId, "hub", browser.id, [preview, browser], null)).toBe(false);
    expect(officeRevealStillSelected(request, office.conversationId, "files", preview.id, [preview], office)).toBe(false);
    expect(officeRevealStillSelected({ ...office, sheet: "files" }, office.conversationId, "files", null, [], { ...office, path: "other.xlsx" })).toBe(false);
    expect(officeRevealStillSelected(request, office.conversationId, "hub", preview.id, [{ ...preview, path: "other.docx" }], null)).toBe(false);
  });

  it("keeps ordinary browser immersion available and exits on non-immersive sheets", () => {
    expect(immersiveWorkspaceAvailable("hub", [browser], browser.id, null, office.conversationId)).toBe(true);
    expect(immersiveWorkspaceAvailable("hub", [{ id: "shell", kind: "shell", label: "Shell", closable: true }], "shell", office, office.conversationId)).toBe(false);
    for (const sheet of [null, "skills"]) expect(immersiveWorkspaceAvailable(sheet, [browser], browser.id, office, office.conversationId)).toBe(false);
  });
});
