/** A bounded, display-only view of confirmed Office operation receipts. */
export type OfficeActivityPhase = "reading" | "writing" | "verifying" | "saving";
export type OfficeActivityStatus = "pending" | "running" | "succeeded" | "rejected" | "unknown";

export interface OfficeActivityChange {
  label?: string;
  before?: string;
  after?: string;
}

export interface OfficeActivityItem {
  id: string;
  operation: string;
  phase: OfficeActivityPhase;
  status: OfficeActivityStatus;
  location?: string;
  summary?: string;
  /** Confirmed changed targets only. Reads, rejected writes and guesses are not changes. */
  changeCount?: number;
  changes?: readonly OfficeActivityChange[];
  /** Receipt text or the returned list is an excerpt, not a complete document diff. */
  truncated?: boolean;
  error?: string;
  startedAt?: number;
  completedAt?: number;
}

export const OFFICE_ACTIVITY_VISIBLE_LIMIT = 20;

export function officeActivityShownChanges(item: OfficeActivityItem): number {
  return Math.min(4, item.changes?.length ?? 0);
}

export function officeActivityTotalChanges(item: OfficeActivityItem): number {
  return Math.max(item.changes?.length ?? 0, Number.isSafeInteger(item.changeCount) && item.changeCount! > 0 ? item.changeCount! : 0);
}

export function officeActivityIsChange(item: OfficeActivityItem): boolean {
  return item.phase === "writing" || item.status === "rejected" || item.status === "unknown";
}

export function officeActivityWindow(items: readonly OfficeActivityItem[], changesOnly = false): OfficeActivityItem[] {
  const matching = changesOnly ? items.filter(officeActivityIsChange) : items;
  return matching.slice(-OFFICE_ACTIVITY_VISIBLE_LIMIT).reverse();
}

export function officeActivityChangedCount(items: readonly OfficeActivityItem[]): number {
  return items.reduce((sum, item) => item.status === "succeeded" && item.phase === "writing" &&
    Number.isSafeInteger(item.changeCount) && item.changeCount! > 0 ? sum + item.changeCount! : sum, 0);
}

export function officeActivityHeadline(items: readonly OfficeActivityItem[]): OfficeActivityItem | undefined {
  for (let i = items.length - 1; i >= 0; i--) {
    if (items[i]?.status === "running") return items[i];
  }
  for (let i = items.length - 1; i >= 0; i--) {
    if (items[i]?.status === "pending") return items[i];
  }
  for (let i = items.length - 1; i >= 0; i--) {
    if (items[i]?.status === "unknown") return items[i];
  }
  return items[items.length - 1];
}

/** Keep rendering bounded without changing or injecting any document content. */
export function officeActivityText(value: string | undefined, limit = 320): string {
  if (!value) return "";
  let text = "", count = 0;
  for (const point of value) {
    const code = point.codePointAt(0)!;
    if (code < 32 && code !== 9 && code !== 10 && code !== 13 || code === 127) continue;
    if (count >= limit) return text + "…";
    text += point;
    count++;
  }
  return text;
}
