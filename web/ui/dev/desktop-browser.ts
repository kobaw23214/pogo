import { createApp, h, reactive } from "vue";
import ElementPlus from "element-plus";
import { i18n } from "../src/i18n";
import BrowserPane from "../src/components/BrowserPane.vue";
import DockArea from "../src/components/DockArea.vue";
import { api } from "../src/api/client";
import type { DesktopBrowserBinding, DesktopBrowserBridge, DesktopBrowserEvent } from "../src/agent/desktopBrowser";
import type { NativeBrowserState } from "../src/agent/nativeBrowser";
import type { ElementPick } from "../src/agent/elementPicker";
import "element-plus/dist/index.css";
import "../src/styles.scss";

// Presentation contract fixture only. It does not emulate native OS input.
const context = `ctx_${"b".repeat(24)}`;
const dockMode = new URLSearchParams(location.search).has("dock");
const desktopOnly = new URLSearchParams(location.search).get("display") === "desktop";
type FixturePage = { id: string; url: string; title: string; doc: number; history?: { url: string; title: string }[] };
const model = reactive({ active: dockMode ? "t2" : "t1", dockOpen: false, control: "agent" as "agent" | "human", epoch: 1, generation: 1, token: "", tabs: [{ id: "t1", url: "about:blank", title: "Blank", doc: 1 }, { id: "t2", url: "https://fixture.invalid/b", title: "Page B", doc: 1 }] as FixturePage[] });
const counters = { frames: 0, states: 0, inputs: [] as unknown[], controls: [] as unknown[], updates: [] as Parameters<DesktopBrowserBridge["update"]>[0][], detached: [] as string[], picker: [] as Parameters<DesktopBrowserBridge["setPicker"]>[0][], tools: [] as string[] };
const picks: ElementPick[] = [];
const surfaces = new Map<string, DesktopBrowserBinding>();
const listeners = new Set<(event: DesktopBrowserEvent) => void>();
let nextSurface = 0;
let holdControl = false;
let releaseControl: (() => void) | undefined;
function state(tab = model.active): NativeBrowserState {
  const page = model.tabs.find(page => page.id === tab)!;
  return { backend: "native", ...(desktopOnly ? { display_mode: "desktop" as const } : {}), control: model.control, context_ref: context, viewed_tab: tab, active_tab: "t1", control_epoch: model.epoch, generation: model.generation, url: page.url, title: page.title, can_go_back: (page.history?.length ?? 0) > 1, tabs: model.tabs.map(({ id, url, title }) => ({ id, url, title })), viewport: { width: 900, height: 520 }, version: `${model.generation}:${tab}:${page.doc}:0`, frame_identity: `${model.generation}:${tab}:${page.doc}`, page_loading: false };
}
api.browserState = async (_session, _signal, target) => { counters.states++; return state(target?.tab_id); };
api.browserFrame = async () => { counters.frames++; throw new Error("Desktop surface must never fetch JPEG"); };
api.browserControl = async (_session, action, options) => {
  counters.controls.push({ action, ...options }); model.control = action; model.epoch++; model.generation++; model.token = action === "human" ? `lease-${model.epoch}` : "";
  const reply = { ...state(), ...(model.token ? { lease_token: model.token } : {}) };
  if (holdControl) { holdControl = false; await new Promise<void>(resolve => { releaseControl = resolve; }); }
  return reply;
};
api.browserInput = async (_session, input, token, target) => {
  if (token !== model.token || model.control !== "human") throw new Error("No human lease");
  counters.inputs.push(structuredClone(input));
  if (input.type !== "navigate" && input.type !== "back" && input.type !== "dialog") throw new Error("Native input must not be forwarded");
  const page = model.tabs.find(page => page.id === target!.tab_id)!;
  if (input.type === "navigate") { page.history ??= [{ url: page.url, title: page.title }]; page.url = input.url; page.title = input.url; page.history.push({ url: page.url, title: page.title }); page.doc++; }
  if (input.type === "back" && (page.history?.length ?? 0) > 1) { page.history!.pop(); Object.assign(page, page.history!.at(-1)); page.doc++; }
  return state(target!.tab_id);
};
const bridge: DesktopBrowserBridge = {
  version: 1,
  async attach(binding) { const surface_id = `surface-${++nextSurface}`; surfaces.set(surface_id, binding); return { surface_id }; },
  async update(request) { counters.updates.push(structuredClone(request)); return { interactive: request.visible && !request.blocked && request.lease_token === model.token && request.control_epoch === model.epoch && request.generation === model.generation && model.control === "human" }; },
  async detach({ surface_id }) { counters.detached.push(surface_id); surfaces.delete(surface_id); },
  async openDevTools({ surface_id }) { counters.tools.push(surface_id); },
  async setPicker(request) { counters.picker.push(request); },
  onEvent(listener) { listeners.add(listener); return () => listeners.delete(listener); },
};
if (!new URLSearchParams(location.search).has("noBridge")) Object.defineProperty(window, "pogoDesktopBrowser", { value: Object.freeze(bridge), configurable: false, writable: false });
const app = createApp({ setup() { return () => h("main", { style: "height:760px;max-width:1100px;margin:auto;padding:12px;display:flex;flex-direction:column" }, [
  h("header", { style: "display:flex;gap:12px;margin-bottom:10px" }, [h("strong", "Desktop bridge contract fixture"), ...model.tabs.map(tab => h("button", { "data-sheet": tab.id, onClick: () => { model.active = tab.id; } }, tab.id))]),
  ...(dockMode ? [h("div", { id: "qa-dock-layout", style: "display:flex;flex:1;min-height:0;overflow:hidden" }, [h("div", { style: "flex:1;min-width:0" }, "真实 DockArea 的宽度过渡"), h(DockArea, { active: model.dockOpen ? "hub" : null, activeTab: model.active, tabs: model.tabs.map(tab => ({ id: tab.id, kind: "browser" as const, label: tab.title, browserBinding: { sessionId: "desktop-fixture", context_ref: context, tab_id: tab.id }, closable: true })), panels: [], liveTabs: {}, maximized: false, layout: "right", items: [], sessionId: "desktop-fixture", "onUpdate:active": value => { model.dockOpen = value !== null; }, "onUpdate:activeTab": value => { model.active = value; } })])] : model.tabs.map(tab => h("div", { key: tab.id, style: { display: tab.id === model.active ? "flex" : "none", flex: 1, minHeight: 0, flexDirection: "column" } }, [h(BrowserPane, { sessionId: "desktop-fixture", binding: { context_ref: context, tab_id: tab.id }, active: tab.id === model.active, title: tab.title, onPick: (pick: ElementPick) => picks.push(pick) })])))
]); } });
app.use(i18n); app.use(ElementPlus); app.mount("#app");
(window as unknown as { __desktopQA: unknown }).__desktopQA = {
  counters, picks, model, state, surfaces,
  emit: (event: DesktopBrowserEvent) => listeners.forEach(listener => listener(event)),
  surfaceFor: (tab: string) => [...surfaces].find(([, binding]) => binding.tab_id === tab)?.[0],
  holdNextControl: () => { holdControl = true; }, releaseControl: () => { releaseControl?.(); releaseControl = undefined; },
  dispose: () => app.unmount(),
};
