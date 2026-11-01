import type { EditorState, Transaction } from "prosemirror-state";
import { composerSlashQuery, type ComposerSlashQuery } from "./composerSlash";
import { skillComposerPart } from "./composerParts";
import { inlineComposerSchema } from "./inlineComposerModel";

export function inlineComposerSlashQuery(state: EditorState): ComposerSlashQuery | null {
  if (!state.selection.empty) return null;
  const { $from, from } = state.selection;
  const text = $from.parent.textBetween(0, $from.parent.content.size, "", "\ufffc");
  const query = composerSlashQuery(text, $from.parentOffset);
  const parentStart = from - $from.parentOffset;
  return query ? { ...query, from: parentStart + query.from, to: parentStart + query.to } : null;
}

/** A reference is a real token boundary; no extra user-written space is needed. */
export function completeInlineComposerSlash(state: EditorState, value: string, skillName?: string): Transaction | null {
  const query = inlineComposerSlashQuery(state);
  if (!query) return null;
  return skillName
    ? state.tr.replaceWith(query.from, query.to, inlineComposerSchema.nodes.reference.create({ part: skillComposerPart(skillName) }))
    : state.tr.insertText(value, query.from, query.to);
}
