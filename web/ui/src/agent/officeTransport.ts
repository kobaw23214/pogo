import { officeRequest } from "@/api/client";
import { sameWorkspaceFile, workspaceFilePath } from "./officeFiles";
import { OFFICE_STARTUP_TIMEOUT_MS, officeStartupTimeout, waitForOfficeStartup } from "./officeStartup";

export interface OfficeCapabilities {
  read?: boolean;
  sheetList?: boolean;
  selectionRead?: boolean;
  nativeUndo?: boolean;
  nativeRedo?: boolean;
  historyScope?: string;
  blocked?: boolean;
}
export interface OfficeActivityRecord {
  sequence: number;
  operationId: string;
  operation: string;
  actor: "agent" | "user";
  phase: "reading" | "writing" | "verifying" | "saving" | "history";
  status: "queued" | "running" | "succeeded" | "rejected" | "unknown";
  target: { kind: "word" | "cell"; sheet?: string; range?: string };
  queuedAt: string;
  startedAt?: string;
  finishedAt?: string;
  changes?: { address?: string; before: string; after: string }[];
  changeCount?: number;
  truncated?: boolean;
  verified?: boolean;
  code?: string;
  message?: string;
  revision?: number;
}
export interface OfficeActivitySnapshot { version: number; items: OfficeActivityRecord[]; }
export interface OfficeBatchState {
  id: string;
  purpose: "batch" | "reload";
  phase: "freeze_requested" | "frozen" | "prepared";
  discardChanges?: boolean;
  confirmedDirtyEpoch?: number;
  confirmedCanvasVersion?: string;
  expiresAt?: string;
}
export interface OfficeRecovery {
  state: "checking" | "ready";
  operationIds: string[];
  exportIds?: string[];
  revision: number;
  needsRead?: boolean;
  readTarget?: { operation: "word.read" | "sheet.read" | "sheet.list"; args: Record<string, unknown> };
}
export interface OfficeSettlement {
  operationId: string;
  operation: string;
  ok: boolean;
  result?: unknown;
  error?: { code?: string; message?: string };
}
export interface OfficeRecoveryCopy {
  id: string;
  state: "pending" | "ready" | "failed" | "unknown";
  title: string;
  fileType: "docx" | "xlsx";
  createdAt: string;
  sourceSessionId: string;
  generation: number;
  dirtyEpoch: number;
  persistedEpoch: number;
  baseVersion: string;
  hash?: string;
  bytes?: number;
  error?: string;
  downloadURL?: string;
  source?: "final" | "forcesave" | "forgotten";
  coverage?: "engine-snapshot";
}
export interface OfficeAccess {
  ok: boolean;
  fileRef: string;
  path: string;
  diskVersion: string;
  writer: OfficeSession | null;
  access: { state: "available" | "online" | "disconnected" | "unconfirmed"; canTakeover: boolean; canRecover: boolean; canReadForgotten?: boolean; canReset?: boolean; resetRequiresOnlineConfirmation?: boolean; lastSeenAt?: string; leaseExpiresAt?: string; leaseEpoch: number };
  recoveryCopies: OfficeRecoveryCopy[];
}
export interface OfficeSession {
  id: string;
  documentId: string;
  conversationId: string;
  path: string;
  fileRef?: string;
  bootEpoch?: string;
  title: string;
  fileType: "docx" | "xlsx";
  editor: "word" | "cell";
  generation: number;
  ready: boolean;
  instanceBound?: boolean;
  canvasMounted?: boolean;
  closed: boolean;
  dirty: boolean;
  dirtyEpoch: number;
  persistedEpoch: number;
  saveState: "idle" | "dirty" | "saving" | "saved" | "error";
  revision: number;
  savedAt?: string | null;
  blocked: boolean;
  recovery?: OfficeRecovery;
  pending?: boolean;
  queueDepth?: number;
  savePhase?: "" | "queued" | "exporting" | "committing";
  error?: string | null;
  capabilities?: OfficeCapabilities | null;
  supportedOperations?: string[];
  activity?: OfficeActivitySnapshot;
  canvasVersion?: string;
  diskVersion?: string;
  externalChange?: boolean;
  reloadAvailable?: boolean;
  versionError?: string;
  syncRevision?: number;
  batch?: OfficeBatchState | null;
  replacedBy?: string;
  replacementPending?: boolean;
  reloadConfirmed?: boolean;
  leaseEpoch?: number;
  lastSeenAt?: string;
  leaseExpiresAt?: string;
  connectionState?: "unconfirmed" | "online" | "disconnected" | "closed";
  recoveryCopy?: OfficeRecoveryCopy;
  recoverySealed?: boolean;
}
export interface OfficeConfiguration {
  enabled: boolean;
  configured?: boolean;
  state?: "disabled" | "config_error" | "unavailable" | "ready";
  ready?: boolean;
  reason?: string;
  browserURL?: string;
}
export interface OfficeOpen {
  session: OfficeSession;
  config: Record<string, unknown>;
  browserURL: string;
  bridgeURL: string;
  bridgeToken: string;
  instanceOwned: boolean;
  requestedPath?: string;
  bootstrapToken?: string;
}
export interface OfficeCommand { operationId: string; operation: string; args: Record<string, unknown>; actor?: "agent" | "user"; }
export interface OfficeResult { ok: boolean; result?: unknown; error?: { code?: string; message?: string }; operationId?: string; }
export interface OfficeBridgeMessage {
  protocol: "pogo-office-lab/v1";
  type: "ready" | "response" | "reconciled" | "bootstrap-error";
  sessionId: string;
  documentId: string;
  token: string;
  operationId?: string;
  ok?: boolean;
  result?: unknown;
  error?: { code?: string; message?: string };
  recoverable?: boolean;
  capabilities?: OfficeCapabilities;
  barrierId?: string;
  recoveryRevision?: number;
  settled?: boolean;
  operations?: OfficeSettlement[];
}

const sessionPath = (id: string) => `/api/office/sessions/${encodeURIComponent(id)}`;
export const officeApi = {
  configuration: (signal?: AbortSignal) => officeRequest<OfficeConfiguration>("GET", "/api/office/config", undefined, signal),
  list: (conversationId: string, signal?: AbortSignal, path?: string) => officeRequest<{ sessions: OfficeSession[]; requestedPath?: string; fileRef?: string }>("GET", `/api/office/sessions?conversation_id=${encodeURIComponent(conversationId)}${path === undefined ? "" : `&path=${encodeURIComponent(path)}`}`, undefined, signal),
  open: (path: string, conversationId: string, instanceId: string, signal?: AbortSignal) => officeRequest<OfficeOpen>("POST", "/api/office/sessions", { path, conversation_id: conversationId, instanceId }, signal),
  config: (id: string, instanceId: string, signal?: AbortSignal, path?: string) => officeRequest<OfficeOpen>("GET", `${sessionPath(id)}/config?instanceId=${encodeURIComponent(instanceId)}${path === undefined ? "" : `&path=${encodeURIComponent(path)}`}`, undefined, signal),
  release: (opened: OfficeOpen, instanceId: string, signal?: AbortSignal) => officeRequest<{ ok: boolean }>("POST", `${sessionPath(opened.session.id)}/release`, { instanceId, bootstrapToken: opened.bootstrapToken, fileRef: opened.session.fileRef, bootEpoch: opened.session.bootEpoch }, signal),
  status: (id: string, signal?: AbortSignal) => officeRequest<{ session: OfficeSession }>("GET", `${sessionPath(id)}/status`, undefined, signal),
  access: (path: string, conversationId: string, signal?: AbortSignal) => officeRequest<OfficeAccess>("GET", `/api/office/access?conversation_id=${encodeURIComponent(conversationId)}&path=${encodeURIComponent(path)}`, undefined, signal),
  heartbeat: (id: string, instanceId: string, leaseEpoch: number, signal?: AbortSignal) => officeRequest<{ ok: boolean; session: OfficeSession }>("POST", `${sessionPath(id)}/heartbeat`, { instanceId, leaseEpoch }, signal),
  recover: (session: OfficeSession, expectedVersion: string, signal?: AbortSignal, readForgotten = false) => officeRequest<{ ok: boolean; state: OfficeRecoveryCopy["state"]; recoveryCopy: OfficeRecoveryCopy; session: OfficeSession }>("POST", `${sessionPath(session.id)}/recover`, { expectedGeneration: session.generation, expectedDirtyEpoch: session.dirtyEpoch, expectedLeaseEpoch: session.leaseEpoch, expectedVersion, ...(readForgotten ? { readForgotten: true } : {}) }, signal),
  takeover: (path: string, conversationId: string, instanceId: string, access: OfficeAccess, signal?: AbortSignal) => officeRequest<OfficeOpen>("POST", "/api/office/takeover", { conversationId, path, instanceId, expectedSessionId: access.writer?.id, expectedGeneration: access.writer?.generation, expectedDirtyEpoch: access.writer?.dirtyEpoch, expectedVersion: access.diskVersion, expectedLeaseEpoch: access.writer?.leaseEpoch, ...(access.writer?.recoveryCopy?.state === "ready" ? { recoveryId: access.writer.recoveryCopy.id } : {}) }, signal),
  reset: (path: string, conversationId: string, instanceId: string, access: OfficeAccess, allowInterruptOnline: boolean, signal?: AbortSignal) => officeRequest<OfficeOpen>("POST", "/api/office/reset", { conversationId, path, instanceId, expectedSessionId: access.writer?.id, expectedGeneration: access.writer?.generation, expectedDirtyEpoch: access.writer?.dirtyEpoch, expectedPersistedEpoch: access.writer?.persistedEpoch, expectedLeaseEpoch: access.writer?.leaseEpoch, expectedVersion: access.diskVersion, expectedCanvasVersion: access.writer?.canvasVersion, confirmDiscardChanges: true, allowInterruptOnline }, signal),
  savedCopy: (path: string, conversationId: string, expectedVersion: string, operationId: string) => officeRequest<{ ok: boolean; path: string; title: string; diskVersion: string }>("POST", "/api/office/saved-copy", { conversationId, path, expectedVersion, operationId }),
  command: (id: string, command: OfficeCommand, signal?: AbortSignal) => officeRequest<OfficeResult>("POST", `${sessionPath(id)}/commands`, command, signal),
  save: (id: string, operationId: string, signal?: AbortSignal) => officeRequest<{ session?: OfficeSession; status?: OfficeSession; ok?: boolean }>("POST", `${sessionPath(id)}/save`, { operationId }, signal),
  close: (id: string, signal?: AbortSignal) => officeRequest<{ ok: boolean }>("POST", `${sessionPath(id)}/close`, {}, signal),
  reload: (id: string, operationId: string, expectedVersion: string, signal?: AbortSignal, discard?: { discardChanges: true; confirmedDirtyEpoch: number; confirmedCanvasVersion: string; instanceId: string }) => officeRequest<{ ok: boolean; session: OfficeSession }>("POST", `${sessionPath(id)}/reload`, { operationId, expectedVersion, ...discard }, signal),
  abortBatch: (id: string, conversationId: string, operationId: string, signal?: AbortSignal) => officeRequest<{ ok: boolean; state: string; batch_id: string }>("POST", `/api/office/batches/${encodeURIComponent(id)}/abort`, { conversationId, operationId }, signal),
};

type OfficeBridgeAction = "mounted" | "ready" | "next" | "results" | "state" | "export" | "detach" | "batch-freeze" | "reload-ready" | "reconcile";
export function officeBridgeBase(base: string): string {
  const url = new URL(base, location.origin);
  if (!/^https?:$/.test(url.protocol) || url.origin !== location.origin || url.username || url.password || url.search || url.hash || !/^\/(?:api\/office|office)\/bridge\/[A-Za-z0-9_-]+\/[A-Za-z0-9_-]+\/?$/.test(url.pathname)) throw new Error("Office bridge origin is not authorized");
  // A capability names a path in the current Pogo Web entry point. Keep it
  // relative so local forwarding and a public server address are interchangeable.
  return `${url.pathname.replace(/\/$/, "")}/`;
}
export function officeBridgeURL(base: string, action: OfficeBridgeAction, instanceId?: string, activityVersion?: number, syncRevision?: number, inFlightId?: string): string {
  const url = new URL(officeBridgeBase(base), location.origin);
  url.pathname = `${url.pathname.replace(/\/$/, "")}/${action}`;
  if (instanceId) url.searchParams.set("instanceId", instanceId);
  if (action === "next" && activityVersion !== undefined) {
    if (!Number.isSafeInteger(activityVersion) || activityVersion < 0) throw new Error("Office activity version is invalid");
    url.searchParams.set("activityVersion", String(activityVersion));
  }
  if (action === "next" && syncRevision !== undefined) {
    if (!Number.isSafeInteger(syncRevision) || syncRevision < 0) throw new Error("Office sync revision is invalid");
    url.searchParams.set("syncRevision", String(syncRevision));
  }
  if (action === "next" && inFlightId) url.searchParams.set("inFlightId", inFlightId);
  return `${url.pathname}${url.search}`;
}
export function detachOfficeView(opened: OfficeOpen, instanceId: string): boolean {
  if (typeof navigator === "undefined" || typeof navigator.sendBeacon !== "function") return false;
  return navigator.sendBeacon(officeBridgeURL(opened.bridgeURL, "detach"), new Blob([JSON.stringify({ instanceId })], { type: "text/plain;charset=UTF-8" }));
}

export function officeBridge<T>(opened: OfficeOpen, action: Exclude<OfficeBridgeAction, "detach">, instanceId: string, body?: Record<string, unknown>, signal?: AbortSignal, activityVersion?: number, syncRevision?: number, inFlightId?: string): Promise<T> {
  return officeRequest<T>(body === undefined ? "GET" : "POST", officeBridgeURL(opened.bridgeURL, action, body === undefined ? instanceId : undefined, activityVersion, syncRevision, inFlightId), body === undefined ? undefined : { ...body, instanceId }, signal);
}

const views = new Map<string, Pick<OfficeSession, "id" | "path" | "conversationId" | "fileRef"> & { requestedPath: string }>();
export const officeViewMounted = (id: string): boolean => views.has(id);
export const officePathMounted = (path: string, conversationId: string): boolean => [...views.values()].some(view => (sameWorkspaceFile(view.path, path) || sameWorkspaceFile(view.requestedPath, path)) && view.conversationId === conversationId);
export function registerOfficeView(session: OfficeSession, requestedPath = session.path): () => void {
  if (views.has(session.id) || (session.fileRef && [...views.values()].some(view => view.fileRef === session.fileRef))) throw new Error("Office document is already open in this workbench");
  const record = { id: session.id, path: workspaceFilePath(session.path), requestedPath: workspaceFilePath(requestedPath), conversationId: session.conversationId, fileRef: session.fileRef };
  views.set(session.id, record);
  return () => { if (views.get(session.id) === record) views.delete(session.id); };
}
export interface OfficeReveal { id: string; conversationId: string; path: string; fileType: "docx" | "xlsx"; }
const revealListeners = new Set<(request: OfficeReveal) => void>();
export function observeOfficeOpen(listener: (request: OfficeReveal) => void): () => void {
  revealListeners.add(listener); return () => { revealListeners.delete(listener); };
}
export function publishOfficeOpen(request: OfficeReveal): void {
  for (const listener of [...revealListeners]) listener(request);
}

// Push is only an invalidation hint. The mounted, scoped editor fetches the
// authoritative session state before it freezes input or replaces a canvas.
const syncListeners = new Set<(request: OfficeReveal) => void>();
export function observeOfficeSync(listener: (request: OfficeReveal) => void): () => void {
  syncListeners.add(listener); return () => { syncListeners.delete(listener); };
}
export function publishOfficeSync(request: OfficeReveal): void {
  for (const listener of [...syncListeners]) listener(request);
}

interface OfficeDocEditor {
  destroyEditor(): void;
  downloadAs(format: string): void;
}
interface OfficeDocsAPI { DocEditor: new (id: string, config: Record<string, unknown>) => OfficeDocEditor; }
declare global { interface Window { DocsAPI?: OfficeDocsAPI; } }
export type { OfficeDocEditor };
const editorGatewayPath = "/office/editor";
export function officeEditorBase(browserURL: string): string {
  const engine = new URL(browserURL, location.origin);
  if (!/^https?:$/.test(engine.protocol) || engine.origin !== location.origin || engine.username || engine.password || engine.search || engine.hash || engine.pathname.replace(/\/$/, "") !== editorGatewayPath) throw new Error("Office editor must use the Pogo Web gateway");
  return `${location.origin}${editorGatewayPath}`;
}
let loadedOrigin = "";
let loadedAPI: OfficeDocsAPI | null = null;
let loading: Promise<OfficeDocsAPI> | null = null;
let engineAttempt = 0;
function isOfficeDocsAPI(value: OfficeDocsAPI | undefined): value is OfficeDocsAPI { return !!value && typeof value.DocEditor === "function"; }
export async function loadOfficeEngine(browserURL: string, signal?: AbortSignal): Promise<OfficeDocsAPI> {
  // The authenticated open/config call grants a scoped HttpOnly gateway
  // cookie before this loader runs. Editor resources never receive the
  // account credential or connect to a tool container directly.
  const base = officeEditorBase(browserURL);
  if (signal?.aborted) throw Object.assign(new Error("Office startup was cancelled"), { name: "AbortError", code: "OFFICE_STARTUP_CANCELLED" });
  if (loadedOrigin && loadedOrigin !== base) throw new Error("Office editor service identity changed; reload the workbench");
  // Keep the captured successful API even if an older, removed script executes
  // late and changes the global. Its stale onload cannot settle a newer attempt.
  if (loadedAPI && loadedOrigin === base) return loadedAPI;
  if (loading) {
    const api = await waitForOfficeStartup(loading, signal);
    if (loadedOrigin !== base) throw new Error("A different Office editor service is already loaded");
    return api;
  }
  if (window.DocsAPI && !loadedOrigin) throw new Error("Office editor service identity changed; reload the workbench");
  // A timed-out script may finish at the same authorized gateway afterwards.
  // The origin binding survives failure so this is a valid retry, not a service
  // identity change. An API that predates any authorized load is still refused.
  if (isOfficeDocsAPI(window.DocsAPI) && loadedOrigin === base) { loadedAPI = window.DocsAPI; return loadedAPI; }
  loadedOrigin = base;
  const attempt = ++engineAttempt;
  const pending = new Promise<OfficeDocsAPI>((resolve, reject) => {
    const script = document.createElement("script");
    let settled = false;
    const fail = (error: Error) => {
      if (settled) return;
      settled = true; clearTimeout(timer); script.onload = null; script.onerror = null; script.remove();
      reject(error);
    };
    const timer = setTimeout(() => fail(officeStartupTimeout("engine")), OFFICE_STARTUP_TIMEOUT_MS);
    script.src = `${base}/web-apps/apps/api/documents/api.js`;
    script.onload = () => {
      if (settled || engineAttempt !== attempt) return;
      if (!isOfficeDocsAPI(window.DocsAPI)) { fail(Object.assign(new Error("Office editor API is unavailable"), { code: "OFFICE_ENGINE_API_UNAVAILABLE", phase: "engine" })); return; }
      settled = true; clearTimeout(timer); script.onload = null; script.onerror = null;
      loadedAPI = window.DocsAPI; resolve(loadedAPI);
    };
    script.onerror = () => fail(Object.assign(new Error("Office editor service could not be loaded"), { code: "OFFICE_ENGINE_LOAD_FAILED", phase: "engine", status: 502 }));
    try { document.head.append(script); }
    catch (error) { fail(Object.assign(new Error(error instanceof Error ? error.message : "Office editor service could not be loaded"), { code: "OFFICE_ENGINE_LOAD_FAILED", phase: "engine", status: 502 })); }
  });
  // One document leaving cancels only its waiter. Other documents may still be
  // using this exact script, and a retry must not start a parallel SDK download.
  const shared = pending.finally(() => { if (loading === shared) loading = null; });
  loading = shared;
  return waitForOfficeStartup(shared, signal);
}
