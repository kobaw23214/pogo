import { describe, expect, it } from "vitest";
import { composerSlashQuery, completeComposerTextSkill } from "./composerSlash";
import { composerDraftWithToken, readComposerToken } from "./composerTokens";
import { composerTypedText, replaceComposerTypedText, skillComposerPart, type ComposerPart } from "./composerParts";
import { composerInputParts } from "./orderedInput";

const atEnd = (text: string) => composerSlashQuery(text, text.length);
describe("caret-scoped slash completion", () => {
  it("offers another skill in an existing task and immediately after any reference", () => {
    expect(atEnd("整理后 /skill:")).toEqual({ text: "/skill:", from: 4, to: 11 });
    expect(atEnd("\ufffc/skill")).toEqual({ text: "/skill", from: 1, to: 7 });
    expect(atEnd("\ufffc/skill:yx")).toEqual({ text: "/skill:yx", from: 1, to: 10 });
    expect(atEnd("第一步\n/skill:")).toEqual({ text: "/skill:", from: 4, to: 11 });
  });
  it("replaces the whole current token and preserves the remaining prompt", () => {
    const text = "先 /skill:offi旧名称 再核对";
    const query = composerSlashQuery(text, "先 /skill:offi".length)!;
    expect(query).toEqual({ text: "/skill:offi", from: 2, to: 16 });
    const result = completeComposerTextSkill([{ type: "text", text }], query, "office");
    expect(result.map(part => part.type === "text" ? part.text : part.type === "skill" ? part.name : part.type)).toEqual(["先 ", "office", " 再核对"]);
  });
  it.each(["https://example.invalid/skill:", "文档/skill:", "`/skill:", ""]) ("does not hijack paths or quoted prose %j", text => {
    expect(atEnd(text)).toBeNull();
  });
  it("does not complete selected text or invalid offsets", () => {
    expect(composerSlashQuery("/skill:", 0, 7)).toBeNull();
    expect(composerSlashQuery("/skill:", -1)).toBeNull();
    expect(composerSlashQuery("/skill:", 99)).toBeNull();
  });
  it("converts the first top token and appends a second card without losing references", () => {
    const first = readComposerToken("/skill:office 先读文件，再 /skill:yx", [{ name: "office" }])!;
    const file: ComposerPart = { type: "file", id: "file", key: "upload", label: "报表.xlsx", path: "uploads/报表.xlsx" };
    const page: ComposerPart = { type: "element", id: "page", label: "查询", text: "PAGE OBSERVATION" };
    const parts: ComposerPart[] = [{ type: "text", text: "先读文件" }, file, { type: "text", text: "，再 /skill:yx" }, page];
    const result = completeComposerTextSkill(composerDraftWithToken(parts, first), atEnd(first.body)!, "yx-business");
    expect(result.map(part => part.type)).toEqual(["skill", "text", "file", "text", "skill", "element"]);
    expect(result[0]).toMatchObject({ name: "office" });
    expect(result[4]).toMatchObject({ name: "yx-business" });
    expect(result[2]).toEqual(file); expect(result[5]).toEqual(page);
    expect(composerTypedText(result)).toBe("先读文件，再 ");
    expect(composerInputParts(result, () => ["uploads/报表.xlsx"]).map(part => part.type)).toEqual(["skill", "text", "file", "text", "skill", "element"]);
  });
  it("keeps existing reference occurrences when a top-mode query crosses their prose offsets", () => {
    const old = skillComposerPart("office");
    const parts: ComposerPart[] = [{ type: "text", text: "再 /sk" }, old, { type: "text", text: "ill:yx 然后" }];
    const result = completeComposerTextSkill(parts, composerSlashQuery("再 /skill:yx 然后", 11)!, "yx-business");
    expect(result.filter(part => part.type === "skill").map(part => part.name)).toEqual(["yx-business", "office"]);
    expect(result.find(part => part.type === "skill" && part.name === "office")).toEqual(old);
    expect(composerTypedText(result)).toBe("再  然后");
  });
  it("appends a third Skill after editing the task without moving its earlier references", () => {
    const first = skillComposerPart("office"), second = skillComposerPart("yx-business");
    const file: ComposerPart = { type: "file", id: "file", key: "file", label: "报表.xlsx", path: "uploads/报表.xlsx" };
    const page: ComposerPart = { type: "element", id: "page", label: "查询", text: "PAGE OBSERVATION" };
    const original: ComposerPart[] = [first, { type: "text", text: "请核对 " }, file, second, { type: "text", text: " 尾部保留" }, page];
    const body = composerTypedText(original) + "\n然后浏览网页 /skill:bro";
    const typed = replaceComposerTypedText(original, body);
    expect(typed.filter(part => part.type !== "text")).toEqual([first, file, second, page]);
    const result = completeComposerTextSkill(typed, atEnd(body)!, "browser-use");
    expect(result.filter(part => part.type === "skill").map(part => part.name)).toEqual(["office", "yx-business", "browser-use"]);
    expect(result.filter(part => part.type !== "text").slice(0, 4)).toEqual([first, file, second, page]);
    expect(composerTypedText(result)).toBe("请核对  尾部保留\n然后浏览网页 ");
    expect(composerInputParts(result, () => ["uploads/报表.xlsx"]).filter(part => part.type === "skill").map(part => part.name)).toEqual(["office", "yx-business", "browser-use"]);
  });
  it("respects existing textarea replacement anchors instead of moving them to append a Skill", () => {
    const first = skillComposerPart("office"), second = skillComposerPart("yx-business");
    const original: ComposerPart[] = [first, { type: "text", text: "请核对 " }, second, { type: "text", text: " 尾部保留" }];
    const body = "先读文件，再核对 /skill:bro";
    // A full textarea replacement has already moved the affected second card
    // to the replacement end. Completion uses this cursor location faithfully.
    const typed = replaceComposerTypedText(original, body);
    expect(typed).toEqual([first, { type: "text", text: body }, second]);
    const result = completeComposerTextSkill(typed, atEnd(body)!, "browser-use");
    expect(result.filter(part => part.type === "skill").map(part => part.name)).toEqual(["office", "browser-use", "yx-business"]);
    expect(result.filter(part => part.type !== "text").filter(part => part.id === first.id || part.id === second.id)).toEqual([first, second]);
  });
});
