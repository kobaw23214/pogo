import type { StudioMcpConfigSync, StudioSessionCapabilities } from "./studioSessionCapabilities";

export function validStudioMcpConfig(value: unknown): value is StudioMcpConfigSync {
  if (!value || typeof value !== "object") return false;
  const v = value as StudioMcpConfigSync;
  return typeof v.changed === "boolean" && typeof v.deferred === "boolean"
    && Number.isSafeInteger(v.revision) && v.revision >= 0
    && Number.isSafeInteger(v.desired_revision) && v.desired_revision >= v.revision
    && Array.isArray(v.mcp_servers) && v.mcp_servers.every(name => typeof name === "string" && !!name);
}

/** A pre-sync server keeps its original contract; do not probe an endpoint
 * until the scoped capability explicitly advertises it. */
export async function syncStudioMcpCapabilities(
  capabilities: StudioSessionCapabilities,
  request: () => Promise<StudioMcpConfigSync>,
): Promise<StudioSessionCapabilities> {
  if (!capabilities.mcp_sync) return capabilities;
  const result = await request();
  if (!validStudioMcpConfig(result)) throw new Error("Invalid MCP configuration receipt");
  if (result.revision < (capabilities.mcp_revision ?? 0)
    || result.revision === capabilities.mcp_revision && result.desired_revision < (capabilities.mcp_desired_revision ?? 0)) return capabilities;
  return { ...capabilities, mcp_servers: [...new Set(result.mcp_servers)].sort(),
    mcp_revision: result.revision, mcp_desired_revision: result.desired_revision, mcp_pending: result.deferred };
}

/** Pending-only receipts do not invalidate documents or grant desired tools. */
export function studioCapabilityFingerprint(capabilities: StudioSessionCapabilities): string {
  return JSON.stringify([capabilities.skills_revision ?? null, [...new Set(capabilities.skills)].sort(),
    capabilities.mcp_revision ?? null, [...new Set(capabilities.mcp_servers)].sort()]);
}
