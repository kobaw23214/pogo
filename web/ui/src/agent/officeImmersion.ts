import type { DockTab } from "@/components/dockSheets";

export interface OfficeViewContext {
  path: string;
  conversationId: string;
}

/** A new editor view, rather than a status/dirty update or a tab selection. */
export interface OfficeRevealRequest extends OfficeViewContext {
  sheet: "hub" | "files";
  tabId?: string;
}

export function immersiveWorkspaceAvailable(
  sheet: string | null,
  tabs: DockTab[],
  activeTab: string | null,
  fileOffice: OfficeViewContext | null,
  conversationId: string | null,
): boolean {
  if (sheet === "files") return !!fileOffice && fileOffice.conversationId === conversationId;
  const selected = tabs.find(tab => tab.id === activeTab) ?? tabs[0];
  return sheet === "hub" && !!selected && ["browser", "panel", "preview", "html"].includes(selected.kind);
}

/** Never steal focus back after a document, sheet, or conversation changed. */
export function officeRevealStillSelected(
  request: OfficeRevealRequest,
  conversationId: string | null,
  sheet: string | null,
  activeTab: string | null,
  tabs: DockTab[],
  fileOffice: OfficeViewContext | null,
): boolean {
  if (request.conversationId !== conversationId || request.sheet !== sheet) return false;
  if (sheet === "files") return fileOffice?.conversationId === conversationId && fileOffice.path === request.path;
  const selected = tabs.find(tab => tab.id === activeTab) ?? tabs[0];
  return !!selected && selected.kind === "preview" && selected.id === request.tabId && selected.path === request.path;
}
