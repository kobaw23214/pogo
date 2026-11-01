import { describe, expect, it } from "vitest";
import { composerDisplayText, composerOrderedText, composerTypedText, elementComposerPart, fileComposerPart, skillComposerPart, normalizeComposerParts, readComposerReference, replaceComposerTypedText, updateComposerFile, type ComposerPart } from "./composerParts";
import { composerDocument, composerDocumentParts, composerTextSelection, freshComposerSlice, inlineComposerSchema } from "./inlineComposerModel";
import { Slice } from "prosemirror-model";
import { EditorState, TextSelection } from "prosemirror-state";
import { undo, history } from "prosemirror-history";

const pick = { key: "tab1:5:button2", block: '<page-element source="系统" url="https://example.com">\nelement: button "导出"\n</page-element>', label: "导出", pageTitle: "集成系统" };
const element = (id = "e1") => ({ ...elementComposerPart(pick), id });
const file = (id = "f1") => ({ ...fileComposerPart({ key: "upload-1", label: "处理要求.docx", status: "uploading" }), id });
const text = (value: string): ComposerPart => ({ type: "text", text: value });

const mixed = (): ComposerPart[] => [text("1.点击"), element(), text("，再按"), file(), text("处理。\n2.再次点击"), element("e2")];

describe("ordered composer parts", () => {
  it("keeps skill atoms anchored with files and page elements across text edits and undo", () => {
    const parts = [text("先"), skillComposerPart("office"), text("处理"), file(), text("然后"), skillComposerPart("browser-use"), element()];
    const changed = replaceComposerTypedText(parts, "请先处理然后");
    expect(changed.map(part => part.type)).toEqual(["text", "skill", "text", "file", "text", "skill", "element"]);
    expect(composerDisplayText(changed)).toBe("请先〔office〕处理〔处理要求.docx〕然后〔browser-use〕〔导出〕");
    expect(composerDocumentParts(composerDocument(changed))).toEqual(changed);
    expect(composerOrderedText(parts, () => "[file]")).toContain("/skill:office 处理[file]然后/skill:browser-use ");
    expect(readComposerReference({type:"skill",name:"../escape",label:"x"})).toBeNull();
    expect(readComposerReference({...skillComposerPart("office"), text:"untrusted skill body"})).toMatchObject({type:"skill",name:"office",label:"office"});
  });
  it("retains relative order, whitespace, and two occurrences of one DOM element", () => {
    const parts = normalizeComposerParts(mixed());
    expect(composerTypedText(parts)).toBe("1.点击，再按处理。\n2.再次点击");
    expect(composerDisplayText(parts)).toBe("1.点击〔导出〕，再按〔处理要求.docx〕处理。\n2.再次点击〔导出〕");
    expect(composerOrderedText(parts, () => "<file />")).toBe(`1.点击${pick.block}，再按<file />处理。\n2.再次点击${pick.block}`);
    expect(parts.filter((part) => part.type === "element").map((part) => part.id)).toEqual(["e1", "e2"]);
  });
  it("does not mutate source while merging adjacent text or assigning unique ids", () => {
    const source = [text(" "), text("\n"), element(), element()];
    const result = normalizeComposerParts(source);
    expect(source[0]).toEqual(text(" "));
    expect(result[0]).toEqual(text(" \n"));
    expect(result[1]).not.toEqual(result[2]);
  });
  it("resolves every occurrence of a pending file in its original position", () => {
    const source = [text("按"), file(), text("更新，再核对"), file("f2")];
    const result = updateComposerFile(source, "upload-1", { status: "ready", path: "uploads/处理要求.docx" });
    expect(result.map((part) => part.type)).toEqual(["text", "file", "text", "file"]);
    expect(result.filter((part) => part.type === "file").every((part) => part.path === "uploads/处理要求.docx")).toBe(true);
    expect(source[1]).toMatchObject({ status: "uploading" });
  });
  it("keeps references anchored when the textarea inserts text in the prefix", () => {
    const parts = [text("点击"), element(), text("，完成")];
    expect(composerDisplayText(replaceComposerTypedText(parts, "请先点击，完成"))).toBe("请先点击〔导出〕，完成");
  });
  it("keeps suffix anchors and clamps references inside a changed span", () => {
    const parts = [text("甲"), element(), text("乙"), file(), text("丙"), element("e2"), text("丁")];
    const next = replaceComposerTypedText(parts, "甲新版丁");
    expect(composerDisplayText(next)).toBe("甲〔导出〕新版〔处理要求.docx〕〔导出〕丁");
    expect(composerTypedText(next)).toBe("甲新版丁");
  });
  it("retains all references when all prose is deleted", () => {
    const result = replaceComposerTypedText(mixed(), "");
    expect(result.map((part) => part.type)).toEqual(["element", "file", "element"]);
    expect(composerTypedText(result)).toBe("");
  });
  it("keeps a final reference before text appended from the top layout", () => {
    const result = replaceComposerTypedText([text("点击"), element()], "点击然后填写");
    expect(composerDisplayText(result)).toBe("点击〔导出〕然后填写");
  });
  it("rejects invalid clipboard atoms and normalizes trusted shapes only", () => {
    expect(readComposerReference({ type: "file", key: 123, label: "evil" })).toBeNull();
    expect(readComposerReference({ type: "element", text: {}, label: "evil" })).toBeNull();
    expect(readComposerReference({ type: "script", text: "alert(1)", label: "evil" })).toBeNull();
    const restored = readComposerReference({ ...element(), pick: { ...pick, icon: "javascript:alert(1)" } });
    expect(restored).toMatchObject({ type: "element", text: pick.block, pick });
    expect(restored && restored.type === "element" ? restored.pick?.icon : undefined).toBeUndefined();
  });
});

describe("ProseMirror inline composer model", () => {
  it.each([[], [text("")], [text("中文\n\n\n末行\n")], mixed(), [file(), element()], [text("\n"), file(), text("\n\n")]].map((parts) => ({ parts })))("round-trips exact text and reference order: %j", ({ parts }) => {
    const doc = composerDocument(parts);
    doc.check();
    expect(composerDocumentParts(doc)).toEqual(normalizeComposerParts(parts));
  });
  it("represents references as indivisible inline atoms", () => {
    const node = inlineComposerSchema.nodes.reference.create({ part: element() });
    expect(node.isAtom).toBe(true);
    expect(node.isInline).toBe(true);
    expect(node.nodeSize).toBe(1);
    expect(node.textBetween(0, 0, "", (leaf) => inlineComposerSchema.nodes.reference.spec.leafText!(leaf))).toBe("");
  });
  it("preserves plain text and metadata but gives pasted references distinct occurrence ids", () => {
    const doc = composerDocument(mixed());
    const pasted = freshComposerSlice(new Slice(doc.content, 0, 0));
    const after = composerDocumentParts(inlineComposerSchema.nodes.doc.create(null, pasted.content));
    expect(composerDisplayText(after)).toBe(composerDisplayText(mixed()));
    const oldIDs = mixed().filter((part) => part.type !== "text").map((part) => part.id);
    for (const part of after) if (part.type !== "text") expect(oldIDs).not.toContain(part.id);
    expect(after.filter((part) => part.type === "element").map((part) => part.text)).toEqual([pick.block, pick.block]);
  });
  it("inserts at a retained selection between instructions, then undoes atomically", () => {
    let state = EditorState.create({ doc: composerDocument([text("点击，再完成")]), plugins: [history()] });
    state = state.apply(state.tr.setSelection(TextSelection.create(state.doc, 3)));
    // Retaining EditorState selection across blur is sufficient; no DOM focus
    // reconstruction or text offset calculation is needed after a browser pick.
    state = state.apply(state.tr.replaceSelectionWith(inlineComposerSchema.nodes.reference.create({ part: element() })));
    expect(composerDisplayText(composerDocumentParts(state.doc))).toBe("点击〔导出〕，再完成");
    expect(undo(state, (transaction) => { state = state.apply(transaction); })).toBe(true);
    expect(composerDisplayText(composerDocumentParts(state.doc))).toBe("点击，再完成");
  });
  it("deletes one occurrence without deleting another occurrence of the same DOM element", () => {
    let state = EditorState.create({ doc: composerDocument([element(), text("然后"), element("e2")]) });
    state = state.apply(state.tr.delete(1, 2));
    const parts = composerDocumentParts(state.doc);
    expect(composerDisplayText(parts)).toBe("然后〔导出〕");
    expect(parts[1]).toMatchObject({ id: "e2" });
  });
  it("inserts a multiline snippet without consuming neighboring references", () => {
    let state = EditorState.create({ doc: composerDocument([text("前"), element(), text("后")]) });
    state = state.apply(state.tr.setSelection(TextSelection.create(state.doc, 3)));
    const snippet = composerDocument([text("第一步\n第二步")]);
    state = state.apply(state.tr.replaceSelection(snippet.slice(1, snippet.content.size - 1)));
    state.doc.check();
    expect(composerDisplayText(composerDocumentParts(state.doc))).toBe("前〔导出〕第一步\n第二步后");
  });

  it("maps textarea offsets across leading references and repeated boundary atoms", () => {
    const doc = composerDocument([element("leading"), text("点击"), element(), file(), text("完成")]);
    expect(composerTextSelection(doc, 0)).toEqual({ from: 2, to: 2 });
    expect(composerTextSelection(doc, 1)).toEqual({ from: 3, to: 3 });
    expect(composerTextSelection(doc, 2)).toEqual({ from: 6, to: 6 });
    expect(composerTextSelection(doc, 4)).toEqual({ from: 8, to: 8 });
    expect(composerTextSelection(doc, 1, 3)).toEqual({ from: 3, to: 7 });
  });
  it("counts paragraph newlines but skips reference width when moving a text selection", () => {
    const doc = composerDocument([text("甲"), element(), text("\n\n乙"), file()]);
    expect(composerTextSelection(doc, 1)).toEqual({ from: 3, to: 3 });
    expect(composerTextSelection(doc, 2)).toEqual({ from: 5, to: 5 });
    expect(composerTextSelection(doc, 3)).toEqual({ from: 7, to: 7 });
    expect(composerTextSelection(doc, 4)).toEqual({ from: 9, to: 9 });
  });
  it("clamps plain-text selections and keeps UTF-16 textarea offsets", () => {
    const doc = composerDocument([text("😊好"), element()]);
    expect(composerTextSelection(doc, -10, 999)).toEqual({ from: 1, to: 5 });
    expect(composerTextSelection(doc, 2)).toEqual({ from: 3, to: 3 });
    expect(composerTextSelection(composerDocument([]), 100)).toEqual({ from: 1, to: 1 });
  });

});
