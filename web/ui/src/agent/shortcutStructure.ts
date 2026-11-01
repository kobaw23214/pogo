import type { MessageInputPart, OfficeReferenceSnapshot } from "@/api/types";
import { validShortcutLanguage } from "@/components/shortcutPresentation";

export const shortcutStructureLimits = { depth: 5, nodes: 512, bytes: 256 * 1024 } as const;
export function cloneInputParts(parts: readonly MessageInputPart[]): MessageInputPart[] { return JSON.parse(JSON.stringify(parts)); }
export function officeSnapshotText(office: OfficeReferenceSnapshot): string {
  return `[Office选区：${[office.title, office.sheet, office.range].filter(Boolean).join(" · ")}；版本 ${office.canvas_version}${office.information ? `；来源 信息库 ${office.information.entry_id}；保存版本 ${office.information.version}；只读选取；资料快照 ${office.information.snapshot_sha256}` : ""}${office.dirty ? "；包含未保存编辑" : ""}]\n${office.text}`;
}
export function inputPartsText(parts: readonly MessageInputPart[]): string {
  return parts.map(part => part.type === "text" || part.type === "element" ? part.text : part.type === "file" ? `〔${part.path.split("/").pop()}〕` : part.type === "skill" ? `/skill:${part.name} ` : part.type === "office" ? officeSnapshotText(part.office) : `[资料：${part.title}]\n${part.parts?.length ? inputPartsText(part.parts) : part.text}`).join("");
}
export function inputPartsHaveFiles(parts: readonly MessageInputPart[]): boolean { return parts.some(part => part.type === "file" || part.type === "shortcut" && !!part.parts?.length && inputPartsHaveFiles(part.parts)); }
export function inputPartsSkills(parts: readonly MessageInputPart[]): string[] { return parts.flatMap(part => part.type === "skill" ? [part.name] : part.type === "shortcut" && part.parts ? inputPartsSkills(part.parts) : []); }
/** Clipboard and UI guard. The server independently enforces identical limits. */
export function validateInputParts(parts: readonly MessageInputPart[], ancestors: string[] = []): string {
  let nodes = ancestors.length, size = 0;
  const bytes = (text: string) => new TextEncoder().encode(text).length;
  const relativePath = (path: unknown) => typeof path === "string" && !!path && bytes(path) <= 4096 && !path.startsWith("/") && !path.includes("\\") && !path.includes("\u0000") && !path.split("/").some(part => !part || part === "." || part === "..") && (!path.startsWith("information-assets/") || /^information-assets\/[a-f0-9]{64}\/[^/\\\u0000]+$/.test(path));
  const badText = (value: unknown, max = 256 * 1024) => typeof value !== "string" || /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/.test(value) || new TextEncoder().encode(value).length > max;
  const visit = (list: readonly MessageInputPart[], chain: string[], depth: number): string => {
    if (!Array.isArray(list)) return "invalid";
    for (const part of list) {
      if (++nodes > shortcutStructureLimits.nodes) return "nodes";
      if (!part || typeof part !== "object") return "invalid";
      for (const key of ["type", "text", "path", "name", "favicon", "shortcut_id", "title", "icon", "language"] as const) { const value = (part as unknown as Record<string, unknown>)[key]; if (typeof value === "string") size += bytes(value); }
      if (part.type === "office" && part.office) { for (const value of Object.values(part.office)) if (typeof value === "string") size += bytes(value); if (part.office.information) size += bytes(part.office.information.entry_id || "") + bytes(part.office.information.snapshot_sha256 || "") + 16; if (part.office.locator) for (const value of Object.values(part.office.locator)) if (typeof value === "string") size += bytes(value); }
      if (size > shortcutStructureLimits.bytes) return "bytes";
      if (part.type === "text" || part.type === "element") { if (badText(part.text, part.type === "element" ? 32 * 1024 : 256 * 1024)) return "invalid"; }
      else if (part.type === "file") { if (!relativePath(part.path)) return "invalid"; }
      else if (part.type === "skill") { if (typeof part.name !== "string" || !/^[^\s"'`<>/\\]{1,128}$/.test(part.name)) return "invalid"; }
      else if (part.type === "office") {
        const v = part.office;
        if (!v || (v.editor !== "word" && v.editor !== "cell") || badText(v.text, 65536) || !v.text.trim() || badText(v.title, 1024) || !v.title.trim() || !relativePath(v.path) || badText(v.canvas_version, 512) || !v.canvas_version) return "invalid";
        if (v.information && (!/^inf_[a-f0-9]{32}$/.test(v.information.entry_id) || !Number.isSafeInteger(v.information.version) || v.information.version < 1 || !/^[a-f0-9]{64}$/.test(v.information.snapshot_sha256) || !/^information-assets\/[a-f0-9]{64}\/[^/\\\u0000]+$/.test(v.path) || !/^disk:[a-f0-9]{64}$/.test(v.canvas_version) || v.canvas_version !== `disk:${v.path.split("/")[1]}` || v.dirty || v.dirty_epoch || v.source_session)) return "invalid";
        if (v.dirty_epoch !== undefined && (!Number.isSafeInteger(v.dirty_epoch) || v.dirty_epoch < 0)) return "invalid";
        if (v.locator && (v.editor !== "word" || v.locator.kind !== "text" || badText(v.locator.exact, 65536) || !v.locator.exact || (v.locator.prefix !== undefined && badText(v.locator.prefix, 4096)) || (v.locator.suffix !== undefined && badText(v.locator.suffix, 4096)))) return "invalid";
      } else if (part.type === "shortcut") {
        if (typeof part.shortcut_id !== "string" || !/^[A-Za-z0-9_-]{1,160}$/.test(part.shortcut_id) || badText(part.title, 1024) || /\p{Cc}/u.test(part.title) || !part.title.trim() || [...part.title].length > 120 || badText(part.text, part.parts?.length ? 256 * 1024 : 65536) || !validShortcutLanguage(part.language) || (part.icon !== undefined && !/^(?:[A-Za-z][A-Za-z0-9_-]{0,63})?$/.test(part.icon))) return "invalid";
        if (chain.includes(part.shortcut_id)) return "cycle";
        if (depth >= shortcutStructureLimits.depth) return "depth";
        if (part.parts !== undefined) { const error = visit(part.parts, [...chain, part.shortcut_id], depth + 1); if (error) return error; }
        if (!part.parts?.length && !part.text.trim()) return "invalid";
      } else return "invalid";
    }
    return "";
  };
  let result: string;
  try { result = visit(parts, ancestors, ancestors.length);  } catch { return "invalid"; }
  return result;
}
