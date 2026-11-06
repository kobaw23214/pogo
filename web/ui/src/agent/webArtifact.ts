import { validWebServiceId } from "@/api/webServices";

export interface WebArtifactSpec { service_id: string; title?: string }
export interface WebArtifactScope { sessionId: string }

/** Persist service identity, never a bearer URL or a model-chosen account. */
export function parseWebArtifact(source: string): WebArtifactSpec | null {
  if (!source.trim() || source.length > 4096) return null;
  try {
    const value = JSON.parse(source) as Record<string, unknown>;
    if (!value || typeof value !== "object" || Array.isArray(value) ||
      Object.keys(value).some(key => key !== "service_id" && key !== "title") ||
      !validWebServiceId(value.service_id) || !value.service_id.startsWith("ws_")) return null;
    if (value.title !== undefined && (typeof value.title !== "string" || !value.title.trim() || value.title.length > 160 || /[\x00-\x1f]/.test(value.title))) return null;
    return { service_id: value.service_id, title: typeof value.title === "string" ? value.title.trim() : undefined };
  } catch { return null; }
}

export function validWebArtifactScope(scope: WebArtifactScope | null): scope is WebArtifactScope {
  return !!scope && validWebServiceId(scope.sessionId);
}
