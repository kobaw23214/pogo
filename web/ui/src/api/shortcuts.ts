import type { InputLayout, MessageInputPart, AttachmentMeta, StarterCard, Starters } from "./types";
import { authHeaders } from "./client";
import { validateInputParts } from "@/agent/shortcutStructure";
import { validShortcutLanguage } from "@/components/shortcutPresentation";

/** Saved reference data. Inserting it never sends or executes the content. */
export interface ShortcutSnapshot {
  id: string;
  title: string;
  content: string;
  parts?: MessageInputPart[];
  input_layout?: InputLayout;
  icon?: string;
  /** Frozen syntax display hint. Never included in the model-facing text. */
  language?: string;
  /** Parent in this owner + Agent's private folder tree; absent means root. */
  parent_id?: string;
  /** Live homepage projection; never persisted as an ordinary private item. */
  source?: "starter";
}

export interface ShortcutFolder {
  id: string;
  name: string;
  parent_id?: string;
}

export interface ShortcutsView {
  scope_id: string;
  agent_id: string;
  revision: number;
  shortcuts: ShortcutSnapshot[];
  /** New servers always return an array. Optional for older responses/callers. */
  folders?: ShortcutFolder[];
  starter_shortcuts?: ShortcutSnapshot[];
  starter_actions?: StarterCard[];
  can_manage_starters?: boolean;
}

export function shortcutEntries(view: ShortcutsView | null | undefined): ShortcutSnapshot[] {
  return [...(view?.shortcuts || []), ...(view?.starter_shortcuts || []).map(item => ({ ...item, source: "starter" as const }))];
}

export class ShortcutsAPIError extends Error {
  constructor(message: string, public status: number, public code = "", public current?: ShortcutsView) { super(message); }
}

const privateFolderId = /^[A-Za-z0-9_-]{1,160}$/;
function validParent(parent: unknown): parent is string | undefined { return parent === undefined || parent === "" || typeof parent === "string" && privateFolderId.test(parent); }
function isShortcut(shortcut: ShortcutSnapshot) { return shortcut && typeof shortcut.id === "string" && typeof shortcut.title === "string" && typeof shortcut.content === "string" && validParent(shortcut.parent_id) && (shortcut.icon === undefined || typeof shortcut.icon === "string") && validShortcutLanguage(shortcut.language) && (shortcut.parts === undefined || Array.isArray(shortcut.parts) && !validateInputParts([{ type: "shortcut", shortcut_id: shortcut.id, title: shortcut.title, text: shortcut.content, icon: shortcut.icon, language: shortcut.language, parts: shortcut.parts }])); }
function validPrivateTree(folders: unknown, shortcuts: ShortcutSnapshot[]) {
  if (folders === undefined) return shortcuts.every(item => !item.parent_id);
  if (!Array.isArray(folders) || folders.length > 256) return false;
  const ids = new Set(shortcuts.map(item => item.id)), parents = new Map<string, string>();
  if (ids.size !== shortcuts.length) return false;
  for (const folder of folders) {
    if (!folder || typeof folder !== "object" || typeof folder.id !== "string" || !privateFolderId.test(folder.id) || ids.has(folder.id) || typeof folder.name !== "string" || !folder.name.trim() || [...folder.name].length > 120 || /[\u0000-\u001f\u007f-\u009f]/.test(folder.name) || !validParent(folder.parent_id)) return false;
    ids.add(folder.id); parents.set(folder.id, folder.parent_id || "");
  }
  if (shortcuts.some(item => item.parent_id && !parents.has(item.parent_id))) return false;
  for (const [id] of parents) {
    const seen = new Set<string>(); let current = id;
    while (current) {
      if (seen.has(current) || !parents.has(current) || seen.size >= 16) return false;
      seen.add(current); current = parents.get(current)!;
    }
  }
  return true;
}
function isView(value: unknown): value is ShortcutsView {
  if (!value || typeof value !== "object") return false;
  const item = value as Partial<ShortcutsView>;
  return typeof item.scope_id === "string" && typeof item.agent_id === "string" && Number.isInteger(item.revision) && item.revision! >= 0
    && Array.isArray(item.shortcuts) && item.shortcuts.every(isShortcut)
    && validPrivateTree(item.folders, item.shortcuts)
    && (item.starter_shortcuts === undefined || Array.isArray(item.starter_shortcuts) && item.starter_shortcuts.every(isShortcut))
    && (item.starter_actions === undefined || Array.isArray(item.starter_actions) && item.starter_actions.every(card => card && typeof card.title === "string" && typeof card.panel === "string" && !!card.panel && !card.parts && !card.prompt))
    && (item.can_manage_starters === undefined || typeof item.can_manage_starters === "boolean");
}

async function request(agentId: string, update?: ShortcutsView, signal?: AbortSignal): Promise<ShortcutsView> {
  const query = agentId ? `?${new URLSearchParams({ agent_id: agentId })}` : "";
  const response = await fetch(`/api/shortcuts${query}`, {
    method: update ? "PUT" : "GET", credentials: "same-origin", cache: "no-store", signal,
    headers: { ...authHeaders(), ...(update ? { "Content-Type": "application/json" } : {}) },
    body: update ? JSON.stringify({ scope_id: update.scope_id, revision: update.revision, shortcuts: update.shortcuts, ...(update.folders === undefined ? {} : { folders: update.folders }) }) : undefined,
  });
  let value: unknown;
  try { value = await response.json(); } catch { value = undefined; }
  if (!response.ok) {
    const error = value as { error?: string; code?: string; current?: unknown } | undefined;
    throw new ShortcutsAPIError(error?.error || response.statusText || "Request failed", response.status, error?.code, isView(error?.current) ? error.current : undefined);
  }
  if (!isView(value) || value.agent_id !== agentId) throw new ShortcutsAPIError("Invalid shortcut response", response.status);
  return value;
}

export const shortcutsApi = {
  list: (agentId = "", signal?: AbortSignal) => request(agentId, undefined, signal),
  save: (agentId: string, view: ShortcutsView, signal?: AbortSignal) => request(agentId, view, signal),
};

export interface ShortcutAssets { parts: MessageInputPart[]; input_layout?: InputLayout; prompt?: string; attachments: AttachmentMeta[] }
async function assets(action: "assets" | "resolve", sid: string, input: { parts: MessageInputPart[]; input_layout?: InputLayout; prompt?: string }): Promise<ShortcutAssets> {
  const response = await fetch(`/api/sessions/${encodeURIComponent(sid)}/shortcuts/${action}`, { method: "POST", credentials: "same-origin", headers: { ...authHeaders(), "Content-Type": "application/json" }, body: JSON.stringify(input) });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || response.statusText);
  if (!Array.isArray(result.parts) || !Array.isArray(result.attachments)) throw new Error("Invalid shortcut assets response");
  return result;
}
export const shortcutAssetsApi = { save: (sid: string, input: Parameters<typeof assets>[2]) => assets("assets", sid, input), resolve: (sid: string, input: Parameters<typeof assets>[2]) => assets("resolve", sid, input) };

export async function resolveStarterShortcut(sid: string, item: ShortcutSnapshot): Promise<ShortcutSnapshot> {
  const response = await fetch(`/api/sessions/${encodeURIComponent(sid)}/shortcuts/starters/${encodeURIComponent(item.id)}/resolve`, { method: "POST", credentials: "same-origin", headers: authHeaders() });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || response.statusText);
  if (!Array.isArray(result.parts) || !Array.isArray(result.attachments) || validateInputParts(result.parts)) throw new Error("Invalid shortcut source response");
  const { source: _source, ...snapshot } = item;
  return { ...snapshot, parts: result.parts };
}

export async function saveHomepageStarters(set: Pick<Starters, "heading" | "send" | "cards" | "followups">, expectedRevision: string) {
  const response = await fetch("/api/starters", { method: "PUT", credentials: "same-origin", headers: { ...authHeaders(), "Content-Type": "application/json" }, body: JSON.stringify({ ...set, expected_revision: expectedRevision }) });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || response.statusText);
  return result as { ok: boolean; cards: number; warnings?: string[] };
}
