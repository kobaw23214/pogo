import type { BrowserDownload } from "./nativeBrowser";

export interface BrowserDownloadSnapshot {
  downloads: BrowserDownload[];
  max_file_bytes?: number;
  max_total_bytes?: number;
  rejected_count?: number;
  last_rejection?: string;
}

export function browserDownloadSize(bytes: number): string {
  const value = Number.isFinite(bytes) ? Math.max(0, bytes) : 0;
  if (value >= 1024 ** 3) return `${(value / 1024 ** 3).toFixed(1)} GB`;
  if (value >= 1024 ** 2) return `${(value / 1024 ** 2).toFixed(1)} MB`;
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${Math.ceil(value)} B`;
}

export function browserDownloadSizeSummary(item: BrowserDownload): string {
  if (item.state === "in_progress" && item.total_bytes > 0) {
    return `${browserDownloadSize(item.received_bytes)} / ${browserDownloadSize(item.total_bytes)}`;
  }
  const bytes = item.size || item.total_bytes || item.received_bytes;
  return bytes > 0 ? browserDownloadSize(bytes) : "";
}

export function browserDownloadStatus(item: BrowserDownload): string {
  if (item.state !== "failed") return item.state;
  return ({ DOWNLOAD_TOO_LARGE: "too_large", DOWNLOAD_TOTAL_LIMIT: "total_limit", DOWNLOAD_CANCELED: "canceled",
    DOWNLOAD_TIMEOUT: "timeout", DOWNLOAD_EXPIRED: "expired", DOWNLOAD_FILE_UNAVAILABLE: "unavailable",
    IMPORT_FAILED: "import_failed", ATTACHMENT_REJECTED: "rejected_file", DOWNLOAD_TYPE_NOT_ALLOWED: "type_not_allowed",
    DOWNLOAD_INVALID_FILE: "invalid_file", DOWNLOAD_SIZE_MISMATCH: "invalid_file", DOWNLOAD_EMPTY: "empty_file" } as Record<string, string>)[item.error_code ?? ""] ?? "failed";
}
// Delivery reuses the conversation stream. No per-tab polling or file bytes
// enter browser rendering; a newly mounted view gets its initial host state.
const observers = new Map<string, Set<(snapshot: BrowserDownloadSnapshot) => void>>();
export function observeBrowserDownloads(sessionId: string, listener: (snapshot: BrowserDownloadSnapshot) => void) {
  let group = observers.get(sessionId);
  if (!group) { group = new Set(); observers.set(sessionId, group); }
  group.add(listener);
  return () => { group.delete(listener); if (!group.size && observers.get(sessionId) === group) observers.delete(sessionId); };
}
export function publishBrowserDownloads(sessionId: string, snapshot: BrowserDownloadSnapshot) {
  for (const listener of observers.get(sessionId) ?? []) listener(snapshot);
}
