import { readFileSync } from "node:fs";
import { compile } from "@vue/compiler-dom";
import { parse } from "@vue/compiler-sfc";
import ts from "typescript";
import * as Vue from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRenderer, createSSRApp, h, nextTick, reactive, ref, type App, type Component } from "vue";
import { renderToString } from "vue/server-renderer";
import type { WebServiceGrant, WebServiceRecord } from "@/api/webServices";

const pathURL = `http://192.168.1.10:19770/_pogo/web/${"a".repeat(48)}/`;
const mock = vi.hoisted(() => ({ current: null as unknown as ReturnType<typeof makeState>, success: vi.fn(), error: vi.fn() }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/i18n", () => ({ gt: (key: string) => key }));
vi.mock("element-plus", () => ({ ElMessage: { success: mock.success, error: mock.error } }));
vi.mock("@/agent/useWebServicePreview", () => ({ useWebServicePreview: () => mock.current }));
import WebServicePane from "./WebServicePane.vue";
function makeState() {
  return {
    record: ref<WebServiceRecord | null>({ service_id: "ws_one", generation: 1, revision: 1, name: "网站计数器", slot_name: "counter", lease_id: "lease_one", state: "ready", profile: "node-web", project_root: "app", lease_expires_at: "2099-01-01T02:00:00Z" }),
    grant: ref<WebServiceGrant | null>({ service_id: "ws_one", generation: 1, grant_id: "grant_one", preview_url: pathURL, grant_expires_at: "2099-01-01T00:30:00Z", lease_expires_at: "2099-01-01T02:00:00Z", browser_url: null, limitations: [] }),
    viewVersion: ref(0), issue: ref(""), closeIssue: ref(""), loading: ref(false), closing: ref(false), stale: ref(false), now: ref(Date.now()),
    refresh: vi.fn(), open: vi.fn(), prepareClose: vi.fn(),
  };
}
const apps: App[] = [];
beforeEach(() => { mock.current = makeState(); mock.success.mockReset(); mock.error.mockReset(); });
afterEach(() => { for (const app of apps.splice(0)) app.unmount(); targets.clear(); roots.splice(0); vi.unstubAllGlobals(); });
const html = () => renderToString(createSSRApp({ render: () => h(WebServicePane, { sessionId: "session_one", serviceId: "ws_one", active: true }) }));

describe("managed website page", () => {
  it("keeps the current iframe and browser action usable through a temporary storage warning", async () => {
    const onOpenBrowser = vi.fn(), root = mount({ onOpenBrowser }), frame = find(root, "iframe");
    mock.current.record.value = { ...mock.current.record.value!, revision: 2, storage_warning: { code: "STORAGE_CHECK_UNAVAILABLE", message: "managed storage usage could not be verified", retryable: true } };
    await nextTick();
    expect(find(root, "iframe")).toBe(frame);
    const warning = flat(root).find(n => n.props["data-testid"] === "website-storage-warning")!;
    expect(warning.props.role).toBe("status"); expect(text(warning)).toBe("webServices.storageCheckRetrying");
    expect(text(root)).not.toContain("managed storage usage could not be verified");
    const browser = flat(root).find(n => n.props["data-testid"] === "website-open-browser")!;
    expect(browser.props.disabled).toBe(false);
    await (browser.props.onClick as () => Promise<void>)(); expect(onOpenBrowser).toHaveBeenCalledOnce();
    mock.current.record.value = { ...mock.current.record.value!, revision: 3, storage_warning: undefined }; await nextTick();
    expect(find(root, "iframe")).toBe(frame);
    expect(flat(root).some(n => n.props["data-testid"] === "website-storage-warning")).toBe(false);
    expect(mock.current.open).not.toHaveBeenCalled();
  });
  it("does not describe a stopped website as still running when its storage warning is retained", async () => {
    mock.current.record.value = { ...mock.current.record.value!, state: "stopped", storage_warning: { code: "STORAGE_CHECK_UNAVAILABLE", message: "temporary" } };
    const output = await html();
    expect(output).toContain("webServices.storageCheckUnavailable"); expect(output).not.toContain("webServices.storageCheckRetrying");
    expect(output).not.toContain("<iframe");
  });
  it("shows the service failure state and its cause instead of an invitation to open it", async () => {
    mock.current.record.value = { ...mock.current.record.value!, state: "failed", last_error: { code: "SERVICE_EXITED", message: "The website process exited." } };
    mock.current.grant.value = null;
    const output = await html();
    expect(output).toContain("webServices.state_failed"); expect(output).toContain("The website process exited.");
    expect(output).not.toContain("webServices.previewIdle"); expect(output).not.toContain("<iframe");
  });
  it("supplies live service metadata to extra actions in the same toolbar", async () => {
    const root = mount({}, false, { actions: ({ record }: { record: WebServiceRecord | null }) => h("button", { "data-testid": "inline-dock-action" }, record?.name) });
    const frame = find(root, "iframe");
    expect(flat(root).filter(n => n.tag === "header")).toHaveLength(1);
    expect(text(flat(root).find(n => n.props["data-testid"] === "inline-dock-action")!)).toBe("网站计数器");
    mock.current.record.value = { ...mock.current.record.value!, name: "新网站名称", revision: 2 }; await nextTick();
    expect(text(flat(root).find(n => n.props["data-testid"] === "inline-dock-action")!)).toBe("新网站名称");
    expect(find(root, "iframe")).toBe(frame);
  });
  it("renders the stable website URL in an opaque-origin iframe and a safe external link", async () => {
    const output = await html();
    expect(output).toContain(`src="${pathURL}"`); expect(output).toContain(`href="${pathURL}"`);
    expect(output).toContain('target="_blank"'); expect(output).toContain('rel="noopener noreferrer"');
    expect(output).toContain('sandbox="allow-scripts allow-forms"'); expect(output).toContain('referrerpolicy="no-referrer"');
    expect(output).not.toContain("allow-same-origin"); expect(output).not.toContain("allow-top-navigation");
  });
  it("shows the website name and one service deadline, without the obsolete link expiry", async () => {
    const output = await html();
    expect(output).toContain("网站计数器"); expect(output).toContain("2099-01-01T02:00:00Z");
    expect(output).toContain("webServices.serviceLease"); expect(output).toContain("websiteSettings.copyAddress");
    expect(output).not.toContain("2099-01-01T00:30:00Z"); expect(output).not.toContain("webServices.previewLease");
    expect(output).toContain("webServices.closeHint");
  });
  it("recreates the iframe for a recovered generation while retaining its URL", async () => {
    const root = mount(); const first = find(root, "iframe");
    mock.current.record.value = { ...mock.current.record.value!, generation: 2, revision: 2 };
    mock.current.grant.value = { ...mock.current.grant.value!, generation: 2 }; await nextTick();
    const next = find(root, "iframe"); expect(next).not.toBe(first); expect(next.props.src).toBe(pathURL);
  });
  it("reloads an explicitly refreshed website even when URL and generation stay unchanged", async () => {
    const root = mount(); const first = find(root, "iframe");
    mock.current.viewVersion.value++; await nextTick();
    const reloaded = find(root, "iframe"); expect(reloaded).not.toBe(first); expect(reloaded.props.src).toBe(pathURL);
    expect(mock.current.record.value?.generation).toBe(1); expect(mock.current.grant.value?.grant_id).toBe("grant_one");
  });
  it("keeps the iframe mounted on renewal and updates only the displayed deadline", async () => {
    const root = mount(); const first = find(root, "iframe"); const deadline = "2099-01-02T02:00:00Z";
    mock.current.record.value = { ...mock.current.record.value!, revision: 2, lease_expires_at: deadline };
    mock.current.grant.value = { ...mock.current.grant.value!, grant_expires_at: deadline, lease_expires_at: deadline }; await nextTick();
    expect(find(root, "iframe")).toBe(first); expect(flat(root).some(n => String(n.props.title).includes(deadline))).toBe(true);
  });
  it("keeps the shared address visible during restart and reconnects when ready", async () => {
    const root = mount(); mock.current.record.value = { ...mock.current.record.value!, state: "starting", generation: 2, revision: 2 }; await nextTick();
    expect(flat(root).some(n => n.tag === "iframe")).toBe(false); expect(find(root, "a").props.href).toBe(pathURL);
    expect(text(root)).toContain("websiteSettings.updating");
    mock.current.record.value = { ...mock.current.record.value!, state: "ready", revision: 3 }; await nextTick();
    expect(find(root, "iframe").props.src).toBe(pathURL); expect(text(root)).not.toContain("websiteSettings.updating");
  });
  it("copies the displayed website URL", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined); vi.stubGlobal("navigator", { clipboard: { writeText } });
    const root = mount(); const button = flat(root).find(n => n.tag === "button" && n.props["aria-label"] === "websiteSettings.copyAddress")!;
    await (button.props.onClick as () => Promise<void>)();
    expect(writeText).toHaveBeenCalledExactlyOnceWith(pathURL); expect(mock.success).toHaveBeenCalledWith("websiteSettings.copied");
  });
  it("opens the stable website URL in the built-in browser without issuing a new grant", async () => {
    const onOpenBrowser = vi.fn(), root = mount({ onOpenBrowser });
    const button = flat(root).find(n => n.props["data-testid"] === "website-open-browser")!;
    await (button.props.onClick as () => Promise<void>)();
    expect(onOpenBrowser).toHaveBeenCalledExactlyOnceWith({ url: pathURL, title: "网站计数器", sessionId: "session_one", serviceId: "ws_one" });
    expect(mock.current.open).not.toHaveBeenCalled();
  });
  it("does not open an expired website in the built-in browser", async () => {
    mock.current.record.value = { ...mock.current.record.value!, lease_expires_at: "2000-01-01T00:00:00Z" };
    const onOpenBrowser = vi.fn(), root = mount({ onOpenBrowser });
    const button = flat(root).find(n => n.props["data-testid"] === "website-open-browser")!;
    expect(button.props.disabled).toBe(true); await (button.props.onClick as () => Promise<void>)();
    expect(onOpenBrowser).not.toHaveBeenCalled();
  });
  it("moves only the active preview controls into the shared tab row", async () => {
    const toolbar = node("shared-toolbar"); targets.set("#toolbar", toolbar);
    const props = reactive({ active: false, toolbarTarget: "#toolbar" }), root = mount(props);
    expect(flat(toolbar).some(n => n.tag === "header")).toBe(false);
    const frame = find(root, "iframe"); props.active = true; await nextTick();
    expect(flat(toolbar).filter(n => n.tag === "header")).toHaveLength(1);
    expect(flat(root).some(n => n.tag === "header")).toBe(false); expect(find(root, "iframe")).toBe(frame);
    props.active = false; await nextTick(); expect(flat(toolbar).some(n => n.tag === "header")).toBe(false);
    expect(find(root, "iframe")).toBe(frame);
  });
  it("waits for the first dock mount to attach the shared toolbar before teleporting", async () => {
    // A document query cannot find the earlier sibling while their shared
    // parent is still being constructed outside the mounted document.
    const root = mount({ toolbarTarget: "#attached-toolbar" }, true); await nextTick();
    const toolbar = flat(root).find(n => n.props.id === "attached-toolbar")!;
    expect(toolbar).toBeDefined(); expect(flat(toolbar).filter(n => n.tag === "header")).toHaveLength(1);
    expect(flat(toolbar).some(n => n.props["data-testid"] === "website-open-browser")).toBe(true);
    expect(find(root, "iframe").props.src).toBe(pathURL);
  });
  it("uses the refresh control to reload the current website", async () => {
    const root = mount(), button = flat(root).find(n => n.tag === "button" && n.props["aria-label"] === "webServices.reopen")!;
    await (button.props.onClick as () => Promise<void>)(); expect(mock.current.open).toHaveBeenCalledOnce();
  });
});

// Exercise the component's real template through Vue's renderer so a change in
// generation must replace the iframe node, rather than merely changing text.
interface HostNode { tag: string; text: string; props: Record<string, unknown>; children: HostNode[]; parent: HostNode | null }
const node = (tag: string, text = ""): HostNode => ({ tag, text, props: {}, children: [], parent: null });
const targets = new Map<string, HostNode>();
const roots: HostNode[] = [];
const renderer = createRenderer<HostNode, HostNode>({
  createElement: tag => node(tag), createText: value => node("text", value), createComment: value => node("comment", value),
  setText: (n, value) => { n.text = value; }, setElementText: (n, value) => { n.text = value; n.children = []; },
  patchProp: (n, key, _old, value) => { n.props[key] = value; }, setScopeId() {},
  insert(n, parent, anchor) { if (n.parent) n.parent.children.splice(n.parent.children.indexOf(n), 1); n.parent = parent; const i = anchor ? parent.children.indexOf(anchor) : -1; if (i < 0) parent.children.push(n); else parent.children.splice(i, 0, n); },
  remove(n) { if (n.parent) n.parent.children.splice(n.parent.children.indexOf(n), 1); n.parent = null; },
  parentNode: n => n.parent, nextSibling: n => n.parent?.children[n.parent.children.indexOf(n) + 1] ?? null,
  querySelector: selector => targets.get(selector) ?? roots.flatMap(flat).find(n => n.props.id === selector.slice(1)) ?? null,
});
const flat = (n: HostNode): HostNode[] => [n, ...n.children.flatMap(flat)];
const text = (n: HostNode): string => n.text + n.children.map(text).join("");
const find = (root: HostNode, tag: string) => { const found = flat(root).find(n => n.tag === tag); if (!found) throw new Error(`Missing ${tag}`); return found; };
function mount(extra: Record<string, unknown> = {}, attachToolbar = false, slots?: Record<string, (props: { record: WebServiceRecord | null }) => Vue.VNode>) {
  const template = parse(readFileSync(new URL("./WebServicePane.vue", import.meta.url), "utf8")).descriptor.template!.content;
  const code = ts.transpileModule(compile(template, { mode: "function", prefixIdentifiers: true, expressionPlugins: ["typescript"] }).code, { compilerOptions: { target: ts.ScriptTarget.ES2020 } }).outputText;
  const render = new Function("Vue", code)(Vue);
  const original = WebServicePane as unknown as { setup(props: unknown, context: unknown): Record<string, unknown> };
  const component = { ...WebServicePane, ssrRender: undefined, setup: (props: unknown, context: unknown) => ({ ...original.setup(props, context) }), render } as Component;
  const app = renderer.createApp({ render: () => {
    const pane = h(component, { sessionId: "session_one", serviceId: "ws_one", active: true, ...extra }, slots);
    return attachToolbar ? h("div", [h("div", { id: "attached-toolbar" }), pane]) : pane;
  } });
  app.provide(Vue.ssrContextKey, { modules: new Set() }); apps.push(app); const root = node("root"); roots.push(root); app.mount(root); return root;
}
