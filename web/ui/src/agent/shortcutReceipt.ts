import type { ShortcutChange, ShortcutDetails, ShortcutMergePlan, ShortcutMutationAction } from "@/api/types";
import type { ShortcutSnapshot } from "@/api/shortcuts";
import { validateInputParts } from "@/agent/shortcutStructure";

const identity = /^[A-Za-z0-9_-]{1,160}$/;
const privateID = (value: unknown): value is string => typeof value === "string" && identity.test(value);
const parentID = (value: unknown) => value === undefined || value === "" || privateID(value);
const revision = (value: unknown, minimum = 0): value is number => typeof value === "number" && Number.isSafeInteger(value) && value >= minimum;
const title = (value: unknown): value is string => typeof value === "string" && !!value.trim() && [...value].length <= 120 && !/\p{Cc}/u.test(value);
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const actionTools: Record<ShortcutMutationAction, string> = {
  folder_create: "shortcut_folder_create", update: "shortcut_update", folder_rename: "shortcut_folder_rename",
  move: "shortcut_move", delete: "shortcut_delete", merge: "shortcut_merge",
};
export type SavedShortcutDetails = ShortcutDetails & { action: "create" | "update"; saved: true; shortcut: ShortcutSnapshot };
export type ShortcutMutationReceipt = ShortcutDetails & { action: ShortcutMutationAction; saved: true; changed: boolean; agent_id: string; revision: number; applied_revision: number; request_id: string; changes: ShortcutChange[] };
export type ShortcutMergePreview = ShortcutDetails & { action: "merge"; saved: false; changed: false; agent_id: string; revision: number; plan: ShortcutMergePlan; changes: ShortcutChange[] };

function scope(d: ShortcutDetails, expectedAgent?: string) {
  return privateID(d.agent_id) && (expectedAgent === undefined || d.agent_id === expectedAgent) && revision(d.revision)
    && (d.reused === undefined || typeof d.reused === "boolean");
}
function snapshot(value: unknown): value is ShortcutSnapshot {
  if (!object(value)) return false;
  const item = value as unknown as ShortcutSnapshot;
  return privateID(item.id) && title(item.title) && typeof item.content === "string" && parentID(item.parent_id) && item.source === undefined && (item.input_layout === undefined || item.input_layout === "top" || item.input_layout === "inline")
    && !validateInputParts([{ type: "shortcut", shortcut_id: item.id, title: item.title, text: item.content, icon: item.icon, language: item.language, parts: item.parts }]);
}
function folder(value: unknown): boolean {
  return object(value) && privateID(value.id) && title(value.name) && parentID(value.parent_id) && (value.read_only === undefined || value.read_only === false);
}
function changes(value: unknown, action: ShortcutMutationAction): value is ShortcutChange[] {
  if (!Array.isArray(value) || value.length > 512) return false;
  const seen = new Set<string>();
  return value.every(change => {
    if (!object(change) || change.action !== (action === "merge" ? "delete" : action) || !["folder", "shortcut"].includes(String(change.kind))
      || !privateID(change.id) || !title(change.title) || !parentID(change.from_parent_id) || !parentID(change.to_parent_id) || seen.has(change.id)) return false;
    if ((action === "folder_create" || action === "folder_rename") && change.kind !== "folder" || (action === "update" || action === "merge") && change.kind !== "shortcut") return false;
    seen.add(change.id); return true;
  });
}
function plan(value: unknown, current: number): value is ShortcutMergePlan {
  if (!object(value) || !privateID(value.id) || value.revision !== current || !Array.isArray(value.groups) || value.groups.length > 256 || !revision(value.removals) || value.removals > 256) return false;
  const seen = new Set<string>(); let removals = 0;
  for (const group of value.groups) {
    if (!object(group) || !privateID(group.keep_id) || !title(group.title) || !parentID(group.parent_id) || !Array.isArray(group.duplicate_ids) || !group.duplicate_ids.length || group.duplicate_ids.length > 256) return false;
    for (const id of [group.keep_id, ...group.duplicate_ids]) { if (!privateID(id) || seen.has(id)) return false; seen.add(id); }
    removals += group.duplicate_ids.length;
  }
  return removals === value.removals;
}

/** Preview and apply are deliberately distinct: a valid plan never means saved. */
export function isShortcutMergePreview(value: unknown, expectedAgent?: string): value is ShortcutMergePreview {
  if (!object(value)) return false;
  const d = value as unknown as ShortcutDetails;
  return d.type === "shortcut" && d.action === "merge" && d.saved === false && d.changed === false && scope(d, expectedAgent)
    && (d.request_id === undefined || d.request_id === "") && (d.applied_revision === undefined || d.applied_revision === 0)
    && !d.reused && Array.isArray(d.changes) && d.changes.length === 0 && d.shortcut === undefined && d.folder === undefined && plan(d.plan, d.revision!);
}
export function isShortcutMutationDetails(value: unknown, tool?: string, expectedAgent?: string): value is ShortcutMutationReceipt | ShortcutMergePreview {
  if (!object(value)) return false;
  const d = value as unknown as ShortcutDetails;
  if (!Object.prototype.hasOwnProperty.call(actionTools, d.action) || tool !== undefined && actionTools[d.action as ShortcutMutationAction] !== tool) return false;
  if (isShortcutMergePreview(value, expectedAgent)) return true;
  if (d.type !== "shortcut" || d.saved !== true || typeof d.changed !== "boolean" || !scope(d, expectedAgent) || !privateID(d.request_id)
    || !revision(d.applied_revision, 1) || d.applied_revision > d.revision! || !changes(d.changes, d.action as ShortcutMutationAction)
    || d.reused === true && d.changed || d.changed && d.changes.length === 0 || d.plan !== undefined && (d.action !== "merge" || !plan(d.plan, d.applied_revision - 1))) return false;
  if (d.action === "update") return d.folder === undefined && (snapshot(d.shortcut) && d.changes.every(change => change.id === d.shortcut!.id) || d.reused === true && d.shortcut === undefined && d.changes.length === 0);
  if (d.shortcut !== undefined) return false;
  if (d.action === "folder_create" || d.action === "folder_rename") return folder(d.folder) && d.changes.every(change => change.id === d.folder!.id) || d.reused === true && d.folder === undefined && d.changes.length === 0;
  return d.folder === undefined;
}
/** A preview opens only a persisted, validated host snapshot. Older create receipts remain supported. */
export function isSavedShortcutDetails(value: unknown, expectedAgent?: string): value is SavedShortcutDetails {
  if (!object(value)) return false;
  const d = value as unknown as ShortcutDetails;
  if (d.action === "update") return isShortcutMutationDetails(value, "shortcut_update", expectedAgent) && d.saved === true && snapshot(d.shortcut);
  return d.type === "shortcut" && d.action === "create" && d.saved === true && scope(d, expectedAgent) && revision(d.revision, 1) && privateID(d.request_id) && snapshot(d.shortcut);
}
