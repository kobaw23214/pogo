import { describe, expect, it } from "vitest";
import { buildTimeline } from "./timeline";
import { emptyLive } from "@/api/types";
import { blocksComposerParts, composerInputParts, inputPartsBlocks, orderedDisplayParts } from "./orderedInput";
import { composerDisplayText } from "./composerParts";
import type { Block, MessageInputPart } from "@/api/types";

const element = '<page-element source="系统" url="https://example.test/">\nobservation_ref: p1\nelement: button "导出"\n</page-element>';
const input: MessageInputPart[] = [{ type: "text", text: "1.点击" }, { type: "element", text: element },
  { type: "text", text: "，按" }, { type: "file", path: "uploads/要求.docx" },
  { type: "text", text: "处理后，再点击" }, { type: "element", text: element }, { type: "text", text: "。\n2.完成" }];

describe("ordered input round trip", () => {
  it("serializes only explicit skill names and restores expanded history as movable tags in place", () => {
    const wire: MessageInputPart[] = [input[0], {type:"skill",name:"office"}, input[1], input[2], {type:"file",path:"uploads/要求.docx",as_file:true}, {type:"skill",name:"browser-use"}, input[6]];
    const history = inputPartsBlocks(wire).map(block => block.type === "text" && block.input_skill
      ? {...block, text:`<skill name="${block.input_skill}" location="/private/skill.md">\nHOST-ONLY BODY\n</skill>`} : block);
    const restored = blocksComposerParts(history);
    expect(restored.map(part => part.type)).toEqual(["text","skill","element","text","file","skill","text"]);
    expect(composerInputParts(restored, key => [key])).toEqual(wire);
    expect(composerDisplayText(restored)).toContain("〔office〕");
    expect(composerDisplayText(restored)).not.toContain("HOST-ONLY BODY");
    expect(orderedDisplayParts(history).filter(part => part.type === "skill")).toEqual([{type:"skill",name:"office"},{type:"skill",name:"browser-use"}]);
  });
  it("retains every text/reference/file position through history and editing", () => {
    const blocks = inputPartsBlocks(input);
    const parts = blocksComposerParts(blocks);
    expect(composerDisplayText(parts)).toBe('1.点击〔button "导出"〕，按〔要求.docx〕处理后，再点击〔button "导出"〕。\n2.完成');
    const references = parts.filter(part => part.type === "element");
    expect(references[0].id).not.toBe(references[1].id);
    expect(composerInputParts(parts, key => [key])).toEqual(input.map(part => part.type === "file" ? { ...part, as_file: true } : part));
    expect(orderedDisplayParts(blocks).map(part => part.type)).toEqual(["text", "element", "text", "file", "text", "element", "text"]);
  });
  it("keeps repeated folder image references as files while plain images remain images", () => {
    const parts: MessageInputPart[] = [{ type: "file", path: "uploads/a.png" }, { type: "text", text: " then " }, { type: "file", path: "uploads/a.png", as_file: true }];
    const blocks = inputPartsBlocks(parts, new Map([["uploads/a.png", { image: true, name: "a.png" }]]));
    expect(blocks.map(block => block.type)).toEqual(["image", "text", "file"]);
  });
  it("refuses unresolved files instead of silently dropping their position", () => {
    const draft = blocksComposerParts(inputPartsBlocks(input));
    expect(() => composerInputParts(draft, () => [])).toThrow("File is not ready");
  });
  it("keeps ordinary markup and incomplete references as text", () => {
    const blocks: Block[] = [{ type: "text", text: '<b>literal</b> <page-element>incomplete' }];
    expect(orderedDisplayParts(blocks)).toEqual([{ type: "text", text: '<b>literal</b> <page-element>incomplete' }]);
  });
  it("timeline keeps exact original blocks and layout on streaming/replayed ordered messages", () => {
    const blocks = inputPartsBlocks(input);
    const timeline = buildTimeline([{ id: "u1", role: "user", input_layout: "inline", content: blocks }], {}, emptyLive());
    expect(timeline[0]).toMatchObject({ kind: "user", orderedContent: blocks, inputLayout: "inline" });
  });
});
