import { describe, it, expect } from "vitest";
import type { MessageInputPart, OfficeReferenceSnapshot } from "@/api/types";
import { shortcutComposerPart, officeComposerPart, readComposerReference, composerDisplayText } from "./composerParts";
import { composerInputParts, inputPartsBlocks, blocksComposerParts, orderedDisplayParts } from "./orderedInput";
import { validateInputParts, cloneInputParts, inputPartsText, inputPartsHaveFiles, inputPartsSkills } from "./shortcutStructure";
import { inboxDraftParts, inboxDraftSubmission } from "./conversationInbox";
import { cloneStarterDraft, starterCardComposerParts, prepareStableStarterDraft } from "@/components/starterCardDraft";
import { composerDocument, composerDocumentParts, freshComposerSlice } from "./inlineComposerModel";
import { Slice } from "prosemirror-model";
const page: MessageInputPart = { type: "element", text: '<page-element source="系统" url="https://example.invalid/">\nelement: button "运行"\n</page-element>', favicon: "data:image/png;base64,AAAA" };
const office: OfficeReferenceSnapshot = { path: "uploads/报告.xlsx", title: "报告.xlsx", editor: "cell", text: "地区\t收入\n中国\t100", sheet: "销售", range: "A1:B2", canvas_version: "v2", dirty: true, dirty_epoch: 4, source_session: "session-a", captured_at: "2026-10-16T01:00:00Z" };
const leaf = (id: string, parts?: MessageInputPart[]): MessageInputPart => ({ type: "shortcut", shortcut_id: id, title: id, text: parts ? "DISPLAY FALLBACK ONLY" : "LEAF BODY", language: "sql", ...(parts ? { parts } : {}) });
const mixed: MessageInputPart[] = [{ type: "text", text: "1. " }, page, { type: "text", text: "\n2. " }, { type: "file", path: "uploads/附件.txt", as_file: true }, { type: "skill", name: "office" }, { type: "office", office }, leaf("nested", [{ type: "text", text: "INNER DATA /skill:plain-is-data" }, leaf("second")]), { type: "text", text: "\n结束" }];

describe("recursive shortcut snapshots", () => {
  it("round trips grouped history without duplicate fallback or eating following prose", () => {
    const wire = [leaf("root", mixed), { type: "text" as const, text: "OUTSIDE" }, leaf("other")];
    const blocks = inputPartsBlocks(wire);
    expect(blocks[0]).toMatchObject({ type: "text", text: "[资料：root]\n", input_shortcut: { parts: mixed, block_count: blocks.length - 3 } });
    expect(blocks.filter(block => block.type === "text" && block.text.includes("DISPLAY FALLBACK ONLY"))).toHaveLength(0);
    expect(orderedDisplayParts(blocks).map(part => part.type)).toEqual(["shortcut", "text", "shortcut"]);
    expect(composerInputParts(blocksComposerParts(blocks), key => [key])).toEqual(wire);
    expect(orderedDisplayParts(blocks)[1]).toEqual({ type: "text", text: "OUTSIDE" });
  });
  it("freezes independent nested occurrences across library, draft, clipboard, queue and starter edits", () => {
    const original = cloneInputParts(mixed);
    const part = shortcutComposerPart({ id: "root", title: "流程", content: "卡片摘要", language: "yml", parts: original });
    original[0] = { type: "text", text: "MUTATED LIBRARY" };
    expect(part.parts?.[0]).toEqual(mixed[0]);
    const saved = cloneStarterDraft({ parts: [part], inputLayout: "inline" });
    const mutated = saved.parts[0]; if (mutated.type === "shortcut") mutated.parts![0] = { type: "text", text: "MUTATED STARTER" };
    expect(part.parts?.[0]).toEqual(mixed[0]);
    const doc = composerDocument([part, part]);
    const copied = freshComposerSlice(new Slice(doc.content, 0, 0));
    const restored = composerDocumentParts(doc.type.create(null, copied.content));
    expect(restored[0]).not.toHaveProperty("id", part.id);
    expect(restored[0]).toMatchObject({ language: "yml", parts: mixed });
    expect(new Set(restored.flatMap(p => p.type !== "text" ? [p.id] : [])).size).toBe(2);
    const wire = composerInputParts([part], () => []);
    const queue = inboxDraftParts({ id: "queued", text: "", mode: "queued", status: "queued", revision: 1, created_at: 1, updated_at: 1, parts: wire });
    expect(inboxDraftSubmission(queue).parts).toEqual(wire);
    expect(prepareStableStarterDraft({ parts: starterCardComposerParts({ title: "复用", parts: wire }), inputLayout: "inline" }).parts).toEqual(wire);
    expect(composerDisplayText(queue)).toBe("〔流程〕");
  });
  it("accepts complete Office canvas snapshots without confusing them with file uploads", () => {
    const part = officeComposerPart(office);
    expect(readComposerReference(part)).toMatchObject(part);
    const wire = composerInputParts([part], () => { throw new Error("Office selection is data, never upload"); });
    expect(wire).toEqual([{ type: "office", office }]);
    expect(composerInputParts(blocksComposerParts(inputPartsBlocks(wire)), () => [])).toEqual(wire);
    expect(inputPartsHaveFiles(wire)).toBe(false);
    expect(inputPartsText(wire)).toContain("地区\t收入\n中国\t100");
  });
  it("detects self and indirect cycles while allowing repeated siblings with the same source ID", () => {
    expect(validateInputParts([leaf("A")], ["A"])).toBe("cycle");
    expect(validateInputParts([leaf("B", [leaf("A")])], ["A"])).toBe("cycle");
    expect(validateInputParts([leaf("B"), leaf("B")], ["A"])).toBe("");
    expect(validateInputParts([leaf("A", [leaf("B", [leaf("A")])])])).toBe("cycle");
  });
  it("enforces shared depth, count and byte budgets and rejects forged nested clipboard atoms", () => {
    let nested = leaf("d1"); for (let i = 2; i <= 5; i++) nested = leaf(`d${i}`, [nested]);
    expect(validateInputParts([nested])).toBe("");
    expect(validateInputParts([leaf("d6", [nested])])).toBe("depth");
    expect(validateInputParts(Array.from({ length: 513 }, () => ({ type: "text", text: "a" })))).toBe("nodes");
    expect(validateInputParts(Array.from({ length: 5 }, () => ({ type: "text", text: "a".repeat(65536) })))).toBe("bytes");
    const shortcut = shortcutComposerPart({ id: "root", title: "流程", content: "", parts: mixed });
    expect(readComposerReference(shortcut)).not.toBeNull();
    expect(readComposerReference({ ...shortcut, parts: [leaf("root")] })).toBeNull();
    expect(readComposerReference({ ...shortcut, parts: [{ type: "script", text: "bad" }] })).toBeNull();
    const large = shortcutComposerPart({ id: "large", title: "大资料", content: "x".repeat(70000), parts: [{ type: "text", text: "x".repeat(70000) }, { type: "skill", name: "office" }] });
    expect(readComposerReference(large)).not.toBeNull();
    expect(validateInputParts([{ type: "file", path: "../escape.txt" }])).toBe("invalid");
    expect(validateInputParts([{ type: "file", path: "/absolute.txt" }])).toBe("invalid");
    expect(inputPartsHaveFiles([leaf("root", mixed)])).toBe(true);
    expect(inputPartsSkills([leaf("root", mixed)])).toEqual(["office"]);
    expect(inputPartsSkills([{ type: "text", text: "/skill:office" }])).toEqual([]);
  });
});
