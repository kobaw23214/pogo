import type { ShortcutFolder, ShortcutSnapshot } from "@/api/shortcuts";
export const shortcutParent = (value: { parent_id?: string }) => value.parent_id || "";
/** A bounded ancestry walk is defensive against stale or incomplete views. */
export function shortcutFolderPath(folders: readonly ShortcutFolder[], id: string): ShortcutFolder[] {
  const index = new Map(folders.map(folder => [folder.id, folder])), path: ShortcutFolder[] = [], seen = new Set<string>();
  while (id && path.length < 16 && !seen.has(id)) {
    const folder = index.get(id); if (!folder) break;
    seen.add(id); path.unshift(folder); id = shortcutParent(folder);
  }
  return path;
}
export function shortcutFolderContains(folders: readonly ShortcutFolder[], ancestor: string, target: string): boolean {
  return !ancestor || shortcutFolderPath(folders, target).some(folder => folder.id === ancestor);
}
export function shortcutFolderChildren(folders: readonly ShortcutFolder[], parent: string): ShortcutFolder[] {
  return folders.filter(folder => shortcutParent(folder) === parent);
}
export function shortcutFolderNotEmpty(folders: readonly ShortcutFolder[], items: readonly ShortcutSnapshot[], id: string): boolean {
  return folders.some(folder => shortcutParent(folder) === id) || items.some(item => shortcutParent(item) === id);
}
