import type { ToolName } from "@/api/types";

/** Legacy names remain valid grants; displaying a group never expands them. */
export const legacyShortcutManagementNames = [
  "shortcut_create", "shortcut_update", "shortcut_folder_create", "shortcut_folder_rename", "shortcut_move",
] as const;

const groupedActions: Record<string, Record<string, ToolName>> = {
  shortcut_query: { search: "shortcut_search", read: "shortcut_read", list: "shortcut_list" },
  shortcut_manage: { create: "shortcut_create", update: "shortcut_update", folder_create: "shortcut_folder_create", folder_rename: "shortcut_folder_rename", move: "shortcut_move" },
};

/** Resolve only declared actions, retaining the original shape for old history. */
export function shortcutCallTool(name: ToolName, args: unknown): ToolName {
  const actions = groupedActions[name];
  if (!actions || !args || typeof args !== "object" || Array.isArray(args)) return name;
  const action = (args as Record<string, unknown>).action;
  return typeof action === "string" && Object.prototype.hasOwnProperty.call(actions, action) ? actions[action]! : name;
}
