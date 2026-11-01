import type { MessageInputPart } from "@/api/types";
import type { ShortcutSnapshot } from "@/api/shortcuts";
import { composerDisplayText, composerTypedText, shortcutComposerPart, type ComposerPart, type ComposerShortcutPart } from "./composerParts";
import { cloneInputParts, validateInputParts } from "./shortcutStructure";

/** An occurrence belongs to this draft; shortcutId alone also matches siblings. */
export interface ShortcutOccurrenceEdit { occurrenceId: string; shortcutId: string; fingerprint: string }
export function shortcutOccurrenceEdit(part: ComposerShortcutPart): ShortcutOccurrenceEdit {
  return { occurrenceId: part.id, shortcutId: part.shortcutId, fingerprint: JSON.stringify(part) };
}
export function shortcutSnapshotFromParts(value: ShortcutSnapshot, parts: readonly ComposerPart[], wire: MessageInputPart[]): ShortcutSnapshot {
  const item: ShortcutSnapshot = { ...value, title: value.title.trim(), content: wire.some(p => p.type !== "text") ? composerDisplayText(parts) : composerTypedText(parts) };
  if (wire.some(p => p.type !== "text")) { item.parts = cloneInputParts(wire); item.input_layout = "inline"; }
  else { delete item.parts; delete item.input_layout; }
  return item;
}
export function validateShortcutSnapshot(item: ShortcutSnapshot, ancestors: string[] = []): string {
  return validateInputParts([{ type: "shortcut", shortcut_id: item.id, title: item.title || "Draft", text: item.content, icon: item.icon || "", language: item.language, ...(item.parts?.length ? { parts: item.parts } : {}) }], ancestors);
}
/** Replace only the still-current occurrence; never rewrite its source or peers. */
export function replaceShortcutOccurrence(parts: readonly ComposerPart[], origin: ShortcutOccurrenceEdit, snapshot: ShortcutSnapshot): ComposerPart[] {
  const matches = parts.filter((part): part is ComposerShortcutPart => part.type === "shortcut" && part.id === origin.occurrenceId);
  if (matches.length !== 1 || snapshot.id !== origin.shortcutId || JSON.stringify(matches[0]) !== origin.fingerprint) throw new Error("changedDraft");
  const replacement = { ...shortcutComposerPart(snapshot), id: origin.occurrenceId };
  return parts.map(part => part === matches[0] ? replacement : part);
}
