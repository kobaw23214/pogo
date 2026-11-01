import type { OfficeSession } from "./officeTransport";

export const validOfficeVersion = (value: unknown): value is string => typeof value === "string" && /^[a-f0-9]{64}$/.test(value);

/** A disk version is distinct from the editor's workspace-save counter. */
export function officeHasNewVersion(session: OfficeSession | null): boolean {
  // An atomic replacement may keep identical bytes but change file identity.
  // The host's externalChange flag covers that case as well as content changes.
  return !!session && session.externalChange === true && validOfficeVersion(session.canvasVersion) && validOfficeVersion(session.diskVersion);
}

export function officeStatusIsOlder(current: OfficeSession | null, next: OfficeSession): boolean {
  if (!current) return false;
  return next.revision < current.revision || next.dirtyEpoch < current.dirtyEpoch || next.persistedEpoch < current.persistedEpoch ||
    (current.syncRevision !== undefined && (next.syncRevision === undefined || next.syncRevision < current.syncRevision));
}

/** Never replace a dirty, saving, queued or outcome-unknown editor. Local input
 * and unconfirmed bridge receipts must additionally be checked by the caller.
 */
export function officeSessionClean(session: OfficeSession | null): boolean {
  return !!session && !session.dirty && session.pending === false && !session.blocked && !session.closed &&
    !session.savePhase && session.saveState !== "saving" && session.saveState !== "error" && session.dirtyEpoch === session.persistedEpoch;
}

export function officeReplacementMatches(previous: OfficeSession, next: OfficeSession, id: string): boolean {
  return next.id === id && next.id !== previous.id && next.documentId !== previous.documentId && next.generation > previous.generation &&
    next.conversationId === previous.conversationId && next.fileRef === previous.fileRef && !!next.fileRef && next.path === previous.path &&
    next.fileType === previous.fileType && !next.closed && validOfficeVersion(next.canvasVersion) &&
    next.canvasVersion === previous.diskVersion && next.diskVersion === next.canvasVersion;
}
