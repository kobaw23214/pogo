import { normalizeComposerParts, skillComposerPart, type ComposerPart } from "./composerParts";

export interface ComposerSlashQuery { text: string; from: number; to: number }

/** Match the slash token at a collapsed caret, including after a reference atom.
 * Positions are UTF-16 offsets, like textarea selections and ProseMirror text. */
export function composerSlashQuery(text: string, from: number, to = from): ComposerSlashQuery | null {
  if (!Number.isInteger(from) || from < 0 || from > text.length || from !== to) return null;
  const match = /(?:^|[\s\ufffc])(\/[^\s\ufffc]*)$/.exec(text.slice(0, from));
  if (!match) return null;
  const suffix = /^[^\s\ufffc]*/.exec(text.slice(from))![0];
  return { text: match[1], from: from - match[1].length, to: from + suffix.length };
}

/** Replace only the selected query with an explicit Skill card. Existing
 * references keep their identity and order, even in a textarea's prose view. */
export function completeComposerTextSkill(parts: readonly ComposerPart[], query: ComposerSlashQuery, name: string): ComposerPart[] {
  const textLength = parts.reduce((length, part) => length + (part.type === "text" ? part.text.length : 0), 0);
  if (query.from < 0 || query.to < query.from || query.to > textLength) return normalizeComposerParts(parts);
  const result: ComposerPart[] = [];
  let offset = 0, inserted = false;
  for (const part of parts) {
    if (part.type !== "text") { result.push(part); continue; }
    const end = offset + part.text.length;
    if (offset < query.from) result.push({ type: "text", text: part.text.slice(0, Math.min(part.text.length, query.from - offset)) });
    if (!inserted && query.from < end) {
      result.push(skillComposerPart(name));
      inserted = true;
    }
    if (end > query.to) result.push({ type: "text", text: part.text.slice(Math.max(0, query.to - offset)) });
    offset = end;
  }
  if (!inserted) result.push(skillComposerPart(name));
  return normalizeComposerParts(result);
}
