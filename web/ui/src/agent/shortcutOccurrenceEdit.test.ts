import { describe, expect, it } from "vitest";
import { shortcutComposerPart, type ComposerPart } from "./composerParts";
import { replaceShortcutOccurrence, shortcutOccurrenceEdit, validateShortcutSnapshot } from "./shortcutOccurrenceEdit";
import type { ShortcutSnapshot } from "@/api/shortcuts";

const source: ShortcutSnapshot = { id: "shared", title: "来源卡", content: "原文", icon: "book", language: "text", parts: [{ type: "text", text: "正文" }, { type: "skill", name: "office" }] };
describe("embedded shortcut occurrence edits", () => {
  it("updates one repeated occurrence and preserves siblings, IDs and the source snapshot", () => {
    const first = shortcutComposerPart(source), second = shortcutComposerPart(source), prose: ComposerPart = { type: "text", text: "父稿继续输入" };
    const parts = [first, prose, second], before = JSON.stringify(source);
    const output = replaceShortcutOccurrence(parts, shortcutOccurrenceEdit(second), { ...source, title: "仅改此处", content: "新文", icon: "", language: "sql", parts: [{ type: "text", text: "SELECT 1" }] });
    expect(output[0]).toBe(first); expect(output[1]).toBe(prose); expect(output[2]).toMatchObject({ id: second.id, shortcutId: "shared", title: "仅改此处", label: "仅改此处", language: "sql" });
    expect(output[2]).not.toHaveProperty("icon"); expect(first.parts).toEqual(source.parts); expect(JSON.stringify(source)).toBe(before); expect(parts[2]).toBe(second);
  });
  it("accepts other parent edits but rejects a moved identity, missing or changed target", () => {
    const original = shortcutComposerPart(source), origin = shortcutOccurrenceEdit(original);
    expect(replaceShortcutOccurrence([{ type: "text", text: "新前文" }, original], origin, source)[0]).toEqual({ type: "text", text: "新前文" });
    expect(() => replaceShortcutOccurrence([], origin, source)).toThrow("changedDraft");
    expect(() => replaceShortcutOccurrence([{ ...original, text: "已被另一次编辑修改" }], origin, source)).toThrow("changedDraft");
    expect(() => replaceShortcutOccurrence([original, original], origin, source)).toThrow("changedDraft");
    expect(() => replaceShortcutOccurrence([original], origin, { ...source, id: "another" })).toThrow("changedDraft");
  });
  it("retains detached edited snapshots and rejects root-inclusive depth and cycles", () => {
    const target = shortcutComposerPart(source), edit = { ...source, parts: [{ type: "text" as const, text: "独立快照" }] };
    const result = replaceShortcutOccurrence([target], shortcutOccurrenceEdit(target), edit);
    edit.parts[0].text = "后来改变"; expect(result[0]).toMatchObject({ parts: [{ text: "独立快照" }] });
    expect(validateShortcutSnapshot({ id: "fifth", title: "第五层", content: "允许" }, ["root", "two", "three", "four"])).toBe("");
    expect(validateShortcutSnapshot({ id: "sixth", title: "第六层", content: "禁止" }, ["root", "two", "three", "four", "five"])).toBe("depth");
    expect(validateShortcutSnapshot({ id: "root", title: "父卡", content: "循环" }, ["root"])).toBe("cycle");
  });
});
