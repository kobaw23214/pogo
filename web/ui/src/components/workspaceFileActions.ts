import { reactive } from "vue";
import { invalidateIndex, invalidateTree, treeExpanded } from "./fileTreeStore";
import { workspaceFilePath } from "@/agent/officeFiles";

export interface WorkspaceFileTarget { path: string; name: string; dir: boolean; size: number; mtime_ms: number }
export interface WorkspaceFileMutation { ok: true; kind: "rename" | "delete"; path: string; new_path: string; is_directory: boolean }
const guards = new Set<(path: string) => boolean>();
const listeners = new Set<(change: WorkspaceFileMutation) => void>();
const pending = reactive(new Set<string>());

export function workspacePathContains(parent: string, path: string): boolean {
  const a = workspaceFilePath(parent), b = workspaceFilePath(path);
  return b === a || b.startsWith(`${a}/`);
}
export function workspaceMutationPending(path: string): boolean { return [...pending].some(parent => workspacePathContains(parent, path)); }
export function registerWorkspaceMutationGuard(guard: (path: string) => boolean): () => void { guards.add(guard); return () => guards.delete(guard); }
export function observeWorkspaceFileMutation(listener: (change: WorkspaceFileMutation) => void): () => void { listeners.add(listener); return () => listeners.delete(listener); }

// Reserve before sending a request. A text draft or live Office canvas blocks
// the operation; a late click cannot start another editor during publication.
export function beginWorkspaceFileMutation(path: string): (() => void) | null {
  if ([...pending].some(held => workspacePathContains(held,path) || workspacePathContains(path,held)) || [...guards].some(guard => !guard(path))) return null;
  pending.add(path);
  return () => pending.delete(path);
}
export function publishWorkspaceFileMutation(change: WorkspaceFileMutation): void {
  for (const path of [...treeExpanded]) {
    if (!workspacePathContains(change.path,path)) continue;
    treeExpanded.delete(path);
    if (change.kind === "rename") treeExpanded.add(change.new_path + path.slice(change.path.length));
  }
  invalidateTree(); invalidateIndex();
  for (const listener of listeners) listener(change);
}
