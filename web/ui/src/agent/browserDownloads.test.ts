import { describe, expect, it, vi } from "vitest";
import { browserDownloadSize, browserDownloadSizeSummary, browserDownloadStatus, observeBrowserDownloads, publishBrowserDownloads, type BrowserDownloadSnapshot } from "./browserDownloads";
import type { BrowserDownload } from "./nativeBrowser";

describe("browser download delivery", () => {
  it("routes updates only to this conversation and releases hidden/closed subscribers", () => {
    const first = vi.fn(), second = vi.fn(), other = vi.fn();
    const stopFirst = observeBrowserDownloads("a", first), stopSecond = observeBrowserDownloads("a", second), stopOther = observeBrowserDownloads("b", other);
    const snapshot: BrowserDownloadSnapshot = { downloads: [{ id: "download-1", filename: "订单.xlsx", state: "saved", path: "uploads/browser/1/订单.xlsx", received_bytes: 1024, total_bytes: 1024, created_at: "2026-10-13T00:00:00Z" }] };
    publishBrowserDownloads("a", snapshot);
    expect(first).toHaveBeenCalledWith(snapshot); expect(second).toHaveBeenCalledWith(snapshot); expect(other).not.toHaveBeenCalled();
    stopFirst(); publishBrowserDownloads("a", snapshot);
    expect(first).toHaveBeenCalledTimes(1); expect(second).toHaveBeenCalledTimes(2);
    stopSecond(); stopOther(); publishBrowserDownloads("a", snapshot);
    expect(second).toHaveBeenCalledTimes(2);
  });
});

describe("download feedback", () => {
  const item: BrowserDownload = { id: "download-1", filename: "情报共享.xlsx", state: "failed", received_bytes: 0, total_bytes: 98426744, created_at: "2026-10-14T00:00:00Z" };
  it("shows the known file size even when download was rejected before receiving bytes", () => {
    expect(browserDownloadSizeSummary(item)).toBe("93.9 MB");
    expect(browserDownloadStatus({ ...item, error_code: "DOWNLOAD_TOO_LARGE" })).toBe("too_large");
  });
  it("distinguishes cancellation, storage quota and a completed download that could not be imported", () => {
    expect(browserDownloadStatus({ ...item, error_code: "DOWNLOAD_CANCELED" })).toBe("canceled");
    expect(browserDownloadStatus({ ...item, error_code: "DOWNLOAD_TOTAL_LIMIT" })).toBe("total_limit");
    expect(browserDownloadStatus({ ...item, error_code: "IMPORT_FAILED" })).toBe("import_failed");
    expect(browserDownloadStatus({ ...item, error_code: "ATTACHMENT_REJECTED" })).toBe("rejected_file");
    expect(browserDownloadStatus({ ...item, error_code: "DOWNLOAD_TYPE_NOT_ALLOWED" })).toBe("type_not_allowed");
    expect(browserDownloadStatus({ ...item, error_code: "DOWNLOAD_SIZE_MISMATCH" })).toBe("invalid_file");
    expect(browserDownloadStatus({ ...item, error_code: "unknown" })).toBe("failed");
  });
  it("shows progress against the full file and never marks in-progress data as saved", () => {
    expect(browserDownloadSizeSummary({ ...item, state: "in_progress", received_bytes: 5 * 1024 ** 2 })).toBe("5.0 MB / 93.9 MB");
    expect(browserDownloadStatus({ ...item, state: "in_progress", error_code: "DOWNLOAD_TOO_LARGE" })).toBe("in_progress");
    expect(browserDownloadStatus({ ...item, state: "completed" })).toBe("completed");
    expect(browserDownloadSizeSummary({ ...item, state: "saved", size: 1024 })).toBe("1.0 KB");
  });
  it("formats empty, unknown and multi-gigabyte sizes without invalid labels", () => {
    expect(browserDownloadSize(Number.NaN)).toBe("0 B");
    expect(browserDownloadSize(-1)).toBe("0 B");
    expect(browserDownloadSize(2 * 1024 ** 3)).toBe("2.0 GB");
    expect(browserDownloadSizeSummary({ ...item, total_bytes: 0 })).toBe("");
  });
});
