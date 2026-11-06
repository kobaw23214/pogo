import { authHeaders } from "./client";
import { getLocale, gt } from "@/i18n";
import { hostAPIError } from "./hostErrorCopy";
import type { PolicyMode, SessionInfo } from "./types";
import type {
  AgentTemplate,
  PlatformAgent,
  PlatformAgentInput,
  PlatformCapabilities,
  SkillSelection,
  SkillSelectionPlan,
  StarterGenerationInput,
  StarterGenerationResult,
} from "@/agent/platformTypes";
import { isStudioDestination } from "@/agent/studioRoutes";
import { confirmStudioNavigation } from "@/agent/studioNavigation";
export { isStudioPath, studioAgentId } from "@/agent/studioRoutes";

async function studioRequest<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, {
    method, credentials: "same-origin", cache: "no-store",
    headers: { ...authHeaders(), "Accept-Language": getLocale(), ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
  if (response.status === 204) return undefined as T;
  const raw = await response.text();
  let data: unknown;
  try { data = raw ? JSON.parse(raw) : undefined; } catch { data = undefined; }
  if (!response.ok) {
    const failure = data as { error?: string | { message?: string; code?: string }; code?: string } | undefined;
    const message = typeof failure?.error === "string" ? failure.error : failure?.error?.message || raw || gt("apiErrors.requestFailed");
    throw Object.assign(hostAPIError(message), { status: response.status, code: failure?.code || (typeof failure?.error === "object" ? failure.error.code : undefined), details: data });
  }
  return data as T;
}

export const platformApi = {
  list: () => studioRequest<{ agents: PlatformAgent[]; can_manage: boolean }>("GET", "/api/manage/agents"),
  // Catalog metadata for the editor's capability pickers. No credentials or
  // server config bodies ride in this response, only ids/labels/counts.
  capabilities: () => studioRequest<PlatformCapabilities>("GET", "/api/manage/capabilities"),
  skillSelection: (selection: SkillSelection) => studioRequest<SkillSelectionPlan>("POST", "/api/manage/skill-selection", selection),
  // The creation dialog's starting points — server-owned so they are workspace
  // furniture every admin sees and curates together.
  templates: {
    list: () => studioRequest<{ templates: AgentTemplate[] }>("GET", "/api/manage/templates"),
    create: (input: Omit<AgentTemplate, "id">) => studioRequest<AgentTemplate>("POST", "/api/manage/templates", input),
    remove: (id: string) => studioRequest<void>("DELETE", `/api/manage/templates/${encodeURIComponent(id)}`),
  },
  create: (input: PlatformAgentInput) => studioRequest<PlatformAgent>("POST", "/api/manage/agents", input),
  generateStarters: (input: StarterGenerationInput, signal?: AbortSignal) =>
    studioRequest<StarterGenerationResult>("POST", "/api/manage/agents/generate-starters", input, signal),
  update: (id: string, input: PlatformAgentInput, revision: number) => studioRequest<PlatformAgent>("PATCH", `/api/manage/agents/${encodeURIComponent(id)}`, { ...input, revision }),
  remove: (id: string) => studioRequest<void>("DELETE", `/api/manage/agents/${encodeURIComponent(id)}`),
  agent: (id: string) => studioRequest<PlatformAgent>("GET", `/api/apps/${encodeURIComponent(id)}`),
  // Keep workspace discovery scoped to the Agent's own conversation endpoint.
  sessions: (id: string) => studioRequest<{ sessions: SessionInfo[]; cwd?: string }>("GET", `/api/apps/${encodeURIComponent(id)}/sessions`),
  // Creates a real Pogo session pinned to this Agent's model and instructions.
  // workspace is a server-root-relative directory; omit it for the server
  // default. Never send a model override — the Agent definition owns the model.
  createSession: (id: string, options?: { workspace?: string; approvalMode?: PolicyMode }) =>
    studioRequest<{ id: string; agent_id: string; revision: number; model: string }>(
      "POST",
      `/api/apps/${encodeURIComponent(id)}/sessions`,
      {
        ...(options?.workspace ? { workspace: options.workspace } : {}),
        ...(options?.approvalMode ? { approval_mode: options.approvalMode } : {}),
      },
    ),
  cancel: (id: string, sid: string) => studioRequest<{ cancelled: boolean }>("POST", `/api/apps/${encodeURIComponent(id)}/sessions/${encodeURIComponent(sid)}/cancel`, {}),

};

let navigationPending = false;
export async function navigateStudio(path: string): Promise<void> {
  // Only app-local destinations; credentials stay in the existing auth layer.
  if (!isStudioDestination(path) || navigationPending) return;
  if (`${location.pathname}${location.search}` === path) return;
  navigationPending = true;
  try {
    if (!(await confirmStudioNavigation())) return;
    history.pushState(null, "", path);
    window.dispatchEvent(new Event("pogo:studio-navigation"));
  } finally { navigationPending = false; }
}
