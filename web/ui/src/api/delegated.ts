import type { AgentEvent, ModelInfo, SessionHit, SessionInfo, Snapshot } from "./types";
import { randomUUID } from "@/utils/uuid";

// ─────────────────────────────────────────────────────────────────────────────
// Delegated embed transport
//
// /embed/ serves the exact same Vue app as the normal pogo UI. This module is
// the only place that page talks to the outside: the Yw parent bridge over
// postMessage. No token is read from the URL or storage, no Authorization
// header is ever attached, and no legacy /api path is fetched. Every inbound
// message must come from window.parent, from an origin listed by
// GET /embed/config, on the channel UUID adopted at handshake; every outbound
// message goes to that exact origin — never "*".
// ─────────────────────────────────────────────────────────────────────────────

export const POGO_MSG_INIT = "yw.pogo.init";
export const POGO_MSG_READY = "yw.pogo.ready";
export const POGO_MSG_REQUEST = "yw.pogo.request";
export const POGO_MSG_RESPONSE = "yw.pogo.response";

const MAX_ID_LENGTH = 64;
const HANDSHAKE_TIMEOUT_MS = 15_000;
const RPC_TIMEOUT_MS = 30_000;
const MAX_PENDING_RPC = 6;
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const SID_RE = /^[A-Za-z0-9_-]{1,96}$/;

/** True only on the exact /embed/ path — /embed/foo or /embed is a normal page. */
export const isDelegatedEmbed = typeof location !== "undefined" && location.pathname === "/embed/";

export class DelegatedError extends Error {
  readonly status: number;

  constructor(message: string, status = 502) {
    super(message);
    this.name = "DelegatedError";
    this.status = status;
  }
}

function reject(status: number, message: string): never {
  throw new DelegatedError(message, status);
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isUuidChannel(value: unknown): value is string {
  return typeof value === "string" && value.length <= MAX_ID_LENGTH && UUID_RE.test(value);
}

function hostWindow(): Window & typeof globalThis {
  if (typeof window === "undefined") {
    reject(500, "嵌入通道只能在浏览器中使用");
  }
  return window;
}

function newRequestId(): string {
  let uuid: string;
  try {
    uuid = randomUUID();
  } catch {
    reject(500, "无法生成安全请求标识");
  }
  if (!isUuidChannel(uuid)) {
    reject(500, "无法生成安全请求标识");
  }
  return uuid;
}

function abortError(): Error {
  const err = new Error("请求已取消");
  err.name = "AbortError";
  return err;
}

// ── Wire metadata ────────────────────────────────────────────────────────────

export interface DelegatedCapabilities {
  conversation: boolean;
  events: boolean;
  mcp: boolean;
  tools: boolean;
  attachments: boolean;
  terminal: boolean;
  schedules: boolean;
  sharing: boolean;
  workspaces: boolean;
  settings: boolean;
  model_selection: boolean;
}

export interface DelegatedUi {
  profile: unknown;
  capabilities: DelegatedCapabilities;
  models: { models: ModelInfo[]; default: string };
  project: string;
}

/** One catalog row, exactly as pogo/catalog serializes it (Unix ms times). */
export interface DelegatedCatalogSession {
  id: string;
  title: string;
  state: string;
  created_at: number;
  updated_at: number;
}

const CAPABILITY_KEYS = [
  "conversation",
  "events",
  "mcp",
  "tools",
  "attachments",
  "terminal",
  "schedules",
  "sharing",
  "workspaces",
  "settings",
  "model_selection",
] as const;

function parseUi(data: unknown): DelegatedUi {
  if (!isPlainObject(data)) reject(502, "父站点返回的界面元数据无效");
  const { profile, capabilities, models, project } = data;
  if (!isPlainObject(capabilities)) reject(502, "父站点返回的能力清单无效");
  for (const key of CAPABILITY_KEYS) {
    if (typeof capabilities[key] !== "boolean") {
      reject(502, `父站点返回的能力 ${key} 无效`);
    }
  }
  if (!isPlainObject(models) || !Array.isArray(models.models) || typeof models.default !== "string" || !models.default) {
    reject(502, "父站点返回的模型清单无效");
  }
  for (const m of models.models) {
    if (!isPlainObject(m) || typeof m.id !== "string") reject(502, "父站点返回的模型条目无效");
  }
  if (typeof project !== "string" || !project) reject(502, "父站点返回的项目标识无效");
  return {
    profile,
    capabilities: capabilities as unknown as DelegatedCapabilities,
    models: models as unknown as DelegatedUi["models"],
    project,
  };
}

function parseCatalogSession(value: unknown): DelegatedCatalogSession {
  if (!isPlainObject(value)) reject(502, "父站点返回的会话无效");
  const { id, title, state, created_at, updated_at } = value;
  if (typeof id !== "string" || !SID_RE.test(id)) reject(502, "父站点返回的会话标识无效");
  if (typeof title !== "string") reject(502, "父站点返回的会话标题无效");
  if (typeof state !== "string" || !state) reject(502, "父站点返回的会话状态无效");
  if (typeof created_at !== "number" || !Number.isFinite(created_at) || created_at < 0) {
    reject(502, "父站点返回的会话时间无效");
  }
  if (typeof updated_at !== "number" || !Number.isFinite(updated_at) || updated_at < 0) {
    reject(502, "父站点返回的会话时间无效");
  }
  return { id, title, state, created_at, updated_at };
}

function toSessionInfo(cs: DelegatedCatalogSession, project: string): SessionInfo {
  // path stays empty: the catalog row has no transcript path and a real one
  // would leak the host's directory layout. project is a display label, not a
  // filesystem path, so it is safe as cwd.
  return {
    path: "",
    id: cs.id,
    title: cs.title,
    messages: 0,
    created: cs.created_at,
    updated: cs.updated_at,
    cwd: project,
  };
}

// ── Bridge state ─────────────────────────────────────────────────────────────

interface Handshake {
  source: Window;
  origin: string;
  channel: string;
}

interface PendingRpc {
  timer: ReturnType<typeof setTimeout> | null;
  resolve: (value: unknown) => void;
  reject: (error: unknown) => void;
}

type RpcAction = "ui" | "list" | "create" | "get" | "message" | "cancel" | "rename" | "delete" | "updates";

const RPC_FIELDS: Record<RpcAction, readonly string[]> = {
  ui: [],
  list: [],
  create: ["title"],
  get: ["sid"],
  message: ["sid", "text", "clientId"],
  cancel: ["sid"],
  rename: ["sid", "title"],
  delete: ["sid"],
  updates: ["sid", "from"],
};

const RPC_REQUIRED: Record<RpcAction, readonly string[]> = {
  ui: [],
  list: [],
  create: [],
  get: ["sid"],
  message: ["sid", "text"],
  cancel: ["sid"],
  rename: ["sid", "title"],
  delete: ["sid"],
  updates: ["sid"],
};

let allowedOrigins: string[] | null = null;
let handshakePromise: Promise<Handshake> | null = null;
let ready: Handshake | null = null;
let initResolved: ((hs: Handshake) => void) | null = null;
let initFailed: ((error: unknown) => void) | null = null;
let handshakeTimer: ReturnType<typeof setTimeout> | null = null;
let messageListener: ((event: MessageEvent) => void) | null = null;
let pagehideListener: (() => void) | null = null;
let disposed = false;
const pending = new Map<string, PendingRpc>();
let uiCache: DelegatedUi | null = null;

/** GET /embed/config is the one direct request this page ever makes. */
async function loadParentOrigins(): Promise<string[]> {
  let res: Response;
  try {
    res = await fetch("/embed/config", { headers: {}, credentials: "same-origin" });
  } catch {
    return reject(502, "无法获取嵌入配置");
  }
  if (!res.ok) return reject(502, `嵌入配置不可用（${res.status}）`);
  let data: unknown;
  try {
    data = await res.json();
  } catch {
    return reject(502, "嵌入配置格式无效");
  }
  const origins = isPlainObject(data) ? data.parent_origins : undefined;
  if (!Array.isArray(origins) || origins.length === 0) return reject(502, "嵌入配置缺少父站点");
  for (const origin of origins) {
    if (typeof origin !== "string" || !origin) return reject(502, "嵌入配置的父站点无效");
    let parsed: URL;
    try {
      parsed = new URL(origin);
    } catch {
      return reject(502, "嵌入配置的父站点无效");
    }
    if (
      (parsed.protocol !== "https:" && parsed.protocol !== "http:") ||
      parsed.origin !== origin ||
      parsed.pathname !== "/" ||
      parsed.search ||
      parsed.hash
    ) {
      return reject(502, "嵌入配置的父站点必须是精确 origin");
    }
  }
  return origins;
}

function isApprovedOrigin(origin: unknown): origin is string {
  return typeof origin === "string" && allowedOrigins !== null && allowedOrigins.includes(origin);
}

function clearHandshakeTimer(): void {
  if (handshakeTimer !== null) {
    clearTimeout(handshakeTimer);
    handshakeTimer = null;
  }
}

function detachBridgeListeners(): void {
  const win = typeof window === "undefined" ? null : window;
  if (win && messageListener) win.removeEventListener("message", messageListener);
  if (win && pagehideListener) win.removeEventListener("pagehide", pagehideListener);
  messageListener = null;
  pagehideListener = null;
}

function sendReady(hs: Handshake): void {
  if (disposed) return;
  // Exact target origin, never "*": the handshake proves this source/origin
  // pair, and postMessage's second argument is what enforces it on the wire.
  hs.source.postMessage({ type: POGO_MSG_READY, channel: hs.channel }, hs.origin);
}

function toRpcFailure(data: Record<string, unknown>): DelegatedError {
  const status = typeof data.status === "number" && Number.isInteger(data.status) && data.status >= 400 && data.status <= 599
    ? data.status
    : 502;
  const message = typeof data.error === "string" && data.error ? data.error : "父站点请求失败";
  return new DelegatedError(message, status);
}

function handleEvent(event: MessageEvent): void {
  if (disposed) return;
  const win = hostWindow();
  if (event.source !== win.parent) return;
  if (!isApprovedOrigin(event.origin)) return;
  const data = event.data;
  if (!isPlainObject(data)) return;
  const { type, channel } = data;
  if (typeof channel !== "string" || !isUuidChannel(channel)) return;

  if (type === POGO_MSG_INIT) {
    if (ready) {
      // Same channel init repeats (the parent retries while its own ready
      // listener was not yet up): answer again. A different channel belongs
      // to another user's frame — only a page reload can adopt a new one.
      if (ready.channel === channel) sendReady(ready);
      return;
    }
    const hs: Handshake = { source: event.source as Window, origin: event.origin, channel };
    ready = hs;
    sendReady(hs);
    clearHandshakeTimer();
    const resolve = initResolved;
    initResolved = null;
    initFailed = null;
    resolve?.(hs);
    return;
  }

  if (type !== POGO_MSG_RESPONSE || !ready) return;
  if (event.source !== ready.source || event.origin !== ready.origin || channel !== ready.channel) return;
  const id = data.id;
  if (!isUuidChannel(id)) return;
  const entry = pending.get(id);
  if (!entry) return; // duplicate or late: already settled, drop silently
  pending.delete(id);
  if (entry.timer !== null) clearTimeout(entry.timer);
  if (data.ok === true) entry.resolve(data.data);
  else entry.reject(toRpcFailure(data));
}

function performHandshake(): Promise<Handshake> {
  return new Promise<Handshake>((resolve, rejectPromise) => {
    if (disposed) {
      rejectPromise(new DelegatedError("嵌入通道已关闭", 503));
      return;
    }
    const win = hostWindow();
    if (win.parent === win) {
      rejectPromise(new DelegatedError("当前页面不在 iframe 中", 503));
      return;
    }
    initResolved = (hs) => resolve(hs);
    initFailed = (error) => {
      detachBridgeListeners();
      clearHandshakeTimer();
      rejectPromise(error);
    };
    messageListener = (event) => handleEvent(event);
    pagehideListener = () => disposeDelegatedBridge();
    win.addEventListener("message", messageListener);
    win.addEventListener("pagehide", pagehideListener);
    handshakeTimer = setTimeout(() => {
      initFailed?.(new DelegatedError("等待父页面握手超时", 504));
    }, HANDSHAKE_TIMEOUT_MS);
  });
}

async function ensureReady(): Promise<Handshake> {
  if (disposed) reject(503, "嵌入通道已关闭");
  if (ready) return ready;
  if (allowedOrigins === null) allowedOrigins = await loadParentOrigins();
  if (!handshakePromise) {
    handshakePromise = performHandshake();
    // A failed handshake (no parent, timeout) cleans up so a later call —
    // the user pressing retry — can try again.
    handshakePromise.catch(() => {
      handshakePromise = null;
    });
  }
  return handshakePromise;
}

function validateRpcParams(action: RpcAction, params: Record<string, unknown>): void {
  const fields = RPC_FIELDS[action];
  for (const [key, value] of Object.entries(params)) {
    if (!fields.includes(key)) reject(400, `嵌入请求不支持字段 ${key}`);
    if (key === "from") {
      if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) {
        reject(400, "事件序号必须是非负整数");
      }
      continue;
    }
    if (typeof value !== "string" || !value.length) reject(400, `嵌入请求字段 ${key} 无效`);
    if (key === "sid" && !SID_RE.test(value)) reject(400, "会话标识无效");
    if (key === "text" && value.length > 65_536) reject(400, "消息内容过长");
    if (key === "title" && value.length > 400) reject(400, "会话标题过长");
    if (key === "clientId" && value.length > 128) reject(400, "客户端标识过长");
  }
  for (const key of RPC_REQUIRED[action]) {
    if (!(key in params)) reject(400, `嵌入请求缺少字段 ${key}`);
  }
}

function rpc<T>(action: RpcAction, params: Record<string, unknown>, signal?: AbortSignal): Promise<T> {
  return ensureReady().then(
    (hs) =>
      new Promise<T>((resolve, rejectPromise) => {
        if (disposed) {
          rejectPromise(new DelegatedError("嵌入通道已关闭", 503));
          return;
        }
        if (signal?.aborted) {
          rejectPromise(abortError());
          return;
        }
        if (pending.size >= MAX_PENDING_RPC) {
          rejectPromise(new DelegatedError("请求过多，请稍后再试", 429));
          return;
        }
        validateRpcParams(action, params);
        const id = newRequestId();
        const settle = (fn: () => void) => {
          pending.delete(id);
          if (entry.timer !== null) clearTimeout(entry.timer);
          signal?.removeEventListener("abort", onAbort);
          fn();
        };
        const entry: PendingRpc = {
          timer: null,
          resolve: (value: unknown) => settle(() => resolve(value as T)),
          reject: (error) => settle(() => rejectPromise(error)),
        };
        const onAbort = () => entry.reject(abortError());
        signal?.addEventListener("abort", onAbort, { once: true });
        entry.timer = setTimeout(() => {
          entry.reject(new DelegatedError("请求超时，请稍后重试", 504));
        }, RPC_TIMEOUT_MS);
        pending.set(id, entry);
        try {
          hs.source.postMessage(
            { type: POGO_MSG_REQUEST, channel: hs.channel, id, action, params },
            hs.origin,
          );
        } catch {
          entry.reject(new DelegatedError("无法向父页面发送请求", 502));
        }
      }),
  ) as Promise<T>;
}

/** Tear the bridge down (pagehide does this automatically). Late responses,
 *  duplicate ids, and in-flight requests are all discarded, not delivered. */
export function disposeDelegatedBridge(): void {
  if (disposed) return;
  disposed = true;
  ready = null;
  initResolved = null;
  const fail = initFailed;
  initFailed = null;
  clearHandshakeTimer();
  detachBridgeListeners();
  for (const entry of pending.values()) {
    if (entry.timer !== null) clearTimeout(entry.timer);
    entry.reject(new DelegatedError("嵌入通道已关闭", 503));
  }
  pending.clear();
  handshakePromise = null;
  fail?.(new DelegatedError("嵌入通道已关闭", 503));
}

/** The real /v1/ui metadata, fetched once over the bridge and cached. Only
 *  this non-secret metadata is cached — never session content or results. */
export async function getDelegatedUi(signal?: AbortSignal): Promise<DelegatedUi> {
  if (!uiCache) uiCache = parseUi(await rpc<unknown>("ui", {}, signal));
  return uiCache;
}

/** The parent-authorized long poll behind useAgentStream's delegated loop. */
export async function delegatedUpdates(
  sid: string,
  from?: number,
  signal?: AbortSignal,
): Promise<{ events: AgentEvent[] }> {
  const params: Record<string, unknown> = { sid };
  if (from !== undefined) params.from = from;
  const data = await rpc<{ events?: unknown }>("updates", params, signal);
  if (!isPlainObject(data) || !Array.isArray(data.events)) {
    reject(502, "父站点返回的事件批次无效");
  }
  return { events: data.events as AgentEvent[] };
}

// ── Legacy path mapping ──────────────────────────────────────────────────────

function splitLegacyPath(path: string): { pathname: string; query: URLSearchParams } | null {
  if (typeof path !== "string" || !path.startsWith("/") || path.includes("://")) return null;
  const q = path.indexOf("?");
  const pathname = q === -1 ? path : path.slice(0, q);
  if (!/^\/[A-Za-z0-9._/-]*$/.test(pathname) || pathname.includes("//")) return null;
  return { pathname, query: new URLSearchParams(q === -1 ? "" : path.slice(q + 1)) };
}

function requireObject(body: unknown, what: string): Record<string, unknown> {
  if (!isPlainObject(body)) reject(400, `${what}请求体无效`);
  return body;
}

function requireString(value: unknown, what: string, max: number): string {
  if (typeof value !== "string" || !value.trim() || value.length > max) reject(400, `${what}无效`);
  return value;
}

function sessionPathSid(pathname: string): string | null {
  const m = /^\/api\/sessions\/([A-Za-z0-9_-]{1,96})$/.exec(pathname);
  return m ? m[1] : null;
}

function sessionSubPath(pathname: string, suffix: string): string | null {
  const re = new RegExp(`^/api/sessions/([A-Za-z0-9_-]{1,96})/${suffix}$`);
  const m = re.exec(pathname);
  return m ? m[1] : null;
}

/**
 * Maps the original UI's request() calls onto the parent RPC. Anything not in
 * the table — skills, panels, files, terminal, config, sharing, the SSE
 * stream, ... — is a local 403: the legacy /api is never fetched from /embed/.
 */
export async function delegatedRequest<T>(
  method: string,
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const split = splitLegacyPath(path);
  if (!split) reject(403, `嵌入模式不支持请求 ${path}`);
  const { pathname, query } = split;

  if (method === "GET" && pathname === "/api/auth/status") {
    await ensureReady(); // no fabricated token/password: the parent must be live
    return { mode: "delegated" } as T;
  }

  if (method === "GET" && pathname === "/api/models") {
    const ui = await getDelegatedUi(signal);
    return { models: ui.models.models, default: ui.models.default } as T;
  }

  if (method === "GET" && pathname === "/api/sessions") {
    const [ui, list] = await Promise.all([
      getDelegatedUi(signal),
      rpc<{ items?: unknown }>("list", {}, signal),
    ]);
    if (!isPlainObject(list) || !Array.isArray(list.items)) reject(502, "父站点返回的会话清单无效");
    const sessions = list.items.map((item) => toSessionInfo(parseCatalogSession(item), ui.project));
    return { sessions, cwd: ui.project } as T;
  }

  if (method === "GET" && pathname === "/api/sessions/search") {
    const q = query.get("q") ?? "";
    const limitRaw = query.get("limit");
    let limit = 20;
    if (limitRaw !== null) {
      const n = Number(limitRaw);
      if (!Number.isSafeInteger(n) || n < 1 || n > 200) reject(400, "搜索数量限制无效");
      limit = n;
    }
    const [ui, list] = await Promise.all([
      getDelegatedUi(signal),
      rpc<{ items?: unknown }>("list", {}, signal),
    ]);
    if (!isPlainObject(list) || !Array.isArray(list.items)) reject(502, "父站点返回的会话清单无效");
    const needle = q.trim().toLowerCase();
    const hits: SessionHit[] = list.items
      .map((item) => parseCatalogSession(item))
      .filter((cs) => !needle || cs.title.toLowerCase().includes(needle))
      .slice(0, limit)
      .map((cs) => ({ ...toSessionInfo(cs, ui.project), score: 0 }));
    return { hits, q } as T;
  }

  if (method === "POST" && pathname === "/api/sessions") {
    const input = body === undefined ? {} : requireObject(body, "创建会话");
    for (const key of Object.keys(input)) {
      if (key !== "model" && key !== "workspace" && key !== "title") {
        reject(403, `嵌入模式不支持会话选项 ${key}`);
      }
    }
    if (input.model !== undefined && input.model !== null) reject(403, "嵌入模式不能指定模型");
    if (input.workspace !== undefined && input.workspace !== null) reject(403, "嵌入模式不能指定工作区");
    const params: Record<string, unknown> = {};
    if (input.title !== undefined) params.title = requireString(input.title, "会话标题", 400);
    const [ui, created] = await Promise.all([
      getDelegatedUi(signal),
      rpc<unknown>("create", params, signal),
    ]);
    const cs = parseCatalogSession(created);
    const model = ui.models.models.find((m) => m.id === ui.models.default);
    return {
      session_id: cs.id,
      path: "",
      model: ui.models.default,
      provider: model?.provider ?? "",
      workspace: "",
    } as T;
  }

  const sid = sessionPathSid(pathname);
  if (sid !== null) {
    if (method === "GET") {
      const data = await rpc<{ session?: unknown; snapshot?: unknown }>("get", { sid }, signal);
      if (!isPlainObject(data)) reject(502, "父站点返回的会话详情无效");
      const cs = parseCatalogSession(data.session);
      if (cs.id !== sid) reject(502, "父站点返回了错误的会话");
      if (!isPlainObject(data.snapshot)) reject(502, "父站点返回的快照无效");
      return { session_id: cs.id, snapshot: data.snapshot as unknown as Snapshot } as T;
    }
    if (method === "PATCH") {
      const patch = requireObject(body, "更新会话");
      for (const key of Object.keys(patch)) {
        if (key !== "title" && key !== "pinned") reject(403, `嵌入模式不支持会话字段 ${key}`);
      }
      if (patch.pinned !== undefined) reject(403, "嵌入模式不支持置顶会话");
      const title = requireString(patch.title, "会话标题", 400);
      await rpc("rename", { sid, title }, signal);
      return undefined as T;
    }
    if (method === "DELETE") {
      await rpc("delete", { sid }, signal);
      return undefined as T;
    }
  }

  const messageSid = sessionSubPath(pathname, "messages");
  if (messageSid !== null && method === "POST") {
    const input = requireObject(body, "发送消息");
    for (const key of Object.keys(input)) {
      if (key !== "prompt" && key !== "attachments" && key !== "client_id") {
        reject(403, `嵌入模式不支持消息字段 ${key}`);
      }
    }
    const attachments = input.attachments;
    if (attachments !== undefined && (!Array.isArray(attachments) || attachments.length > 0)) {
      reject(403, "嵌入模式未开放附件");
    }
    const text = requireString(input.prompt, "消息内容", 65_536);
    const params: Record<string, unknown> = { sid: messageSid, text };
    if (input.client_id !== undefined) {
      const clientId = requireString(input.client_id, "客户端标识", 128);
      params.clientId = clientId;
    }
    const data = await rpc<{ session_id?: unknown; run_id?: unknown }>("message", params, signal);
    if (
      !isPlainObject(data) ||
      typeof data.session_id !== "string" ||
      data.session_id !== messageSid ||
      typeof data.run_id !== "string"
    ) {
      reject(502, "父站点返回的消息回执无效");
    }
    return { run_id: data.run_id } as T;
  }

  const controlSid = sessionSubPath(pathname, "control");
  if (controlSid !== null && method === "POST") {
    const input = requireObject(body, "会话控制");
    for (const key of Object.keys(input)) {
      if (key !== "action") reject(403, `嵌入模式不支持控制字段 ${key}`);
    }
    if (input.action !== "cancel") reject(403, `嵌入模式不支持控制操作 ${String(input.action)}`);
    const data = await rpc<{ cancelled?: unknown }>("cancel", { sid: controlSid }, signal);
    return { cancelled: isPlainObject(data) && data.cancelled === true } as T;
  }

  reject(403, `嵌入模式不支持请求 ${method} ${path}`);
}
