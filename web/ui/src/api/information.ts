import { authHeaders } from "./client";
import type { AttachmentMeta, InputLayout, MessageInputPart } from "./types";
import type { ShortcutSnapshot } from "./shortcuts";
import { validateInputParts } from "@/agent/shortcutStructure";

export interface InformationFile { name: string; mime: string; size: number; sha256: string; asset: string }
export interface InformationEntry {
  id: string; parent_id: string; kind: "folder" | "file" | "shortcut"; name: string;
  version: number; created_at: string; updated_at: string;
  file?: InformationFile; shortcut?: ShortcutSnapshot;
}
export interface InformationTree { scope_id: string; revision: number; entries: InformationEntry[]; can_write: boolean }
export interface InformationMutation extends InformationTree { entry?: InformationEntry }
export class InformationAPIError extends Error {
  constructor(message: string, public status: number, public code = "", public current?: InformationTree) { super(message); }
}
const validID = (value: unknown) => typeof value === "string" && /^inf_[a-f0-9]{32}$/.test(value);
export function isInformationEntry(value: unknown): value is InformationEntry {
  if (!value || typeof value !== "object") return false;
  const entry = value as InformationEntry;
  if (!validID(entry.id) || entry.parent_id !== "" && !validID(entry.parent_id) || !["folder", "file", "shortcut"].includes(entry.kind) || typeof entry.name !== "string" || !entry.name.trim() || !Number.isSafeInteger(entry.version) || entry.version < 1 || typeof entry.created_at !== "string" || typeof entry.updated_at !== "string") return false;
  if (entry.file && (typeof entry.file.name !== "string" || typeof entry.file.mime !== "string" || !Number.isSafeInteger(entry.file.size) || entry.file.size < 0 || !/^[a-f0-9]{64}$/.test(entry.file.sha256) || !/^information-assets\/[a-f0-9]{64}\/[^/\\\u0000]+$/.test(entry.file.asset))) return false;
  if (entry.shortcut && (typeof entry.shortcut.id !== "string" || typeof entry.shortcut.title !== "string" || typeof entry.shortcut.content !== "string" || validateInputParts([{ type: "shortcut", shortcut_id: entry.shortcut.id, title: entry.shortcut.title, text: entry.shortcut.content, icon: entry.shortcut.icon, language: entry.shortcut.language, ...(entry.shortcut.parts ? { parts: entry.shortcut.parts } : {}) }]))) return false;
  return true;
}
export function isInformationTree(value: unknown): value is InformationTree {
  if (!value || typeof value !== "object") return false;
  const tree = value as InformationTree;
  return typeof tree.scope_id === "string" && !!tree.scope_id && Number.isSafeInteger(tree.revision) && tree.revision >= 0 && typeof tree.can_write === "boolean" && Array.isArray(tree.entries) && tree.entries.every(isInformationEntry) && new Set(tree.entries.map(entry => entry.id)).size === tree.entries.length;
}
async function request<T>(path: string, init: RequestInit, validate: (value: unknown) => boolean): Promise<T> {
  const response = await fetch(`/api/information${path}`, { ...init, credentials: "same-origin", cache: "no-store", headers: { ...authHeaders(), ...(init.body instanceof FormData ? {} : init.body ? { "Content-Type": "application/json" } : {}), ...init.headers } });
  let value: unknown; try { value = await response.json(); } catch { value = undefined; }
  if (!response.ok) { const error = value as { error?: string; code?: string; current?: unknown } | undefined; throw new InformationAPIError(error?.error || response.statusText || "Request failed", response.status, error?.code, isInformationTree(error?.current) ? error.current : undefined); }
  if (!validate(value)) throw new InformationAPIError("Invalid information library response", response.status);
  return value as T;
}
const entryPath = (id: string) => { if (!validID(id)) throw new Error("Invalid information entry"); return `/entries/${encodeURIComponent(id)}`; };
const mutation = (value: unknown) => isInformationTree(value) && (!(value as InformationMutation).entry || isInformationEntry((value as InformationMutation).entry));
export const informationApi = {
  resolve: (sid: string, id: string, version: number) => resolveInformationEntry(sid, id, version),
  tree: (signal?: AbortSignal) => request<InformationTree>("", { signal }, isInformationTree),
  entry: (id: string, version?: number, signal?: AbortSignal) => request<InformationEntry>(`${entryPath(id)}${version ? `?version=${version}` : ""}`, { signal }, isInformationEntry),
  create: (body: { revision: number; parent_id: string; kind: "folder" | "shortcut"; name: string; shortcut?: ShortcutSnapshot }) => request<InformationMutation>("/entries", { method: "POST", body: JSON.stringify(body) }, mutation),
  update: (id: string, body: { revision: number; name?: string; parent_id?: string; shortcut?: ShortcutSnapshot }) => request<InformationMutation>(entryPath(id), { method: "PATCH", body: JSON.stringify(body) }, mutation),
  remove: (id: string, revision: number, recursive = false) => request<InformationMutation>(`${entryPath(id)}?${new URLSearchParams({ revision: String(revision), ...(recursive ? { recursive: "true" } : {}) })}`, { method: "DELETE" }, mutation),
  upload: (file: File, parentID: string, revision: number) => { const body = new FormData(); body.append("file", file, file.name); body.append("parent_id", parentID); body.append("revision", String(revision)); return request<InformationMutation>("/files", { method: "POST", body }, mutation); },
  uploadAsset: (file: File) => { const body = new FormData(); body.append("file", file, file.name); return request<{ part: Extract<MessageInputPart, { type: "file" }>; attachment: AttachmentMeta }>("/assets", { method: "POST", body }, value => { const result = value as { part?: MessageInputPart; attachment?: AttachmentMeta }; return !!result?.part && result.part.type === "file" && /^information-assets\/[a-f0-9]{64}\/[^/\\\u0000]+$/.test(result.part.path) && !validateInputParts([result.part]) && !!result.attachment && typeof result.attachment.name === "string"; }); },
  importShortcut: (body: { revision: number; parent_id: string; source_agent_id: string; shortcut_id: string }) => request<InformationMutation>("/import-shortcut", { method: "POST", body: JSON.stringify(body) }, mutation),
  assetContent: async (id: string, version: number, asset: string, download = false, signal?: AbortSignal): Promise<Blob> => {
    if (!/^information-assets\/[a-f0-9]{64}\/[^/\\\u0000]+$/.test(asset)) throw new Error("Invalid information asset");
    const response = await fetch(`/api/information${entryPath(id)}/assets/content?${new URLSearchParams({ version: String(version), asset, ...(download ? { download: "1" } : {}) })}`, { credentials: "same-origin", cache: "no-store", headers: authHeaders(), signal });
    if (!response.ok) throw new InformationAPIError("Information attachment could not be loaded", response.status);
    return response.blob();
  },
  content: async (id: string, version: number, download = false, signal?: AbortSignal): Promise<Blob> => { const response = await fetch(`/api/information${entryPath(id)}/content?${new URLSearchParams({ version: String(version), ...(download ? { download: "1" } : {}) })}`, { credentials: "same-origin", cache: "no-store", headers: authHeaders(), signal }); if (!response.ok) { let error = ""; try { error = (await response.json()).error; } catch { /* Do not echo raw file bytes as an error. */ } throw new InformationAPIError(error || response.statusText || "File could not be loaded", response.status); } return response.blob(); },
};
/** Resolve an explicitly chosen immutable version into the conversation's owned files. */
export async function resolveInformationEntry(sid: string, id: string, version: number): Promise<{ parts: MessageInputPart[]; input_layout?: InputLayout; attachments: AttachmentMeta[]; entry: InformationEntry }> {
  if (!validID(id) || !Number.isSafeInteger(version) || version < 1) throw new Error("Invalid information reference");
  const response = await fetch(`/api/sessions/${encodeURIComponent(sid)}/information/resolve`, { method: "POST", credentials: "same-origin", headers: { ...authHeaders(), "Content-Type": "application/json" }, body: JSON.stringify({ id, version }) });
  const value = await response.json();
  if (!response.ok) throw new InformationAPIError(value.error || response.statusText, response.status, value.code);
  if (!Array.isArray(value.parts) || validateInputParts(value.parts) || !Array.isArray(value.attachments) || !isInformationEntry(value.entry) || value.entry.id !== id || value.entry.version !== version) throw new Error("Invalid information reference response");
  return value;
}
