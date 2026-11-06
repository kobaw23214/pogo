import type { ModelInfo } from "@/api/types";

/** Effective model and tool policies for this conversation. */
export interface StudioSessionCapabilities {
  models: string[];
  model: string;
  skills: string[];
  mcp_servers: string[];
  /** Effective native-tool selection; absent on a pre-selection server. */
  tools?: string[];
  /** New servers support applying the latest Agent model policy to this chat. */
  model_sync?: boolean;
  tools_sync?: boolean;
  tools_revision?: number;
  tools_pending?: boolean;
  tools_desired_revision?: number;
  skills_revision?: number;
  skills_desired_revision?: number;
  skills_pending?: boolean;
  mcp_sync?: boolean;
  mcp_revision?: number;
  mcp_desired_revision?: number;
  mcp_pending?: boolean;
}

export interface StudioModelConfigSync {
  changed: boolean;
  deferred: boolean;
  model: string;
  models: string[];
  revision: number;
  desired_revision: number;
}

export interface StudioToolConfigSync {
  changed: boolean;
  deferred: boolean;
  revision: number;
  desired_revision: number;
  /** Effective tools, including protected tools; never the pending selection. */
  tools: string[];
}

export interface StudioSkillConfigSync {
  changed: boolean;
  deferred: boolean;
  revision: number;
  desired_revision: number;
}

export interface StudioMcpConfigSync {
  changed: boolean;
  deferred: boolean;
  revision: number;
  desired_revision: number;
  /** Effective servers, never a pending selection. */
  mcp_servers: string[];
}

export function studioSessionModels(
  catalog: readonly ModelInfo[],
  capabilities: StudioSessionCapabilities | null,
): ModelInfo[] {
  if (!capabilities) return [];
  const allowed = new Set(capabilities.models);
  return catalog.filter((model) => allowed.has(model.id));
}

export function canSelectStudioModel(capabilities: StudioSessionCapabilities | null, id: string): boolean {
  return capabilities?.models.includes(id) === true;
}
