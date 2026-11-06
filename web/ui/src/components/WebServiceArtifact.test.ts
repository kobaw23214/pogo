import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRenderer, createSSRApp, defineComponent, h, nextTick, onMounted, onUnmounted, reactive, ref, ssrContextKey, type App, type Component } from "vue";
import { renderToString } from "vue/server-renderer";
import { webArtifactNavigationKey, type WebArtifactNavigation } from "@/agent/webArtifactNavigation";
import { clientComponent } from "./websiteComponentTestHarness";

const mock = vi.hoisted(() => ({ mounted: [] as { sessionId: string; serviceId: string; active: boolean; openRequest: number }[], unmounted: vi.fn() }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/i18n", () => ({ gt: (key: string) => key }));
vi.mock("./WebServicePane.vue", () => ({ default: defineComponent({
  props: { sessionId: { type: String, required: true }, serviceId: { type: String, required: true }, active: Boolean, openRequest: { type: Number, default: 0 } }, emits: ["openBrowser"],
  setup: (props, { slots, emit }) => {
    onMounted(() => mock.mounted.push({ sessionId: props.sessionId!, serviceId: props.serviceId!, active: props.active, openRequest: props.openRequest! }));
    onUnmounted(mock.unmounted);
    return () => h("div", { "data-pane": true }, [
      h("header", [slots.actions?.({ record: { name: "Renamed Website" } }), h("button", { "data-browser": true, onClick: () => emit("openBrowser", { sessionId: props.sessionId, serviceId: props.serviceId, title: "Renamed Website", url: "http://localhost:19770/_pogo/web/" + "a".repeat(48) + "/" }) })]),
      h("iframe", { "data-private-preview": true }),
    ]);
  },
}) }));
import WebServiceArtifact from "./WebServiceArtifact.vue";
import WebServicePane from "./WebServicePane.vue";

const source = '{"service_id":"ws_example","title":"Old title"}';
const apps: App[] = [];
beforeEach(() => { mock.mounted.splice(0); mock.unmounted.mockReset(); });
afterEach(() => { apps.splice(0).forEach(app => app.unmount()); vi.unstubAllGlobals(); });

describe("managed website artifact", () => {
  it("renders a lazy private card without creating a website view", async () => {
    const html = await renderToString(createSSRApp({ render: () => h(WebServiceArtifact, { source, scope: { sessionId: "chat_one" } }) }));
    expect(html).toContain("Old title"); expect(html).toContain("htmlArtifact.load"); expect(html).not.toContain("iframe");
    expect(mock.mounted).toEqual([]);
  });
  it("activates the existing owner-bound pane and keeps its actions in one bar", async () => {
    const { root, navigation } = mount();
    expect(mock.mounted).toEqual([]);
    await click(find(root, n => n.tag === "button" && content(n) === "htmlArtifact.load"));
    expect(mock.mounted).toEqual([{ sessionId: "chat_one", serviceId: "ws_example", active: true, openRequest: 1 }]);
    expect(flat(root).filter(n => n.tag === "header")).toHaveLength(1);
    await click(find(root, n => n.props["aria-label"] === "htmlArtifact.openDock"));
    expect(navigation.openDock).toHaveBeenCalledExactlyOnceWith({ sessionId: "chat_one", serviceId: "ws_example", label: "Renamed Website", open: true });
    await click(find(root, n => n.props["data-browser"] === true));
    expect(navigation.openBrowser).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ sessionId: "chat_one", serviceId: "ws_example", title: "Renamed Website" }));
  });
  it("lazily activates when the card enters the viewport", async () => {
    let enter!: (entries: { isIntersecting: boolean }[]) => void;
    vi.stubGlobal("IntersectionObserver", class { constructor(callback: typeof enter) { enter = callback; } observe() {} disconnect() {} });
    mount(); expect(mock.mounted).toEqual([]);
    enter([{ isIntersecting: false }]); await nextTick(); expect(mock.mounted).toEqual([]);
    enter([{ isIntersecting: true }]); await nextTick(); expect(mock.mounted).toHaveLength(1);
  });
  it("retires the old session's pane and refuses stale view actions", async () => {
    const { root, props, activeSession, navigation } = mount();
    await click(find(root, n => n.tag === "button" && content(n) === "htmlArtifact.load"));
    const oldBrowser = find(root, n => n.props["data-browser"] === true).props.onClick as () => void;
    activeSession.value = "chat_two";
    // Host scope changes immediately, even before the rendered prop catches up.
    oldBrowser(); await nextTick(); expect(navigation.openBrowser).not.toHaveBeenCalled();
    props.scope = null; await nextTick();
    expect(mock.unmounted).toHaveBeenCalledOnce(); expect(flat(root).some(n => n.tag === "iframe")).toBe(false);
    props.scope = { sessionId: "chat_two" }; await nextTick();
    expect(mock.mounted[mock.mounted.length - 1]).toEqual(expect.objectContaining({ sessionId: "chat_two", serviceId: "ws_example" }));
  });
  it("never creates a private pane from an invalid or unscoped saved fence", async () => {
    const missing = await renderToString(createSSRApp({ render: () => h(WebServiceArtifact, { source, scope: null }) }));
    expect(missing).toContain("webArtifact.unavailable"); expect(missing).not.toContain("iframe");
    const invalid = await renderToString(createSSRApp({ render: () => h(WebServiceArtifact, { source: '{"service_id":"ws_example","preview_url":"https://evil.invalid/"}', scope: { sessionId: "chat_one" } }) }));
    expect(invalid).toContain("webArtifact.invalid"); expect(mock.mounted).toEqual([]);
  });
});

interface Node { tag: string; text: string; props: Record<string, unknown>; children: Node[]; parent: Node | null }
const node = (tag: string, text = ""): Node => ({ tag, text, props: {}, children: [], parent: null });
const renderer = createRenderer<Node, Node>({
  createElement: tag => node(tag), createText: text => node("text", text), createComment: text => node("comment", text),
  setText: (n, text) => { n.text = text; }, setElementText: (n, text) => { n.text = text; n.children = []; }, setScopeId() {},
  patchProp: (n, key, _before, value) => { n.props[key] = value; },
  insert(n, parent, anchor) { if (n.parent) n.parent.children.splice(n.parent.children.indexOf(n), 1); n.parent = parent; const index = anchor ? parent.children.indexOf(anchor) : -1; if (index < 0) parent.children.push(n); else parent.children.splice(index, 0, n); },
  remove(n) { if (n.parent) n.parent.children.splice(n.parent.children.indexOf(n), 1); n.parent = null; }, parentNode: n => n.parent, nextSibling: n => n.parent?.children[n.parent.children.indexOf(n) + 1] ?? null,
});
const flat = (n: Node): Node[] => [n, ...n.children.flatMap(flat)];
const content = (n: Node): string => n.text + n.children.map(content).join("");
function find(root: Node, test: (n: Node) => boolean): Node { const result = flat(root).find(test); if (!result) throw new Error("Missing test node"); return result; }
async function click(n: Node) { await (n.props.onClick as () => unknown)(); await nextTick(); }
function mount() {
  const props = reactive<{ source: string; scope: { sessionId: string } | null }>({ source, scope: { sessionId: "chat_one" } });
  const activeSession = ref("chat_one");
  const navigation: WebArtifactNavigation = { scope: () => ({ sessionId: activeSession.value }), openDock: vi.fn(), openBrowser: vi.fn() };
  const Client = clientComponent(WebServiceArtifact as Component, "./WebServiceArtifact.vue");
  const app = renderer.createApp({ render: () => h(Client, props) });
  app.provide(webArtifactNavigationKey, navigation); app.provide(ssrContextKey, { modules: new Set() });
  app.component("WebServicePane", WebServicePane);
  app.component("Monitor", defineComponent({ setup: () => () => h("span") }));
  app.component("el-icon", defineComponent({ setup: (_, { slots }) => () => h("span", slots.default?.()) }));
  const root = node("root"); apps.push(app); app.mount(root);
  return { root, props, activeSession, navigation };
}
