// Isolated real AgentView with synthetic API/pixels. Never forwards API or
// third-party network requests, and never touches a deployed browser/session.
import { createApp } from "vue";
import type { Composer } from "vue-i18n";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import "../src/styles.scss";
import AgentView from "../src/views/AgentView.vue";
import { i18n } from "../src/i18n";
import { applyTheme, THEMES } from "../src/theme";
import { emptyLive, type CiteSource, type Snapshot } from "../src/api/types";
import { SHEET_KEY, TABS_KEY } from "../src/components/dockSheets";
import type { NativeBrowserState } from "../src/agent/nativeBrowser";
(i18n.global as unknown as Composer).locale.value = "zh-CN";
const query = new URLSearchParams(location.search);
applyTheme(query.get("theme") === "dark" ? THEMES.find(t => t.mode === "dark")!.id : "paper", false);
if (!query.has("restore")) {
  localStorage.setItem(SHEET_KEY, "hub");
  localStorage.setItem(TABS_KEY, JSON.stringify({ open: [] }));
  localStorage.setItem(`${TABS_KEY}:native-only:v1`, "done");
}
type Page = { id: string; title: string; url: string; favicon?: string; revision: number; loading: boolean; control: "agent" | "human"; epoch: number; opener?: string; inheritedEpoch?: number; lease?: string; viewport?: { width: number; height: number }; history?: { url: string; title: string }[] };
type Context = { ref: string; pages: Page[]; active: string; serial: number; control: "agent" | "human"; epoch: number; generation: number };
const contexts: Record<string, Context> = {
  "sheets-a": { ref: "ctx_111111111111111111111111", pages: [{ id: "t1", title: "Page A", url: "https://a.invalid/", revision: 1, loading: false, control: "agent", epoch: 0 }], active: "t1", serial: 1, control: "agent", epoch: 0, generation: 1 },
  "sheets-b": { ref: "ctx_222222222222222222222222", pages: [{ id: "t1", title: "Other conversation", url: "https://other.invalid/", revision: 1, loading: false, control: "agent", epoch: 0 }], active: "t1", serial: 1, control: "agent", epoch: 0, generation: 1 },
};
const streamSinks = new Map<string, Set<(event: Record<string, unknown>) => void>>();
const statePatches = new Map<string, Partial<NativeBrowserState>>();
const archivedIcons: Record<string, { url: string; favicon: string }[]> = { "sheets-a": [], "sheets-b": [] };
const archivedSnapshots: Record<string, Partial<Snapshot>> = {};
const discoveryReplies = new Map<string, { state: NativeBrowserState; delay: number }[]>();
function emitFixtureEvent(event: Record<string, unknown>, sid = "sheets-a") {
  for (const send of streamSinks.get(sid) ?? []) send(event);
}
if (query.has("restore") && sessionStorage.getItem("qa:browser-contexts")) Object.assign(contexts, JSON.parse(sessionStorage.getItem("qa:browser-contexts")!));
if (query.has("restore") && sessionStorage.getItem("qa:browser-icons")) Object.assign(archivedIcons, JSON.parse(sessionStorage.getItem("qa:browser-icons")!));
if (query.has("restore") && sessionStorage.getItem("qa:browser-snapshots")) Object.assign(archivedSnapshots, JSON.parse(sessionStorage.getItem("qa:browser-snapshots")!));
const leaseFor = (sid: string, c: Context, tab: string) => c.pages.find(page => page.id === tab)?.lease ?? `lease-${sid}-${c.ref}-${tab}`;
const fixture = {
  patchState: (patch: Partial<NativeBrowserState>, id = "t1", sid = "sheets-a") => {
    statePatches.set(`${sid}:${id}`, { ...statePatches.get(`${sid}:${id}`), ...patch });
    broadcast(contexts[sid]);
  },
  emit: emitFixtureEvent,
  seedConversation: (count: number, seq = 2) => emitFixtureEvent({ type: "snapshot", seq, ts: Date.now(), snapshot: {
    seq, messages: Array.from({ length: count }, (_, i) => ({ id: `history-${i}`, role: i % 2 ? "assistant" : "user", content: [{ type: "text", text: `历史消息 ${i}\n\n${"这是一段用于验证布局变化后阅读位置的内容。".repeat(8)}\n\n${"页面与对话保持各自的上下文。".repeat(6)}` }] })),
    live: emptyLive(), run: { active: false, model: "demo" }, policy: { mode: "standard" },
  } }),
  requests: [] as { method: string; path: string; tab: string | null; context: string | null; body?: Record<string, unknown> }[],
  errors: [] as { code: string; status: number; context: string | null; tab: string | null; endpoint: string }[],
  inputs: [] as Record<string, unknown>[], tabCalls: [] as Record<string, unknown>[], controls: [] as Record<string, unknown>[], denied: [] as string[],
  controlDelay: 0, controlReplies: 0, frameDelay: 0, stateDelay: 0, frameWrongTarget: false, frameStatus: 200, offline: false,
  liveReady: [] as { tab: string; context: string }[], liveFrames: [] as { tab: string; context: string }[],
  liveViewports: [] as { tab: string; width: number; height: number }[],
  liveInputs: [] as Record<string, unknown>[],
  liveMessages: [] as Record<string, unknown>[],
  newOutcome: "" as "" | "lost" | "pending", pendingResolved: false,
  adoptionRace: "" as "" | "agent" | "human" | "error",
  runtimeFailure: null as { status: number; code: string; error: string } | null,
  boundFailure: null as { status: number; code: string; error: string } | null,
  allContexts: () => structuredClone(contexts),
  context: (sid = "sheets-a") => structuredClone(contexts[sid]),
  state: (sid = "sheets-a", id?: string) => structuredClone(state(contexts[sid], id)),
  setContext: (next: Context, sid = "sheets-a") => { contexts[sid] = structuredClone(next); },
  queueDiscovery: (next: NativeBrowserState, delay = 0, sid = "sheets-a") => {
    const queue = discoveryReplies.get(sid) ?? []; queue.push({ state: structuredClone(next), delay }); discoveryReplies.set(sid, queue);
  },
  lateState: (next: NativeBrowserState, delay = 0) => {
    // Capture a wire callback before replacement, then deliver after its socket
    // was retired. This models a queued stale message, never a real request.
    const callbacks = [...sockets].map(socket => socket.onmessage).filter(Boolean);
    setTimeout(() => callbacks.forEach(callback => callback!({ data: JSON.stringify({ type: "state", state: next }) })), delay);
  },
  liveError: (code: string, error: string) => { for (const socket of sockets) socket.deliver({ type: "error", code, error }); },
  setArchive: (icons: { url: string; favicon: string }[], sid = "sheets-a") => { archivedIcons[sid] = structuredClone(icons); sessionStorage.setItem("qa:browser-icons", JSON.stringify(archivedIcons)); },
  archive: (sid = "sheets-a") => structuredClone(archivedIcons[sid]),
  seedCitation: (source: CiteSource, sid = "sheets-a", seq = 1) => {
    const snapshot: Partial<Snapshot> = {
      seq, messages: [
        { id: `citation-user-${sid}`, role: "user", content: [{ type: "text", text: "读取这份虚构网页材料。" }] },
        { id: `citation-tool-${sid}`, role: "assistant", content: [{ type: "tool_use", id: source.call_id, name: "browser", input: { action: "snapshot" } }] },
        { id: `citation-answer-${sid}`, role: "assistant", content: [{ type: "text", text: `${source.title}：这份来源属于 ${sid}。[1](#cite:${source.label})` }] },
      ], results: { [source.call_id]: { call_id: source.call_id, name: "browser", text: source.content ?? source.excerpt, source } },
      live: emptyLive(), run: { active: false, model: "demo" }, policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0,
    };
    archivedSnapshots[sid] = snapshot; sessionStorage.setItem("qa:browser-snapshots", JSON.stringify(archivedSnapshots));
    emitFixtureEvent({ type: "snapshot", seq, ts: Date.now(), snapshot }, sid);
  },
  save: () => { sessionStorage.setItem("qa:browser-contexts", JSON.stringify(contexts)); sessionStorage.setItem("qa:browser-icons", JSON.stringify(archivedIcons)); sessionStorage.setItem("qa:browser-snapshots", JSON.stringify(archivedSnapshots)); sessionStorage.setItem("qa:browser-options", JSON.stringify({ offline: fixture.offline, runtimeFailure: fixture.runtimeFailure, boundFailure: fixture.boundFailure })); },
  addPopup: () => { const c = contexts["sheets-a"]; const id = `t${++c.serial}`; c.pages.push({ id, title: "Agent popup", url: "https://popup.invalid/", revision: 1, loading: false, control: "agent", epoch: 0 }); c.active = id; return id; },
  addUnownedPage: (control: "agent" | "human" = "human", epoch = 0) => {
    const c = contexts["sheets-a"], id = `t${++c.serial}`;
    c.pages.push({ id, title: "Native new page", url: "https://native-new.invalid/", revision: 1, loading: false, control, epoch });
    broadcast(c); return id;
  },
  addHumanPopup: (opener: string, notify = true) => {
    const c = contexts["sheets-a"], parent = c.pages.find(page => page.id === opener)!;
    if (parent.control !== "human") throw new Error("Fixture popup needs a human opener");
    const id = `t${++c.serial}`;
    c.pages.push({ id, title: "Human popup", url: "https://auth.invalid/", revision: 1, loading: false,
      control: "human", epoch: parent.epoch, opener, inheritedEpoch: parent.epoch, lease: leaseFor("sheets-a", c, opener) });
    if (notify) broadcast(c); return id;
  },
  claim: (id: string) => {
    const c = contexts["sheets-a"], page = c.pages.find(page => page.id === id)!;
    page.control = "agent"; page.epoch++; page.inheritedEpoch = undefined; page.lease = undefined;
    c.active = id; broadcast(c); return structuredClone(state(c, id));
  },
  closePage: (id: string) => {
    const c = contexts["sheets-a"];
    c.pages = c.pages.filter(page => page.id !== id);
    if (c.active === id) c.active = c.pages[0]?.id ?? "";
    broadcast(c);
  },
  setLoading: (id: string, value: boolean) => { contexts["sheets-a"].pages.find(p => p.id === id)!.loading = value; broadcast(contexts["sheets-a"]); },
  navigate: (id: string, url: string) => { const page = contexts["sheets-a"].pages.find(p => p.id === id)!; page.url = url; page.revision++; page.loading = false; broadcast(contexts["sheets-a"]); },
  setFavicon: (id: string, favicon?: string) => { contexts["sheets-a"].pages.find(page => page.id === id)!.favicon = favicon; broadcast(contexts["sheets-a"]); },
  restart: () => { const previous = contexts["sheets-a"]; contexts["sheets-a"] = { ref: "ctx_333333333333333333333333", pages: [{ id: "t1", title: "Recreated browser", url: "about:blank", revision: 1, loading: false, control: "agent", epoch: 0 }], active: "t1", serial: 1, control: "agent", epoch: previous.epoch + 1, generation: previous.generation + 1 }; },
};
if (query.has("restore") && sessionStorage.getItem("qa:browser-options")) Object.assign(fixture, JSON.parse(sessionStorage.getItem("qa:browser-options")!));
Object.assign(window, { __sheetsFixture: fixture });
const state = (c: Context, id = c.active): NativeBrowserState => {
  const page = c.pages.find(p => p.id === id);
  const sid = Object.keys(contexts).find(key => contexts[key] === c);
  return { backend: "native", context_ref: c.ref, active_tab: c.active, viewed_tab: page?.id || "", control: page?.control ?? c.control, control_epoch: page?.epoch ?? c.epoch, generation: c.generation, document_version: page?.revision || 0,
    url: page?.url || "", title: page?.title || "", can_go_back: (page?.history?.length ?? 0) > 1, page_loading: !!page?.loading, version: page ? `${c.generation}:${page.id}:${page.revision}` : "", frame_identity: page ? `${c.generation}:${page.id}:${page.revision}` : "",
    viewport: page?.viewport ?? { width: 1280, height: 800 }, tabs: c.pages.map(p => ({ id: p.id, url: p.url, title: p.title, favicon: p.favicon, control: p.control, control_epoch: p.epoch, opener_tab: p.opener, inherited_control_epoch: p.inheritedEpoch })),
    ...statePatches.get(`${sid}:${id}`) };
};
// This fixture uses the current live protocol, with synthetic pixels only.
// The browser never opens a real Pogo WebSocket or website.
const sockets = new Set<FixtureSocket>();
class FixtureSocket {
  readyState = 1; bufferedAmount = 0; frameId = 0;
  onmessage: ((event: { data: string }) => unknown) | null = null;
  onclose: (() => unknown) | null = null; onerror: (() => unknown) | null = null;
  c: Context; tab: string;
  constructor(url: string | URL) {
    const parsed = new URL(url), match = /^\/api\/sessions\/(sheets-[ab])\/browser\/live$/.exec(parsed.pathname);
    if (!match) throw new Error("Unsupported fixture WebSocket");
    this.c = contexts[match[1]]; this.tab = parsed.searchParams.get("tab_id")!; sockets.add(this);
    setTimeout(() => { if (this.readyState === 1) { fixture.liveReady.push({ tab: this.tab, context: this.c.ref }); this.deliver({ type: "ready", protocol: 1, state: state(this.c, this.tab) }); this.frame(); } }, 30);
  }
  deliver(value: unknown) { this.onmessage?.({ data: JSON.stringify(value) }); }
  update() { if (this.readyState !== 1 || !this.c.pages.some(p => p.id === this.tab)) return; this.deliver({ type: "state", state: state(this.c, this.tab) }); this.frame(); }
  frame() {
    const view = state(this.c, this.tab);
    if (!view.url || view.url === "about:blank" || view.page_loading) return;
    fixture.liveFrames.push({ tab: this.tab, context: this.c.ref });
    const canvas = document.createElement("canvas"); canvas.width = view.viewport.width; canvas.height = view.viewport.height;
    const g = canvas.getContext("2d")!; g.fillStyle = this.tab === "t1" ? "#16865a" : "#bd7921"; g.fillRect(0, 0, canvas.width, canvas.height);
    g.fillStyle = "white"; g.font = "36px sans-serif"; g.fillText(`${view.title} · ${this.tab}`, 50, 90); g.font = "24px sans-serif"; g.fillText(view.url, 50, 140);
    this.deliver({ type: "frame", frame_id: ++this.frameId, data: canvas.toDataURL("image/jpeg", .9).split(",")[1], mime: "image/jpeg", context_ref: this.c.ref, tab_id: this.tab, viewport: view.viewport, frame_identity: view.frame_identity, generation: view.generation, control_epoch: view.control_epoch, document_version: view.document_version });
  }
  send(raw: string) {
    const message = JSON.parse(raw), page = this.c.pages.find(p => p.id === this.tab);
    fixture.liveMessages.push({ context_ref: this.c.ref, tab_id: this.tab, ...message });
    if (message.type === "viewport" && page) {
      fixture.liveViewports.push({ tab: this.tab, width: message.width, height: message.height });
      page.viewport = { width: message.width, height: message.height }; this.update();
    } else if (["pointer", "key", "text", "wheel"].includes(message.type)) {
      fixture.liveInputs.push(message);
    }
  }
  close() { this.readyState = 3; sockets.delete(this); this.onclose?.(); }
}
function broadcast(context: Context) { for (const socket of sockets) if (socket.c === context) socket.update(); }
const NativeWebSocket = window.WebSocket;
window.WebSocket = Object.assign(function (url: string | URL, protocols?: string | string[]) {
  return new URL(url, location.href).pathname.startsWith("/api/") ? new FixtureSocket(url) : new NativeWebSocket(url, protocols);
}, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 }) as unknown as typeof WebSocket;
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const pause = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));
const operations = new Map<string, { reply: unknown; pending: boolean }>();
const originalFetch = window.fetch.bind(window);
window.fetch = async (input, options = {}) => {
  const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url, location.href);
  if (!url.pathname.startsWith("/api/")) { if (url.origin !== location.origin) throw new Error("External fixture network forbidden"); return originalFetch(input, options); }
  const path = url.pathname, method = options.method || "GET";
  const requestBody = options.body ? JSON.parse(String(options.body)) : undefined;
  fixture.requests.push({ path, method, tab: url.searchParams.get("tab_id"), context: url.searchParams.get("context_ref"), ...(requestBody ? { body: requestBody } : {}) });
  if (path === "/api/auth/status") return json({ mode: "password", password: true, user: "Fixture", admin: false });
  if (path === "/api/browser/bookmarks" && method === "GET") return json({ source: "pogo_bookmarks", scope: "account", status: "ready", account_id: "fixture-account", revision: 0, total: 0, bookmarks: [] });
  if (path === "/api/sessions/activity") return json({ busy: [] });
  if (path === "/api/sessions") return json({ cwd: "/demo", sessions: Object.keys(contexts).map((id, i) => ({ id, title: i ? "第二会话" : "第一会话", cwd: "/demo", model: "demo", provider: "fixture", messages: 0, updated: 2 - i, created: 1 })) });
  if (path === "/api/models") return json({ default: "demo", models: [{ id: "demo", name: "Fixture", provider: "fixture" }] });
  if (path === "/api/skills") return json({ skills: [] });
  if (path === "/api/panels") return json({ panels: [], auto_register: false });
  if (path === "/api/starters") return json({ starters: null });
  if (path === "/api/attachments/policy") return json({ allowed_exts: [".txt"], max_mb: 20 });
  if (path === "/api/schedule/jobs") return json({ jobs: [], enabled: false });
  if (path.endsWith("/attachments")) return json({ attachments: [] });
  if (path === "/api/browser/migrate-urls") return json({ mappings: [] });
  const match = /^\/api\/sessions\/(sheets-[ab])\/(stream|browser(?:\/(frame|tabs|input|control|pick|live-ticket|icons))?)$/.exec(path);
  if (match) {
    const c = contexts[match[1]], endpoint = match[3] || match[2];
    if (endpoint === "stream") {
      const encoder = new TextEncoder();
      return new Response(new ReadableStream({ start(controller) {
        const send = (event: Record<string, unknown>) => controller.enqueue(encoder.encode(`data: ${JSON.stringify(event)}\n\n`));
        const sinks = streamSinks.get(match[1]) ?? new Set(); streamSinks.set(match[1], sinks); sinks.add(send);
        const snapshot = archivedSnapshots[match[1]] ?? { seq: 1, messages: [], live: emptyLive(), run: { active: false, model: "demo" }, policy: { mode: "standard" } };
        controller.enqueue(encoder.encode(`data: ${JSON.stringify({ type: "snapshot", seq: snapshot.seq ?? 1, ts: Date.now(), snapshot })}\n\n`));
        options.signal?.addEventListener("abort", () => { sinks.delete(send); controller.close(); }, { once: true });
      } }), { headers: { "Content-Type": "text/event-stream", "X-Pogo-Stream-Backlog-Through": query.get("backlog") || "1" } });
    }
    const body = requestBody ?? {};
    const target = url.searchParams.get("tab_id") || body.target_tab || (["control", "pick", "tabs", "live-ticket"].includes(endpoint) ? body.tab_id : undefined);
    const context = url.searchParams.get("context_ref") || body.context_ref;
    // Archived metadata survives runtime loss. Reading it cannot create a page,
    // reconnect a worker or replay a saved URL.
    if (endpoint === "icons") return json({ icons: archivedIcons[match[1]] });
    if (fixture.runtimeFailure) return json(fixture.runtimeFailure, fixture.runtimeFailure.status);
    if (fixture.offline) return json({ error: "Fixture disconnected" }, 503);
    if (context && fixture.boundFailure) { fixture.errors.push({ code: fixture.boundFailure.code, status: fixture.boundFailure.status, context: String(context), tab: target ? String(target) : null, endpoint }); return json(fixture.boundFailure, fixture.boundFailure.status); }
    if (context && context !== c.ref) { fixture.errors.push({ code: "BROWSER_CONTEXT_CHANGED", status: 409, context: String(context), tab: target ? String(target) : null, endpoint }); return json({ error: "Browser context changed", code: "BROWSER_CONTEXT_CHANGED" }, 409); }
    if (target && !c.pages.some(p => p.id === target)) { fixture.errors.push({ code: "BROWSER_TAB_CLOSED", status: 410, context: context ? String(context) : null, tab: String(target), endpoint }); return json({ error: "Browser tab closed", code: "BROWSER_TAB_CLOSED" }, 410); }
    if (endpoint === "live-ticket") return json({ ticket: "isolated-fixture", expires_in: 30 });
    if (endpoint === "browser") {
      const queued = !context && !target ? discoveryReplies.get(match[1])?.shift() : undefined;
      const reply = json(queued?.state ?? state(c, target)); if (queued?.delay || fixture.stateDelay) await pause(queued?.delay ?? fixture.stateDelay); return reply;
    }
    if (endpoint === "frame") {
      const view = state(c, target);
      if (fixture.frameStatus !== 200) return json({ error: "Fixture frame unavailable" }, fixture.frameStatus);
      const canvas = document.createElement("canvas"); canvas.width = 1280; canvas.height = 800;
      const g = canvas.getContext("2d")!; g.fillStyle = target === "t1" ? "#16865a" : "#bd7921"; g.fillRect(0, 0, 1280, 800); g.fillStyle = "white"; g.font = "36px sans-serif"; g.fillText(`${view.title} · ${target}`, 50, 90); g.font = "24px sans-serif"; g.fillText(view.url, 50, 140);
      const pixels = await new Promise<Blob>(resolve => canvas.toBlob(blob => resolve(blob!), "image/jpeg", .9));
      const reply = new Response(pixels, { headers: { "Content-Type": "image/jpeg", "X-Browser-Version": view.version!, "X-Browser-Frame-Identity": view.frame_identity!, "X-Browser-Context-Ref": fixture.frameWrongTarget ? "ctx_999999999999999999999999" : c.ref, "X-Browser-Viewed-Tab": fixture.frameWrongTarget ? "t99" : target || view.viewed_tab! } });
      if (fixture.frameDelay) await pause(fixture.frameDelay); return reply;
    }
    if (endpoint === "control") {
      fixture.controls.push(body);
      const page = c.pages.find(p => p.id === target);
      if (!page) return json({ error: "bound control needs a Page" }, 400);
      if (body.adopt_if_unowned) {
        if (fixture.adoptionRace === "error") return json({ error: "Fixture initialization outcome unknown" }, 503);
        if (fixture.adoptionRace) { page.control = fixture.adoptionRace; page.epoch++; fixture.adoptionRace = ""; broadcast(c); }
        if (body.mode !== "human" || body.force !== undefined || body.generation !== c.generation || body.control_epoch !== 0
          || page.control !== "human" || page.epoch !== 0 || page.lease) return json({ error: "Human page already has an owner or observation changed" }, 409);
      } else if (body.mode === "human" && page.control === "human" && !body.force) return json({ error: "this tab is already controlled by a human" }, 409);
      if (body.mode === "agent" && body.lease_token !== leaseFor(match[1], c, String(target))) return json({ error: "control required" }, 403);
      page.control = body.mode; page.epoch++; page.inheritedEpoch = undefined; page.lease = undefined; broadcast(c);
      const reply = json({ ...state(c, target), ...(page.control === "human" ? { lease_token: leaseFor(match[1], c, String(target)) } : {}) });
      if (fixture.controlDelay) await pause(fixture.controlDelay); fixture.controlReplies++; return reply;
    }
    if (endpoint === "tabs") {
      fixture.tabCalls.push(body);
      const key = `${c.ref}:${body.op_id}`;
      const existing = operations.get(key);
      if (existing) return existing.pending && !fixture.pendingResolved ? json({ error: "TAB_OPERATION_PENDING: still running", code: "TAB_OPERATION_PENDING" }, 409) : json(existing.reply);
      let id = body.tab_id;
      if (body.action === "new") { id = `t${++c.serial}`; c.pages.push({ id, title: `Page ${id}`, url: "about:blank", revision: 1, loading: false, control: "human", epoch: 1 }); }
      else {
        if (c.pages.find(p => p.id === id)?.control !== "human" || body.lease_token !== leaseFor(match[1], c, String(id))) return json({ error: "control required" }, 403);
        c.pages = c.pages.filter(p => p.id !== id); if (c.active === id) c.active = c.pages[0]?.id || "";
      }
      const reply = { tab_id: id, context_ref: c.ref, state: state(c, body.action === "new" ? id : undefined),
        ...(body.action === "new" ? { lease_token: leaseFor(match[1], c, String(id)) } : {}) };
      operations.set(key, { reply, pending: fixture.newOutcome === "pending" });
      if (fixture.newOutcome) { fixture.newOutcome = ""; throw new TypeError("Fixture response lost after dispatch"); }
      return json(reply);
    }
    if (endpoint === "input") {
      if (c.pages.find(p => p.id === target)?.control !== "human" || body.lease_token !== leaseFor(match[1], c, String(target)) || !target) return json({ error: "bound input needs control" }, 403);
      fixture.inputs.push(body);
      const p = c.pages.find(p => p.id === target)!;
      if (body.type === "navigate") { p.history ??= [{ url: p.url, title: p.title }]; p.url = body.url; p.title = `Opened ${target}`; p.history.push({ url: p.url, title: p.title }); p.revision++; }
      if (body.type === "back" && (p.history?.length ?? 0) > 1) { p.history!.pop(); Object.assign(p, p.history!.at(-1)); p.revision++; }
      broadcast(c);
      return json(state(c, target));
    }
    if (endpoint === "pick") return json({ ...state(c, target), tab_id: target, element: null });
  }
  fixture.denied.push(`${method} ${path}`);
  return json({ error: "Unsupported isolated fixture operation" }, 501);
};
createApp(AgentView).use(i18n).use(ElementPlus).mount("#app");
