import { describe, expect, it } from "vitest";
import type { CiteSource } from "@/api/types";
import { citePresentation } from "./citePresentation";

const source = (overrides: Partial<CiteSource> = {}): CiteSource => ({
  label: "S1", type: "file_read", title: "src/SettingsDialog.vue",
  locator: "file://src/SettingsDialog.vue#L575-582", call_id: "c1",
  excerpt: "", hash: "abc", ts: 1, ...overrides,
});

describe("citePresentation", () => {
  it("shows the file identity and a validated recorded range separately", () => {
    expect(citePresentation(source())).toEqual({
      title: "src/SettingsDialog.vue", label: "SettingsDialog.vue",
      location: ":575–582", iconType: "file_read",
    });
    expect(citePresentation(source({ locator: "file://src/SettingsDialog.vue#L575" })).location).toBe(":575");
    expect(citePresentation(source({ locator: "file://src/SettingsDialog.vue#L575-575" })).location).toBe(":575");
  });

  it("keeps complete paths for identical basenames and does not decode filenames", () => {
    const one = citePresentation(source({ title: "client/资料 #1%20.md", locator: "file://client/资料 #1%20.md#L2-4" }));
    const two = citePresentation(source({ title: "server/资料 #1%20.md", locator: "file://server/资料 #1%20.md" }));
    expect(one.label).toBe(two.label);
    expect(one.title).toBe("client/资料 #1%20.md");
    expect(two.title).toBe("server/资料 #1%20.md");
    expect(one.location).toBe(":2–4");
    expect(two.location).toBeUndefined();
  });

  it("never promotes an ambiguous filename, unsafe path or write into a line target", () => {
    const ambiguous = citePresentation(source({ title: "notes/report#L10-14", locator: "file://notes/report#L10-14" }));
    expect(ambiguous.label).toBe("report#L10-14");
    expect(ambiguous.location).toBeUndefined();
    for (const override of [
      { title: "Report", locator: "file://notes/report#L10-14" },
      { title: "../secret", locator: "file://../secret#L1-4" },
      { type: "file_write" },
      { locator: "file://src/SettingsDialog.vue#L10-4" },
    ]) expect(citePresentation(source(override)).location).toBeUndefined();
  });

  it("identifies web publishers first and retains the full URL in the title", () => {
    const page = source({ type: "web_page", title: "Citation guide", locator: "https://www.example.com/guide?q=1#intro" });
    expect(citePresentation(page)).toEqual({
      title: "Citation guide · https://www.example.com/guide?q=1#intro",
      label: "example.com", iconType: "web_page",
    });
    expect(citePresentation({ ...page, title: page.locator }).label).toBe("example.com");
    expect(citePresentation({ ...page, title: "" }).label).toBe("example.com");
  });

  it("does not guess a publisher from page titles or collapse distinct hosts", () => {
    const page = source({ type: "web_page", title: "OpenAI official guide", locator: "https://other.example.test/article" });
    expect(citePresentation(page).label).toBe("other.example.test");
    expect(citePresentation({ ...page, locator: "https://docs.example.test/article" }).label).toBe("docs.example.test");
    expect(citePresentation({ ...page, locator: "http://localhost:8929/project" }).label).toBe("localhost:8929");
  });

  it("uses a recognized PDF identity without turning output page markers into positions", () => {
    const pdf = source({
      type: "command_exec", title: "pdf-text reports/security.pdf", locator: "cmd://pdf-text reports/security.pdf",
      content: "# 安全报告.pdf · 第 1-6 页 / 共 6 页\n===== Page 1 =====\n报告内容",
    });
    expect(citePresentation(pdf)).toEqual({ title: "安全报告.pdf", label: "安全报告.pdf", iconType: "file_read" });
    expect(citePresentation({ ...pdf, content: "安全报告.pdf 第 6 页" }).label).toBe(pdf.title);
  });

  it("keeps commands readable while preserving historical status in the complete title", () => {
    for (const prefix of ["exit 0 · ", "exit -1 | "]) {
      const title = `${prefix}npm run test -- --run`;
      expect(citePresentation(source({ type: "command_exec", title, locator: "cmd://npm run test" }))).toEqual({
        title, label: "npm run test -- --run", iconType: "command_exec",
      });
    }
    expect(citePresentation(source({ type: "search_result", title: "src/.*timeout", locator: "file://src" })).label).toBe("src/.*timeout");
  });
});
