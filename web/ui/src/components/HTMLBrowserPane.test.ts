import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { compile, createRenderer, createSSRApp, defineComponent, effectScope, h, nextTick, reactive, type Ref } from "vue";
import { renderToString } from "vue/server-renderer";
const host = vi.hoisted(() => ({ create: vi.fn(), revoke: vi.fn(), mounted: [] as (() => void)[], cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onMounted: (callback: () => void) => host.mounted.push(callback), onBeforeUnmount: (callback: () => void) => host.cleanup.push(callback), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/api/client", () => ({ api: { createHTMLPreview: host.create, revokeHTMLPreview: host.revoke } }));
import HTMLBrowserPane from "./HTMLBrowserPane.vue";

interface Props { path: string; sessionId: string; initialUrl?: string; initialId?: string; active: boolean; showToolbar?: boolean; }
interface Controller { current: Ref<{ id: string; url: string } | null>; loading: Ref<boolean>; failed: Ref<string>; updated: Ref<boolean>; settle(): void; reload(): Promise<void>; }
const firstId = "a".repeat(32), nextId = "b".repeat(32);
const url = (letter: string) => `/api/html-previews/${letter.repeat(64)}/index.html`;
const capability = (id = nextId, value: unknown = url("b")) => ({ id, url: value });
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
function mountRuntime(extra: Partial<Props> = {}) {
  const node = (tag: string, text?: string): RuntimeNode => ({ tag, props: {}, children: [], parent: null, text });
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
  });
  // Vitest's Vue transform supplies only ssrRender. Compile the actual template
  // for a runtime mount so Vue performs its real Boolean prop normalization.
  const template = readFileSync(new URL("./HTMLBrowserPane.vue", import.meta.url), "utf8").split("<template>")[1]!.split("</template>")[0]!;
  const source = HTMLBrowserPane as unknown as { setup(props: Props, context: unknown): object };
  const runtimeComponent = { ...HTMLBrowserPane, setup: (props: Props, context: unknown) => ({ ...source.setup(props, context) }), render: compile(template) };
  const root = node("root"), props = { path: "reports/index.html", sessionId: "session-a", initialId: firstId, initialUrl: url("a"), active: true, ...extra };
  const app = renderer.createApp({ render: () => h(runtimeComponent as unknown as Parameters<typeof h>[0], props) });
  app.component("el-icon", defineComponent({ setup: (_, { slots }) => () => h("span", slots.default?.()) }));
  app.component("Refresh", defineComponent({ render: () => h("svg") }));
  app.mount(root);
  const allNodes = (parent: RuntimeNode): RuntimeNode[] => [parent, ...parent.children.flatMap(allNodes)];
  return { find: (tag: string) => allNodes(root).filter(child => child.tag === tag), close: () => app.unmount() };
}
beforeEach(() => {
  vi.useFakeTimers(); host.create.mockReset().mockResolvedValue(capability()); host.revoke.mockReset().mockResolvedValue({ ok: true });
});
afterEach(() => { host.mounted.splice(0); host.cleanup.splice(0).forEach(callback => callback()); scopes.splice(0).forEach(scope => scope.stop()); vi.clearAllTimers(); vi.useRealTimers(); });

describe("HTMLBrowserPane capability lifecycle", () => {
  it("shows the refresh toolbar when the runtime caller omits showToolbar", () => {
    const pane = mountRuntime();
    try { expect(pane.find("button")).toHaveLength(1); expect(pane.find("button")[0]?.props["aria-label"]).toBe("filePreview.htmlReload"); }
    finally { pane.close(); }
  });

  it("honors an explicitly hidden toolbar in a runtime mount", () => {
    const pane = mountRuntime({ showToolbar: false });
    try { expect(pane.find("button")).toHaveLength(0); expect(pane.find("iframe")).toHaveLength(1); }
    finally { pane.close(); }
  });

  it("adopts and claims a valid one-shot initial capability without issuing a duplicate", async () => {
    const { c, mounted, emit } = mount(); mounted(); await flush();
    expect(c.current.value).toEqual({ id: firstId, url: url("a") }); expect(c.loading.value).toBe(true); expect(host.create).not.toHaveBeenCalled();
    expect(emit).toHaveBeenCalledExactlyOnceWith("claimCapability", firstId);
    c.settle(); expect(c.loading.value).toBe(false); expect(c.updated.value).toBe(false);
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
