import { describe, expect, it } from "vitest";
import type { CiteSource } from "@/api/types";
import { citationFileTarget, informationCitationTarget } from "./citeNavigation";

describe("informationCitationTarget", () => {
  const id = "inf_" + "a".repeat(32), hash = "b".repeat(64);
  const entry = (): CiteSource => ({ label: "S1", type: "information", title: "研究流程", call_id: "c1", excerpt: "版本一", hash: "result-hash", ts: 1,
    locator: `information://${id}?version=1#${hash}`, information: { entry_id: id, version: 1, snapshot_sha256: hash } });
  it("opens the host-recorded immutable version without navigating its URL", () => {
    expect(informationCitationTarget(entry())).toEqual({ id, version: 1 });
    expect(citationFileTarget(entry())).toBeNull();
  });
  it("rejects a plain URL, incomplete stamp, retargeted locator or malformed version", () => {
    const cases: CiteSource[] = [
      { ...entry(), information: undefined }, { ...entry(), type: "web_page" },
      { ...entry(), locator: `information://${id}?version=2#${hash}` },
      { ...entry(), information: { ...entry().information!, version: 0 } },
      { ...entry(), information: { ...entry().information!, entry_id: "../private" } },
      { ...entry(), information: { ...entry().information!, snapshot_sha256: "invalid" } },
    ];
    for (const item of cases) expect(informationCitationTarget(item)).toBeNull();
  });
});

const source = (locator: string, overrides: Partial<CiteSource> = {}): CiteSource => ({
  label: "S1", type: "file_read", title: "src/main.go", locator, call_id: "c1",
  excerpt: "", hash: "abc", ts: 1, ...overrides,
});

describe("citationFileTarget", () => {
  it("preserves a genuine file read’s inclusive line range for navigation", () => {
    expect(citationFileTarget(source("file://src/main.go#L10-14"))).toEqual({ path: "src/main.go", name: "main.go", lineStart: 10, lineEnd: 14 });
    expect(citationFileTarget(source("file://src/main.go#L10"))).toEqual({ path: "src/main.go", name: "main.go", lineStart: 10, lineEnd: 10 });
  });

  it("opens a relative file write without inventing a line range", () => {
    expect(citationFileTarget(source("file://notes/new report.md", { type: "file_write" }))).toEqual({
      path: "notes/new report.md", name: "new report.md",
    });
    expect(citationFileTarget(source("file://notes/report#L10-14", { type: "file_write" }))).toEqual({
      path: "notes/report#L10-14", name: "report#L10-14",
    });
  });

  it("preserves spaces, Unicode, literal URL escapes and hashes in raw filenames", () => {
    const path = "资料/阶段 #2 进度%20记录.md";
    expect(citationFileTarget(source(`file://${path}`, { title: path }))).toEqual({ path, name: "阶段 #2 进度%20记录.md" });
    expect(citationFileTarget(source(`file://${path}#L2-4`, { title: path }))).toEqual({ path, name: "阶段 #2 进度%20记录.md", lineStart: 2, lineEnd: 4 });
  });

  it("preserves a range-shaped suffix when it is part of the recorded filename or ambiguous", () => {
    expect(citationFileTarget(source("file://notes/report#L10-14", { title: "notes/report#L10-14" }))?.path).toBe("notes/report#L10-14");
    expect(citationFileTarget(source("file://notes/report#L10-14", { title: "Report" }))?.path).toBe("notes/report#L10-14");
    expect(citationFileTarget(source("file://notes/report#section-2", { title: "notes/report" }))?.path).toBe("notes/report#section-2");
    expect(citationFileTarget(source("file://notes/report#L10-14", { title: "Report" }))?.lineStart).toBeUndefined();
    expect(citationFileTarget(source("file://notes/report#L10-14", { title: "notes/report#L10-14" }))?.lineEnd).toBeUndefined();
  });

  it("does not treat invalid or reversed line ranges as metadata", () => {
    for (const suffix of ["#L0-4", "#L8-2", "#L1-0", "#L9007199254740992-9007199254740993"]) {
      expect(citationFileTarget(source(`file://src/main.go${suffix}`))?.path).toBe(`src/main.go${suffix}`);
      expect(citationFileTarget(source(`file://src/main.go${suffix}`))?.lineStart).toBeUndefined();
    }
  });

  it("does not make unsafe paths navigable when they have a valid line suffix", () => {
    for (const path of ["../secret", "src/../../secret", "/etc/passwd", "C:/secret", "%2e%2e/secret"]) {
      expect(citationFileTarget(source(`file://${path}#L2-4`, { title: path }))).toBeNull();
    }
  });

  it("normalizes relative Windows separators while keeping the target workspace-relative", () => {
    expect(citationFileTarget(source("file://src\\main.go#L10-14", { title: "src\\main.go" }))).toEqual({
      path: "src/main.go", name: "main.go", lineStart: 10, lineEnd: 14,
    });
  });

  it.each([
    "file://", "file:///etc/passwd", "file:////server/share/file", "file://\\\\server\\share\\file",
    "file://C:/Users/me/secret", "file://C:\\Users\\me\\secret", "file://C:relative.txt",
    "file://../secret", "file://src/../../secret", "file://src\\..\\secret", "file://./report.md",
    "file://src/.. /secret", "file://src//report.md", "file://src/", "file://.",
    "file://a\0b", "file://a\nb", "file://a\tb", "file://file.txt:stream",
    "file://%2e%2e/secret", "file://src%2f..%2fsecret", "file://%5c%5cserver/share", "file://%252e%252e/secret",
  ])("rejects an unsafe or non-file target: %s", (locator) => {
    expect(citationFileTarget(source(locator))).toBeNull();
  });

  it("does not guess navigation from commands, search results, tools or web URLs", () => {
    for (const type of ["command_exec", "search_result", "mcp_tool", "web_page", "subagent"]) {
      expect(citationFileTarget(source("file://uploads/report.pdf", { type }))).toBeNull();
    }
    for (const locator of ["https://example.com/report.pdf", "cmd://cat report.pdf", "tool://read/c1", "FILE://src/main.go"]) {
      expect(citationFileTarget(source(locator))).toBeNull();
    }
  });
});
