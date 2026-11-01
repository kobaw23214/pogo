import type { OfficeActivitySnapshot } from "./officeTransport";
import { officeActivityText, type OfficeActivityItem, type OfficeActivityStatus } from "./officeActivityView";

type Translate = (key: string, values?: Record<string, number | string>) => string;
const statuses = new Set(["queued", "running", "succeeded", "rejected", "unknown"]);
const phases = new Set(["reading", "writing", "verifying", "saving", "history"]);
const mutations = new Set(["word.replace", "sheet.apply"]);
const stamp = (value?: string) => { const parsed = value ? Date.parse(value) : NaN; return Number.isFinite(parsed) ? parsed : undefined; };

/** Discard stale status replies without reallocating the visible receipt list. */
export function newerOfficeActivity(current: OfficeActivitySnapshot, incoming?: OfficeActivitySnapshot): OfficeActivitySnapshot | null {
  if (!incoming || !Number.isSafeInteger(incoming.version) || incoming.version <= current.version || !Array.isArray(incoming.items)) return null;
  // The host bounds this projection too. Keep the view safe with an old or
  // malformed host; display metadata never gains document editing authority.
  return { version: incoming.version, items: incoming.items.slice(0, 20) };
}

/** Project only trusted-host agent receipts, never infer success from a request. */
export function officeActivityItems(snapshot: OfficeActivitySnapshot, t: Translate): OfficeActivityItem[] {
  const seen = new Set<string>();
  const result: OfficeActivityItem[] = [];
  for (const record of snapshot.items.slice(0, 20)) {
    if (!record || record.actor !== "agent" || typeof record.operationId !== "string" || seen.has(record.operationId) || !statuses.has(record.status) || !phases.has(record.phase)) continue;
    seen.add(record.operationId);
    const phase = record.phase === "history" ? "writing"
      : record.phase === "verifying" && record.status === "succeeded" && !record.verified ? "reading" : record.phase;
    let status: OfficeActivityStatus = record.status === "queued" ? "pending" : record.status;
    // A successful transport without native before/after agreement is not a
    // confirmed document change, and a save requires its commit revision.
    if (status === "succeeded" && ((mutations.has(record.operation) && record.verified !== true) || (record.phase === "saving" && !(record.verified && record.revision && record.revision > 0)))) status = "unknown";
    const location = record.target?.kind === "cell" && record.target.sheet && record.target.range
      ? `${officeActivityText(record.target.sheet, 128)}!${officeActivityText(record.target.range, 48)}` : undefined;
    const changes = status === "succeeded" && record.verified && Array.isArray(record.changes)
      ? record.changes.slice(0, 6).map(change => ({ label: change.address ? officeActivityText(change.address, 48) : undefined,
        before: officeActivityText(change.before, 160), after: officeActivityText(change.after, 160) })) : undefined;
    let summary: string | undefined;
    if (record.phase === "history") summary = t("officeEditor.undoNote");
    else if (status === "succeeded" && record.phase === "verifying") summary = t(record.verified ? "officeActivity.rangeConfirmed" : "officeActivity.rangeReadOnly");
    else if (status === "succeeded" && mutations.has(record.operation) && record.changeCount === 0) summary = t("officeActivity.noDifference");
    else if (status === "succeeded" && phase === "saving") summary = t("officeActivity.savedVersion", { version: record.revision! });
    else if (status === "unknown") summary = t("officeActivity.receiptUnknown");
    const item: OfficeActivityItem = {
      id: record.operationId, operation: record.operation, phase, status, location, summary, changes,
      changeCount: status === "succeeded" && record.verified && mutations.has(record.operation) && Number.isSafeInteger(record.changeCount) && record.changeCount! >= 0 ? record.changeCount : undefined,
      error: record.message ? officeActivityText(record.message, 240) : undefined,
      startedAt: stamp(record.startedAt ?? record.queuedAt), completedAt: stamp(record.finishedAt),
      truncated: !!record.truncated,
    };
    result.push(item);
  }
  return result.reverse();
}
