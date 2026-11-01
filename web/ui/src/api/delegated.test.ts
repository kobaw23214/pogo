import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Every test loads a fresh copy of client.ts/delegated.ts after installing the
// location/sessionStorage spies, because both modules read location once at
// import time. The fake window has a distinct `parent` object; messages are
// delivered straight into the bridge's listeners.

const PARENT_ORIGIN = "https://parent.example";
const CHANNEL = "123e4567-e89b-42d3-a456-426614174000";
const OTHER_CHANNEL = "123e4567-e89b-42d3-a456-426614174999";

const MODEL = { id: "model-a", provider: "provider-x", context_window: 1000, configured: true };
const UI_DATA = {
  profile: { subject: "alice" },
  capabilities: {
    conversation: true,
    events: true,
    mcp: true,
    tools: true,
    attachments: false,
    terminal: false,
    schedules: false,
    sharing: false,
    workspaces: false,
    settings: false,
    model_selection: false,
  },
  models: { models: [MODEL], default: "model-a" },
  project: "展示项目",
};
const CS = { id: "sess-1", title: "旧会话", state: "active", created_at: 1000, updated_at: 2000 };
const SNAP = { seq: 5, messages: [], results: {}, live: { active: false }, run: {}, usage: { input: 0, output: 0 } };

type Listener = (event: unknown) => void;

interface Boot {
  client: typeof import("./client");
  delegated: typeof import("./delegated");
  parent: { postMessage: ReturnType<typeof vi.fn> };
  location: { pathname: string; search: string; href: string };
  sessionStorage: { getItem: ReturnType<typeof vi.fn>; setItem: ReturnType<typeof vi.fn> };
  history: { replaceState: ReturnType<typeof vi.fn> };
  fetchMock: ReturnType<typeof vi.fn>;
  messageListeners: Set<Listener>;
  pagehideListeners: Set<() => void>;
  sendFromParent: (data: unknown, opts?: { source?: unknown; origin?: string }) => void;
  firePagehide: () => void;
  rpcRequests: () => { id: string; action: string; params: Record<string, unknown>; channel: string }[];
  lastRpc: () => { id: string; action: string; params: Record<string, unknown> };
  respondTo: (action: string, data: unknown) => void;
  respondLast: (data: unknown) => void;
  failLast: (status: number, error: string) => void;
}

async function flush(rounds = 30): Promise<void> {
  for (let i = 0; i < rounds; i++) await Promise.resolve();
}

async function boot(opts: { pathname?: string; search?: string; configOrigins?: string[] } = {}): Promise<Boot> {
  vi.resetModules();
  const messageListeners = new Set<Listener>();
  const pagehideListeners = new Set<() => void>();
  const parent = { postMessage: vi.fn() };
  const win = {
    parent,
    addEventListener: (type: string, fn: Listener) => {
      (type === "message" ? messageListeners : pagehideListeners).add(fn as () => void);
    },
    removeEventListener: (type: string, fn: Listener) => {
      (type === "message" ? messageListeners : pagehideListeners).delete(fn as () => void);
    },
  };
  const pathname = opts.pathname ?? "/embed/";
  const search = opts.search ?? "";
  const location = {
    pathname,
    search,
    href: `https://child.example${pathname}${search}`,
  };
  const sessionStorage = { getItem: vi.fn(() => null), setItem: vi.fn(), removeItem: vi.fn(), clear: vi.fn() };
  const history = { replaceState: vi.fn() };
  const fetchMock = vi.fn(async (input: unknown) => {
    const url = String(input);
    if (url === "/embed/config") {
      return { ok: true, status: 200, json: async () => ({ parent_origins: opts.configOrigins ?? [PARENT_ORIGIN] }) };
    }
    return { ok: false, status: 403, text: async () => `unexpected fetch ${url}`, json: async () => ({ error: "unexpected fetch" }) };
  });
  vi.stubGlobal("window", win);
  vi.stubGlobal("location", location);
  vi.stubGlobal("sessionStorage", sessionStorage);
  vi.stubGlobal("history", history);
  vi.stubGlobal("fetch", fetchMock);
  let counter = 0;
  vi.spyOn(globalThis.crypto, "randomUUID").mockImplementation(() =>
    `00000000-0000-4000-8000-${String(++counter).padStart(12, "0")}` as ReturnType<Crypto["randomUUID"]>);

  const client = await import("./client");
  const delegated = await import("./delegated");

  const sendFromParent = (data: unknown, evOpts: { source?: unknown; origin?: string } = {}) => {
    const event = { source: evOpts.source === undefined ? parent : evOpts.source, origin: evOpts.origin ?? PARENT_ORIGIN, data };
    for (const fn of [...messageListeners]) fn(event);
  };
  const rpcRequests = () =>
    parent.postMessage.mock.calls
      .map((call) => call[0] as Record<string, unknown>)
      .filter((payload) => payload?.type === "yw.pogo.request")
      .map((payload) => payload as unknown as { id: string; action: string; params: Record<string, unknown>; channel: string });
  const lastRpc = () => { const requests = rpcRequests(); return requests[requests.length - 1]!; };
  const respondId = (id: string, result: Record<string, unknown>) =>
    sendFromParent({ type: "yw.pogo.response", channel: CHANNEL, id, ...result });
  const respondTo = (action: string, data: unknown) => {
    const found = [...rpcRequests()].reverse().find((r) => r.action === action);
    if (!found) throw new Error(`no rpc request for ${action}`);
    respondId(found.id, { ok: true, data });
  };
  return {
    client,
    delegated,
    parent,
    location,
    sessionStorage,
    history,
    fetchMock,
    messageListeners,
    pagehideListeners,
    sendFromParent,
    firePagehide: () => {
      for (const fn of [...pagehideListeners]) fn();
    },
    rpcRequests,
    lastRpc,
    respondTo,
    respondLast: (data: unknown) => respondId(lastRpc().id, { ok: true, data }),
    failLast: (status: number, error: string) => respondId(lastRpc().id, { ok: false, status, error }),
  };
}

/** Boot plus a completed parent handshake; authStatus resolving proves it. */
async function bootReady(opts: Parameters<typeof boot>[0] = {}): Promise<Boot> {
  const h = await boot(opts);
  const status = h.client.api.authStatus();
  await flush();
  h.sendFromParent({ type: "yw.pogo.init", channel: CHANNEL });
  await expect(status).resolves.toEqual({ mode: "delegated" });
  expect(h.parent.postMessage).toHaveBeenCalledWith({ type: "yw.pogo.ready", channel: CHANNEL }, PARENT_ORIGIN);
  return h;
}

describe("delegated embed transport", () => {
  beforeEach(() => {
    vi.useRealTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    vi.resetModules();
  });

  it("reads and writes no credentials in /embed/", async () => {
    const h = await boot({ search: "?token=leaked" });
    expect(h.client.isDelegatedEmbed).toBe(true);
    expect(h.client.token).toBe("");
    expect(h.sessionStorage.getItem).not.toHaveBeenCalled();
    expect(h.sessionStorage.setItem).not.toHaveBeenCalled();
    expect(h.location.search).toBe("?token=leaked"); // URL was not rewritten
    expect(h.client.authHeaders()).toEqual({});
    h.client.setToken("must-not-persist");
    expect(h.sessionStorage.setItem).not.toHaveBeenCalled();
    expect(h.history.replaceState).not.toHaveBeenCalled();
  });

  it("keeps normal token behavior outside /embed/", async () => {
    h_search: {
      const h = await boot({ pathname: "/", search: "?token=abc" });
      expect(h.client.isDelegatedEmbed).toBe(false);
      expect(h.client.token).toBe("abc");
      expect(h.sessionStorage.setItem).toHaveBeenCalledWith("pogo-token", "abc");
      expect(h.client.authHeaders()).toEqual({ Authorization: "Bearer abc" });
      break h_search;
    }
  });

  it("adopts init only from the approved parent source, origin, and channel", async () => {
    const h = await boot();
    const status = h.client.api.authStatus();
    await flush();

    h.sendFromParent({ type: "yw.pogo.init", channel: CHANNEL }, { origin: "https://evil.example" });
    h.sendFromParent({ type: "yw.pogo.init", channel: CHANNEL }, { source: { postMessage: vi.fn() } });
    h.sendFromParent({ type: "yw.pogo.init", channel: "not-a-uuid" });
    h.sendFromParent({ type: "yw.pogo.init", channel: `${CHANNEL}-over-64-characters-pad-pad-pad-pad-pad-pad-pad-pad` });
    expect(h.parent.postMessage).not.toHaveBeenCalled();
    expect(h.messageListeners.size).toBeGreaterThan(0); // still waiting

    h.sendFromParent({ type: "yw.pogo.init", channel: CHANNEL });
    await expect(status).resolves.toEqual({ mode: "delegated" });
    expect(h.parent.postMessage.mock.calls.every((call) => call[1] === PARENT_ORIGIN)).toBe(true);
  });

  it("rejects malformed embed configs", async () => {
    const h = await boot({ configOrigins: ["https://parent.example/path"] });
    await expect(h.client.api.authStatus()).rejects.toMatchObject({ status: 502 });
  });

  it("re-sends ready for a repeated init but never adopts a different channel", async () => {
    const h = await bootReady();
    h.sendFromParent({ type: "yw.pogo.init", channel: CHANNEL });
    const readyCount = h.parent.postMessage.mock.calls.filter((c) => c[0]?.type === "yw.pogo.ready").length;
    expect(readyCount).toBe(2);

    h.sendFromParent({ type: "yw.pogo.init", channel: OTHER_CHANNEL });
    expect(h.parent.postMessage.mock.calls.filter((c) => c[0]?.type === "yw.pogo.ready").length).toBe(2);
    // A response on that other channel must not answer a real request.
    const p = h.client.api.models();
    await flush();
    const req = h.lastRpc();
    h.sendFromParent({ type: "yw.pogo.response", channel: OTHER_CHANNEL, id: req.id, ok: true, data: UI_DATA });
    h.respondTo("ui", UI_DATA);
    await expect(p).resolves.toEqual({ models: [MODEL], default: "model-a" });
  });

  it("times the handshake out after 15s and allows a retry", async () => {
    vi.useFakeTimers();
    const h = await boot();
    const first = h.client.api.authStatus();
    const rejected = expect(first).rejects.toMatchObject({ status: 504 });
    await vi.advanceTimersByTimeAsync(15_000);
    await rejected;

    const second = h.client.api.authStatus();
    await flush();
    h.sendFromParent({ type: "yw.pogo.init", channel: CHANNEL });
    await expect(second).resolves.toEqual({ mode: "delegated" });
  });

  it("maps models through the ui action and caches only that metadata", async () => {
    const h = await bootReady();
    const p = h.client.api.models();
    await flush();
    expect(h.lastRpc()).toMatchObject({ action: "ui", channel: CHANNEL, params: {} });
    h.respondLast(UI_DATA);
    await expect(p).resolves.toEqual({ models: [MODEL], default: "model-a" });
    await expect(h.delegated.getDelegatedUi()).resolves.toMatchObject({
      project: "展示项目",
      capabilities: { attachments: false },
    });

    const again = h.client.api.models();
    await expect(again).resolves.toEqual({ models: [MODEL], default: "model-a" });
    expect(h.rpcRequests().filter((r) => r.action === "ui")).toHaveLength(1);
  });

  it("uses strong request UUIDs when HTTP does not expose randomUUID", async () => {
    const h = await bootReady();
    const getRandomValues = globalThis.crypto.getRandomValues.bind(globalThis.crypto);
    vi.stubGlobal("crypto", { getRandomValues });
    const models = h.client.api.models();
    await flush();
    expect(h.lastRpc().id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    h.respondLast(UI_DATA);
    await expect(models).resolves.toEqual({ models: [MODEL], default: "model-a" });
  });

  it("keeps a structured error and sends no request when secure randomness is unavailable", async () => {
    const h = await bootReady();
    vi.stubGlobal("crypto", undefined);
    await expect(h.client.api.models()).rejects.toMatchObject({ name: "DelegatedError", status: 500, message: "无法生成安全请求标识" });
    expect(h.rpcRequests()).toHaveLength(0);
  });

  it("maps list/create/get/rename/delete/message/cancel onto the RPC actions", async () => {
    const h = await bootReady();

    const list = h.client.api.sessions();
    await flush();
    h.respondTo("ui", UI_DATA);
    h.respondTo("list", { items: [CS] });
    await expect(list).resolves.toEqual({
      sessions: [{ path: "", id: "sess-1", title: "旧会话", messages: 0, created: 1000, updated: 2000, cwd: "展示项目" }],
      cwd: "展示项目",
    });

    const created = h.client.api.createSession();
    await flush();
    expect(h.lastRpc()).toMatchObject({ action: "create", params: {} });
    h.respondLast(CS);
    await expect(created).resolves.toEqual({
      session_id: "sess-1",
      path: "",
      model: "model-a",
      provider: "provider-x",
      workspace: "",
    });

    const got = h.client.api.session("sess-1");
    await flush();
    expect(h.lastRpc()).toMatchObject({ action: "get", params: { sid: "sess-1" } });
    h.respondLast({ session: CS, snapshot: SNAP });
    await expect(got).resolves.toEqual({ session_id: "sess-1", snapshot: SNAP });

    const renamed = h.client.api.updateSession("sess-1", { title: "新标题" });
    await flush();
    expect(h.lastRpc()).toMatchObject({ action: "rename", params: { sid: "sess-1", title: "新标题" } });
    h.respondLast({ ok: true });
    await expect(renamed).resolves.toBeUndefined();

    const removed = h.client.api.deleteSession("sess-1");
    await flush();
    expect(h.lastRpc()).toMatchObject({ action: "delete", params: { sid: "sess-1" } });
    h.respondLast({ ok: true });
    await expect(removed).resolves.toBeUndefined();

    const sent = h.client.api.send("sess-1", "你好", undefined, "client-7");
    await flush();
    expect(h.lastRpc()).toMatchObject({ action: "message", params: { sid: "sess-1", text: "你好", clientId: "client-7" } });
    h.respondLast({ session_id: "sess-1", run_id: "run-9" });
    await expect(sent).resolves.toEqual({ run_id: "run-9" });

    const cancelled = h.client.api.cancel("sess-1");
    await flush();
    expect(h.lastRpc()).toMatchObject({ action: "cancel", params: { sid: "sess-1" } });
    h.respondLast({ cancelled: true });
    await expect(cancelled).resolves.toEqual({ cancelled: true });
  });

  it("maps title search over the current list", async () => {
    const h = await bootReady();
    const p = h.client.api.sessionSearch("旧", 5);
    await flush();
    h.respondTo("ui", UI_DATA);
    h.respondTo("list", { items: [CS, { ...CS, id: "sess-2", title: "另一个" }] });
    const res = await p;
    expect(res.q).toBe("旧");
    expect(res.hits).toHaveLength(1);
    expect(res.hits[0]).toMatchObject({ id: "sess-1", title: "旧会话", score: 0, cwd: "展示项目" });
  });

  it("refuses unsupported legacy paths locally without fetching them", async () => {
    const h = await bootReady();
    await expect(h.client.api.skills()).rejects.toMatchObject({ status: 403 });
    await expect(h.client.api.files("x")).rejects.toMatchObject({ status: 403 });
    await expect(h.client.api.sessionAttachments("sess-1")).rejects.toMatchObject({ status: 403 });
    expect(h.fetchMock.mock.calls.map((c) => String(c[0])).filter((u) => u !== "/embed/config")).toHaveLength(0);
    expect(h.rpcRequests()).toHaveLength(0);
  });

  it("rejects explicit model/workspace, pinning, attachments, and other control actions", async () => {
    const h = await bootReady();
    await expect(h.client.api.createSession("model-b")).rejects.toMatchObject({ status: 403 });
    await expect(h.client.api.createSession(undefined, "ws")).rejects.toMatchObject({ status: 403 });
    await expect(h.client.api.updateSession("sess-1", { pinned: true })).rejects.toMatchObject({ status: 403 });
    await expect(h.client.api.send("sess-1", "hi", ["uploads/a.txt"])).rejects.toMatchObject({ status: 403 });
    await expect(h.client.api.send("sess-1", "")).rejects.toMatchObject({ status: 400 });
    await expect(h.client.api.compact("sess-1")).rejects.toMatchObject({ status: 403 });
    expect(h.rpcRequests()).toHaveLength(0);
  });

  it("does not auto-retry a failed message and passes the parent status through", async () => {
    const h = await bootReady();
    const p = h.client.api.send("sess-1", "你好");
    await flush();
    h.failLast(409, "会话正忙");
    await expect(p).rejects.toMatchObject({ status: 409, message: "会话正忙" });
    expect(h.rpcRequests().filter((r) => r.action === "message")).toHaveLength(1);
  });

  it("ignores duplicate and late responses", async () => {
    const h = await bootReady();
    const p = h.client.api.models();
    await flush();
    const id = h.lastRpc().id;
    h.sendFromParent({ type: "yw.pogo.response", channel: CHANNEL, id: "not-a-uuid", ok: true, data: { models: [], default: "other" } });
    h.respondLast(UI_DATA);
    // Duplicate id: already settled, must be dropped without side effects.
    h.sendFromParent({ type: "yw.pogo.response", channel: CHANNEL, id, ok: false, error: "late", status: 500 });
    await expect(p).resolves.toEqual({ models: [MODEL], default: "model-a" });

    const next = h.client.api.models();
    await expect(next).resolves.toEqual({ models: [MODEL], default: "model-a" }); // bridge still healthy
  });

  it("aborts an in-flight RPC and ignores its late response", async () => {
    const h = await bootReady();
    const controller = new AbortController();
    const p = h.delegated.delegatedUpdates("sess-1", 3, controller.signal);
    await flush();
    const id = h.lastRpc().id;
    expect(h.lastRpc()).toMatchObject({ action: "updates", params: { sid: "sess-1", from: 3 } });
    controller.abort();
    await expect(p).rejects.toMatchObject({ name: "AbortError" });
    h.sendFromParent({ type: "yw.pogo.response", channel: CHANNEL, id, ok: true, data: { events: [] } });
    await flush();

    const after = h.delegated.delegatedUpdates("sess-1");
    await flush();
    h.respondLast({ events: [{ seq: 1, type: "message", ts: 1 }] });
    await expect(after).resolves.toEqual({ events: [{ seq: 1, type: "message", ts: 1 }] });
  });

  it("validates updates cursors", async () => {
    const h = await bootReady();
    await expect(h.delegated.delegatedUpdates("sess-1", -1)).rejects.toMatchObject({ status: 400 });
    await expect(h.delegated.delegatedUpdates("sess-1", 1.5)).rejects.toMatchObject({ status: 400 });
    expect(h.rpcRequests().filter((r) => r.action === "updates")).toHaveLength(0);
  });

  it("dispose and pagehide discard pending work; late responses go nowhere", async () => {
    const h = await bootReady();
    const p = h.delegated.getDelegatedUi();
    await flush();
    const id = h.lastRpc().id;
    h.firePagehide();
    await expect(p).rejects.toMatchObject({ status: 503 });
    h.sendFromParent({ type: "yw.pogo.response", channel: CHANNEL, id, ok: true, data: UI_DATA });
    await expect(h.client.api.authStatus()).rejects.toMatchObject({ status: 503 });
    expect(h.messageListeners.size).toBe(0);
  });

  it("caps concurrent RPCs at 6", async () => {
    const h = await bootReady();
    const polls = Array.from({ length: 6 }, () => h.delegated.delegatedUpdates("sess-1"));
    await flush();
    await expect(h.delegated.delegatedUpdates("sess-1")).rejects.toMatchObject({ status: 429 });
    expect(h.rpcRequests().filter((r) => r.action === "updates")).toHaveLength(6);
    for (const req of h.rpcRequests().filter((r) => r.action === "updates")) {
      h.sendFromParent({ type: "yw.pogo.response", channel: CHANNEL, id: req.id, ok: true, data: { events: [] } });
    }
    await expect(Promise.all(polls)).resolves.toHaveLength(6);
  });

  it("times an RPC out after 30s", async () => {
    vi.useFakeTimers();
    const h = await bootReady();
    const p = h.client.api.models();
    const rejected = expect(p).rejects.toMatchObject({ status: 504 });
    await flush();
    await vi.advanceTimersByTimeAsync(30_000);
    await rejected;
  });
});
