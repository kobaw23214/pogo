import type { MessageInputPart, OfficeReferenceSnapshot } from "@/api/types";
import { cloneInputParts, validateInputParts, officeSnapshotText, inputPartsText } from "./shortcutStructure";
import { validShortcutLanguage } from "@/components/shortcutPresentation";
import type { ElementPick } from "./elementPicker";
import { browserFavicon } from "./browserFavicon";
import { referenceFavicon } from "./referenceFavicon";

export interface ComposerTextPart { type: "text"; text: string }
export interface ComposerElementPart {
  type: "element";
  /** Identity of this occurrence, independent of the selected DOM element. */
  id: string;
  /** The complete labelled page-element observation. */
  text: string;
  label: string;
  title?: string;
  /** Frozen, bounded icon bytes for this occurrence; never an external URL. */
  favicon?: string;
  pick?: ElementPick;
}
export interface ComposerFilePart {
  type: "file";
  id: string;
  /** Stable upload identity. A pending upload has a key but no path yet. */
  key: string;
  path?: string;
  label: string;
  /** Preserve the sender’s explicit image-as-file choice during queue edits. */
  asFile?: boolean;
  mime?: string;
  size?: number;
  status?: "uploading" | "ready" | "error";
  error?: string;
}
export interface ComposerSkillPart { type: "skill"; id: string; name: string; label: string }
export interface ComposerShortcutPart { type: "shortcut"; id: string; shortcutId: string; title: string; label: string; text: string; icon?: string; language?: string; parts?: MessageInputPart[] }
export interface ComposerOfficePart { type: "office"; id: string; label: string; office: OfficeReferenceSnapshot }
export type ComposerReferencePart = ComposerElementPart | ComposerFilePart | ComposerSkillPart | ComposerShortcutPart | ComposerOfficePart;
export type ComposerPart = ComposerTextPart | ComposerReferencePart;
export interface ComposerSelection { from: number; to: number }

let fallbackID = 0;
export function composerOccurrenceID(): string {
  return globalThis.crypto?.randomUUID?.() ?? `reference-${Date.now().toString(36)}-${++fallbackID}`;
}

/** Merge only adjacent text. References and intentional whitespace keep order. */
export function normalizeComposerParts(parts: readonly ComposerPart[]): ComposerPart[] {
  const result: ComposerPart[] = [];
  const ids = new Set<string>();
  for (const part of parts) {
    if (part.type === "text") {
      if (!part.text) continue;
      const previous = result[result.length - 1];
      if (previous?.type === "text") previous.text += part.text;
      else result.push({ type: "text", text: part.text });
    } else {
      const id = part.id && !ids.has(part.id) ? part.id : composerOccurrenceID();
      ids.add(id);
      result.push({ ...part, id });
    }
  }
  return result;
}

export function elementComposerPart(pick: ElementPick): ComposerElementPart {
  const favicon = referenceFavicon(pick.icon);
  const displayIcon = browserFavicon(pick.icon);
  const { icon: _icon, ...metadata } = pick;
  return { type: "element", id: composerOccurrenceID(), text: pick.block, label: pick.label, title: pick.pageTitle,
    ...(favicon ? { favicon } : {}), pick: { ...metadata, ...(displayIcon ? { icon: displayIcon } : {}) } };
}

export function fileComposerPart(file: Omit<ComposerFilePart, "type" | "id">): ComposerFilePart {
  return { ...file, type: "file", id: composerOccurrenceID() };
}

export function skillComposerPart(name: string): ComposerSkillPart {
  return { type: "skill", id: composerOccurrenceID(), name, label: name };
}

export function shortcutComposerPart(value: { id: string; title: string; content: string; icon?: string; language?: string; parts?: MessageInputPart[] }): ComposerShortcutPart {
  return { type: "shortcut", id: composerOccurrenceID(), shortcutId: value.id, title: value.title, label: value.title, text: value.content, ...(value.parts?.length ? { parts: cloneInputParts(value.parts) } : {}), ...(value.icon ? { icon: value.icon } : {}), ...(value.language ? { language: value.language } : {}) };
}

export function officeComposerPart(snapshot: OfficeReferenceSnapshot): ComposerOfficePart {
  return { type: "office", id: composerOccurrenceID(), label: [snapshot.title, snapshot.sheet, snapshot.range].filter(Boolean).join(" · "), office: JSON.parse(JSON.stringify(snapshot)) };
}

/** User-written prose only; never includes model-facing observation markup. */
export function composerTypedText(parts: readonly ComposerPart[]): string {
  return parts.filter((part): part is ComposerTextPart => part.type === "text").map((part) => part.text).join("");
}

/** Plain clipboard/accessibility text in the same order as the visible draft. */
export function composerDisplayText(parts: readonly ComposerPart[]): string {
  return parts.map((part) => part.type === "text" ? part.text : `〔${part.label}〕`).join("");
}

/** Model text for callers that use an explicit textual marker for files. */
export function composerOrderedText(parts: readonly ComposerPart[], fileText: (part: ComposerFilePart) => string = (part) => part.path ?? part.label): string {
  return parts.map((part) => part.type === "text" || part.type === "element" ? part.text : part.type === "shortcut" ? `[资料：${part.title}]\n${part.parts?.length ? inputPartsText(part.parts) : part.text}` : part.type === "office" ? officeSnapshotText(part.office) : part.type === "skill" ? `/skill:${part.name} ` : fileText(part)).join("");
}

/** Resolve one upload in place, including repeated occurrences of that file. */
export function updateComposerFile(parts: readonly ComposerPart[], key: string, update: Partial<Omit<ComposerFilePart, "type" | "id" | "key">>): ComposerPart[] {
  return parts.map((part) => part.type === "file" && part.key === key ? { ...part, ...update } : { ...part });
}

/** Clipboard HTML is untrusted. Accept only the exact atom shape we render. */
export function readComposerReference(value: unknown): ComposerReferencePart | null {
  if (!value || typeof value !== "object") return null;
  const part = value as Record<string, unknown>;
  if (typeof part.label !== "string" || part.label.length > 4096) return null;
  const id = typeof part.id === "string" && part.id.length <= 200 ? part.id : composerOccurrenceID();
  if (part.type === "shortcut" && typeof part.shortcutId === "string" && /^[A-Za-z0-9_-]{1,160}$/.test(part.shortcutId) && typeof part.title === "string" && part.title.trim() && !/\p{Cc}/u.test(part.title) && [...part.title].length <= 120 && typeof part.text === "string" && (part.text.trim() || Array.isArray(part.parts) && part.parts.length > 0) && !/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/.test(part.text) && new TextEncoder().encode(part.text).length <= (Array.isArray(part.parts) && part.parts.length ? 256 * 1024 : 65536) && validShortcutLanguage(part.language) && (part.icon === undefined || typeof part.icon === "string" && (!part.icon || /^[A-Za-z][A-Za-z0-9_-]{0,63}$/.test(part.icon)))) {
    if (part.parts !== undefined && (!Array.isArray(part.parts) || validateInputParts([{ type: "shortcut", shortcut_id: part.shortcutId, title: part.title, text: part.text, icon: part.icon as string | undefined, language: part.language as string | undefined, parts: part.parts as MessageInputPart[] }]))) return null;
    return { type: "shortcut", id, shortcutId: part.shortcutId, title: part.title, label: part.title, text: part.text, ...(Array.isArray(part.parts) && part.parts.length ? { parts: cloneInputParts(part.parts as MessageInputPart[]) } : {}), ...(part.icon ? { icon: part.icon as string } : {}), ...(part.language ? { language: part.language as string } : {}) };
  }
  if (part.type === "office") {
    if (!part.office || validateInputParts([{ type: "office", office: part.office as OfficeReferenceSnapshot }])) return null;
    return { ...officeComposerPart(part.office as OfficeReferenceSnapshot), id };
  }
  if (part.type === "skill" && typeof part.name === "string" && /^[^\s"'`<>/\\]{1,128}$/.test(part.name)) return { type: "skill", id, name: part.name, label: part.name };
  if (part.type === "element" && typeof part.text === "string" && part.text.length <= 1_000_000) {
    const result: ComposerElementPart = { type: "element", id, text: part.text, label: part.label };
    const favicon = referenceFavicon(part.favicon);
    if (favicon) result.favicon = favicon;
    if (typeof part.title === "string") result.title = part.title.slice(0, 4096);
    // Do not reconstruct executable selectors from arbitrary HTML. The raw
    // labelled observation remains data and the parent can recover display facts.
    if (part.pick && typeof part.pick === "object") {
      const pick = part.pick as Record<string, unknown>;
      if (typeof pick.key === "string" && typeof pick.block === "string" && typeof pick.pageTitle === "string" && typeof pick.label === "string") {
        result.pick = { key: pick.key, block: result.text, pageTitle: pick.pageTitle, label: pick.label };
        if (typeof pick.pageURL === "string") result.pick.pageURL = pick.pageURL;
        if (typeof pick.preview === "string") result.pick.preview = pick.preview;
        if (typeof pick.hasData === "boolean") result.pick.hasData = pick.hasData;
        const pickedIcon = referenceFavicon(pick.icon);
        if (favicon || pickedIcon) {
          result.favicon = favicon || pickedIcon;
          result.pick.icon = result.favicon;
        }
      }
    }
    return result;
  }
  if (part.type === "file" && typeof part.key === "string" && part.key.length <= 4096) {
    const result: ComposerFilePart = { type: "file", id, key: part.key, label: part.label };
    if (typeof part.path === "string" && part.path.length <= 16_384) result.path = part.path;
    if (typeof part.asFile === "boolean") result.asFile = part.asFile;
    if (typeof part.mime === "string") result.mime = part.mime.slice(0, 200);
    if (typeof part.size === "number" && Number.isFinite(part.size) && part.size >= 0) result.size = part.size;
    if (part.status === "uploading" || part.status === "ready" || part.status === "error") result.status = part.status;
    if (typeof part.error === "string") result.error = part.error.slice(0, 4096);
    return result;
  }
  return null;
}

/**
 * A textarea edits only prose. Keep each reference anchored to the unchanged
 * prefix/suffix; references in the replaced span move to the replacement end.
 * Switching layouts therefore never reconstructs (or appends) references.
 */
export function replaceComposerTypedText(parts: readonly ComposerPart[], text: string): ComposerPart[] {
  const previous = composerTypedText(parts);
  if (previous === text) return normalizeComposerParts(parts);
  let prefix = 0;
  while (prefix < previous.length && prefix < text.length && previous[prefix] === text[prefix]) prefix++;
  let suffix = 0;
  while (suffix < previous.length - prefix && suffix < text.length - prefix && previous[previous.length - 1 - suffix] === text[text.length - 1 - suffix]) suffix++;
  const oldEnd = previous.length - suffix;
  const newEnd = text.length - suffix;
  const anchors: { offset: number; part: ComposerReferencePart }[] = [];
  let offset = 0;
  for (const part of parts) {
    if (part.type === "text") offset += part.text.length;
    else anchors.push({ offset: offset <= prefix ? offset : offset >= oldEnd ? offset + text.length - previous.length : newEnd, part });
  }
  const result: ComposerPart[] = [];
  offset = 0;
  for (const anchor of anchors) {
    if (anchor.offset > offset) result.push({ type: "text", text: text.slice(offset, anchor.offset) });
    result.push({ ...anchor.part });
    offset = anchor.offset;
  }
  if (offset < text.length) result.push({ type: "text", text: text.slice(offset) });
  return normalizeComposerParts(result);
}
