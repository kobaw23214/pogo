import { describe, expect, it } from "vitest";
import { informationComposerParts, informationInsertionCurrent } from "./informationComposer";
import { composerInputParts } from "./orderedInput";
import type { AttachmentMeta, MessageInputPart } from "@/api/types";

const attachment: AttachmentMeta = { path: "uploads/information/a/report.txt", name: "研究报告.txt", kind: "text", mime: "text/plain", size: 8, created: "2026-10-18" };
describe("information references in a conversation", () => {
  it("retains the selected snapshot, atomic nesting and order through history serialization", () => {
    const parts: MessageInputPart[] = [
      { type: "text", text: "先读取 " },
      { type: "shortcut", shortcut_id: "inf_abc_v2", title: "研究流程", text: "", parts: [
        { type: "text", text: "阅读报告，然后运行 " }, { type: "file", path: attachment.path, as_file: true }, { type: "skill", name: "office" },
      ] },
      { type: "text", text: " 再汇总" },
      { type: "file", path: attachment.path, as_file: true },
    ];
    const draft = informationComposerParts(parts, [attachment]);
    expect(draft.map(part => part.type)).toEqual(["text", "shortcut", "text", "file"]);
    expect(draft[1]).toMatchObject({ title: "研究流程", parts: parts[1].type === "shortcut" ? parts[1].parts : [] });
    expect(draft[3]).toMatchObject({ label: "研究报告.txt", path: attachment.path, asFile: true });
    expect(composerInputParts(draft, key => [key])).toEqual(parts);
  });
  it("keeps a resolved image's explicit file choice", () => {
    const image = { ...attachment, path: "uploads/information/a/chart.png", kind: "image", name: "chart.png" };
    expect(informationComposerParts([{ type: "file", path: image.path, as_file: true }], [image])[0]).toMatchObject({ type: "file", asFile: true });
  });
  it("rejects unknown reference kinds", () => {
    expect(() => informationComposerParts([{ type: "unknown" } as unknown as MessageInputPart], [])).toThrow();
  });
  it("discards late resolution after navigation, new typing, layout changes or disposal", () => {
    const origin = { session: "s1", draft: '[[],"inline"]' };
    expect(informationInsertionCurrent(origin, "s1", origin.draft, false)).toBe(true);
    expect(informationInsertionCurrent(origin, "s2", origin.draft, false)).toBe(false);
    expect(informationInsertionCurrent(origin, null, origin.draft, false)).toBe(false);
    expect(informationInsertionCurrent(origin, "s1", '[[],"top"]', false)).toBe(false);
    expect(informationInsertionCurrent(origin, "s1", '["new text","inline"]', false)).toBe(false);
    expect(informationInsertionCurrent(origin, "s1", origin.draft, true)).toBe(false);
  });
});
