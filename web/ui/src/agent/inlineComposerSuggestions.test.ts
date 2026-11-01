import { describe, expect, it } from "vitest";
import { EditorState, TextSelection } from "prosemirror-state";
import { composerDocument, composerDocumentParts, inlineComposerSchema } from "./inlineComposerModel";
import { completeInlineComposerSlash, inlineComposerSlashQuery } from "./inlineComposerSuggestions";
import { fileComposerPart, skillComposerPart, type ComposerPart } from "./composerParts";
import { composerInputParts } from "./orderedInput";

function state(parts: ComposerPart[], caret?: number) {
  let result = EditorState.create({ schema: inlineComposerSchema, doc: composerDocument(parts) });
  result = result.apply(result.tr.setSelection(TextSelection.create(result.doc, caret ?? result.doc.content.size - 1)));
  return result;
}

describe("ordered composer slash transactions", () => {
  it("completes a second skill directly after a card without adding a space", () => {
    const first = skillComposerPart("office");
    let editor = state([first, { type: "text", text: "/skill:yx" }]);
    expect(inlineComposerSlashQuery(editor)?.text).toBe("/skill:yx");
    editor = editor.apply(completeInlineComposerSlash(editor, "/skill:yx-business ", "yx-business")!);
    const parts = composerDocumentParts(editor.doc);
    expect(parts.map(part => part.type === "skill" && part.name)).toEqual(["office", "yx-business"]);
    expect(parts[0]).toEqual(first);
    editor = editor.apply(editor.tr.insertText("/skill:bro"));
    expect(inlineComposerSlashQuery(editor)?.text).toBe("/skill:bro");
    editor = editor.apply(completeInlineComposerSlash(editor, "/skill:browser-use ", "browser-use")!);
    expect(composerDocumentParts(editor.doc).map(part => part.type === "skill" && part.name)).toEqual(["office", "yx-business", "browser-use"]);
  });
  it("replaces only the query beside files and webpages, preserving model and history order", () => {
    const file = fileComposerPart({ key: "file", path: "uploads/table.csv", label: "table.csv" });
    const page: ComposerPart = { type: "element", id: "page", label: "查询", text: "PAGE OBSERVATION" };
    const before: ComposerPart[] = [skillComposerPart("office"), { type: "text", text: "先读 " }, file, { type: "text", text: "\n再 " }, page, { type: "text", text: "/skill:yx 后续保持" }];
    let editor = state(before);
    let queryStart = 0;
    editor.doc.descendants((node, pos) => { if (node.isText && node.text!.startsWith("/skill:yx")) queryStart = pos; });
    editor = editor.apply(editor.tr.setSelection(TextSelection.create(editor.doc, queryStart + "/skill:yx".length)));
    editor = editor.apply(completeInlineComposerSlash(editor, "/skill:yx-business ", "yx-business")!);
    const parts = composerDocumentParts(editor.doc);
    expect(parts.map(part => part.type)).toEqual(["skill", "text", "file", "text", "element", "skill", "text"]);
    expect(parts[2]).toEqual(file); expect(parts[4]).toEqual(page);
    expect(parts[6]).toEqual({ type: "text", text: " 后续保持" });
    const wire = composerInputParts(parts, () => ["uploads/table.csv"]);
    expect(wire.map(part => part.type)).toEqual(["skill", "text", "file", "text", "element", "skill", "text"]);
    expect(composerDocumentParts(composerDocument(parts))).toEqual(parts);
  });
  it("handles the skill entry first, then the actual selection at the same cursor", () => {
    let editor = state([skillComposerPart("office"), { type: "text", text: "/" }]);
    editor = editor.apply(completeInlineComposerSlash(editor, "/skill:")!);
    expect(inlineComposerSlashQuery(editor)?.text).toBe("/skill:");
    editor = editor.apply(completeInlineComposerSlash(editor, "/skill:yx-business ", "yx-business")!);
    expect(composerDocumentParts(editor.doc).filter(part => part.type === "skill").map(part => part.name)).toEqual(["office", "yx-business"]);
  });
  it("does not replace an active range selection", () => {
    const editor = state([{ type: "text", text: "/skill:yx" }]);
    const selected = editor.apply(editor.tr.setSelection(TextSelection.create(editor.doc, 1, 4)));
    expect(inlineComposerSlashQuery(selected)).toBeNull();
    expect(completeInlineComposerSlash(selected, "/skill:office ", "office")).toBeNull();
  });
});
