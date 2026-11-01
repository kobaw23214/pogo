import { createApp, defineComponent, h, nextTick, onMounted, ref } from "vue";
import type { Composer } from "vue-i18n";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import "../src/styles.scss";
import AgentView from "../src/views/AgentView.vue";
import BrowserPane from "../src/components/BrowserPane.vue";
import { i18n } from "../src/i18n";
import { applyTheme, THEMES } from "../src/theme";
import { emptyLive } from "../src/api/types";
import { SHEET_KEY, TABS_KEY } from "../src/components/dockSheets";
import { BOOKMARKS_KEY, BROWSER_VIEW_ID, LEGACY_CARDS_KEY } from "../src/agent/browserBookmarks";
import type { NativeBrowserState } from "../src/agent/nativeBrowser";

// Real AgentView/DockArea, entirely fictional memory responses. No backend,
// model, current user session or existing browser is contacted.
(i18n.global as unknown as Composer).locale.value = "zh-CN";
const fixtureQuery = new URLSearchParams(location.search);
applyTheme(fixtureQuery.get("theme") === "dark" ? THEMES.find(theme => theme.mode === "dark")!.id : "paper", false);
const sid = "immersive-fixture";
const tab = { id: BROWSER_VIEW_ID, kind: "browser", label: "演示浏览器", closable: true, url: "https://example.invalid/workbench" };
localStorage.setItem(SHEET_KEY, "hub");
localStorage.setItem(TABS_KEY, JSON.stringify({ open: [tab], active: tab.id }));
localStorage.setItem(`${TABS_KEY}:native-only:v1`, "done");
if (fixtureQuery.has("migration")) {
  localStorage.removeItem(`${TABS_KEY}:native-only:v1`);
  localStorage.removeItem(`${BOOKMARKS_KEY}:legacy-imported`);
  localStorage.setItem(LEGACY_CARDS_KEY, JSON.stringify([
    { id: "known", title: "原系统", url: "/panels/site/app?q=1#detail" },
    { id: "missing", title: "待修复系统", url: "/panels/missing/path#detail" },
  ]));
  localStorage.setItem(TABS_KEY, JSON.stringify({ open: [
    { ...tab, id: "browser:1" }, { id: "panel:missing", label: "旧系统页签", kind: "panel", path: "/panels/missing/", closable: true },
  ], active: "browser:1" }));
}
localStorage.setItem("pogo:files-layout", fixtureQuery.get("layout") === "bottom" ? "bottom" : "right");
localStorage.setItem(BOOKMARKS_KEY, JSON.stringify([{ id: "qa-bookmark", title: "测试资料站", url: "https://reference.invalid/guide" }]));
const state: NativeBrowserState = {
  backend: "native", control: "agent", control_epoch: 0, generation: 1, document_version: 1,
  version: "fixture:1", frame_identity: "1:t1:1", page_loading: false, busy: false, url: fixtureQuery.has("blank") ? "about:blank" : tab.url, title: "研究工作台 · 内存演示",
  tabs: [{ id: "t1", url: tab.url, title: "研究工作台 · 内存演示" }], active_tab: "t1", viewport: { width: 1280, height: 800 },
};
type FixturePickPlan = { delay?: number; hold?: boolean; status?: number; empty?: boolean; reply?: Record<string, unknown> };
const pendingPicks = new Map<number, () => void>();
const fixture = {
  inputReplies: 0, frameReplies: 0,
  requests: [] as { method: string; path: string }[], inputs: [] as Record<string, unknown>[], controls: 0,
  streamsOpened: 0, streamsClosed: 0, denied: [] as string[], state: () => ({ ...state }),
  stateDelay: Number(fixtureQuery.get("stateDelay") || 0), frameDelay: Number(fixtureQuery.get("frameDelay") || 0), frameIdentity: "", navigationDelay: 0, navigationPrecommitDelay: 0, staleInputReply: false,
  offline: false, frameViewOnly: false, frameStatus: 200, frameError: "page changed while capturing frame; request a new frame", inputDelay: 0,
  picks: [] as Record<string, unknown>[], pickResults: [] as { id: number; status: number }[],
  pickPlans: [] as FixturePickPlan[], pickDelay: 0, pickSingleFlight: false,
  // Pick replies intentionally may arrive after AbortSignal cancellation: the
  // UI must fence stale responses, not rely solely on aborting fetch.
  releasePick: (id: number) => { pendingPicks.get(id)?.(); pendingPicks.delete(id); },
  releaseAllPicks: () => { for (const release of pendingPicks.values()) release(); pendingPicks.clear(); },
  patch: (value: Partial<NativeBrowserState>) => {
    Object.assign(state, structuredClone(value));
    if (("active_tab" in value || "generation" in value || "document_version" in value) && !("frame_identity" in value)) state.frame_identity = `${state.generation}:${state.active_tab}:${state.document_version}`;
  },
  emit: (_event: Record<string, unknown>) => {},
};
Object.assign(window, { __immersiveFixture: fixture });
const canvas = document.createElement("canvas");
canvas.width = 1280; canvas.height = 800;
const paint = canvas.getContext("2d")!;
paint.fillStyle = "#f5f7fa"; paint.fillRect(0, 0, 1280, 800);
paint.fillStyle = "#142c40"; paint.fillRect(0, 0, 1280, 74);
paint.font = "600 23px system-ui"; paint.fillStyle = "#fff"; paint.fillText("Atlas · Research workspace", 32, 46);
paint.font = "15px system-ui"; paint.fillStyle = "#c6d8e6"; paint.fillText("example.invalid · in-memory fixture", 850, 44);
paint.fillStyle = "#18364a"; paint.font = "600 36px system-ui"; paint.fillText("A clear view of the work ahead", 48, 143);
paint.font = "18px system-ui"; paint.fillStyle = "#697c8a"; paint.fillText("原站画面模拟 · 沉浸切换不会执行真实网页操作", 48, 183);
for (let i = 0; i < 3; i++) {
  const x = 48 + i * 393;
  paint.fillStyle = "#fff"; paint.fillRect(x, 225, 370, 225);
  paint.strokeStyle = "#dee6ec"; paint.strokeRect(x, 225, 370, 225);
  paint.font = "17px system-ui"; paint.fillStyle = "#69808e"; paint.fillText(["Reading queue", "Open questions", "Completed checks"][i], x + 24, 268);
  paint.font = "600 54px system-ui"; paint.fillStyle = "#285773"; paint.fillText(["12", "4", "28"][i], x + 24, 345);
  paint.font = "16px system-ui"; paint.fillStyle = "#708491"; paint.fillText("Updated just now", x + 24, 417);
}
paint.fillStyle = "#fff"; paint.fillRect(48, 481, 1156, 235);
paint.font = "600 22px system-ui"; paint.fillStyle = "#18364a"; paint.fillText("Recent activity", 72, 525);
paint.font = "17px system-ui";
["Review the document structure", "Keep the original browser session", "Draft stays with the conversation"].forEach((t, i) => paint.fillText(t, 72, 574 + 47 * i));
const jpeg = await new Promise<Blob>(resolve => canvas.toBlob(blob => resolve(blob!), "image/jpeg", .9));
const messages = [
  { id: "u1", role: "user", content: [{ type: "text", text: "查看当前网页，整理研究进度。我希望同时看到浏览器和对话。" }] },
  { id: "a1", role: "assistant", content: [{ type: "text", text: "当前工作台有 **12 篇待阅读资料**、**4 个未决问题**，已完成 **28 项检查**。\n\n你可以进入沉浸模式，把对话放在页面任意位置。这里的网页、会话和输入均为内存演示，不连接真实服务。" }] },
];
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const originalFetch = window.fetch.bind(window);
window.fetch = async (input, options = {}) => {
  const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url, location.href);
  if (!url.pathname.startsWith("/api/")) {
    if (url.origin !== location.origin) throw new Error("External requests are disabled in this fixture");
    return originalFetch(input, options);
  }
  const method = options.method ?? "GET";
  const path = url.pathname;
  fixture.requests.push({ method, path });
  if (path === "/api/auth/status") return json({ mode: "password", password: true, user: "Fixture viewer", admin: false });
  if (path === "/api/sessions/activity") return json({ busy: [] });
  if (path === "/api/sessions" && method === "GET") return json({ cwd: "/demo/workspace", sessions: [{ id: sid, title: "原站研究与对话", cwd: "/demo/workspace", model: "demo", provider: "fixture", messages: 2, updated: Date.now(), created: Date.now() }] });
  if (path === "/api/models") return json({ default: "demo", models: [{ id: "demo", name: "内存演示", provider: "fixture", context_window: 100000 }] });
  if (path === "/api/skills") return json({ skills: [] });
  if (path === "/api/browser/migrate-urls") {
    const { urls } = JSON.parse(String(options.body));
    return json({ mappings: urls.map((url: string) => ({ url, ...(url.startsWith("/panels/site/") ? { resolved_url: url.replace("/panels/site", "https://site.invalid") } : { error: "Legacy registration missing" }) })) });
  }
  if (path === "/api/panels") return json({ panels: [], auto_register: false });
  if (path === "/api/starters") return json({ starters: null });
  if (path === "/api/attachments/policy") return json({ allowed_exts: [".txt", ".md", ".pdf", ".png"], max_mb: 20 });
  if (path === "/api/schedule/jobs") return json({ jobs: [], enabled: false });
  if (path.endsWith("/attachments")) return json({ attachments: [] });
  if (path === `/api/sessions/${sid}/stream`) {
    fixture.streamsOpened++;
    const encoder = new TextEncoder();
    const stream = new ReadableStream<Uint8Array>({ start(controller) {
      let stopped = false;
      fixture.emit = event => { if (!stopped) controller.enqueue(encoder.encode(`data: ${JSON.stringify(event)}\n\n`)); };
      fixture.emit({ type: "snapshot", seq: 1, ts: Date.now(), snapshot: { seq: 1, messages, live: emptyLive(), run: { active: false, model: "demo", context_window: 100000 }, policy: { mode: "standard" } } });
      const timer = setInterval(() => { if (!stopped) controller.enqueue(encoder.encode(": fixture keepalive\n\n")); }, 10000);
      options.signal?.addEventListener("abort", () => { stopped = true; clearInterval(timer); fixture.streamsClosed++; controller.error(new DOMException("Aborted", "AbortError")); }, { once: true });
    } });
    return new Response(stream, { headers: { "Content-Type": "text/event-stream" } });
  }
  if (/^\/api\/sessions\/[^/]+\/browser$/.test(path)) {
    if (fixture.stateDelay) await new Promise(resolve => setTimeout(resolve, fixture.stateDelay));
    return fixtureQuery.has("unavailable") ? json({ backend: "unavailable", error: "Native browser service is not configured" }) : fixture.offline ? json({ error: "Fixture connection interrupted" }, 503) : json(state);
  }
  if (path.endsWith("/browser/frame")) {
    // Freeze the identity/pixels together; late replies intentionally survive
    // AbortSignal to exercise the viewer's own epoch and identity fences.
    const response = fixture.offline ? json({ error: "Fixture connection interrupted" }, 503) : fixture.frameStatus !== 200 ? json({ error: fixture.frameError }, fixture.frameStatus) : new Response(jpeg, { headers: { "Content-Type": "image/jpeg", "X-Browser-Frame-Identity": fixture.frameIdentity || state.frame_identity!, ...(fixture.frameViewOnly ? {} : { "X-Browser-Version": state.version! }) } });
    if (fixture.frameDelay) await new Promise(resolve => setTimeout(resolve, fixture.frameDelay));
    fixture.frameReplies++;
    return response;
  }
  if (path === `/api/sessions/${sid}/browser/pick` && method === "POST") {
    const body = JSON.parse(String(options.body));
    const id = fixture.picks.length;
    fixture.picks.push({ ...body, id });
    const plan = fixture.pickPlans.shift() ?? {};
    const stale = body.version !== state.version || body.tab_id !== state.active_tab;
    const invalid = !Number.isFinite(body.x) || !Number.isFinite(body.y) || body.x < 0 || body.y < 0 || body.x >= 1280 || body.y >= 800;
    const concurrent = fixture.pickSingleFlight && fixture.picks.length > fixture.pickResults.length + 1;
    const status = plan.status ?? (fixture.offline ? 503 : invalid ? 400 : stale || concurrent || state.busy || state.dialog ? 409 : 200);
    const index = Math.min(2, Math.max(0, Math.floor((body.x - 48) / 393)));
    const element = { ref: `p${index + 1}`, tag: "button", role: "button", name: ["阅读队列", "未决问题", "已完成检查"][index],
      text: ["Reading queue · 12", "Open questions · 4", "Completed checks · 28"][index],
      rect: { x: 48 + index * 393, y: 225, width: 370, height: 225 }, frame_url: state.url };
    // Freeze the envelope now so delayed replies really belong to the old page.
    const response = status === 200 ? structuredClone({ version: body.version, tab_id: body.tab_id, url: state.url, title: state.title,
      viewport: state.viewport, element: plan.empty ? null : element, ...plan.reply }) : { error: stale ? "stale fixture frame" : "Fixture pick unavailable" };
    if (plan.hold) await new Promise<void>(resolve => pendingPicks.set(id, resolve));
    else if (plan.delay ?? fixture.pickDelay) await new Promise(resolve => setTimeout(resolve, plan.delay ?? fixture.pickDelay));
    fixture.pickResults.push({ id, status });
    return json(response, status);
  }
  if (path.endsWith("/browser/control")) {
    const body = JSON.parse(String(options.body)); fixture.controls++;
    if (body.mode === "agent" && body.lease_token !== "fixture-lease") return json({ error: "bad fixture lease" }, 403);
    state.control = body.mode; state.control_epoch = (state.control_epoch ?? 0) + 1;
    return json({ ...state, ...(body.mode === "human" ? { lease_token: "fixture-lease" } : {}) });
  }
  if (path.endsWith("/browser/input")) {
    const body = JSON.parse(String(options.body));
    if (state.control !== "human" || body.lease_token !== "fixture-lease") return json({ error: "bad fixture lease" }, 403);
    if (fixture.offline) return json({ error: "Fixture connection interrupted" }, 503);
    if (state.dialog && body.type !== "dialog") return json({ error: "Resolve the fixture dialog first" }, 409);
    fixture.inputs.push(body);
    const oldReply = structuredClone(state);
    if (fixture.navigationDelay && body.type === "navigate") {
      state.page_loading = true;
      state.busy = true;
      if (fixture.navigationPrecommitDelay) await new Promise(resolve => setTimeout(resolve, fixture.navigationPrecommitDelay));
      state.url = body.url;
      state.document_version = (state.document_version ?? 0) + 1;
      state.frame_identity = `${state.generation}:${state.active_tab}:${state.document_version}`;
      state.version = `fixture:${state.document_version}`;
      await new Promise(resolve => setTimeout(resolve, fixture.navigationDelay));
      state.page_loading = false;
      state.busy = false;
    }
    if (fixture.inputDelay) await new Promise(resolve => setTimeout(resolve, fixture.inputDelay));
    if (body.type === "navigate") {
      if (!fixture.navigationDelay) {
        state.document_version = (state.document_version ?? 0) + 1;
        state.frame_identity = `${state.generation}:${state.active_tab}:${state.document_version}`;
        state.version = `fixture:${state.document_version}`;
      }
      state.url = body.url;
      const current = state.tabs.find(item => item.id === state.active_tab)!;
      current.url = body.url;
    } else if (body.type === "tab") {
      if (body.tab_action === "new") {
        const id = `t${Date.now()}`;
        state.tabs.push({ id, url: "about:blank", title: "新标签" }); state.active_tab = id;
      } else if (body.tab_action === "switch") state.active_tab = body.tab_id;
      else if (body.tab_action === "close") {
        state.tabs = state.tabs.filter(item => item.id !== body.tab_id);
        if (!state.tabs.length) state.tabs.push({ id: "t-empty", url: "about:blank", title: "新标签" });
        state.active_tab = state.tabs[0].id;
      }
      const current = state.tabs.find(item => item.id === state.active_tab)!;
      state.url = current.url; state.title = current.title;
      state.frame_identity = `${state.generation}:${state.active_tab}:${state.document_version}`;
    } else if (body.type === "dialog") delete state.dialog;
    fixture.inputReplies++;
    return json(fixture.staleInputReply ? oldReply : state);
  }
  fixture.denied.push(`${method} ${path}`);
  return json({ error: "This isolated fixture does not perform that API operation" }, 501);
};
const paneSession = ref<string | null>(sid);
const paneActive = ref(true);
Object.assign(fixture, { switchSession: (value: string | null) => { paneSession.value = value; }, setActive: (active: boolean) => { paneActive.value = active; } });
// Focused conversation controls fixture: actual production controls and hook,
// with an inert DOM webpage. It never starts a live browser or backend request.
let controlsFixture: ReturnType<typeof defineComponent> | undefined;
if (fixtureQuery.has("chat-controls")) {
  const [{ default: ImmersiveChatControls }, { useImmersiveBrowser }] = await Promise.all([
    import("../src/components/ImmersiveChatControls.vue"), import("../src/agent/useImmersiveBrowser"),
  ]);
  const styles = document.createElement("style");
  styles.textContent = `
    .chat-controls-fixture { position:relative; width:100vw; height:100dvh; display:grid; grid-template-columns:minmax(280px, 42%) 1fr; overflow:hidden; color:var(--el-text-color-primary); background:var(--el-bg-color); }
    .chat-controls-fixture > .main { display:flex; flex-direction:column; min-width:0; min-height:0; overflow:hidden; background:var(--el-bg-color); }
    .chat-controls-fixture > .dock { min-width:0; display:flex; flex-direction:column; height:100dvh; background:var(--el-fill-color-light); }
    .chat-controls-fixture .qa-page-toolbar { display:flex; align-items:center; gap:12px; padding:12px; border-bottom:1px solid var(--el-border-color); }
    .chat-controls-fixture .qa-page-toolbar button { padding:9px 14px; border:1px solid var(--el-border-color); border-radius:10px; color:inherit; background:var(--el-bg-color); }
    .chat-controls-fixture .native-page { min-height:0; flex:1; display:flex; align-items:center; justify-content:center; font:20px system-ui; background:linear-gradient(145deg,#dce8f0,#f8fbff); color:#264b63; }
    .chat-controls-fixture .native-page input { width:220px; padding:10px; border:1px solid #a7bfcf; border-radius:8px; }
    .chat-controls-fixture.browser-immersive { display:block; }
    .chat-controls-fixture.browser-immersive > .dock { position:absolute; inset:0; width:100%; }
    .chat-controls-fixture.browser-immersive > .main { container-type:inline-size; container-name:pogo-floating-chat; position:fixed; z-index:120; display:flex; border:1px solid var(--el-border-color); border-radius:20px; box-shadow:0 20px 72px #0004; }
    .chat-controls-fixture .immersive-chat-head { display:flex; align-items:center; gap:10px; min-height:60px; flex:0 0 auto; padding:0 13px 0 18px; border-bottom:1px solid var(--el-border-color-extra-light); cursor:grab; touch-action:none; user-select:none; }
    .chat-controls-fixture .immersive-status-dot { display:inline-block; width:7px; height:7px; flex:0 0 7px; border-radius:50%; background:var(--el-text-color-placeholder); }
    .chat-controls-fixture .immersive-chat-title { flex:1; min-width:0; display:flex; flex-direction:column; gap:4px; }
    .chat-controls-fixture .immersive-chat-title strong { font-size:13px; font-weight:550; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
    .chat-controls-fixture .immersive-chat-title > span { font-size:11px; color:var(--el-text-color-secondary); }
    .chat-controls-fixture .conversation { flex:1; min-height:0; overflow:auto; padding:14px 18px; }
    .chat-controls-fixture .conv-inner p { line-height:1.7; margin:0 0 18px; }
    .chat-controls-fixture .input-card { flex:0 0 auto; padding:14px; border-top:1px solid var(--el-border-color); }
    .chat-controls-fixture .input-card textarea { width:100%; min-height:76px; box-sizing:border-box; padding:12px; resize:none; font:14px system-ui; color:inherit; background:var(--el-fill-color-light); border:1px solid var(--el-border-color); border-radius:12px; }
    .chat-controls-fixture .immersive-chat-resize { position:absolute; bottom:0; right:0; width:20px; height:20px; border:0; background:transparent; color:inherit; cursor:nwse-resize; }
    .chat-controls-fixture .immersive-chat-restore { position:fixed; right:16px; bottom:20px; z-index:120; padding:15px 19px; border:1px solid var(--el-border-color); border-radius:16px; color:inherit; background:var(--el-bg-color); }
    @container pogo-floating-chat (max-width:400px) { .chat-controls-fixture .immersive-chat-head { flex-wrap:wrap; gap:10px; padding:12px 14px; } .chat-controls-fixture .immersive-chat-title { flex-basis:calc(100% - 24px); } .chat-controls-fixture .immersive-chat-actions { flex-basis:100%; } }
    @media(max-width:720px) { .chat-controls-fixture .immersive-chat-head { padding-left:14px; min-height:48px; } .chat-controls-fixture .immersive-chat-resize { display:none; } }
  `;
  document.head.append(styles);
  controlsFixture = defineComponent({
    setup() {
      const main = ref<HTMLElement | null>(null), draft = ref(""), title = ref("研究进度与浏览器会话");
      const immersive = useImmersiveBrowser(main);
      const focusedFixture = {
        setTitle: (value: string) => { title.value = value; },
        enter: () => immersive.enter(), exit: () => immersive.exit(),
        minimize: () => immersive.minimize(), restore: () => immersive.restore(),
        automaticRestore: () => immersive.restore(false),
        state: () => ({ active: immersive.active.value, minimized: immersive.minimized.value, chatVisible:immersive.chatVisible.value, restoreVisible:immersive.restoreVisible.value, animating:immersive.chatAnimating.value, motionPhase:immersive.chatMotionPhase.value, draft: draft.value }),
        tick: () => nextTick(),
      };
      Object.assign(window, { __chatControlsFixture: focusedFixture });
      onMounted(() => { Object.assign(focusedFixture, { ready: true }); });
      return () => h("section", { class: ["shell", "chat-controls-fixture", { "browser-immersive": immersive.active.value }] }, [
        h("main", { ref: main, id:"qa-floating-chat", inert:immersive.active.value && immersive.minimized.value, "aria-hidden":immersive.active.value && immersive.minimized.value ? "true" : undefined, "data-chat-motion":immersive.chatMotionPhase.value, class: ["main", { "floating-chat": immersive.active.value, manipulating: immersive.manipulating.value, "chat-animating": immersive.chatAnimating.value }], style: [immersive.style.value, !immersive.chatVisible.value ? { display:"none" } : {}] }, [
          immersive.active.value ? h("header", { class: "immersive-chat-head", tabindex:0, "aria-label":"移动会话窗口", onPointerdown: (event: PointerEvent) => immersive.begin(event), onKeydown: (event: KeyboardEvent) => immersive.adjustKey(event) }, [
            h("span", { class:"immersive-status-dot", "aria-hidden":"true" }),
            h("div", { class:"immersive-chat-title" }, [h("strong", { title:title.value }, title.value), h("span", "准备就绪")]),
            h(ImmersiveChatControls, { controlsId:"qa-floating-chat", onMinimize: () => immersive.minimize(), onExit: () => immersive.exit() }),
          ]) : h("header", { class:"qa-page-toolbar" }, "会话"),
          h("div", { class:"conversation" }, [h("div", { class:"conv-inner" }, Array.from({length:28}, (_unused,index) => h("p", { "data-mid":`qa-message-${index}` }, `历史消息 ${index + 1}。${"页面与对话保持各自的上下文，操作不改变已有草稿。".repeat(6)}`)))]),
          h("div", { class:"input-card" }, [h("textarea", { "data-testid":"composer-input", "aria-label":"会话输入", value:draft.value, onInput:(event:Event) => { draft.value=(event.target as HTMLTextAreaElement).value; } })]),
          immersive.active.value ? h("button", { class:"immersive-chat-resize", "aria-label":"调整会话窗口大小", onPointerdown:(event:PointerEvent) => immersive.begin(event,true), onKeydown:(event:KeyboardEvent) => immersive.adjustKey(event,true) }, "◢") : null,
        ]),
        h("section", { class:["dock", { immersive:immersive.active.value }], "aria-label":"受控布局网页" }, [h("div", { class:"qa-page-toolbar" }, [h("button", { class:"immersion-toggle", onClick:() => immersive.active.value ? immersive.exit() : immersive.enter() }, immersive.active.value ? "退出沉浸模式" : "沉浸模式")]), h("div", { class:"native-page", tabindex:0 }, [h("div", [h("p", "仅用于会话布局检查的静态网页"), h("input", { "data-testid":"page-input", "aria-label":"网页草稿", value:"网页独立输入" })])])]),
        immersive.restoreVisible.value ? h("button", { class:"immersive-chat-restore", "aria-label":"展开会话", "aria-controls":"qa-floating-chat", onClick:() => immersive.restore() }, "展开会话") : null,
      ]);
    },
  });
}
const app = createApp(controlsFixture ?? (fixtureQuery.has("pane-only") ? { render: () => h(BrowserPane, { sessionId: paneSession.value, active: paneActive.value, initialUrl: fixtureQuery.has("blank") ? "" : tab.url, title: "演示浏览器" }) } : AgentView)).use(i18n).use(ElementPlus);
Object.assign(fixture, { dispose: () => app.unmount() });
if (fixtureQuery.has("pane-only")) Object.assign(document.querySelector<HTMLElement>("#app")!.style, { display: "flex", height: "100dvh" });
app.mount("#app");
