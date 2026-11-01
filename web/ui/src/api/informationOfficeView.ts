import { officeRequest } from "./client";
import type { OfficeOpen, OfficeSession } from "@/agent/officeTransport";
import { officeSelectionSnapshot, type OfficeSelectionBinding } from "@/agent/officeSelection";
import type { OfficeReferenceSnapshot } from "./types";

export interface InformationOfficeSource {
  entry_id: string; version: number; sha256: string; snapshot_sha256: string; path: string; title: string;
}
export interface InformationOfficeView extends OfficeOpen { source: InformationOfficeSource }

export function openInformationOfficeView(id: string, version: number, instanceId: string, operationId: string): Promise<InformationOfficeView> {
  if (!/^inf_[a-f0-9]{32}$/.test(id) || !Number.isSafeInteger(version) || version < 1) throw new Error("Invalid information Office version");
  // Keep the nonce and any late receipt available to the owning viewer. A
  // cancelled fetch would hide an allocated session from its cleanup path.
  return officeRequest("POST", `/api/information/entries/${encodeURIComponent(id)}/office-view`, { version, instanceId, operationId });
}
export function validInformationOfficeView(value: InformationOfficeView, id: string, version: number): boolean {
  const source = value?.source, session = value?.session;
  const config = value?.config as { document?: { permissions?: { edit?: boolean; review?: boolean } }; editorConfig?: { mode?: string } } | undefined;
  return !!source && !!session && source.entry_id === id && source.version === version && /^[a-f0-9]{64}$/.test(source.sha256) && /^[a-f0-9]{64}$/.test(source.snapshot_sha256)
    && source.path.startsWith(`information-assets/${source.sha256}/`) && /^information-assets\/[a-f0-9]{64}\/[^/\\\u0000]+$/.test(source.path)
    && typeof source.title === "string" && !!source.title.trim() && ["word", "cell"].includes(session.editor)
    && session.path === source.path && session.canvasVersion === source.sha256 && session.dirty === false && session.closed === false
    && (session as OfficeSession & { readOnly?: boolean }).readOnly === true
    && config?.editorConfig?.mode === "view" && config.document?.permissions?.edit === false && config.document.permissions.review === false
    && !!session.id && !!session.documentId && value.instanceOwned === true && !!value.bridgeToken && !!value.bridgeURL;
}
export function informationOfficeViewSnapshot(opened: InformationOfficeView, binding: OfficeSelectionBinding, session: OfficeSession, result: unknown): OfficeReferenceSnapshot {
  if (!validInformationOfficeView({ ...opened, session }, opened.source.entry_id, opened.source.version)) throw new Error("SELECTION_CHANGED");
  const selection = officeSelectionSnapshot(binding, session, result);
  return { path: opened.source.path, title: opened.source.title, editor: selection.editor, text: selection.text,
    canvas_version: `disk:${opened.source.sha256}`, dirty: false, captured_at: selection.captured_at,
    information: { entry_id: opened.source.entry_id, version: opened.source.version, snapshot_sha256: opened.source.snapshot_sha256 },
    ...(selection.editor === "cell" ? { sheet: selection.sheet, range: selection.range } : { locator: selection.locator }) };
}
