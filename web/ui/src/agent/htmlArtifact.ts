import { attachmentWorkspacePath } from "./officeFiles";
import { workspaceFileTarget, type WorkspaceFileTarget } from "./workspaceFileLinks";
import type { WorkspaceFileScope } from "./workspaceFileNavigation";

export interface HTMLArtifactSpec {
  path: string;
  title?: string;
}

export interface ResolvedHTMLArtifact {
  title: string;
  target: WorkspaceFileTarget;
  fullPath: string;
}

export function parseHTMLArtifact(source: string): HTMLArtifactSpec | null {
  if (!source.trim() || source.length > 4096) return null;
  try {
    const value = JSON.parse(source) as { path?: unknown; title?: unknown };
    if (!value || typeof value !== "object" || Array.isArray(value) || typeof value.path !== "string" || value.path.trim() !== value.path || !value.path) return null;
    if (value.title !== undefined && (typeof value.title !== "string" || !value.title.trim() || value.title.length > 160)) return null;
    return { path: value.path, title: value.title?.trim() };
  } catch {
    return null;
  }
}

export function resolveHTMLArtifact(spec: HTMLArtifactSpec, scope: WorkspaceFileScope | null): ResolvedHTMLArtifact | null {
  if (!scope) return null;
  const target = workspaceFileTarget(spec.path, scope, "href");
  if (!target || !/\.html?$/i.test(target.path)) return null;
  return {
    title: spec.title || target.name,
    target,
    fullPath: attachmentWorkspacePath(scope.workspace, target.path),
  };
}
