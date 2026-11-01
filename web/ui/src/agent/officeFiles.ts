// Workspace file identity is case-sensitive. Do not decode URLs, Unicode-fold
// names, or turn backslashes into separators: those may be different files.
export function workspaceFilePath(path: string): string {
  const parts: string[] = [];
  for (const part of path.split("/")) {
    if (!part || part === ".") continue;
    if (part === ".." && parts.length && parts[parts.length - 1] !== "..") parts.pop();
    else parts.push(part);
  }
  return (path.startsWith("/") ? "/" : "") + parts.join("/");
}

export function attachmentWorkspacePath(workspace: string | undefined, path: string): string {
  const normalized = workspaceFilePath(path);
  const root = workspaceFilePath(workspace || "");
  if (!root || normalized.startsWith("/") || normalized === root || normalized.startsWith(`${root}/`)) return normalized;
  return workspaceFilePath(`${root}/${normalized}`);
}

export function isEditableOfficeFile(path: string): boolean {
  return /\.(docx|xlsx)$/i.test(path);
}

export function sameWorkspaceFile(left: string | undefined, right: string): boolean {
  if (left === undefined) return false;
  const a = workspaceFilePath(left), b = workspaceFilePath(right);
  return a === b || (!!identities.get(a) && identities.get(a) === identities.get(b));
}

// Aliases are learned only from a validated, authenticated Office response.
// Never infer identity by removing users/<owner> or comparing path suffixes.
const identities = new Map<string, string>();
export function rememberOfficeFileIdentity(requestedPath: string, canonicalPath: string, fileRef: string): void {
  if (!fileRef) return;
  identities.set(workspaceFilePath(requestedPath), fileRef);
  identities.set(workspaceFilePath(canonicalPath), fileRef);
}

// Reserve before the first async request, not after the editor has connected.
// This closes the file-tree / attachment / agent-open race while retaining the
// mounted editor's own server session and instance guards.
const openings = new Map<string, object>();
export function reserveOfficeFile(path: string, conversationId: string): (() => void) | null {
  const key = JSON.stringify([conversationId, workspaceFilePath(path)]);
  if (openings.has(key)) return null;
  const owner = {};
  openings.set(key, owner);
  return () => { if (openings.get(key) === owner) openings.delete(key); };
}
