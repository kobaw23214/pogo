import { describe, expect, it, vi } from "vitest";
import { captureStarterFiles, prepareStarterFileParts } from "./starterDraftFiles";
import { fileComposerPart, type ComposerPart } from "./composerParts";

describe("saved shortcut file drafts", () => {
  it("defers uploads, detaches state and preserves repeated mixed occurrences", async () => {
    const blob = new Blob(["report"]);
    const file = { key: "file1", name: "结果.xlsx", status: "ready" as const, file: blob };
    const reference = fileComposerPart({ key: file.key, label: file.name, status: "ready", asFile: true });
    const parts: ComposerPart[] = [{ type: "text", text: "用" }, reference, { type: "text", text: "再核对" }, { ...reference, id: "again" }];
    const upload = vi.fn(async () => ({ path: "uploads/结果.xlsx" }));
    const snapshot = captureStarterFiles(parts, [file]);
    expect(upload).not.toHaveBeenCalled();
    const result = await prepareStarterFileParts(parts, snapshot, upload);
    expect(result).toEqual([{ type: "text", text: "用" }, { type: "file", path: "uploads/结果.xlsx", as_file: true }, { type: "text", text: "再核对" }, { type: "file", path: "uploads/结果.xlsx", as_file: true }]);
    expect(upload).toHaveBeenCalledTimes(1);
    expect(file).not.toHaveProperty("path");
    expect(snapshot.get("file1")?.file).toBe(blob);
  });

  it("retains successful folder members across a retry", async () => {
    const parts = [fileComposerPart({ key: "folder", label: "资料/", status: "ready" })];
    const files = captureStarterFiles(parts, [{ key: "folder", name: "资料/", status: "ready", members: [
      { file: new Blob(["a"]), name: "a.csv", dir: "资料" },
      { file: new Blob(["b"]), name: "b.csv", dir: "资料" },
    ] }]);
    const upload = vi.fn().mockResolvedValueOnce({ path: "uploads/资料/a.csv" }).mockRejectedValueOnce(new Error("断线"));
    await expect(prepareStarterFileParts(parts, files, upload)).rejects.toThrow("断线");
    upload.mockResolvedValueOnce({ path: "uploads/资料/b.csv" });
    expect(await prepareStarterFileParts(parts, files, upload)).toEqual([
      { type: "file", path: "uploads/资料/a.csv", as_file: true }, { type: "file", path: "uploads/资料/b.csv", as_file: true },
    ]);
    expect(upload).toHaveBeenCalledTimes(3);
  });

  it("rejects unresolved or preparing files before any upload", async () => {
    const ready = fileComposerPart({ key: "a", label: "a.txt", status: "ready" });
    const pending = fileComposerPart({ key: "b", label: "b.png", status: "uploading" });
    const files = captureStarterFiles([ready, pending], [{ key: "a", name: "a.txt", status: "ready", file: new Blob(["a"]) }]);
    const upload = vi.fn();
    await expect(prepareStarterFileParts([ready, pending], files, upload)).rejects.toThrow("文件尚未准备好");
    expect(upload).not.toHaveBeenCalled();
  });

  it("edits already durable handles without uploading and preserves labels as data", async () => {
    const parts: ComposerPart[] = [{ type: "text", text: "读取" }, fileComposerPart({ key: "saved", path: "starter-assets/abc/模板.xlsx", label: "模板.xlsx", asFile: true, status: "ready" })];
    const upload = vi.fn();
    expect(await prepareStarterFileParts(parts, new Map(), upload)).toEqual([
      { type: "text", text: "读取" }, { type: "file", path: "starter-assets/abc/模板.xlsx", as_file: true },
    ]);
    expect(upload).not.toHaveBeenCalled();
  });
});
