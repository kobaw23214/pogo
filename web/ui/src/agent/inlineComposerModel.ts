import { Fragment, Schema, Slice, type Node as ProseMirrorNode } from "prosemirror-model";
import { composerOccurrenceID, normalizeComposerParts, readComposerReference, type ComposerPart, type ComposerReferencePart } from "./composerParts";

export const inlineComposerSchema = new Schema({
  nodes: {
    doc: { content: "paragraph+" },
    paragraph: {
      content: "inline*", group: "block",
      parseDOM: [{ tag: "p", preserveWhitespace: "full" }, { tag: "div", preserveWhitespace: "full" }],
      toDOM: () => ["p", 0],
    },
    text: { group: "inline" },
    reference: {
      inline: true, group: "inline", atom: true, selectable: true, draggable: true,
      attrs: { part: {} },
      leafText: (node) => `〔${(node.attrs.part as ComposerReferencePart).label}〕`,
      parseDOM: [{
        tag: "span[data-pogo-composer-part]",
        getAttrs: (element) => {
          const value = (element as HTMLElement).getAttribute("data-pogo-composer-part");
          if (!value || value.length > 1_100_000) return false;
          try {
            const part = readComposerReference(JSON.parse(value));
            return part ? { part } : false;
          } catch { return false; }
        },
      }],
      toDOM: (node) => {
        const part = node.attrs.part as ComposerReferencePart;
        return ["span", { "data-pogo-composer-part": JSON.stringify(part), "contenteditable": "false", "class": `inline-reference ${part.type}` }, part.label];
      },
    },
  },
});

export function composerDocument(parts: readonly ComposerPart[]): ProseMirrorNode {
  const paragraphs: ProseMirrorNode[] = [];
  let children: ProseMirrorNode[] = [];
  for (const part of normalizeComposerParts(parts)) {
    if (part.type !== "text") {
      children.push(inlineComposerSchema.nodes.reference.create({ part }));
      continue;
    }
    const lines = part.text.split("\n");
    for (let index = 0; index < lines.length; index++) {
      if (index > 0) {
        paragraphs.push(inlineComposerSchema.nodes.paragraph.create(null, children));
        children = [];
      }
      if (lines[index]) children.push(inlineComposerSchema.text(lines[index]));
    }
  }
  paragraphs.push(inlineComposerSchema.nodes.paragraph.create(null, children));
  return inlineComposerSchema.nodes.doc.create(null, paragraphs);
}

export function composerDocumentParts(doc: ProseMirrorNode): ComposerPart[] {
  const parts: ComposerPart[] = [];
  doc.forEach((paragraph, _offset, index) => {
    if (index > 0) parts.push({ type: "text", text: "\n" });
    paragraph.forEach((node) => {
      if (node.isText) parts.push({ type: "text", text: node.text ?? "" });
      else if (node.type.name === "reference") parts.push({ ...(node.attrs.part as ComposerReferencePart) });
    });
  });
  return normalizeComposerParts(parts);
}

/** A pasted/copied occurrence is new even when it points at the same element. */
export function freshComposerSlice(slice: Slice): Slice {
  const visit = (fragment: Fragment): Fragment => {
    const nodes: ProseMirrorNode[] = [];
    fragment.forEach((node) => nodes.push(node.type.name === "reference"
      ? node.type.create({ part: { ...(node.attrs.part as ComposerReferencePart), id: composerOccurrenceID() } })
      : node.copy(visit(node.content))));
    return Fragment.fromArray(nodes);
  };
  return new Slice(visit(slice.content), slice.openStart, slice.openEnd);
}

/**
 * Map textarea offsets to ProseMirror positions. At a prose boundary, consume
 * all references anchored there so continuing the sentence inserts after them.
 * A paragraph boundary counts as one newline in the plain-text view.
 */
export function composerTextSelection(doc: ProseMirrorNode, from: number, to = from): { from: number; to: number } {
  const positions: number[] = [];
  let textOffset = 0;
  doc.forEach((paragraph, paragraphOffset, index) => {
    if (index > 0) textOffset++;
    positions[textOffset] = paragraphOffset + 1;
    paragraph.forEach((node, childOffset) => {
      const position = paragraphOffset + childOffset + 1;
      if (node.isText) {
        const length = node.text?.length ?? 0;
        for (let offset = 1; offset <= length; offset++) positions[textOffset + offset] = position + offset;
        textOffset += length;
      } else if (node.type.name === "reference") positions[textOffset] = position + node.nodeSize;
    });
  });
  const at = (offset: number) => positions[Math.max(0, Math.min(Number.isFinite(offset) ? Math.floor(offset) : 0, textOffset))] ?? 1;
  return { from: at(from), to: at(to) };
}
