import type { InformationEntry } from "@/api/information";
import { informationDescendants, informationPath, informationVisible } from "@/agent/informationLibrary";

export interface InformationMoveTargetRow {
  entry: InformationEntry;
  depth: number;
  expandable: boolean;
  expanded: boolean;
  blocked: boolean;
}

/** Only an existing folder with a complete, cycle-free ancestry can receive an item. */
export function informationCanMoveTo(entries: readonly InformationEntry[], movingID: string, parentID: string): boolean {
  if (!entries.some(entry => entry.id === movingID)) return false;
  if (!parentID) return true;
  const excluded = informationDescendants(entries, movingID), byID = new Map(entries.map(entry => [entry.id, entry]));
  const seen = new Set<string>();
  let id = parentID;
  while (id) {
    const entry = byID.get(id);
    if (!entry || entry.kind !== "folder" || excluded.has(id) || seen.has(id)) return false;
    seen.add(id); id = entry.parent_id;
  }
  return true;
}

export function informationMoveTargetRows(entries: readonly InformationEntry[], movingID: string, expanded: readonly string[]): InformationMoveTargetRow[] {
  const folders = entries.filter(entry => entry.kind === "folder"), open = new Set(expanded), seen = new Set<string>();
  const excluded = informationDescendants(entries, movingID), movingExists = entries.some(entry => entry.id === movingID);
  const parents = new Set(folders.map(entry => entry.parent_id)), rows: InformationMoveTargetRow[] = [];
  function walk(parent: string, depth: number) {
    for (const entry of informationVisible(folders, parent, "", false)) {
      if (seen.has(entry.id)) continue;
      seen.add(entry.id);
      rows.push({ entry, depth, expandable: parents.has(entry.id), expanded: open.has(entry.id), blocked: !movingExists || excluded.has(entry.id) });
      if (open.has(entry.id)) walk(entry.id, depth + 1);
    }
  }
  walk("", 0);
  return rows;
}

export function informationMoveInitialExpansion(entries: readonly InformationEntry[], parentID: string): string[] {
  return informationPath(entries, parentID).filter(entry => entry.kind === "folder").map(entry => entry.id);
}
