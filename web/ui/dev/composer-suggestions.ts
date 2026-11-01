// Real AgentView with fictional API/SSE/browser transport. No deployed Pogo,
// real account, model, conversation, or third-party site is contacted.
import { createApp } from "vue";
import type { Composer } from "vue-i18n";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import "../src/styles.scss";
import AgentView from "../src/views/AgentView.vue";
import { i18n } from "../src/i18n";
import { applyTheme, THEMES } from "../src/theme";
import { emptyLive, type SkillInfo } from "../src/api/types";
import { SHEET_KEY, TABS_KEY } from "../src/components/dockSheets";
import { STUDIO_LAST_SESSION, studioKey } from "../src/agent/studioContext";
import type { NativeBrowserState } from "../src/agent/nativeBrowser";
import type { ElementPick } from "../src/agent/elementPicker";

const query = new URLSearchParams(location.search);
const aid = "composer-fixture-agent", sid = "composer-fixture-session", model = "fixture-model";
(i18n.global as unknown as Composer).locale.value = "zh-CN";
applyTheme(query.get("theme") === "dark" ? THEMES.find(theme => theme.mode === "dark")!.id : "paper", false);
const names = ["analysis-lab", "browser-use", "clip-notes", "code-review", "data-table", "diagram-kit", "docs", "forms", "lab-notebook", "research", "team-brief", "web-drawing"];
const builtin = new Set(["browser-use", "docs", "research", "web-drawing"]);
const skills: SkillInfo[] = names.map((name, i) => ({
  name, description: `虚构示例 ${i + 1}：用于隔离输入框验收的技能描述。`,
  path: `/fixture/${builtin.has(name) ? "builtin" : "project"}/${name}/SKILL.md`,
  dir: `/fixture/${builtin.has(name) ? "builtin" : "project"}/${name}`,
  source: builtin.has(name) ? "embedded" : "project",
}));
const capabilities = { models: [model], model, skills: names.map((name, i) => `synthetic-selection-${i}-${name}`), mcp_servers: [], tools: ["browser"] };
const browser: NativeBrowserState = {
  backend: "native", context_ref: "ctx_aaaaaaaaaaaaaaaaaaaaaaaa", control: "agent", control_epoch: 0,
  generation: 1, document_version: 1, active_tab: "t1", viewed_tab: "t1", url: "about:blank", title: "",
  viewport: { width: 1280, height: 800 }, version: "1:t1:1", frame_identity: "1:t1:1",
  tabs: [{ id: "t1", url: "about:blank", title: "", control: "agent", control_epoch: 0 }],
};
localStorage.setItem(studioKey(aid, STUDIO_LAST_SESSION), sid);
if (query.has("browser")) localStorage.setItem(studioKey(aid, SHEET_KEY), "hub");
else localStorage.removeItem(studioKey(aid, SHEET_KEY));
localStorage.setItem(studioKey(aid, TABS_KEY), JSON.stringify({
  open: query.has("browser") ? [{ id: "composer-browser", kind: "browser", label: "隔离浏览器", closable: true, browserBinding: { sessionId: sid, context_ref: browser.context_ref, tab_id: "t1" } }] : [],
  ...(query.has("browser") ? { active: "composer-browser", browserActive: { [sid]: "composer-browser" } } : {}),
}));
localStorage.setItem(`${studioKey(aid, TABS_KEY)}:native-only:v1`, "done");

const fixture = {
  sid, aid, skills: structuredClone(skills), capabilities: structuredClone(capabilities),
  requests: [] as { path: string; query: string; method: string; body?: Record<string, unknown> }[],
  messages: [] as Record<string, unknown>[], controls: [] as Record<string, unknown>[],
  denied: [] as string[], streamsOpened: 0, streamsClosed: 0, compactCalls: 0, skillsFail: false,
  holdMessages: false, failMessages: false, steered: true,
  failUploads: false, uploads: [] as { name: string; size: number }[],
  resolveMessage: () => {}, addPick: (_pick: ElementPick) => {},
  emit: (_event: Record<string, unknown>) => {}, dispose: () => {},
};
Object.assign(window, { __composerFixture: fixture });
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const originalFetch = window.fetch.bind(window);
window.fetch = async (input, options = {}) => {
  const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url, location.href);
  if (!url.pathname.startsWith("/api/")) {
    if (url.origin !== location.origin) { fixture.denied.push("external asset"); throw new Error("External fixture request forbidden"); }
    return originalFetch(input, options);
  }
  if (url.origin !== location.origin) { fixture.denied.push("external API"); throw new Error("External fixture API forbidden"); }
  const path = url.pathname, method = options.method ?? "GET", body = options.body ? JSON.parse(String(options.body)) : undefined;
  fixture.requests.push({ path, query: url.search, method, body });
  if (path === "/api/auth/status") return json({ mode: "password", password: true, user: "Fixture viewer", admin: false });
  if (path === `/api/apps/${aid}` && method === "GET") return json({ id: aid, name: "虚构命令菜单验收", description: "仅使用内存数据与合成会话。", instructions: "No model task is performed.", model, models: [model], skills: capabilities.skills, mcp_servers: [], tools: ["browser"], color: "#d78661", icon: "", category: "fixture", starters: [], status: "ready", revision: 1, created_at: "2026-10-09T00:00:00Z", updated_at: "2026-10-09T00:00:00Z", conversation_count: 1 });
  if (path === `/api/apps/${aid}/sessions` && method === "GET") return json({ cwd: "/fixture", sessions: [{ id: sid, path: `/fixture/${sid}`, title: "合成现有会话", cwd: "/fixture", model, provider: "fixture", messages: 2, created: 1, updated: 1 }] });
  if (path === `/api/apps/${aid}/sessions/${sid}/capabilities`) return json(capabilities);
  if (path === "/api/models") return json({ default: model, models: [{ id: model, name: "内存模型占位", provider: "fixture", configured: true, context_window: 100000 }] });
  if (path === "/api/skills" && url.searchParams.get("session_id") === sid) return fixture.skillsFail ? json({ error: "Synthetic skill request failed" }, 503) : json({ skills: fixture.skills });
  if (path === "/api/sessions/activity") return json({ busy: [] });
  if (path === "/api/panels") return json({ panels: [], auto_register: false });
  if (path === "/api/attachments/policy") return json({ allowed_exts: [".txt", ".md"], max_mb: 20 });
  if (path === "/api/schedule/jobs") return json({ jobs: [], enabled: false });
  if (path === `/api/sessions/${sid}/attachments`) return json({ attachments: [] });
  if (path === `/api/sessions/${sid}/browser/icons`) return json({ icons: [] });
  if (path === "/api/browser/bookmarks" && method === "GET") return json({ source: "pogo_bookmarks", scope: "account", status: "ready", account_id: "fixture-account", revision: 1, bookmarks: [], total: 0 });
  if (path === "/api/browser/migrate-urls") return json({ mappings: [] });
  if (path === `/api/sessions/${sid}/stream`) {
    fixture.streamsOpened++;
    const encoder = new TextEncoder();
    const stream = new ReadableStream<Uint8Array>({ start(controller) {
      let stopped = false;
      fixture.emit = event => { if (!stopped) controller.enqueue(encoder.encode(`data: ${JSON.stringify(event)}\n\n`)); };
      fixture.emit({ type: "snapshot", seq: 1, ts: Date.now(), snapshot: { seq: 1, messages: [
        { id: "fixture-u1", role: "user", content: [{ type: "text", text: "请保留这段虚构会话，用于输入框验收。" }] },
        { id: "fixture-a1", role: "assistant", content: [{ type: "text", text: "这个页面仅连接内存模拟数据，可以验证命令菜单与键盘补全。" }] },
      ], live: emptyLive(), context_tokens: 35678, run: { active: false, model, context_window: 100000 }, policy: { mode: "standard" } } });
      const timer = setInterval(() => { if (!stopped) controller.enqueue(encoder.encode(": fixture keepalive\n\n")); }, 10000);
      options.signal?.addEventListener("abort", () => { stopped = true; clearInterval(timer); fixture.streamsClosed++; controller.error(new DOMException("Aborted", "AbortError")); }, { once: true });
    } });
    return new Response(stream, { headers: { "Content-Type": "text/event-stream" } });
  }
  if (path === `/api/sessions/${sid}/messages` && method === "POST") {
    fixture.messages.push(body);
    if (fixture.holdMessages) await new Promise<void>(resolve => { fixture.resolveMessage = resolve; });
    if (fixture.failMessages) return json({ error: "Synthetic message failure" }, 503);
    return json({ run_id: `synthetic-run-${fixture.messages.length}` });
  }
  if (path === `/api/sessions/${sid}/control` && method === "POST") {
    fixture.controls.push(body);
    if (body.action === "steer") return json({ steered: fixture.steered });
    if (body.action === "compact") { fixture.compactCalls++; return json({ compacted: true, before: 2, before_tokens: 1000, after_tokens: 300, freed_tokens: 700, summary: "Fixture summary" }); }
    return json({ error: "Unsupported synthetic control" }, 501);
  }
  if (path === `/api/sessions/${sid}/browser`) return query.has("browser") ? json(browser) : json({ backend: "unavailable", error: "No browser is needed for the isolated composer fixture" });
  if (path === `/api/sessions/${sid}/browser/live-ticket`) return json({ ticket: "synthetic-composer-ticket", expires_in: 30 });
  fixture.denied.push(`${method} ${path}`);
  return json({ error: "Unsupported isolated fixture operation" }, 501);
};

// Only the optional blank browser used for immersive layout has a synthetic
// socket. Its state is real-protocol shaped; it sends no page image or input.
const NativeWebSocket = window.WebSocket;
class FixtureSocket {
  readyState = 1; bufferedAmount = 0;
  onmessage: ((event: { data: string }) => unknown) | null = null;
  onclose: (() => unknown) | null = null; onerror: (() => unknown) | null = null;
  constructor() { setTimeout(() => this.deliver({ type: "ready", protocol: 1, state: browser }), 20); }
  deliver(value: unknown) { if (this.readyState === 1) this.onmessage?.({ data: JSON.stringify(value) }); }
  send() {}
  close() { this.readyState = 3; this.onclose?.(); }
}
window.WebSocket = Object.assign(function (url: string | URL, protocols?: string | string[]) {
  const parsed = new URL(url, location.href);
  if (parsed.pathname.startsWith("/api/")) {
    if (parsed.pathname !== `/api/sessions/${sid}/browser/live` || parsed.host !== location.host) { fixture.denied.push("unexpected fixture WebSocket"); throw new Error("External fixture socket forbidden"); }
    return new FixtureSocket();
  }
  if (parsed.host !== location.host) { fixture.denied.push("external WebSocket"); throw new Error("External fixture socket forbidden"); }
  return new NativeWebSocket(url, protocols);
}, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 }) as unknown as typeof WebSocket;

// The real composer's progress-aware upload uses XHR. This fixture accepts
// only its own attachment route and keeps every byte in memory, including
// deterministic failure used to check command+attachment draft restoration.
class FixtureUploadXHR {
  status = 0; responseText = "";
  onload: (() => unknown) | null = null; onerror: (() => unknown) | null = null;
  upload = { onprogress: null as ((event: { lengthComputable: boolean; loaded: number }) => unknown) | null };
  method = ""; url = "";
  open(method: string, url: string) { this.method = method; this.url = url; }
  setRequestHeader() {}
  send(form: FormData) {
    const url = new URL(this.url, location.href), file = form.get("file");
    if (this.method !== "POST" || url.origin !== location.origin || url.pathname !== `/api/sessions/${sid}/attachments` || !(file instanceof File)) {
      fixture.denied.push("Unexpected fixture XHR");
      this.onerror?.();
      return;
    }
    fixture.requests.push({ path: url.pathname, query: url.search, method: "POST", body: { name: file.name, size: file.size } });
    fixture.uploads.push({ name: file.name, size: file.size });
    queueMicrotask(() => {
      this.upload.onprogress?.({ lengthComputable: true, loaded: file.size });
      this.status = fixture.failUploads ? 503 : 200;
      this.responseText = JSON.stringify(fixture.failUploads
        ? { error: "Synthetic attachment upload failure" }
        : { path: `uploads/${file.name}`, name: file.name, kind: "text", mime: file.type, size: file.size, created: "2026-10-09T00:00:00Z" });
      this.onload?.();
    });
  }
}
window.XMLHttpRequest = FixtureUploadXHR as unknown as typeof XMLHttpRequest;

const app = createApp(AgentView, { studioAgentId: aid, managedChat: true }).use(i18n).use(ElementPlus);
fixture.dispose = () => app.unmount();
app.mount("#app");
// Test-only access to the real pick handler. The fixture's empty browser does
// not resolve an element; this creates a fictional already-picked exhibit to
// verify command editing preserves the existing composer-chip contract.
fixture.addPick = pick => {
  const state = (app as unknown as { _instance?: { setupState?: { onElementPick?: (pick: ElementPick) => void } } })._instance?.setupState;
  if (!state?.onElementPick) throw new Error("Composer fixture pick handler unavailable");
  state.onElementPick(pick);
};
