import { getLocale, gt } from "@/i18n";
import type { WebServiceOpenResponse, WebServiceLog, WebServiceRecord, WebServiceSpec, WebsiteQuota, WebsitePolicy, WebsiteSettingsResponse, WebsiteSettingsUpdate } from "./webServices";
import { hostAPIError } from "./hostErrorCopy";
import type {
  BackgroundTaskSessionIdentity,
  AttachmentMeta,
  AuthoredSkillSummary,
  AuthoredSkillVersion,
  InboxItem,
  MessageInputPart,
  InputLayout,
  ConfigInfo,
  ConfigProbeResult,
  ConfigMetadataProbeRequest,
  VisionProbePreview,
  VisionProbeResult,
  ConfigSaveResult,
  FileContent,
  FileIndexResponse,
  SessionFileLinkResolution,
  FileRender,
  WorkspaceHTMLPreview,
  FilesResponse,
  ModelInfo,
  MaxTurnsInfo,
  MemoryCatalog,
  MemoryNote,
  ManagedMemoryCatalog,
  ManagedMemoryNoteInput,
  PanelInfo,
  Prefs,
  PrefsInfo,
  ScheduleJob,
  ScheduleJobCreate,
  ScheduleJobUpdate,
  ScheduleJobsInfo,
  MCPCallLog,
  MCPLogStats,
  ScheduleRunsInfo,
  SessionImportResult,
  ShareEntry,
  SkillUploadResult,
  SpillPruneResult,
  SpillEntries,
  SpillFile,
  Starters,
  PolicyMode,
  PolicyState,
  AttachmentPolicy, SandboxInfo,
  MCPConfig,
  MCPInfo,
  MCPProbeResult,
  MCPServerConfig,
  MCPServerStatus,
  SessionInfo,
  SessionHit,
  SkillContent,
  SkillDocumentContent,
  SkillPackageFiles,
  SkillInfo,
  Snapshot,
  ShellJobRecord,
  ShellJobOutput,
  ToolCatalogEntry,
  BuiltinToolEntry,
  McpServerGroup,
  WorkspaceChange,
  WorkspaceDiff,
} from "./types";
import { delegatedRequest, isDelegatedEmbed } from "./delegated";
import { OFFICE_STARTUP_TIMEOUT_MS, waitForOfficeStartup } from "@/agent/officeStartup";
import type { McpCall, McpCallsPage, McpCallStats } from "@/components/mcpMonitor";
import { requireMonitorAgentId } from "@/components/mcpMonitor";
import type { SkillEvent, SkillEventsPage, SkillEventStats } from "@/components/skillMonitor";
import type { ObservabilityStatus } from "@/components/observeStatus";
import type { StudioModelConfigSync, StudioSkillConfigSync, StudioToolConfigSync, StudioMcpConfigSync, StudioSessionCapabilities } from "@/agent/studioSessionCapabilities";
import type { SentinelSessionList, SentinelSessionDetail } from "./sentinelSessions";
import type { SkillLibraryCatalog } from "@/agent/platformTypes";
import { uploadFile, type UploadOptions } from "./upload";
import type { BrowserPickPreview, BrowserPickCommitRequest } from "@/agent/browserPicker";
import { decodeNativeOverlay, type BrowserBackendState, type NativeBrowserControl, type NativeBrowserInput, type NativeBrowserOverlay, type NativeBrowserPick, type NativeBrowserPickRequest, type NativeBrowserState, type NativeBrowserTarget } from "@/agent/nativeBrowser";

// The /embed/ mount serves this exact same app; its transport is the Yw
// parent bridge instead of pogo's own HTTP API. Re-exported so AgentView can
// branch on it without importing the bridge module directly.
// isDelegatedEmbed is true only for the exact /embed/ pathname, fixed at
// import time like `token` below: a page is either mounted by the parent or
// it is a normal pogo tab.
export { isDelegatedEmbed };

// The token arrives as a query parameter on the URL the server prints at
// startup. It is kept in sessionStorage so a reload without the query string
// still works, and mirrored back into the URL so the tab can be duplicated.
// When it is absent or rejected, boot stops at the token gate (TokenGate.vue),
// whose submit is the only other writer — via setToken below.
const TOKEN_KEY = "pogo-token";

function readToken(): string {
  // Guarded so importing this module outside a browser (a unit test) is harmless.
  if (typeof location === "undefined") return "";
  // Embedded mode never reads a token from the URL or storage — the parent's
  // postMessage handshake is the only authority there.
  if (isDelegatedEmbed) return "";
  const fromUrl = new URLSearchParams(location.search).get("token");
  if (fromUrl) {
    sessionStorage.setItem(TOKEN_KEY, fromUrl);
    return fromUrl;
  }
  return sessionStorage.getItem(TOKEN_KEY) ?? "";
}

export const token = readToken();

export function authHeaders(): HeadersInit {
  if (isDelegatedEmbed) return {};
  return token ? { Authorization: `Bearer ${token}` } : {};
}

function notifyAuthChange(authenticated: boolean) {
  if (typeof window !== "undefined") window.dispatchEvent(new CustomEvent("pogo:auth-changed", {detail: {authenticated}}));
}

// The token gate's way in: write the token everywhere readToken looks, then
// the caller reloads the page — `token` above is fixed at import time.
export function setToken(value: string) {
  // Guarded like readToken so unit tests can import this module.
  if (typeof location === "undefined") return;
  // And a no-op in embedded mode: no credential is ever written there.
  if (isDelegatedEmbed) return;
  sessionStorage.setItem(TOKEN_KEY, value);
  const url = new URL(location.href);
  url.searchParams.set("token", value);
  history.replaceState(null, "", url);
  notifyAuthChange(!!value.trim());
}

function authoredSkillPath(agentId: string, id: string, resource: "content" | "files" | "versions" | "export", path?: string, revision?: string) {
  if (!agentId || !id) throw new Error("Skill scope is required");
  const params = new URLSearchParams();
  if (path) params.set("path", path);
  if (revision) params.set("revision", revision);
  const query = params.toString();
  return `/api/apps/${encodeURIComponent(agentId)}/authored-skills/${encodeURIComponent(id)}/${resource}${query ? `?${query}` : ""}`;
}

async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal, finishResponse: (res: Response) => Promise<T> = finish<T>, headers: Record<string, string> = {}, keepalive = false): Promise<T> {
  // /embed/ reroutes every request() call through the parent bridge; no call
  // below this line — and no legacy /api fetch — happens in that mode.
  if (isDelegatedEmbed) return delegatedRequest<T>(method, path, body);
  const res = await fetch(path, {
    method,
    ...(keepalive ? { keepalive: true } : {}),
    signal,
    headers: {
      ...authHeaders(),
      "Accept-Language": getLocale(),
      ...headers,
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return finishResponse(res);
}

// The background-task module shares the authenticated/delegated transport;
// it cannot use this wrapper for arbitrary API paths or methods.
export function taskCenterRequest<T>(method: "GET" | "POST", path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const read = method === "GET" && /^\/api\/tasks(?:\?[^#]*)?$/.test(path);
  const control = method === "POST" && (/^\/api\/tasks\/(schedule|sentinel)\/[^/?#]+\/(pause|resume)$/.test(path) || /^\/api\/task-runs\/[^/?#]+\/(cancel|retry)$/.test(path));
  if (!read && !control) throw new Error("Invalid background-task request path");
  return request<T>(method, path, body, signal);
}

/** Internal workflows share authentication, while staying in the bound Agent API. */
export function agentWorkflowRequest<T>(method: "GET" | "POST" | "PUT", path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const read = method === "GET" && /^\/api\/apps\/[A-Za-z0-9_-]{1,160}\/agent-workflows$/.test(path);
  const action = method === "POST" && /^\/api\/apps\/[A-Za-z0-9_-]{1,160}\/agent-workflows\/(?:read|edit|enable|run|confirm)$/.test(path);
  const layout = (method === "GET" || method === "PUT") && /^\/api\/apps\/[A-Za-z0-9_-]{1,160}\/agent-workflows\/[A-Za-z0-9_-]{1,160}\/layout$/.test(path);
  if (!read && !action && !layout) throw new Error("Invalid Agent workflow request path");
  return request<T>(method, path, body, signal, finishOffice<T>);
}

// Office calls keep the workbench's authenticated/delegated transport. The
// editor service never receives the account credential through this helper.
export const OFFICE_REQUEST_TIMEOUT_MS = OFFICE_STARTUP_TIMEOUT_MS + 10_000;
export function officeRequest<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const libraryViewer = method === "POST" && /^\/api\/information\/entries\/inf_[a-f0-9]{32}\/office-view$/.test(path);
  if (!path.startsWith("/api/office/") && !path.startsWith("/office/bridge/") && !libraryViewer) throw new Error("Invalid Office request path");
  // GET bootstraps need the browser's scheme even when TLS ends at a proxy
  // and referrers are disabled. The server validates this authenticated hint
  // against Host, Origin and the existing document binding.
  const headers: Record<string, string> = typeof location === "undefined" ? {} : { "X-Pogo-Office-Origin": location.origin };
  if (signal?.aborted) return Promise.reject(signal.reason instanceof Error ? signal.reason : Object.assign(new Error("Office request was cancelled"), { name: "AbortError" }));
  const controller = new AbortController();
  const abort = () => controller.abort(signal?.reason instanceof Error ? signal.reason : Object.assign(new Error("Office request was cancelled"), { name: "AbortError" }));
  signal?.addEventListener("abort", abort, { once: true });
  const timer = setTimeout(() => controller.abort(Object.assign(new Error("Office request timed out after 5 minutes"), { name: "TimeoutError", code: "OFFICE_REQUEST_TIMEOUT", status: 504, timeoutMs: OFFICE_REQUEST_TIMEOUT_MS })), OFFICE_REQUEST_TIMEOUT_MS);
  // This wrapper never retries a request. In particular, a timed-out write has
  // an unknown outcome and must be reconciled using its original operation ID.
  // Keep the deadline active until finishOffice has consumed the full body.
  const task = isDelegatedEmbed ? delegatedRequest<T>(method, path, body, controller.signal) : request<T>(method, path, body, controller.signal, finishOffice<T>, headers);
  return waitForOfficeStartup(task, controller.signal).finally(() => {
    clearTimeout(timer);
    signal?.removeEventListener("abort", abort);
  });
}

// Office's bounded API returns {error:{code,message}}. Keep that envelope's
// identity for recovery decisions without changing other API error contracts.
async function finishOffice<T>(res: Response): Promise<T> {
  if (res.ok) return finish<T>(res);
  const text = await res.text();
  let data: Record<string, unknown> | undefined;
  try {
    const value: unknown = JSON.parse(text);
    if (value && typeof value === "object" && !Array.isArray(value)) data = value as Record<string, unknown>;
  } catch { /* Authentication middleware can return a plain-text error. */ }
  const nested = data?.error && typeof data.error === "object" && !Array.isArray(data.error) ? data.error as Record<string, unknown> : undefined;
  const message = typeof nested?.message === "string" && nested.message ? nested.message
    : typeof data?.error === "string" && data.error ? data.error
    : typeof data?.message === "string" && data.message ? data.message
    : !data && text ? text : `HTTP ${res.status}`;
  const code = typeof nested?.code === "string" ? nested.code : typeof data?.code === "string" ? data.code : undefined;
  throw Object.assign(hostAPIError(message), { status: res.status, code, details: data });
}

// finish is the shared tail of every request: 204 passes through, error bodies
// are parsed best-effort (the auth middleware's 401 is plain text), and a
// failure throws an Error carrying the status for the caller's 401 handling.
async function finish<T>(res: Response): Promise<T> {
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: { error?: string | { message?: string; code?: string }; code?: string; message?: string } | undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
  }
  if (!res.ok) {
    const nested = data?.error && typeof data.error === "object" ? data.error : undefined;
    const message = typeof data?.error === "string" ? data.error : nested?.message || data?.message || (text || `HTTP ${res.status}`);
    throw Object.assign(hostAPIError(message), { status: res.status, code: nested?.code || data?.code, details: data });
  }
  return data as T;
}

const browserTargetQuery = (target?: NativeBrowserTarget) => target ? `?${new URLSearchParams(target as unknown as Record<string, string>)}` : "";

export const api = {
  websiteSettings: () => request<WebsiteSettingsResponse>("GET", "/api/web-services/settings"),
  saveWebsiteSettings: (settings: WebsitePolicy) => request<WebsiteSettingsUpdate>("PUT", "/api/web-services/settings", settings),
  webServices: (sid: string, signal?: AbortSignal) => request<{ services: WebServiceRecord[]; quota?: WebsiteQuota; default_lease_seconds?: number }>("GET", `/api/sessions/${encodeURIComponent(sid)}/web-services`, undefined, signal),
  startWebService: (sid: string, spec: WebServiceSpec, operation_id: string, signal?: AbortSignal) => request<WebServiceRecord>("POST", `/api/sessions/${encodeURIComponent(sid)}/web-services`, { ...spec, operation_id }, signal),
  webService: (sid: string, id: string, signal?: AbortSignal) => request<WebServiceRecord>("GET", `/api/sessions/${encodeURIComponent(sid)}/web-services/${encodeURIComponent(id)}`, undefined, signal),
  deleteWebService: (sid: string, id: string, signal?: AbortSignal) => request<void>("DELETE", `/api/sessions/${encodeURIComponent(sid)}/web-services/${encodeURIComponent(id)}`, undefined, signal),
  webServiceLogs: (sid: string, id: string, cursor = "", signal?: AbortSignal) => request<WebServiceLog>("GET", `/api/sessions/${encodeURIComponent(sid)}/web-services/${encodeURIComponent(id)}/logs?${new URLSearchParams({ cursor, limit_bytes: "65536" })}`, undefined, signal),
  webServiceAction: (sid: string, id: string, action: "stop" | "restart" | "renew", operation_id: string, signal?: AbortSignal, lease_seconds?: number) => request<WebServiceRecord>("POST", `/api/sessions/${encodeURIComponent(sid)}/web-services/${encodeURIComponent(id)}/${action}`, { operation_id, ...(action === "renew" ? { lease_seconds } : {}) }, signal),
  renameWebService: (sid: string, id: string, display_name: string, operation_id: string, signal?: AbortSignal) => request<WebServiceRecord>("POST", `/api/sessions/${encodeURIComponent(sid)}/web-services/${encodeURIComponent(id)}/rename`, { display_name, operation_id }, signal),
  openWebService: (sid: string, id: string, operation_id: string) => request<WebServiceOpenResponse>("POST", `/api/sessions/${encodeURIComponent(sid)}/web-services/${encodeURIComponent(id)}/previews`, { operation_id }),
  revokeWebService: (sid: string, id: string, grantId: string) => request<unknown>("DELETE", `/api/sessions/${encodeURIComponent(sid)}/web-services/${encodeURIComponent(id)}/previews/${encodeURIComponent(grantId)}`, undefined, undefined, finish<unknown>, {}, true),
  migrateBrowserURLs: (urls: string[]) =>
    request<{ mappings: { url: string; resolved_url?: string; error?: string }[] }>("POST", "/api/browser/migrate-urls", { urls }),
  browserState: (sid: string, signal?: AbortSignal, target?: NativeBrowserTarget) =>
    request<BrowserBackendState>("GET", `/api/sessions/${encodeURIComponent(sid)}/browser${browserTargetQuery(target)}`, undefined, signal),
  browserAuthoringCreate: (operation_id: string) => request<{ session_id: string; expires_in: number }>("POST", "/api/browser/authoring-sessions", { operation_id }),
  browserAuthoringClose: (sid: string) => request<void>("DELETE", `/api/browser/authoring-sessions/${encodeURIComponent(sid)}`),
  browserIcons: (sid: string, signal?: AbortSignal) =>
    request<{ icons: { url: string; favicon: string }[] }>("GET", `/api/sessions/${encodeURIComponent(sid)}/browser/icons`, undefined, signal),
  browserLiveTicket: (sid: string, target: NativeBrowserTarget, signal?: AbortSignal) =>
    request<{ ticket: string; expires_in: number }>("POST", `/api/sessions/${encodeURIComponent(sid)}/browser/live-ticket`, { context_ref: target.context_ref, tab_id: target.tab_id }, signal),
  browserMediaRelayTicket: (sid: string, input: NativeBrowserTarget & { generation: number; media_epoch: string; live_ticket: string }, signal?: AbortSignal) =>
    request<unknown>("POST", `/api/sessions/${encodeURIComponent(sid)}/browser/media-relay-ticket`, { context_ref: input.context_ref, tab_id: input.tab_id, generation: input.generation, media_epoch: input.media_epoch, live_ticket: input.live_ticket }, signal),
  browserTabs: (sid: string, input: { action: "new" | "close"; op_id: string; context_ref: string; tab_id?: string; lease_token?: string; generation?: number; control_epoch?: number }) =>
    request<{ tab_id: string; context_ref: string; state: NativeBrowserState; lease_token?: string }>("POST", `/api/sessions/${encodeURIComponent(sid)}/browser/tabs`, input),
  browserControl: (sid: string, mode: "human" | "agent", options: NativeBrowserTarget & { lease_token?: string; force?: boolean; adopt_if_unowned?: true; generation?: number; control_epoch?: number }) =>
    request<NativeBrowserControl>("POST", `/api/sessions/${encodeURIComponent(sid)}/browser/control`, { mode, context_ref: options.context_ref, tab_id: options.tab_id, lease_token: options.lease_token, force: options.force,
      adopt_if_unowned: options.adopt_if_unowned, generation: options.generation, control_epoch: options.control_epoch }),
  browserInput: (sid: string, input: NativeBrowserInput, lease_token: string, target: NativeBrowserTarget & { generation: number }) =>
    request<NativeBrowserState>("POST", `/api/sessions/${encodeURIComponent(sid)}/browser/input`, { ...input, lease_token, context_ref: target.context_ref, target_tab: target.tab_id, generation: target.generation }),
  browserPickPreview: (sid: string, point: NativeBrowserPickRequest, signal?: AbortSignal) =>
    request<BrowserPickPreview>("POST", `/api/sessions/${encodeURIComponent(sid)}/browser/pick/preview`, { context_ref: point.context_ref, tab_id: point.tab_id, version: point.version, x: point.x, y: point.y }, signal),
  browserPick: (sid: string, point: NativeBrowserPickRequest | BrowserPickCommitRequest, signal?: AbortSignal) =>
    request<NativeBrowserPick>("POST", `/api/sessions/${encodeURIComponent(sid)}/browser/pick`, { context_ref: point.context_ref, tab_id: point.tab_id, version: point.version, x: point.x, y: point.y, ...("preview_token" in point ? { preview_token: point.preview_token } : {}) }, signal),
  browserFrame: async (sid: string, signal?: AbortSignal, target?: NativeBrowserTarget): Promise<{ blob: Blob; version: string; identity: string; context_ref: string; viewed_tab: string; overlay: NativeBrowserOverlay | null }> => {
    const res = await fetch(`/api/sessions/${encodeURIComponent(sid)}/browser/frame${browserTargetQuery(target)}`, {
      headers: authHeaders(), cache: "no-store", signal,
    });
    if (!res.ok) return finish<never>(res);
    if (!res.headers.get("Content-Type")?.startsWith("image/")) throw new Error("Browser frame did not return an image");
    const version = res.headers.get("X-Browser-Version") ?? "";
    return { blob: await res.blob(), version, identity: res.headers.get("X-Browser-Frame-Identity") ?? "", context_ref: res.headers.get("X-Browser-Context-Ref") ?? "", viewed_tab: res.headers.get("X-Browser-Viewed-Tab") ?? "", overlay: decodeNativeOverlay(res.headers.get("X-Browser-Overlay"), version) };
  },
  models: () => request<{ models: ModelInfo[]; default: string }>("GET", "/api/models"),

  memoryNotes: (sessionId: string) => request<MemoryCatalog>("GET", `/api/sessions/${encodeURIComponent(sessionId)}/memory`),
  saveMemoryNote: (sessionId: string, input: Pick<MemoryNote, "path" | "type" | "content">) =>
    request<MemoryNote>("PUT", `/api/sessions/${encodeURIComponent(sessionId)}/memory`, input),
  deleteMemoryNote: (sessionId: string, path: string) =>
    request<{ deleted: boolean }>("DELETE", `/api/sessions/${encodeURIComponent(sessionId)}/memory?path=${encodeURIComponent(path)}`),

  managedMemoryCatalog: () => request<ManagedMemoryCatalog>("GET", "/api/memory/catalog"),
  saveManagedMemoryNote: (scopeId: string, input: ManagedMemoryNoteInput) =>
    request<MemoryNote>("PUT", `/api/memory/notes?scope_id=${encodeURIComponent(scopeId)}`, input),
  deleteManagedMemoryNote: (scopeId: string, path: string, expectedModified?: string) =>
    request<{ deleted: boolean }>("DELETE", `/api/memory/notes?scope_id=${encodeURIComponent(scopeId)}&path=${encodeURIComponent(path)}${expectedModified !== undefined ? `&expected_modified=${encodeURIComponent(expectedModified)}` : ""}`),

  // The Studio workbench requests the pinned session selection; management
  // deliberately omits sessionId to inspect the whole installed library.
  skills: (sessionId?: string) => request<{ skills: SkillInfo[] }>(
    "GET", `/api/skills${sessionId ? `?session_id=${encodeURIComponent(sessionId)}` : ""}`,
  ),

  // Paths are package-relative; the server resolves them within the selected
  // Skill and checks session access again for every document.
  skillContent: (name: string, sessionId?: string, path?: string) =>
    request<SkillContent>("GET", `/api/skills/content?name=${encodeURIComponent(name)}${sessionId ? `&session_id=${encodeURIComponent(sessionId)}` : ""}${path ? `&path=${encodeURIComponent(path)}` : ""}`),

  // Installs a skill bundle from a zip. The server validates the structure
  // (SKILL.md at the root or one wrapper directory), installs under the user
  // skills directory and reloads, so the bundle is live without a restart.
  // XHR rather than fetch: the body may be gigabytes, and upload.onprogress
  // plus a real abort are the two things a long transfer owes its user.
  uploadSkill: (
    file: File,
    opts?: UploadOptions,
  ): Promise<SkillUploadResult> =>
    uploadFile<SkillUploadResult>("/api/skills/upload", file, { ...opts, headers: authHeaders() }),

  // Answers a confirm_replace upload: "replace" installs the parked bytes and
  // force-deletes the other versions of the same package, "keep-old" discards
  // them and leaves the installed versions untouched.
  resolveSkillUpload: (token: string, action: "replace" | "keep-old") =>
    request<SkillUploadResult>("POST", "/api/skills/upload/resolve", { token, action }),

  skillLibrary: () => request<SkillLibraryCatalog>("GET", "/api/manage/skills"),
  managedSkillContent: (id: string, path?: string) => request<SkillDocumentContent>("GET", `/api/manage/skills/${encodeURIComponent(id)}/content${path ? `?path=${encodeURIComponent(path)}` : ""}`),
  saveManagedSkillContent: (id: string, content: string, expectedRevision: string) => request<SkillDocumentContent>("PUT", `/api/manage/skills/${encodeURIComponent(id)}/content`, { content, expected_revision: expectedRevision }),
  managedSkillFiles: (id: string, path?: string) => request<SkillPackageFiles>("GET", `/api/manage/skills/${encodeURIComponent(id)}/files${path ? `?path=${encodeURIComponent(path)}` : ""}`),
  authoredSkills: (agentId: string) => request<{ skills: AuthoredSkillSummary[] }>("GET", `/api/apps/${encodeURIComponent(agentId)}/authored-skills`),
  authoredSkillContent: (agentId: string, id: string, path?: string, revision?: string) => request<SkillDocumentContent>("GET", authoredSkillPath(agentId, id, "content", path, revision)),
  saveAuthoredSkillContent: (agentId: string, id: string, content: string, expectedRevision: string) => request<SkillDocumentContent>("PUT", authoredSkillPath(agentId, id, "content"), { content, expected_revision: expectedRevision }),
  authoredSkillFiles: (agentId: string, id: string, path?: string, revision?: string) => request<SkillPackageFiles>("GET", authoredSkillPath(agentId, id, "files", path, revision)),
  authoredSkillVersions: (agentId: string, id: string) => request<{ versions: AuthoredSkillVersion[]; current_revision: string }>("GET", authoredSkillPath(agentId, id, "versions")),
  exportAuthoredSkill: (agentId: string, id: string, revision?: string) => request<Blob>("GET", authoredSkillPath(agentId, id, "export", undefined, revision), undefined, undefined, async res => {
    if (!res.ok) return finish<never>(res);
    if (!res.headers.get("Content-Type")?.toLowerCase().startsWith("application/zip")) throw new Error("Skill export did not return a ZIP");
    return res.blob();
  }),
  disableSkill: (id: string) => request<{ disabled: boolean }>("POST", `/api/manage/skills/${encodeURIComponent(id)}/disable`, {}),
  // force skips the reference check: agents/sessions that pinned the skill
  // keep their transcripts and simply lose it (dynamic unload).
  deleteSkill: (id: string, force = false) => request<{ deleted: boolean; cleanup_pending: boolean }>("DELETE", `/api/manage/skills/${encodeURIComponent(id)}${force ? "?force=1" : ""}`),

  // The manifest tool catalog: every declared tool, registered or not. Same
  // once-at-boot rule as skills — the set is fixed for the server's life.
  tools: (sessionId?: string, agentId?: string) => request<{ tools: ToolCatalogEntry[]; mcp: McpServerGroup[]; builtin?: BuiltinToolEntry[]; mcp_methods?: { server: string; name: string; wire_name: string; description: string }[] }>(
    "GET", `/api/tools${sessionId ? `?session_id=${encodeURIComponent(sessionId)}` : agentId ? `?agent_id=${encodeURIComponent(agentId)}` : ""}`,
  ),

  studioSessionCapabilities: (agentId: string, sessionId: string) =>
    request<StudioSessionCapabilities>("GET", `/api/apps/${encodeURIComponent(agentId)}/sessions/${encodeURIComponent(sessionId)}/capabilities`),
  syncStudioModelConfig: (agentId: string, sessionId: string) =>
    request<StudioModelConfigSync>("POST", `/api/apps/${encodeURIComponent(agentId)}/sessions/${encodeURIComponent(sessionId)}/model-config/sync`, {}),
  syncStudioToolConfig: (agentId: string, sessionId: string) =>
    request<StudioToolConfigSync>("POST", `/api/apps/${encodeURIComponent(agentId)}/sessions/${encodeURIComponent(sessionId)}/tools-config/sync`, {}),
  syncStudioSkillConfig: (agentId: string, sessionId: string) =>
    request<StudioSkillConfigSync>("POST", `/api/apps/${encodeURIComponent(agentId)}/sessions/${encodeURIComponent(sessionId)}/skill-config/sync`, {}),
  syncStudioMcpConfig: (agentId: string, sessionId: string) =>
    request<StudioMcpConfigSync>("POST", `/api/apps/${encodeURIComponent(agentId)}/sessions/${encodeURIComponent(sessionId)}/mcp-config/sync`, {}),

  // Read-only registry used to migrate saved browser entrypoints.
  panels: () => request<{ panels: PanelInfo[]; auto_register: boolean }>("GET", "/api/panels"),

  // Empty-state cards contributed by the loaded skills (or, once saved, the
  // user-edited override). The server re-reads the files each time, so a
  // reload is enough to pick up an edit.
  starters: () => request<{ starters: Starters }>("GET", "/api/starters"),

  // Saves the user-edited starter set. One PUT of the whole set: the editor
  // seeds itself with the effective cards, so nothing else is lost. Pass the
  // followups from the GET straight back — the editor only knows cards.
  saveStarters: (set: { heading?: string; send?: boolean; cards: unknown[]; followups?: unknown }) =>
    request<{ ok: boolean; cards: number; warnings?: string[] }>("PUT", "/api/starters", set),

  saveStarterAssets: (sid: string, input: { prompt?: string; parts: MessageInputPart[]; input_layout?: InputLayout }) =>
    request<{ prompt: string; parts: MessageInputPart[]; input_layout?: InputLayout; attachments: AttachmentMeta[] }>("POST", `/api/sessions/${encodeURIComponent(sid)}/starter-assets`, input),
  resolveStarterAssets: (sid: string, input: { prompt?: string; parts: MessageInputPart[]; input_layout?: InputLayout }) =>
    request<{ prompt: string; parts: MessageInputPart[]; input_layout?: InputLayout; attachments: AttachmentMeta[] }>("POST", `/api/sessions/${encodeURIComponent(sid)}/starter-assets/resolve`, input),

  // ── Login (password mode) ────────────────────────────────────────────────
  // Which gate the deployment uses: "token" (classic) or "password" (a
  // users.json exists server-side). Public by design — it answers "how do I
  // log in", nothing about who can.
  authStatus: () =>
    request<{
      mode: "token" | "password" | "delegated";
      password: boolean;
      user: string;
      admin: boolean;
    }>("GET", "/api/auth/status"),

  // Token-mode validation probe: /api/auth/status is deliberately public
  // (always 200), so a candidate access key is checked against an
  // authenticated route before it is stored. A wrong key must reach the
  // login form as an error, not as a silent reload back to the form.
  verifyAccessKey: async (key: string): Promise<void> => {
    const res = await fetch("/api/models", { headers: { Authorization: `Bearer ${key}` } });
    if (!res.ok) {
      throw Object.assign(new Error(`HTTP ${res.status}`), { status: res.status });
    }
  },

  // The accounts list behind the settings' account panel. Admin-only
  // server-side; hashes and token values never leave the server.
  accounts: () =>
    request<{ accounts: { username: string; created?: string; admin?: boolean; has_token: boolean }[] }>(
      "GET",
      "/api/accounts",
    ),

  // The account panel's delete action. Admin-gated server-side, and the
  // server refuses admin rows; deleting revokes the row's password, fixed
  // token and MCP credentials on the spot.
  deleteAccount: (username: string) =>
    request<{ ok: true }>("DELETE", `/api/accounts/${encodeURIComponent(username)}`),

  // Password login sets an HttpOnly session cookie; success is all the
  // caller needs (the cookie rides along on every later fetch, same-origin).
  login: async (username: string, password: string) => {
    const result = await request<{ ok: true }>("POST", "/api/login", { username, password });
    notifyAuthChange(true);
    return result;
  },

  logout: () => {
    notifyAuthChange(false);
    return request<{ ok: true }>("POST", "/api/logout");
  },

  // The settings pane's change-password action. "current" accepts the old
  // password or the web token (the recovery path for whoever holds the
  // operator key); the server re-verifies it, rate-limits guesses, and
  // rewrites users.json with a fresh hash.
  changePassword: (payload: { username?: string; current?: string; next: string }) =>
    request<{ ok: true }>("POST", "/api/auth/password", payload),

  sessions: () => request<{ sessions: SessionInfo[]; cwd: string }>("GET", "/api/sessions"),

  // Full-text search across every saved transcript — served by the session
  // index's FTS on the server, falling back to a scan when the index is off.
  // Best matches first, limit-capped.
  sessionSearch: (q: string, limit = 20) =>
    request<{ hits: SessionHit[]; q: string }>(
      "GET",
      `/api/sessions/search?q=${encodeURIComponent(q)}&limit=${limit}`,
    ),

  // The sidebar working indicators' cheap poll: just the ids with a run in
  // flight, from memory — unlike sessions() above, nothing is read off disk.
  sessionsActivity: () => request<{ busy: string[] }>("GET", "/api/sessions/activity"),

  // workspace is a directory relative to the server root; "" or omitted picks
  // the root itself.
  createSession: (model?: string, workspace?: string, approvalMode?: PolicyMode) =>
    request<{ session_id: string; path: string; model: string; provider: string; workspace: string }>(
      "POST",
      "/api/sessions",
      { model, workspace, ...(approvalMode ? { approval_mode: approvalMode } : {}) },
    ),

  session: (sid: string, signal?: AbortSignal) => request<{ session_id: string; snapshot: Snapshot; background_task?: BackgroundTaskSessionIdentity; agent_workflow?: { owner_id: string; agent_id: string; workflow_id: string; run_id: string; source_session_id?: string }; sentinel?: { watch_id: string; parent_session_id?: string; agent_id: string; title?: string } }>("GET", `/api/sessions/${encodeURIComponent(sid)}`, undefined, signal),

  shellTasks: (sid: string, signal?: AbortSignal) =>
    request<{ tasks: ShellJobRecord[] }>("GET", `/api/sessions/${encodeURIComponent(sid)}/shell-tasks`, undefined, signal),
  shellTaskOutput: (sid: string, taskId: string, offset = 0, limit = 65536, signal?: AbortSignal) =>
    request<ShellJobOutput>("GET", `/api/sessions/${encodeURIComponent(sid)}/shell-tasks/${encodeURIComponent(taskId)}/output?${new URLSearchParams({ offset: String(Math.max(0, Math.trunc(offset))), limit: String(Math.min(65536, Math.max(1, Math.trunc(limit)))) })}`, undefined, signal),
  stopShellTask: (sid: string, taskId: string, reason?: string) =>
    request<{ task: ShellJobRecord }>("POST", `/api/sessions/${encodeURIComponent(sid)}/shell-tasks/${encodeURIComponent(taskId)}/stop`, reason ? { reason } : {}),

  sentinelSessions: (sid: string, signal?: AbortSignal) =>
    request<SentinelSessionList>("GET", `/api/sessions/${encodeURIComponent(sid)}/sentinels`, undefined, signal),
  sentinelSession: (watchId: string, signal?: AbortSignal) =>
    request<SentinelSessionDetail>("GET", `/api/tasks/sentinel/${encodeURIComponent(watchId)}/session`, undefined, signal),

  // Shared answers. create snapshots one exchange — the question named by its
  // timeline id (the hub's u<N>, the same id a rewind targets) plus the
  // exchange's whole timeline projection — and returns the public path; view
  // is the one API path that needs no credentials (the server exempts GET
  // /api/share/*), which is what makes the link shareable.
  createShare: (sid: string, messageId: string) =>
    request<{ id: string; url: string; created_at: number }>(
      "POST",
      `/api/sessions/${sid}/share`,
      { message_id: messageId },
    ),
  shareView: (id: string) => request<ShareEntry>("GET", `/api/share/${encodeURIComponent(id)}`),

  // A fresh read off disk, not the in-memory hub: the only view that follows a
  // transcript another process is still writing, which is what a delegated
  // run's file is. The dock's subagent sheet polls this.
  transcript: (sid: string) =>
    request<{ session_id: string; snapshot: Snapshot }>("GET", `/api/sessions/${sid}/transcript`),
  retainedSubagentTranscript: (sid: string, agentId: string) =>
    request<{ session_id: string; snapshot: Snapshot }>("GET", `/api/sessions/${encodeURIComponent(sid)}/subagents/${encodeURIComponent(agentId)}/transcript`),

  deleteSession: (sid: string) => request<void>("DELETE", `/api/sessions/${sid}`),

  // Pin and rename are sidebar edits; either field may be sent on its own.
  updateSession: (sid: string, patch: { title?: string; pinned?: boolean }) =>
    request<void>("PATCH", `/api/sessions/${sid}`, patch),

  // The portable backup pair. Export is a download link (Content-Disposition
  // comes from the server); import takes the export's text and writes each
  // entry as a NEW session — the original file is never touched.
  importSessions: (text: string) =>
    request<SessionImportResult>("POST", "/api/sessions/import", JSON.parse(text)),

  // The Markdown reading copy's way back — the ⋮ menu only hands out
  // Markdown, so this is the import that matches what users actually have.
  // workspace is the folder the import was started from (root-relative,
  // "" = the root): the imported session groups under it, not under the
  // document's own 工作目录 row.
  importSessionMarkdown: (markdown: string, workspace?: string) =>
    request<SessionImportResult>("POST", "/api/sessions/import", { markdown, workspace }),

  // Returns 409 when a run is already in progress; the caller surfaces that as
  // "the agent is busy" rather than retrying. attachments are workspace paths
  // from uploadAttachment ("uploads/report.docx"); a turn may be attachment-
  // only (prompt empty) but not empty. clientId is the sender's optimistic-
  // bubble id, echoed on the user_message event so the placeholder can retire.
  send: (sid: string, prompt: string, attachments?: string[], clientId?: string, parts?: MessageInputPart[], inputLayout?: InputLayout) =>
    request<{ run_id: string }>("POST", `/api/sessions/${sid}/messages`, {
      prompt,
      ...(attachments && attachments.length ? { attachments } : {}),
      ...(clientId ? { client_id: clientId } : {}),
      ...(parts ? { parts, input_layout: inputLayout ?? "top" } : {}),
    }),

  // Session attachments: files land in the session's own workspace under
  // uploads/, where the read tools, the docs skill and the file tree all
  // operate. attachmentUrl points at the raw bytes through the files endpoint
  // (an <img> cannot set headers, hence the query token). dir is the
  // folder-upload half — the slash-separated subdir under uploads/ the file
  // keeps; omitted for a flat upload, sanitized server-side before use.
  uploadAttachment: async (
    sid: string,
    file: Blob,
    name: string,
    dir?: string,
    onProgress?: (loaded: number) => void,
  ): Promise<AttachmentMeta> => {
    const form = new FormData();
    form.append("file", file, name);
    if (dir) form.append("dir", dir);
    // fetch has no upload-progress channel (request streams measure the wrong
    // side), so a send that counts its bytes rides XHR; the plain call stays
    // fetch.
    if (onProgress) {
      return new Promise<AttachmentMeta>((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        xhr.open("POST", `/api/sessions/${sid}/attachments`);
        for (const [k, v] of Object.entries(authHeaders())) xhr.setRequestHeader(k, v);
        xhr.setRequestHeader("Accept-Language", getLocale());
        xhr.upload.onprogress = (e) => {
          if (e.lengthComputable) onProgress(e.loaded);
        };
        xhr.onload = () => {
          if (xhr.status >= 200 && xhr.status < 300) {
            resolve(JSON.parse(xhr.responseText) as AttachmentMeta);
            return;
          }
          let message = gt("apiErrors.uploadFailed", { status: xhr.status });
          try {
            message = (JSON.parse(xhr.responseText) as { error?: string }).error ?? message;
          } catch {
            /* plain-text errors keep the default */
          }
          reject(hostAPIError(message));
        };
        xhr.onerror = () => reject(new Error(gt("apiErrors.uploadNetwork")));
        xhr.send(form);
      });
    }
    const res = await fetch(`/api/sessions/${sid}/attachments`, {
      method: "POST",
      headers: { ...authHeaders(), "Accept-Language": getLocale() },
      body: form,
    });
    if (!res.ok) {
      let message = gt("apiErrors.uploadFailed", { status: res.status });
      try {
        message = (await res.json()).error ?? message;
      } catch {
        /* plain-text errors keep the default */
      }
      throw hostAPIError(message);
    }
    return res.json();
  },

  attachmentUrl: (workspaceRel: string) =>
    `/api/files/content?path=${encodeURIComponent(workspaceRel)}&raw=1&token=${encodeURIComponent(token)}`,

  // The session's uploads as the server attributes them (transcript blocks ∪
  // upload ledger), minus anything gone from disk. This is the session-files
  // popover's initial state; attachment_uploaded events keep it current after.
  sessionAttachments: (sid: string) =>
    request<{ attachments: AttachmentMeta[] }>("GET", `/api/sessions/${sid}/attachments`)
      .then((r) => r.attachments ?? []),

  // Cancelling goes through this endpoint rather than aborting the SSE
  // connection: dropping a socket would leave the bash child process running.
  cancel: (sid: string) => control<{ cancelled: boolean }>(sid, { action: "cancel" }),

  // Rewind forks the transcript away from a user message: the message and
  // everything after it leave the branch, and the caller then refills the
  // composer with the withdrawn text.
  //
  // mode says what to act on: "chat" forks only, "files" restores the workspace
  // to the checkpoint taken when the message was sent and leaves the conversation
  // alone, "both" does both. paths narrows a file restore to a subset of what the
  // preview listed; omit it for all of them.
  //
  // 409 while a run is in flight, 404 when the timeline id is unknown, 422 when
  // files were asked for but no checkpoint exists for that point.
  rewind: (sid: string, messageId: string, mode: "chat" | "files" | "both", paths?: string[]) =>
    control<{ rewound: boolean }>(sid, {
      action: "rewind",
      message_id: messageId,
      mode,
      ...(paths && paths.length ? { paths } : {}),
    }),

  // Replace model context with a summary while preserving the visible transcript.
  // Costs one model call; 409 means a run is in flight and 422 means compaction
  // would not make the prompt smaller.
  compact: (sid: string, instructions?: string) =>
    control<{
      compacted: boolean;
      before: number;
      before_tokens: number;
      after_tokens: number;
      freed_tokens: number;
      summary: string;
    }>(sid, {
      action: "compact",
      ...(instructions?.trim() ? { instructions: instructions.trim() } : {}),
    }),

  // What a rewind-with-files would restore: the modified/deleted/created
  // paths between the checkpoint and now, each with the line counts the
  // restore itself would add and remove (-1/-1 for binary). available is
  // false when the point predates checkpointing, and the dialog then offers
  // conversation-only.
  rewindPreview: (sid: string, messageId: string) =>
    control<{ available: boolean; changes: { path: string; status: string; added: number; removed: number }[] }>(sid, {
      action: "rewind_preview",
      message_id: messageId,
    }),

  // Steering joins the run in flight. It goes through /control rather than
  // /messages because it must never start one: `steered: false` means there was
  // nothing to join and the caller should send an ordinary message instead.
  steer: (sid: string, prompt: string) => control<{ steered: boolean }>(sid, { action: "steer", prompt }),

  inboxSend: (sid: string, id: string, prompt: string, mode: "queued" | "steer", expectedRunId?: string, parts?: MessageInputPart[], inputLayout?: InputLayout) =>
    control<{ accepted: boolean; item: InboxItem }>(sid, { action: "inbox_send", id, prompt, mode, ...(expectedRunId ? { expected_run_id: expectedRunId } : {}), ...(parts ? { parts, input_layout: inputLayout ?? "top" } : {}) }),
  inboxSteer: (sid: string, item: Pick<InboxItem, "id" | "revision">) =>
    control<{ accepted: boolean; item: InboxItem }>(sid, { action: "inbox_steer", ...item }),
  inboxEdit: (sid: string, item: Pick<InboxItem, "id" | "revision">, prompt: string, parts?: MessageInputPart[], inputLayout?: InputLayout) =>
    control<{ accepted: boolean; item: InboxItem }>(sid, { action: "inbox_edit", ...item, prompt, ...(parts ? { parts, input_layout: inputLayout ?? "top" } : {}) }),
  inboxWithdraw: (sid: string, item: Pick<InboxItem, "id" | "revision">) =>
    control<{ accepted: boolean; item: InboxItem }>(sid, { action: "inbox_withdraw", ...item }),

  setModel: (sid: string, model: string) =>
    control<{ model: string; provider: string }>(sid, { action: "set_model", model }),

  setPolicy: (sid: string, mode: PolicyMode, turns?: number) =>
    control<PolicyState>(sid, { action: "set_policy", mode, turns }),

  allowTool: (sid: string, tool: string) =>
    control<PolicyState>(sid, { action: "set_policy", allow_tool: tool }),

  allowCommand: (sid: string, command: string) =>
    control<PolicyState>(sid, { action: "set_policy", allow_command: command }),

  decideGate: (
    sid: string,
    gateId: string,
    allow: boolean,
    opts: { args?: unknown; reason?: string; remember?: "tool" | "command" } = {},
  ) => control<{ ok: true }>(sid, { action: "gate_decide", gate_id: gateId, allow, ...opts }),

  answerUserInput: (sid: string, requestId: string, revision: number, answers: Record<string, string[]>) =>
    control<{ accepted: true; duplicate: boolean }>(sid, { action: "user_input_answer", request_id: requestId, revision, answers }),

  skipUserInput: (sid: string, requestId: string, revision: number) =>
    control<{ accepted: true; duplicate: boolean }>(sid, { action: "user_input_cancel", request_id: requestId, revision }),

  // Freeze stops the server-side countdown while the user edits the arguments;
  // thaw resumes it from where it stopped and publishes the new deadline.
  freezeGate: (sid: string, gateId: string) => control<{ ok: true }>(sid, { action: "gate_freeze", gate_id: gateId }),

  thawGate: (sid: string, gateId: string) => control<{ ok: true }>(sid, { action: "gate_thaw", gate_id: gateId }),

  // The file panel's workspace API (web-ui-design §16). Paths are
  // workspace-relative; the server refuses escapes with the same check the
  // agent's own tools get.
  files: (path: string) => request<FilesResponse>("GET", `/api/files?path=${encodeURIComponent(path)}`),

  // The quick-open path index: whole workspace in one shot, fuzzy filtering
  // happens client-side.
  fileIndex: () => request<FileIndexResponse>("GET", "/api/files/index"),

  resolveSessionFileLink: (sid: string, path: string, lookupByName: boolean, signal?: AbortSignal) => {
    const query = new URLSearchParams({ path });
    if (lookupByName) query.set("basename", "1");
    return request<SessionFileLinkResolution>("GET", `/api/sessions/${encodeURIComponent(sid)}/files/resolve?${query}`, undefined, signal);
  },

  fileContent: (path: string) =>
    request<FileContent>("GET", `/api/files/content?path=${encodeURIComponent(path)}`),

  // docx/xlsx → markdown, for the preview tab's rendering of the kinds a
  // browser cannot display natively. 415 carries a specific message (old
  // binary formats, "not needed" for renderable kinds).
  fileRender: (path: string) =>
    request<FileRender>("GET", `/api/files/render?path=${encodeURIComponent(path)}`),

  createHTMLPreview: (sid: string, path: string) =>
    request<WorkspaceHTMLPreview>("POST", `/api/sessions/${encodeURIComponent(sid)}/html-previews`, { path }),

  revokeHTMLPreview: (sid: string, id: string) =>
    request<{ ok: boolean }>("DELETE", `/api/sessions/${encodeURIComponent(sid)}/html-previews/${encodeURIComponent(id)}`),

  // The user's own edit from the panel, not the agent's: optimistic mtime
  // concurrency, 409 when the file moved on disk since it was read.
  saveFile: (path: string, text: string, baseMtimeMs: number, force = false) =>
    request<{ path: string; size: number; mtime_ms: number }>("PUT", "/api/files/content", {
      path,
      text,
      base_mtime_ms: baseMtimeMs,
      force,
    }),

  // One level at a time; 409 when it exists, 404 when the parent does not.
  mkdir: (path: string) => request<{ path: string }>("POST", "/api/files/mkdir", { path }),

  renameFile: (path: string, name: string, mtimeMs: number, size: number) =>
    request<import("@/components/workspaceFileActions").WorkspaceFileMutation>("POST", "/api/files/rename", { path, name, mtime_ms: mtimeMs, size }),

  deleteFile: (path: string, recursive: boolean, mtimeMs: number, size: number) =>
    request<import("@/components/workspaceFileActions").WorkspaceFileMutation>("DELETE", "/api/files", { path, recursive, mtime_ms: mtimeMs, size }),

  // Workspace-level changes, journaled against first-touch pre-images (§16 M4).
  workspaceChanges: () => request<{ changes: WorkspaceChange[] }>("GET", "/api/workspace/changes"),

  workspaceDiff: (path: string) =>
    request<WorkspaceDiff>("GET", `/api/workspace/diff?path=${encodeURIComponent(path)}`),

  // Reset the baseline: diffs accumulate from this moment on.
  clearWorkspaceJournal: () => request<{ ok: true }>("POST", "/api/workspace/journal/clear"),

  // ── Model endpoint settings ──────────────────────────────────────────────
  // The dialog edits providers.json. The file never holds credentials — only
  // which variable each key lives in; the optional env values go to the
  // server's process memory and take effect on the next resolve.
  getConfig: () => request<ConfigInfo>("GET", "/api/config"),

  saveConfig: (body: { file?: unknown; raw?: string; env?: Record<string, string> }) =>
    request<ConfigSaveResult>("PUT", "/api/config", body),

  // Runs the curl preview for real, from the server (which sits on the
  // networks the gateways live on). The response carries what came back.
  probeConfig: (body: {
    name: string;
    base_url: string;
    key_env: string;
    key: string;
    model: string;
    headers: Record<string, string>;
    extra_body: Record<string, unknown>;
    stream: boolean;
  }) => request<ConfigProbeResult>("POST", "/api/config/probe", body),

  // Fixed service metadata endpoints only; never invokes generation or saves
  // settings. A cancelled dialog also cancels the server-side query.
  probeConfigMetadata: (body: ConfigMetadataProbeRequest, signal?: AbortSignal) =>
    request<ConfigProbeResult>("POST", "/api/config/probe-metadata", body, signal),

  // The vision variant: same server-side send, but the body is three
  // multiple-choice questions about embedded test images, graded on return.
  visionProbe: (body: {
    name: string;
    base_url: string;
    key_env: string;
    key: string;
    model: string;
    headers: Record<string, string>;
    extra_body: Record<string, unknown>;
  }) => request<VisionProbeResult>("POST", "/api/config/vision-probe", body),

  // The same probe's material for the curl preview: the asks and the inline
  // image data URLs, so the dialog can show the request it is about to send.
  visionProbeQuestions: () =>
    request<VisionProbePreview>("GET", "/api/config/vision-probe/questions"),

  // ── Storage settings ─────────────────────────────────────────────────────
  // The dialog's storage section: spill retention in prefs.json, the spill
  // directory's current cost, and a manual prune. Independent of the
  // providers.json editor above — separate API, separate save.
  getPrefs: () => request<PrefsInfo>("GET", "/api/prefs"),

  spillEntries: (path = "", offset = 0, signal?: AbortSignal) => {
    const params = new URLSearchParams({ path, offset: String(offset), limit: "100" });
    return request<SpillEntries>("GET", `/api/prefs/spill/entries?${params}`, undefined, signal);
  },

  spillFile: (path: string, offset = 0, signal?: AbortSignal) => {
    const params = new URLSearchParams({ path, offset: String(offset), limit: "32768" });
    return request<SpillFile>("GET", `/api/prefs/spill/file?${params}`, undefined, signal);
  },

  savePrefs: (prefs: Prefs) => request<PrefsInfo>("PUT", "/api/prefs", prefs),
  // A storage page may have been open before another tab changed memory or
  // other preferences. Carry a fresh server snapshot, never the page's cache.
  saveSpillRetentionDays: async (days: number): Promise<PrefsInfo> => {
    const latest = await request<PrefsInfo>("GET", "/api/prefs");
    return request<PrefsInfo>("PUT", "/api/prefs", { ...latest.prefs, spill_retention_days: days });
  },

  // The live turn cap: 0 explicitly disables it. Server-side this both
  // persists to prefs.json and pushes the new cap into already-open
  // conversations, so the answer applies without a reload.
  setMaxTurns: (n: number) => request<MaxTurnsInfo>("PUT", "/api/max-turns", { max_turns: n }),

  // The memory-distillation switch: persists to prefs.json and applies to the
  // next run of even an already-open conversation — sessions read the live
  // value at a run's end.
  setMemoryAuto: (on: boolean) => request<{ memory_auto: boolean }>("PUT", "/api/memory-auto", { memory_auto: on }),

  // The sandbox's runtime switch: current containment state, and the override
  // ("" clears it, so the env baseline decides again). A deliberate escape
  // hatch — every change is persisted and logged server-side.
  getSandbox: () => request<SandboxInfo>("GET", "/api/sandbox"),

  saveSandbox: (mode: string) => request<SandboxInfo>("PUT", "/api/sandbox", { mode }),
  getAttachmentPolicy: () => request<AttachmentPolicy>("GET", "/api/attachments/policy"),
  saveAttachmentPolicy: (exts: string[], maxMB: number) =>
    request<AttachmentPolicy>("PUT", "/api/attachments/policy", { exts, max_mb: maxMB }),

  // ── MCP client settings ──────────────────────────────────────────────────
  // The dialog's MCP sheet edits mcp.json — the external MCP servers this
  // agent connects to. PUT replaces the file wholesale; the server reconciles
  // the connection pool at once, but connections are asynchronous, so a fresh
  // read of the status endpoint is how the lights catch up after a save.
  getMCP: () => request<MCPInfo>("GET", "/api/mcp"),

  saveMCP: (config: MCPConfig) => request<MCPInfo>("PUT", "/api/mcp", config),

  mcpStatus: () => request<{ status: MCPServerStatus[] }>("GET", "/api/mcp/status"),

  // Trial-connects one entry without saving it — the editor's "test
  // connection" button. A connect failure is 200 with ok:false and the
  // reason: a probe is a diagnostic, not an accident.
  probeMCP: (server: MCPServerConfig) => request<MCPProbeResult>("POST", "/api/mcp/probe", server),

  // One prune pass under the saved retention. 400 when retention is 0
  // (disabled), 409 when the prefs file cannot be read — the server never
  // guesses a retention to delete with.
  pruneSpill: () => request<SpillPruneResult>("POST", "/api/prefs/prune-spill"),

  // ── Scheduled jobs ───────────────────────────────────────────────────────
  // The clock panel manages the same jobs.json the CLI's -schedule does.
  // 503 from any of these means the platform has no scheduler backend (the
  // panel shows the reason as a banner); POGO_SCHEDULE_BACKEND=noop is the
  // development answer.
  scheduleJobs: (filters?: { scope?: "agent" | "legacy" | "all"; agent_id?: string }) => {
    const params = new URLSearchParams();
    if (filters?.scope) params.set("scope", filters.scope);
    if (filters?.agent_id) params.set("agent_id", filters.agent_id);
    return request<ScheduleJobsInfo>("GET", `/api/schedule/jobs${params.size ? `?${params}` : ""}`);
  },
  schedulePreview: (body: Pick<ScheduleJobCreate, "kind" | "cron" | "every_sec" | "at" | "timezone">) => request<{ next_runs: string[]; timezone: string; current_time: string }>("POST", "/api/schedule/preview", body),
  scheduleJob: (id: string) => request<{ job: ScheduleJob }>("GET", `/api/schedule/jobs/${encodeURIComponent(id)}`),
  scheduleUpdateJob: (id: string, body: ScheduleJobUpdate) => request<{ job: ScheduleJob }>("PATCH", `/api/schedule/jobs/${encodeURIComponent(id)}`, body),

  // MCP call log (web/mcplog.go). Entries come newest-first from the ring;
  // stats aggregate the persistent history. Clear scope "screen" empties the
  // ring, "all" also truncates the history table.
  mcpLogs: (params: { limit?: number; verdict?: string; transport?: string; q?: string }) => {
    const p = new URLSearchParams();
    if (params.limit) p.set("limit", String(params.limit));
    if (params.verdict) p.set("verdict", params.verdict);
    if (params.transport) p.set("transport", params.transport);
    if (params.q) p.set("q", params.q);
    const qs = p.toString();
    return request<{ logs: MCPCallLog[] }>("GET", `/api/mcplog${qs ? `?${qs}` : ""}`);
  },

  mcpLogStats: () => request<MCPLogStats>("GET", "/api/mcplog/stats"),

  mcpCalls: (query: string) =>
    request<McpCallsPage>("GET", `/api/mcp/calls${query ? `?${query}` : ""}`),

  mcpCallStats: (query: string) =>
    request<McpCallStats>("GET", `/api/mcp/calls/stats${query ? `?${query}` : ""}`),

  mcpCall: (id: string) =>
    request<{ call: McpCall }>("GET", `/api/mcp/calls/${encodeURIComponent(id)}`),

  mcpAgentCalls: (agentId: string, query: string) =>
    request<McpCallsPage>("GET", `/api/apps/${encodeURIComponent(requireMonitorAgentId(agentId))}/mcp/calls${query ? `?${query}` : ""}`),

  mcpAgentCallStats: (agentId: string, query: string) =>
    request<McpCallStats>("GET", `/api/apps/${encodeURIComponent(requireMonitorAgentId(agentId))}/mcp/calls/stats${query ? `?${query}` : ""}`),

  mcpAgentCall: (agentId: string, id: string) =>
    request<{ call: McpCall }>("GET", `/api/apps/${encodeURIComponent(requireMonitorAgentId(agentId))}/mcp/calls/${encodeURIComponent(id)}`),

  skillEvents: (query: string) =>
    request<SkillEventsPage>("GET", `/api/skills/events${query ? `?${query}` : ""}`),

  skillEventStats: (query: string) =>
    request<SkillEventStats>("GET", `/api/skills/events/stats${query ? `?${query}` : ""}`),

  skillEvent: (id: string) =>
    request<{ event: SkillEvent }>("GET", `/api/skills/events/${encodeURIComponent(id)}`),

  // Collection status (admin): local recording + OTLP metadata exporter
  // counters. Never carries endpoints, headers or credentials.
  observabilityStatus: () => request<ObservabilityStatus>("GET", "/api/observability/status"),

  mcpLogClear: (scope: "screen" | "all") =>
    request<{ scope: string; history_removed: number }>("DELETE", `/api/mcplog?scope=${scope}`),

  scheduleCreateJob: (body: ScheduleJobCreate) =>
    request<{ job: ScheduleJob; backend: string }>("POST", "/api/schedule/jobs", body),

  scheduleSetEnabled: (id: string, enabled: boolean) =>
    request<{ job: ScheduleJob }>("POST", `/api/schedule/jobs/${encodeURIComponent(id)}/${enabled ? "enable" : "disable"}`),

  // Runs/logs survive the delete; warning names an orphaned OS trigger.
  scheduleDeleteJob: (id: string) =>
    request<{ removed: string; warning?: string }>("DELETE", `/api/schedule/jobs/${encodeURIComponent(id)}`),

  // 202: the run outlives the request. 409 when already in flight or disabled.
  scheduleRunNow: (id: string) =>
    request<{ id: string; started: boolean }>("POST", `/api/schedule/jobs/${encodeURIComponent(id)}/run-now`),

  scheduleRuns: (id: string) =>
    request<ScheduleRunsInfo>("GET", `/api/schedule/jobs/${encodeURIComponent(id)}/runs`),
};

// Download URLs for the token-gated attachment endpoints. An <a download> cannot
// set headers, so the token rides in the query the same way fileRawURL does for
// img tags; the server accepts it there for exactly this kind of client.
export function skillSampleURL(): string {
  return `/api/skills/sample?token=${encodeURIComponent(token)}`;
}

export function sessionSampleURL(): string {
  return `/api/sessions/sample?token=${encodeURIComponent(token)}`;
}

export function sessionExportURL(sid: string): string {
  return `/api/sessions/${encodeURIComponent(sid)}/export?token=${encodeURIComponent(token)}`;
}

// The Markdown reading copy of the same session: the whole live branch as a
// document rather than the backup JSON above.
export function sessionExportMarkdownURL(sid: string): string {
  return `/api/sessions/${encodeURIComponent(sid)}/export.md?token=${encodeURIComponent(token)}`;
}

// fileRawURL builds the <img> src for a workspace image. The token rides in
// the query because an img tag cannot set headers — the server accepts it
// there for exactly this kind of client, and the raw endpoint only serves
// sniffed images (and sandboxed PDFs) inline for the same origin-safety
// reason.
export function fileRawURL(path: string): string {
  return `/api/files/content?path=${encodeURIComponent(path)}&raw=1&token=${encodeURIComponent(token)}`;
}

// fileDownloadURL is the preview tab's download button: download=1 makes the
// server attach the response (Content-Disposition: attachment), which is what
// lets it hand out any binary — docx, xlsx, anything — without ever offering
// the origin to a rendered document.
export function fileDownloadURL(path: string): string {
  return `${fileRawURL(path)}&download=1`;
}

function control<T>(sid: string, body: Record<string, unknown>): Promise<T> {
  return request<T>("POST", `/api/sessions/${sid}/control`, body);
}

export function streamURL(sid: string, from?: number, epoch?: string): string {
  const params = new URLSearchParams();
  if (from !== undefined) params.set("from", String(from));
  if (epoch) params.set("epoch", epoch);
  const q = params.toString();
  return `/api/sessions/${sid}/stream${q ? `?${q}` : ""}`;
}
