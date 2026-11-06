import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { compile, createRenderer, createSSRApp, defineComponent, effectScope, h, nextTick, reactive, type Ref } from "vue";
import { renderToString } from "vue/server-renderer";
const host = vi.hoisted(() => ({ create: vi.fn(), revoke: vi.fn(), mounted: [] as (() => void)[], cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onMounted: (callback: () => void) => host.mounted.push(callback), onBeforeUnmount: (callback: () => void) => host.cleanup.push(callback), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/api/client", () => ({ api: { createHTMLPreview: host.create, revokeHTMLPreview: host.revoke } }));
import HTMLBrowserPane from "./HTMLBrowserPane.vue";

interface Props { path: string; sessionId: string; initialUrl?: string; initialId?: string; active: boolean; showToolbar?: boolean; toolbarTarget?: string; }
interface Controller { current: Ref<{ id: string; url: string } | null>; loading: Ref<boolean>; failed: Ref<string>; updated: Ref<boolean>; browserOpening: Ref<boolean>; settle(): void; reload(): Promise<void>; openBrowser(): Promise<void>; }
const firstId = "a".repeat(32), nextId = "b".repeat(32);
const url = (letter: string) => `/api/html-previews/${letter.repeat(64)}/index.html`;
const capability = (id = nextId, value: unknown = url("b")) => ({ id, url: value });
const nativeCapability = () => ({ ...capability(), browser_url: `http://pogo.internal${url("b")}` });
const scopes: ReturnType<typeof effectScope>[] = [];
async function flush() { for (let count = 0; count < 8; count++) await Promise.resolve(); await nextTick(); }
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done; }); return { promise, resolve }; }
function mount(extra: Partial<Props> = {}) {
  const props = reactive<Props>({ path: "reports/index.html", sessionId: "session-a", initialId: firstId, initialUrl: url("a"), active: true, ...extra });
  const emit = vi.fn(), scope = effectScope(); scopes.push(scope);
  const mountedStart = host.mounted.length, cleanupStart = host.cleanup.length;
  const controller = scope.run(() => (HTMLBrowserPane as unknown as { setup(props: Props, context: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  const mounted = host.mounted.slice(mountedStart), cleanup = host.cleanup.slice(cleanupStart);
  let closed = false;
  return { props, c: controller, emit,
    mounted: () => mounted.forEach(callback => callback()),
    close: () => { if (!closed) { closed = true; cleanup.forEach(callback => callback()); } },
  };
}
async function render(c: Controller, props: Props) {
  const component = { ...HTMLBrowserPane, setup: () => c };
  const app = createSSRApp({ render: () => h(component as Parameters<typeof h>[0], props) });
  app.component("el-icon", defineComponent({ setup: (_, { slots }) => () => h("span", slots.default?.()) }));
  return renderToString(app);
}
interface RuntimeNode { tag: string; props: Record<string, unknown>; children: RuntimeNode[]; parent: RuntimeNode | null; text?: string; }
function mountRuntime(extra: Partial<Props> = {}, attachToolbar = false) {
  const node = (tag: string, text?: string): RuntimeNode => ({ tag, props: {}, children: [], parent: null, text });
  const toolbar = node("shared-toolbar");
  const root = node("root");
  const allNodes = (parent: RuntimeNode): RuntimeNode[] => [parent, ...parent.children.flatMap(allNodes)];
  const insert = (child: RuntimeNode, parent: RuntimeNode, anchor: RuntimeNode | null = null) => {
    if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1);
    const index = anchor ? parent.children.indexOf(anchor) : -1;
    parent.children.splice(index < 0 ? parent.children.length : index, 0, child); child.parent = parent;
  };
  const renderer = createRenderer<RuntimeNode, RuntimeNode>({
    createElement: tag => node(tag), createText: text => node("#text", text), createComment: text => node("#comment", text),
    insert, remove: child => { if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1); child.parent = null; },
    setElementText: (element, text) => { element.children = []; element.text = text; },
    setText: (child, text) => { child.text = text; },
    parentNode: child => child.parent, nextSibling: child => child.parent?.children[child.parent.children.indexOf(child) + 1] ?? null,
    patchProp: (element, key, _oldValue, value) => { element.props[key] = value; },
    insertStaticContent: (text, parent, anchor) => { const child = node("#static", text); insert(child, parent, anchor); return [child, child]; },
    querySelector: selector => attachToolbar ? allNodes(root).find(child => child.props.id === selector.slice(1)) ?? null : selector === "#shared-toolbar" ? toolbar : null,
  });
  // Vitest's Vue transform supplies only ssrRender. Compile the actual template
  // for a runtime mount so Vue performs its real Boolean prop normalization.
  const template = readFileSync(new URL("./HTMLBrowserPane.vue", import.meta.url), "utf8").split("<template>")[1]!.split("</template>")[0]!;
  const source = HTMLBrowserPane as unknown as { setup(props: Props, context: unknown): object };
  const runtimeComponent = { ...HTMLBrowserPane, setup: (props: Props, context: unknown) => ({ ...source.setup(props, context) }), render: compile(template) };
  const props = reactive({ path: "reports/index.html", sessionId: "session-a", initialId: firstId, initialUrl: url("a"), active: true, ...extra });
  const app = renderer.createApp({ render: () => {
    const pane = h(runtimeComponent as unknown as Parameters<typeof h>[0], props);
    return attachToolbar ? h("div", [h("div", { id: "shared-toolbar" }), pane]) : pane;
  } });
  app.component("el-icon", defineComponent({ setup: (_, { slots }) => () => h("span", slots.default?.()) }));
  app.component("Refresh", defineComponent({ render: () => h("svg") }));
  app.mount(root);
  const actualToolbar = () => attachToolbar ? allNodes(root).find(child => child.props.id === "shared-toolbar")! : toolbar;
  return { props, find: (tag: string) => allNodes(root).filter(child => child.tag === tag), toolbar: (tag: string) => allNodes(actualToolbar()).filter(child => child.tag === tag), close: () => app.unmount() };
}
beforeEach(() => {
  vi.useFakeTimers(); host.create.mockReset().mockResolvedValue(capability()); host.revoke.mockReset().mockResolvedValue({ ok: true });
});
afterEach(() => { host.mounted.splice(0); host.cleanup.splice(0).forEach(callback => callback()); scopes.splice(0).forEach(scope => scope.stop()); vi.clearAllTimers(); vi.useRealTimers(); });

describe("HTMLBrowserPane capability lifecycle", () => {
  it("shows the refresh toolbar when the runtime caller omits showToolbar", () => {
    const pane = mountRuntime();
    try { expect(pane.find("button")).toHaveLength(2); expect(pane.find("button")[0]?.props["aria-label"]).toBe("filePreview.htmlReload"); expect(pane.find("button")[1]?.props["aria-label"]).toBe("filePreview.htmlOpenBrowser"); }
    finally { pane.close(); }
  });

  it("honors an explicitly hidden toolbar in a runtime mount", () => {
    const pane = mountRuntime({ showToolbar: false });
    try { expect(pane.find("button")).toHaveLength(0); expect(pane.find("iframe")).toHaveLength(1); }
    finally { pane.close(); }
  });

  it("shares the tab row only while this HTML preview is active, keeping its iframe mounted", async () => {
    const pane = mountRuntime({ toolbarTarget: "#shared-toolbar", active: false });
    try {
      const frame = pane.find("iframe")[0]; expect(pane.toolbar("button")).toHaveLength(0); expect(pane.find("header")).toHaveLength(0);
      pane.props.active = true; await nextTick(); expect(pane.toolbar("button")).toHaveLength(2); expect(pane.find("header")).toHaveLength(0);
      expect(pane.find("iframe")[0]).toBe(frame);
      pane.props.active = false; await nextTick(); expect(pane.toolbar("button")).toHaveLength(0); expect(pane.find("iframe")[0]).toBe(frame);
    } finally { pane.close(); }
  });

  it("keeps embedded HTML artifacts free of toolbar actions even with a shared target", () => {
    const pane = mountRuntime({ toolbarTarget: "#shared-toolbar", showToolbar: false });
    try { expect(pane.toolbar("button")).toHaveLength(0); expect(pane.find("button")).toHaveLength(0); expect(pane.find("iframe")).toHaveLength(1); }
    finally { pane.close(); }
  });
  it("waits until the first dock mount attaches its shared toolbar", async () => {
    const pane = mountRuntime({ toolbarTarget: "#shared-toolbar" }, true);
    try {
      await nextTick(); expect(pane.toolbar("header")).toHaveLength(1); expect(pane.toolbar("button")).toHaveLength(2);
      expect(pane.find("iframe")).toHaveLength(1);
    } finally { pane.close(); }
  });

  it("adopts and claims a valid one-shot initial capability without issuing a duplicate", async () => {
    const { c, mounted, emit } = mount(); mounted(); await flush();
    expect(c.current.value).toEqual({ id: firstId, url: url("a") }); expect(c.loading.value).toBe(true); expect(host.create).not.toHaveBeenCalled();
    expect(emit).toHaveBeenCalledExactlyOnceWith("claimCapability", firstId);
    c.settle(); expect(c.loading.value).toBe(false); expect(c.updated.value).toBe(false);
  });

  it("opens a separate native-browser capability without transferring the iframe's capability", async () => {
    const { c, emit, close } = mount(); c.settle(); host.create.mockResolvedValueOnce(nativeCapability()); await c.openBrowser();
    expect(host.create).toHaveBeenCalledExactlyOnceWith("session-a", "reports/index.html");
    expect(emit).toHaveBeenCalledWith("openBrowser", { id: nextId, url: nativeCapability().browser_url, sessionId: "session-a" });
    expect(c.current.value).toEqual({ id: firstId, url: url("a") });
    close();
    expect(host.revoke).toHaveBeenCalledWith("session-a", firstId);
    expect(host.revoke).not.toHaveBeenCalledWith("session-a", nextId);
  });

  it("revokes a late native-browser receipt after the file changes", async () => {
    const { c, props, emit } = mount(); c.settle(); const pending = deferred<ReturnType<typeof capability>>(); host.create.mockReturnValueOnce(pending.promise);
    const request = c.openBrowser(); props.path = "reports/new.html"; props.initialId = undefined; props.initialUrl = undefined;
    await flush(); pending.resolve(capability("c".repeat(32))); await request;
    expect(emit).not.toHaveBeenCalledWith("openBrowser", expect.anything());
    expect(host.revoke).toHaveBeenCalledWith("session-a", "c".repeat(32));
    expect(c.browserOpening.value).toBe(false);
  });

  it("rejects an invalid native-browser URL without replacing the iframe", async () => {
    const { c, emit } = mount(); c.settle(); host.create.mockResolvedValueOnce({ ...capability(), browser_url: "javascript:alert(1)" }); await c.openBrowser();
    expect(emit).not.toHaveBeenCalled(); expect(host.revoke).toHaveBeenCalledWith("session-a", nextId);
    expect(c.failed.value).toBe("filePreview.htmlInvalidPreview"); expect(c.current.value?.id).toBe(firstId);
  });

  it("requests a capability when no initial value is supplied and does not claim a parent-owned URL", async () => {
    const { c, mounted, emit } = mount({ initialId: undefined, initialUrl: undefined }); await flush(); mounted();
    expect(host.create).toHaveBeenCalledExactlyOnceWith("session-a", "reports/index.html"); expect(c.current.value?.id).toBe(nextId); expect(emit).not.toHaveBeenCalled();
  });

  it("keeps the old capability until the replacement iframe reports load", async () => {
    const { c } = mount(); c.settle(); await c.reload();
    expect(c.current.value).toEqual(capability()); expect(c.loading.value).toBe(true); expect(host.revoke).not.toHaveBeenCalled();
    c.settle(); expect(host.revoke).toHaveBeenCalledWith("session-a", firstId); expect(c.loading.value).toBe(false); expect(c.updated.value).toBe(true);
    await vi.advanceTimersByTimeAsync(1800); expect(c.updated.value).toBe(false);
  });

  it("preserves the old page and restores retry after a failed refresh request", async () => {
    const { c, props } = mount(); c.settle(); host.create.mockRejectedValueOnce(new Error("Source unavailable")); await c.reload();
    expect(c.current.value).toEqual({ id: firstId, url: url("a") }); expect(c.loading.value).toBe(false); expect(c.failed.value).toBe("Source unavailable"); expect(host.revoke).not.toHaveBeenCalled();
    const output = await render(c, props); expect(output).toContain(url("a")); expect(output).toContain('role="alert"'); expect(output).toContain("Source unavailable");
  });

  it.each(["path", "sessionId"] as const)("revokes a late old response when %s changes, using each capability's original owner", async field => {
    const { c, props } = mount(); c.settle(); const pending = deferred<ReturnType<typeof capability>>(); host.create.mockReturnValueOnce(pending.promise);
    const oldRequest = c.reload(); props.initialId = undefined; props.initialUrl = undefined;
    if (field === "path") props.path = "reports/new.html"; else props.sessionId = "session-b";
    await flush(); expect(host.revoke).toHaveBeenCalledWith("session-a", firstId);
    pending.resolve(capability("c".repeat(32), url("c"))); await oldRequest; await flush();
    expect(host.revoke).toHaveBeenCalledWith("session-a", "c".repeat(32)); expect(c.current.value?.id).toBe(nextId);
    c.settle(); expect(c.loading.value).toBe(false);
  });

  it("revokes current and retired capabilities when closed before replacement load", async () => {
    const { c, close } = mount(); c.settle(); await c.reload(); close(); await flush();
    expect(host.revoke).toHaveBeenCalledWith("session-a", firstId); expect(host.revoke).toHaveBeenCalledWith("session-a", nextId);
  });

  it("revokes an in-flight receipt after close and never adopts it", async () => {
    const { c, close } = mount(); c.settle(); const pending = deferred<ReturnType<typeof capability>>(); host.create.mockReturnValueOnce(pending.promise);
    const request = c.reload(); close(); pending.resolve(capability()); await request;
    expect(host.revoke).toHaveBeenCalledWith("session-a", nextId); expect(c.current.value?.id).toBe(firstId);
  });

  it("constrains the iframe markup to script-only sandbox and no referrer", async () => {
    const { c, props } = mount(); const output = await render(c, props);
    expect(output).toContain('sandbox="allow-scripts"'); expect(output).toContain('referrerpolicy="no-referrer"'); expect(output).toContain(url("a"));
    expect(output).not.toContain("allow-same-origin"); expect(output).not.toContain("allow-forms"); expect(output).not.toContain("srcdoc"); expect(output).not.toContain("token=");
  });

  it.each([false, true])("clears loading and preserves the previous page after an invalid server URL (old page: %s)", async hasOldPage => {
    host.create.mockResolvedValueOnce(capability(nextId, "https://example.test/untrusted"));
    const { c, props } = mount(hasOldPage ? {} : { initialId: undefined, initialUrl: undefined });
    if (hasOldPage) { c.settle(); await c.reload(); } else await flush();
    expect(host.revoke).toHaveBeenCalledWith("session-a", nextId);
    expect(c.current.value?.id ?? null).toBe(hasOldPage ? firstId : null);
    expect(c.loading.value).toBe(false);
    expect(c.failed.value).toBe("filePreview.htmlInvalidPreview");
    const output = await render(c, props);
    expect(output).toContain('role="alert"');
    expect(output).toContain("filePreview.htmlInvalidPreview");
    expect(output).not.toContain("https://example.test/untrusted");
    expect(output).not.toContain("disabled");
  });

  it.each([false, true])("retries after an invalid address and still waits for the replacement iframe load (old page: %s)", async hasOldPage => {
    host.create.mockResolvedValueOnce(capability(nextId, "https://example.test/untrusted"));
    const { c } = mount(hasOldPage ? {} : { initialId: undefined, initialUrl: undefined });
    if (hasOldPage) { c.settle(); await c.reload(); } else await flush();
    const recovery = capability("c".repeat(32), url("c"));
    host.create.mockResolvedValueOnce(recovery);
    await c.reload();
    expect(c.failed.value).toBe("");
    expect(c.current.value).toEqual(recovery);
    expect(c.loading.value).toBe(true);
    expect(host.revoke).toHaveBeenCalledWith("session-a", nextId);
    expect(host.revoke).not.toHaveBeenCalledWith("session-a", firstId);
    c.settle();
    expect(c.loading.value).toBe(false);
    if (hasOldPage) expect(host.revoke).toHaveBeenCalledWith("session-a", firstId);
  });

  it.each([
    ["missing", undefined],
    ["null", null],
    ["number", 42],
    ["object", { path: url("b") }],
    ["array containing a valid URL", [url("b")]],
    ["protocol relative", "//example.test/untrusted"],
    ["absolute external", "https://example.test/untrusted"],
    ["script", "javascript:alert(1)"],
    ["line break", `${url("b")}\nother`],
  ])("rejects a %s response URL without adopting it or getting stuck", async (_label, value) => {
    // Omit the property entirely for the missing-value case.
    host.create.mockResolvedValueOnce(value === undefined ? { id: nextId } : capability(nextId, value));
    const { c } = mount({ initialId: undefined, initialUrl: undefined });
    await flush();
    expect(c.current.value).toBeNull();
    expect(c.loading.value).toBe(false);
    expect(c.failed.value).toBe("filePreview.htmlInvalidPreview");
    expect(host.revoke).toHaveBeenCalledExactlyOnceWith("session-a", nextId);
  });

  it.each([null, {}, { url: url("b") }, { id: 42, url: url("b") }])("handles a malformed response body without issuing an invalid revocation (%j)", async value => {
    host.create.mockResolvedValueOnce(value);
    const { c } = mount({ initialId: undefined, initialUrl: undefined });
    await flush();
    expect(c.current.value).toBeNull();
    expect(c.loading.value).toBe(false);
    expect(c.failed.value).toBe("filePreview.htmlInvalidPreview");
    expect(host.revoke).not.toHaveBeenCalled();
  });

  it("does not coerce a non-string initial URL into a trusted iframe address", async () => {
    const { c, emit, mounted } = mount({ initialUrl: [url("a")] as unknown as string });
    await flush(); mounted();
    expect(host.create).toHaveBeenCalledExactlyOnceWith("session-a", "reports/index.html");
    expect(c.current.value).toEqual(capability());
    expect(emit).not.toHaveBeenCalled();
  });

  it.each(["path", "sessionId"] as const)("does not let a late invalid %s response clear the replacement request's loading state", async field => {
    const oldResponse = deferred<ReturnType<typeof capability>>(), newResponse = deferred<ReturnType<typeof capability>>();
    host.create.mockReturnValueOnce(oldResponse.promise).mockReturnValueOnce(newResponse.promise);
    const { c, props } = mount({ initialId: undefined, initialUrl: undefined });
    if (field === "path") props.path = "reports/new.html"; else props.sessionId = "session-b";
    await flush();
    expect(host.create).toHaveBeenCalledTimes(2);
    oldResponse.resolve(capability(nextId, "https://example.test/untrusted"));
    await flush();
    expect(host.revoke).toHaveBeenCalledExactlyOnceWith("session-a", nextId);
    expect(c.current.value).toBeNull();
    expect(c.loading.value).toBe(true);
    expect(c.failed.value).toBe("");
    const replacement = capability("c".repeat(32), url("c"));
    newResponse.resolve(replacement); await flush();
    expect(c.current.value).toEqual(replacement);
    expect(c.loading.value).toBe(true);
    c.settle(); expect(c.loading.value).toBe(false);
  });
});
