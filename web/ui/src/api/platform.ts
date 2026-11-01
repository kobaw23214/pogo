import { authHeaders } from "./client";
import type { SessionInfo } from "./types";
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
import type {
  PublishedPointer,
  ScheduleInput,
  ScheduleSummary,
  Workflow,
  WorkflowInput,
  WorkflowRun,
  WorkflowRunParticipants,
  WorkflowAccess,
  WorkflowSpaceRoles,
  WorkflowVersion,
  WorkflowApprovalSubject,
} from "@/agent/workflowTypes";
import { isStudioDestination } from "@/agent/studioRoutes";
import { confirmStudioNavigation } from "@/agent/studioNavigation";
export { isStudioPath, studioAgentId } from "@/agent/studioRoutes";

async function studioRequest<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, {
    method, credentials: "same-origin", cache: "no-store",
    headers: { ...authHeaders(), ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
  if (response.status === 204) return undefined as T;
  const raw = await response.text();
  let data: unknown;
  try { data = raw ? JSON.parse(raw) : undefined; } catch { data = undefined; }
  if (!response.ok) {
    const failure = data as { error?: string | { message?: string; code?: string }; code?: string } | undefined;
    const message = typeof failure?.error === "string" ? failure.error : failure?.error?.message || raw || "请求暂时未完成";
    throw Object.assign(new Error(message), { status: response.status, code: failure?.code || (typeof failure?.error === "object" ? failure.error.code : undefined), details: data });
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
  createSession: (id: string, options?: { workspace?: string }) =>
    studioRequest<{ id: string; agent_id: string; revision: number; model: string }>(
      "POST",
      `/api/apps/${encodeURIComponent(id)}/sessions`,
      options?.workspace ? { workspace: options.workspace } : {},
    ),
  cancel: (id: string, sid: string) => studioRequest<{ cancelled: boolean }>("POST", `/api/apps/${encodeURIComponent(id)}/sessions/${encodeURIComponent(sid)}/cancel`, {}),

  // Workflow definitions: the same manage-API conventions as Agents (admin
  // writes, revision optimistic lock — a 409 means someone else saved first).
  workflows: {
    approvalSubjects: (id: string) => studioRequest<{ subjects: WorkflowApprovalSubject[] }>("GET", `/api/manage/workflows/${encodeURIComponent(id)}/approval-subjects`),
    list: () => studioRequest<{ workflows: Workflow[]; can_manage: boolean }>("GET", "/api/manage/workflows"),
    capabilities: (id: string) => studioRequest<Pick<PlatformCapabilities, "builtin_tools">>("GET", `/api/manage/workflows/${encodeURIComponent(id)}/capabilities`),
    create: (input: WorkflowInput) => studioRequest<Workflow>("POST", "/api/manage/workflows", input),
    update: (id: string, input: WorkflowInput, revision: number) =>
      studioRequest<Workflow>("PATCH", `/api/manage/workflows/${encodeURIComponent(id)}`, { ...input, revision }),
    remove: (id: string) => studioRequest<void>("DELETE", `/api/manage/workflows/${encodeURIComponent(id)}`),
    access: (id: string) => studioRequest<WorkflowAccess>("GET", `/api/manage/workflows/${encodeURIComponent(id)}/access`),
    setAccess: (id: string, body: { principal_id: string; actions?: string[]; inherit?: boolean }) =>
      studioRequest<void>("PUT", `/api/manage/workflows/${encodeURIComponent(id)}/access`, body),
    roles: () => studioRequest<WorkflowSpaceRoles>("GET", "/api/manage/workflow-access/roles"),
    setRole: (body: { principal_id: string; role: string }) => studioRequest<void>("PUT", "/api/manage/workflow-access/roles", body),
    // Draft → immutable version; a run starts from the pointer, not the draft.
    publish: (id: string, meta: { name: string; desc: string; expected_revision?: number; expected_hash?: string }) =>
      studioRequest<PublishedPointer>("POST", `/api/manage/workflows/${encodeURIComponent(id)}/publish`, meta),
    validate: (id: string, revision: number, inputs?: Record<string, string>) =>
      studioRequest<{ valid: boolean; ready_for_test?: boolean; definition_hash: string; diagnostics: { code: string; severity?: string; message: string; field_path?: string; fix_hint?: string }[] }>("POST", "/api/manage/workflow-authoring/validate", { workflow_id: id, expected_revision: revision, ...(inputs ? { inputs } : {}) }),
    test: (id: string, revision: number, hash: string, inputs: Record<string, string>, requestId: string) =>
      studioRequest<{ id: string; test_run: boolean; draft_revision: number; definition_hash: string }>("POST", `/api/manage/workflows/${encodeURIComponent(id)}/test-run`, { expected_revision: revision, expected_hash: hash, inputs, request_id: requestId }),
    versions: (id: string) =>
      studioRequest<{ versions: WorkflowVersion[] }>("GET", `/api/manage/workflows/${encodeURIComponent(id)}/versions`),
    rollback: (id: string, versionId: string) =>
      studioRequest<PublishedPointer>("POST", `/api/manage/workflows/${encodeURIComponent(id)}/rollback`, { version_id: versionId }),
    // One workflow has at most one schedule; re-posting replaces it.
    schedule: {
      put: (id: string, input: ScheduleInput) =>
        studioRequest<ScheduleSummary>("POST", `/api/manage/workflows/${encodeURIComponent(id)}/schedule`, input),
      remove: (id: string) => studioRequest<void>("DELETE", `/api/manage/workflows/${encodeURIComponent(id)}/schedule`),
    },
  },
  // Markdown role-card import: paste one document, get one Studio agent.
  importAgent: (markdown: string) => studioRequest<PlatformAgent>("POST", "/api/manage/agents/import", { markdown }),
  // Runs of one workflow. The run record is the state authority; the stream
  // carries it whole on every structural event, so the editor heals from the
  // stream and only polls as a fallback.
  workflowRuns: {
    start: (wid: string, inputs?: Record<string, string>) => studioRequest<{ id: string }>("POST", `/api/workflows/${encodeURIComponent(wid)}/runs`, inputs ? { inputs } : {}),
    list: (wid: string) => studioRequest<{ runs: WorkflowRun[] }>("GET", `/api/workflows/${encodeURIComponent(wid)}/runs`),
    get: (wid: string, rid: string) =>
      studioRequest<WorkflowRun>("GET", `/api/workflows/${encodeURIComponent(wid)}/runs/${encodeURIComponent(rid)}`),
    cancel: (wid: string, rid: string) =>
      studioRequest<{ cancelled: boolean }>("POST", `/api/workflows/${encodeURIComponent(wid)}/runs/${encodeURIComponent(rid)}/cancel`, {}),
    retry: (wid: string, rid: string) =>
      studioRequest<{ id: string }>("POST", `/api/workflows/${encodeURIComponent(wid)}/runs/${encodeURIComponent(rid)}/retry`, {}),
    participants: (wid: string, rid: string) =>
      studioRequest<WorkflowRunParticipants>("GET", `/api/workflows/${encodeURIComponent(wid)}/runs/${encodeURIComponent(rid)}/participants`),
    setParticipant: (wid: string, rid: string, body: { principal_id: string; actions: string[]; node_id?: string }) =>
      studioRequest<WorkflowRunParticipants>("PUT", `/api/workflows/${encodeURIComponent(wid)}/runs/${encodeURIComponent(rid)}/participants`, body),
    // Wake a suspended run with a person's decision for one waiting node. The
    // 200 record is as-accepted; the decision lands on the stream/next poll.
    resume: (wid: string, rid: string, nid: string, body: { action: string; fields: Record<string, string> }) =>
      studioRequest<WorkflowRun>(
        "POST",
        `/api/workflows/${encodeURIComponent(wid)}/runs/${encodeURIComponent(rid)}/nodes/${encodeURIComponent(nid)}/resume`,
        body,
      ),
    streamUrl: (wid: string, rid: string) =>
      `/api/workflows/${encodeURIComponent(wid)}/runs/${encodeURIComponent(rid)}/stream`,
    // The artifact is a workspace file the node left behind; it comes through
    // fetch (not <a href>) because the Bearer token rides in a header. The
    // body is the file's raw text rather than a JSON envelope.
    artifact: async (wid: string, rid: string, nid: string): Promise<string> => {
      const response = await fetch(
        `/api/workflows/${encodeURIComponent(wid)}/runs/${encodeURIComponent(rid)}/nodes/${encodeURIComponent(nid)}/artifact`,
        { credentials: "same-origin", cache: "no-store", headers: { ...authHeaders() } },
      );
      const raw = await response.text();
      if (!response.ok) throw Object.assign(new Error(raw || "请求暂时未完成"), { status: response.status });
      return raw;
    },
    artifactUrl: (wid: string, rid: string, nid: string) =>
      `/api/workflows/${encodeURIComponent(wid)}/runs/${encodeURIComponent(rid)}/nodes/${encodeURIComponent(nid)}/artifact`,
  },
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
