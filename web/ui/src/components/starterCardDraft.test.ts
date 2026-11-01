import { describe, expect, it } from "vitest";
import { composerDisplayText, type ComposerPart } from "@/agent/composerParts";
import { cloneStarterDraft, prepareStableStarterDraft, starterCardComposerParts, starterDraftHasContent, starterDraftTitle } from "./starterCardDraft";

const observation = '<page-element source="native" url="https://example.test/">\ntitle: 系统\nelement: button "导出"\ntext: 导出\n</page-element>';
const mixed: ComposerPart[] = [
  { type: "text", text: "1. 点击 " },
  { type: "element", id: "ref-1", text: observation, label: 'button "导出"', pick: { key: "k", label: "导出", pageTitle: "系统", block: observation } },
  { type: "text", text: " 再处理 " },
  { type: "file", id: "file-1", key: "upload-1", path: "starter-assets/id/中文 报告.xlsx", label: "中文 报告.xlsx", asFile: true, status: "ready" },
  { type: "text", text: "，重复点击 " },
  { type: "element", id: "ref-2", text: observation, label: 'button "导出"' },
];

describe("Reusable shortcut mixed drafts", () => {
  it("round trips text, duplicate observations and durable files without reordering", () => {
    const saved = prepareStableStarterDraft({ parts: mixed, inputLayout: "inline" });
    expect(saved.parts.map(part => part.type)).toEqual(["text", "element", "text", "file", "text", "element"]);
    expect(saved.parts[3]).toEqual({ type: "file", path: "starter-assets/id/中文 报告.xlsx", as_file: true });
    const restored = starterCardComposerParts({ title: "流程", ...saved });
    expect(restored.map(part => part.type)).toEqual(mixed.map(part => part.type));
    expect(restored[3]).toMatchObject({ type: "file", label: "中文 报告.xlsx", asFile: true, status: "ready" });
    expect(restored[1]).toMatchObject({ type: "element", text: observation });
    expect(restored[5]).toMatchObject({ type: "element", text: observation });
    expect(restored[1]).not.toMatchObject({ id: (restored[5] as { id: string }).id });
    expect(saved.input_layout).toBe("inline");
    expect(saved.prompt).toBe(composerDisplayText(mixed));
  });
  it("copies the draft without taking ownership of the live composer", () => {
    const source = { parts: mixed, inputLayout: "top" as const };
    const copy = cloneStarterDraft(source);
    (copy.parts[1] as typeof mixed[1] & { pick: { pageTitle: string } }).pick.pageTitle = "编辑中";
    expect((mixed[1] as { pick: { pageTitle: string } }).pick.pageTitle).toBe("系统");
    expect(starterDraftTitle(copy)).toContain("1. 点击");
  });
  it("refuses missing, failed and pending files instead of dropping them", () => {
    for (const state of [{ path: undefined }, { status: "uploading" as const }, { status: "error" as const }]) {
      const part = { ...(mixed[3] as Extract<ComposerPart, { type: "file" }>), ...state };
      expect(() => prepareStableStarterDraft({ parts: [part], inputLayout: "top" })).toThrow("File is not ready");
    }
  });
  it("supports reference-only cards, plain legacy prompts and readable title defaults", () => {
    expect(starterDraftHasContent({ parts: [mixed[3]], inputLayout: "top" })).toBe(true);
    expect(starterDraftHasContent({ parts: [{ type: "text", text: "  \n " }], inputLayout: "top" })).toBe(false);
    expect(starterCardComposerParts({ title: "旧卡片", prompt: "你好\n第二行" })).toEqual([{ type: "text", text: "你好\n第二行" }]);
    expect(starterDraftTitle({ parts: [{ type: "text", text: " 流程 \n 一 " }], inputLayout: "top" })).toBe("流程 一");
  });
  it("preserves image and explicit image-as-file choices per repeated occurrence", () => {
    const path = "starter-assets/picture/screenshot.png";
    const parts = starterCardComposerParts({ title: "截图", parts: [{ type: "file", path }, { type: "file", path, as_file: true }, { type: "file", path, as_file: false }] });
    expect(parts.map(part => part.type === "file" ? part.asFile : null)).toEqual([undefined, true, undefined]);
    const saved = prepareStableStarterDraft({ parts, inputLayout: "inline" });
    expect(saved.parts).toEqual([{ type: "file", path }, { type: "file", path, as_file: true }, { type: "file", path }]);
    const reverse = starterCardComposerParts({ title: "截图", parts: [{ type: "file", path, as_file: true }, { type: "file", path }] });
    expect(prepareStableStarterDraft({ parts: reverse, inputLayout: "top" }).parts).toEqual([{ type: "file", path, as_file: true }, { type: "file", path }]);
  });
});
