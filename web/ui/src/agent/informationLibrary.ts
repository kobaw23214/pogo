import { getLocale } from "@/i18n";
import type { InformationEntry } from "@/api/information";
export function informationPath(entries: readonly InformationEntry[], id: string): InformationEntry[] {
  const byID = new Map(entries.map(entry => [entry.id, entry])); const path: InformationEntry[] = []; const seen = new Set<string>();
  while (id && !seen.has(id)) { seen.add(id); const entry = byID.get(id); if (!entry) break; path.unshift(entry); id = entry.parent_id; }
  return path;
}
export function informationDescendants(entries: readonly InformationEntry[], id: string): Set<string> {
  const result = new Set<string>([id]); let changed = true;
  while (changed) { changed = false; for (const entry of entries) if (result.has(entry.parent_id) && !result.has(entry.id)) { result.add(entry.id); changed = true; } }
  return result;
}
export function informationVisible(entries: readonly InformationEntry[], folder: string, query: string, flat: boolean): InformationEntry[] {
  const needle = query.trim().toLocaleLowerCase();
  return entries.filter(entry => (!flat || entry.kind !== "folder") && (needle ? entry.name.toLocaleLowerCase().includes(needle) : flat || entry.parent_id === folder))
    .sort((a, b) => (a.kind === "folder" ? 0 : 1) - (b.kind === "folder" ? 0 : 1) || a.name.localeCompare(b.name, getLocale(), { numeric: true }));
}
export interface InformationViewState { folder: string; selected: string; mode: "list" | "grid"; flat: boolean; expanded: string[] }
export function restoreInformationView(raw: string | null, entries: readonly InformationEntry[]): InformationViewState {
  let source: Partial<InformationViewState> = {}; try { source = raw ? JSON.parse(raw) : {}; } catch { /* A broken preference never blocks the library. */ }
  const folders = new Set(entries.filter(entry => entry.kind === "folder").map(entry => entry.id)), ids = new Set(entries.map(entry => entry.id));
  return { folder: folders.has(source.folder || "") ? source.folder! : "", selected: ids.has(source.selected || "") ? source.selected! : "", mode: source.mode === "grid" ? "grid" : "list", flat: source.flat === true, expanded: Array.isArray(source.expanded) ? source.expanded.filter(id => typeof id === "string" && folders.has(id)) : [] };
}
