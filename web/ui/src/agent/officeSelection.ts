import type { OfficeReferenceSnapshot } from "@/api/types";
import type { OfficeSession } from "./officeTransport";
import { validOfficeVersion } from "./officeVersionSync";

/** No native handles escape into a reusable reference. The executor and local
 * edit epochs are used only to reject an asynchronous, stale capture. */
export interface OfficeSelectionBinding {
  sessionId: string; documentId: string; instanceId: string; generation: number;
  canvasVersion: string; dirtyEpoch: number; localEpoch: number; lifecycle: number;
  path: string; conversationId: string; targetConversationId: string; editor: "word" | "cell";
}

export function bindOfficeSelection(session: OfficeSession, instanceId: string, localEpoch: number, lifecycle: number, targetConversationId: string): OfficeSelectionBinding {
  if (!validOfficeVersion(session.canvasVersion) || !Number.isSafeInteger(session.dirtyEpoch) || !targetConversationId || session.closed) throw new Error("SELECTION_CHANGED");
  return { sessionId: session.id, documentId: session.documentId, instanceId, generation: session.generation,
    canvasVersion: session.canvasVersion, dirtyEpoch: session.dirtyEpoch, localEpoch, lifecycle,
    path: session.path, conversationId: session.conversationId, targetConversationId, editor: session.editor };
}

export function officeSelectionStillCurrent(binding: OfficeSelectionBinding, session: OfficeSession | null, instanceId: string, localEpoch: number, lifecycle: number, targetConversationId: string): boolean {
  return !!session && !session.closed && session.id === binding.sessionId && session.documentId === binding.documentId && instanceId === binding.instanceId &&
    session.generation === binding.generation && session.canvasVersion === binding.canvasVersion && session.dirtyEpoch === binding.dirtyEpoch &&
    localEpoch === binding.localEpoch && lifecycle === binding.lifecycle && session.path === binding.path && session.conversationId === binding.conversationId &&
    targetConversationId === binding.targetConversationId && session.editor === binding.editor;
}

export const officeSelectionArgs = (binding: OfficeSelectionBinding): Record<string, unknown> => ({ documentId: binding.documentId,
  instanceId: binding.instanceId, canvasVersion: binding.canvasVersion, dirtyEpoch: binding.dirtyEpoch });

function boundedRange(value: unknown): value is string {
  if (typeof value !== "string" || !/^[A-Z]{1,3}[1-9]\d{0,6}(?::[A-Z]{1,3}[1-9]\d{0,6})?$/.test(value)) return false;
  const cells = value.split(":").map(cell => {
    const [, letters, row] = /^([A-Z]+)(\d+)$/.exec(cell)!;
    let col = 0; for (const char of letters!) col = col * 26 + char.charCodeAt(0) - 64;
    return { row: Number(row), col };
  });
  const first = cells[0]!, last = cells[1] ?? first, rows = last.row - first.row + 1, cols = last.col - first.col + 1;
  return rows > 0 && cols > 0 && rows * cols <= 400 && last.row <= 1048576 && last.col <= 16384;
}

export function officeSelectionSnapshot(binding: OfficeSelectionBinding, session: OfficeSession, raw: unknown, capturedAt = new Date().toISOString()): OfficeReferenceSnapshot {
  if (!raw || typeof raw !== "object") throw new Error("INVALID_SELECTION");
  const result = raw as Record<string, unknown>;
  if (result.kind !== binding.editor || typeof result.text !== "string" || !result.text.trim() || result.text.length > 32000 || new TextEncoder().encode(result.text).length > 65536 || /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/u.test(result.text)) throw new Error("INVALID_SELECTION");
  const snapshot: OfficeReferenceSnapshot = { path: binding.path, title: session.title, editor: binding.editor, text: result.text,
    canvas_version: binding.canvasVersion, dirty: session.dirty, dirty_epoch: binding.dirtyEpoch,
    captured_at: capturedAt, source_session: binding.targetConversationId };
  if (binding.editor === "cell") {
    if (typeof result.sheet !== "string" || !result.sheet.trim() || result.sheet.length > 128 || !boundedRange(result.range)) throw new Error("INVALID_SELECTION");
    snapshot.sheet = result.sheet; snapshot.range = result.range;
  } else {
    // A read-only exact-text locator never creates a bookmark or moves the
    // selection. Reopening can show the source and version without promising
    // that a later, edited document still has this exact span.
    snapshot.locator = { kind: "text", exact: result.text };
  }
  return snapshot;
}
