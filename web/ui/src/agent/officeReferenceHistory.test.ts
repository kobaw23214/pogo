import { describe, expect, it } from "vitest";
import { emptyLive, type OfficeReferenceSnapshot } from "@/api/types";
import { buildTimeline } from "./timeline";
import { blocksComposerParts, composerInputParts, inputPartsBlocks } from "./orderedInput";
import { composerDisplayText, officeComposerPart } from "./composerParts";
import { cloneInputParts, inputPartsText, validateInputParts } from "./shortcutStructure";
import { inboxDraftParts, inboxDraftSubmission } from "./conversationInbox";
import { prepareStableStarterDraft, starterCardComposerParts } from "@/components/starterCardDraft";

describe("Office reference history", () => {
  it("freezes saved library selection identity through clipboard, shortcut, queue and message history", () => {
    const office: OfficeReferenceSnapshot = { path: `information-assets/${"a".repeat(64)}/报告.xlsx`, title: "报告.xlsx", editor: "cell", sheet: "明细", range: "A1:B2", text: "地区\t数量\n中国\t10", canvas_version: `disk:${"a".repeat(64)}`, dirty: false, information: { entry_id: `inf_${"1".repeat(32)}`, version: 3, snapshot_sha256: "b".repeat(64) } };
    const original = [{ type: "text" as const, text: "前文 " }, { type: "office" as const, office }, { type: "text" as const, text: " 后文" }];
    expect(validateInputParts(original)).toBe("");
    const frozen = cloneInputParts(original);
    const card = officeComposerPart(office);
    const starter = prepareStableStarterDraft({ parts: starterCardComposerParts({ title: "保存版本", parts: frozen }), inputLayout: "inline" });
    const queued = inboxDraftParts({ id: "queued", text: "", mode: "queued", status: "queued", revision: 1, created_at: 1, updated_at: 1, parts: frozen });
    expect(inboxDraftSubmission(queued).parts).toEqual(frozen);
    expect(starter.parts).toEqual(frozen);
    const history = blocksComposerParts(inputPartsBlocks(frozen));
    expect(composerInputParts(history, () => { throw new Error("Selection text is not an upload"); })).toEqual(frozen);
    expect(inputPartsText(frozen)).toContain("保存版本 3；只读选取；资料快照");
    office.information!.version = 4;
    expect(card.office.information?.version).toBe(3);
    expect(frozen[1]).toMatchObject({ office: { information: { version: 3 } } });
    expect(composerDisplayText(history)).not.toContain("地区\t数量");
  });

  it("rejects library selections that claim an unsaved canvas or point at a different disk asset", () => {
    const office: OfficeReferenceSnapshot = { path: `information-assets/${"a".repeat(64)}/文档.docx`, title: "文档.docx", editor: "word", text: "已选内容", canvas_version: `disk:${"a".repeat(64)}`, information: { entry_id: `inf_${"1".repeat(32)}`, version: 1, snapshot_sha256: "b".repeat(64) } };
    expect(validateInputParts([{ type: "office", office }])).toBe("");
    for (const change of [{ dirty: true }, { dirty_epoch: 1 }, { source_session: "another-live-session" }, { canvas_version: `disk:${"c".repeat(64)}` }, { information: { ...office.information!, version: 0 } }]) {
      expect(validateInputParts([{ type: "office", office: { ...office, ...change } }])).toBe("invalid");
    }
  });

  it("restores an Office card without layout metadata and keeps its selected body out of navigation text", () => {
    const office: OfficeReferenceSnapshot = { path: "uploads/report.xlsx", title: "报告.xlsx", editor: "cell", sheet: "明细", range: "B2:C3", text: "PRIVATE_SELECTED_CONTENT", canvas_version: "a".repeat(64), dirty: true, dirty_epoch: 4, source_session: "source-session" };
    const blocks = inputPartsBlocks([{ type: "text", text: "分析 " }, { type: "office", office }, { type: "text", text: " 再总结" }]);
    const user = buildTimeline([{ id: "user-1", role: "user", content: blocks }], {}, emptyLive()).find(item => item.kind === "user");
    expect(user?.kind).toBe("user");
    if (user?.kind !== "user") throw new Error("missing user");
    expect(user.inputLayout).toBe("top");
    const parts = blocksComposerParts(user.orderedContent!);
    expect(parts.map(part => part.type)).toEqual(["text", "office", "text"]);
    expect(composerDisplayText(parts)).toContain("报告.xlsx");
    expect(composerDisplayText(parts)).not.toContain("PRIVATE_SELECTED_CONTENT");
    expect(composerInputParts(parts, () => [])).toEqual([{ type: "text", text: "分析 " }, { type: "office", office }, { type: "text", text: " 再总结" }]);
  });
});
