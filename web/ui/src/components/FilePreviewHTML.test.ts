import { createHash } from "node:crypto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, effectScope, h, nextTick, reactive, type ComputedRef, type Ref } from "vue";
import { renderToString } from "vue/server-renderer";
import type { FileContent, WorkspaceHTMLPreview } from "@/api/types";
const host = vi.hoisted(() => ({ content: vi.fn(), create: vi.fn(), revoke: vi.fn(), save: vi.fn(), cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount: (cleanup: () => void) => host.cleanup.push(cleanup), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async original => { const { ref } = await import("vue"); return { ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key, d: () => "", locale: ref("zh-CN") }) }; });
vi.mock("@/api/client", () => ({ api: { fileContent: host.content, createHTMLPreview: host.create, revokeHTMLPreview: host.revoke, saveFile: host.save }, authHeaders: () => ({}), fileDownloadURL: (path: string) => `/download/${path}`, fileRawURL: (path: string) => `/raw/${path}` }));
vi.mock("@/agent/officeTransport", () => ({ officeApi: {}, officePathMounted: () => false }));
vi.mock("./CodeBlock.vue", () => ({ default: defineComponent({ props: ["code", "highlightStart", "highlightEnd"], setup: props => () => h("pre", { "data-testid": "source-code", "data-highlight-start": props.highlightStart, "data-highlight-end": props.highlightEnd }, props.code) }) }));
vi.mock("./MarkdownView.vue", () => ({ default: defineComponent({ setup: () => () => h("div") }) }));
vi.mock("./MermaidDiagram.vue", () => ({ default: defineComponent({ setup: () => () => h("div") }) }));
import FilePreview from "./FilePreview.vue";
const html = "<!doctype html>\n<html><body>你好<script>window.executed=true</script></body></html>\n";
const capability = (seed = "preview-a", path = "reports/index.html"): WorkspaceHTMLPreview => {
  const token = createHash("sha256").update(seed).digest("hex");
  return { id: token.slice(0, 32), path, url: `/api/html-previews/${token}/index.html`, browser_url: `http://pogo:19777/api/html-previews/${token}/index.html`, expires_at: "2026-10-28T12:00:00Z", read_only: true, project_root: "reports" };
};
interface Props { path: string; sessionId: string | null; citation: boolean; lineStart?: number; lineEnd?: number; revealVersion?: number }
type Controller = { content: Ref<FileContent | null>; mdPreview: Ref<boolean>; htmlError: Ref<string>; htmlCurrentBrowserLoading: Ref<boolean>; htmlBrowserLoading: Ref<boolean>; editing: Ref<boolean>; draft: Ref<string>; canEdit: ComputedRef<boolean>; openCurrentHTMLBrowser(): Promise<void>; openHTMLBrowser(): Promise<void>; startEdit(): void; save(force?: boolean): Promise<void> };
const scopes: ReturnType<typeof effectScope>[] = [];
async function flush() { for (let i = 0; i < 30; i++) await Promise.resolve(); await nextTick(); }
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done; }); return { promise, resolve }; }
function mount(extra: Partial<Props> = {}) {
  const props = reactive<Props>({ path: "reports/index.html", sessionId: "conversation-a", citation: false, ...extra });
  const emit = vi.fn(), scope = effectScope(); scopes.push(scope);
  const cleanupStart = host.cleanup.length;
  const controller = scope.run(() => (FilePreview as unknown as { setup(p: unknown, ctx: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  const cleanup = host.cleanup.slice(cleanupStart);
  let closed = false;
  return { c: controller, props, emit, close: () => {
    if (closed) return;
    closed = true;
    for (const fn of cleanup) {
      const index = host.cleanup.indexOf(fn);
      if (index >= 0) host.cleanup.splice(index, 1);
      fn();
    }
    scope.stop();
  } };
}
async function render(c: Controller, props: Props) {
  const component = { ...FilePreview, setup: () => c };
  const app = createSSRApp({ render: () => h(component as Parameters<typeof h>[0], props) });
  app.component("el-icon", defineComponent({ setup: (_, { slots }) => () => h("span", slots.default?.()) }));
  return renderToString(app);
}
beforeEach(() => {
  host.content.mockReset().mockImplementation(async path => ({ path, text: html, binary: false, size: html.length, mtime_ms: 10 }));
  host.create.mockReset().mockImplementation(async (_sid, path) => capability("preview-a", path));
  host.revoke.mockReset().mockResolvedValue({ ok: true }); host.save.mockReset().mockResolvedValue({ size: 20, mtime_ms: 20 });
  vi.stubGlobal("window", { setTimeout: vi.fn() });
});
afterEach(() => { for (const fn of host.cleanup.splice(0)) fn(); for (const scope of scopes.splice(0)) scope.stop(); vi.unstubAllGlobals(); });


const open = (c: Controller, kind: "current" | "native") => kind === "current" ? c.openCurrentHTMLBrowser() : c.openHTMLBrowser();
describe("Workspace HTML source and browser opening", () => {
  it.each(["reports/index.html", "reports/INDEX.HTM"])("loads %s as source with no automatic capability", async path => {
    const { c, props } = mount({ path }); await flush();
    expect(host.content).toHaveBeenCalledExactlyOnceWith(path); expect(c.content.value?.text).toBe(html); expect(host.create).not.toHaveBeenCalled();
    const output = await render(c, props); expect(output).toContain('data-testid="source-code"'); expect(output).toContain('data-testid="html-open-current-browser"'); expect(output).toContain('data-testid="html-open-browser"');
    expect(output).not.toContain('html-preview-frame'); expect(output).not.toContain('<script>'); expect(output).toContain('href="/download/'); expect(output).toContain('你好&lt;script&gt;'); expect(c.canEdit.value).toBe(true);
  });
  it.each([{ citation: true }, { lineStart: 2, lineEnd: 2 }])("retains source citations and line targets %o", async target => {
    const { c, props } = mount(target); await flush(); expect(c.mdPreview.value).toBe(false); expect(host.create).not.toHaveBeenCalled();
    const output = await render(c, props); expect(output).toContain('data-testid="source-code"'); if (target.lineStart) expect(output).toContain('data-highlight-start="2"');
  });
  it("opens a browser only after source loading finishes and the user requests it", async () => {
    const pending = deferred<FileContent>(); host.content.mockReturnValueOnce(pending.promise); const { c } = mount(); await flush();
    await c.openHTMLBrowser(); expect(host.create).not.toHaveBeenCalled();
    pending.resolve({ path: "reports/index.html", text: html, binary: false, size: html.length, mtime_ms: 10 }); await flush(); expect(c.content.value?.text).toBe(html); expect(host.create).not.toHaveBeenCalled();
    await c.openHTMLBrowser(); expect(host.create).toHaveBeenCalledExactlyOnceWith("conversation-a", "reports/index.html");
  });
  it("creates independent current/native browser capabilities and transfers lifecycle to the host", async () => {
    const { c, close, emit } = mount(); await flush(); const current = capability("current"), native = capability("native"); host.create.mockResolvedValueOnce(current).mockResolvedValueOnce(native);
    await c.openCurrentHTMLBrowser(); await c.openHTMLBrowser();
    expect(emit).toHaveBeenCalledWith("openCurrentBrowser", { url: current.url, id: current.id, sessionId: "conversation-a", path: "reports/index.html" });
    expect(emit).toHaveBeenCalledWith("openBrowser", { url: native.browser_url, id: native.id, sessionId: "conversation-a" }); close(); await flush(); expect(host.revoke).not.toHaveBeenCalled();
  });
  it("preserves source editing and saves with mtime without automatic reissuance", async () => {
    const { c } = mount(); await flush(); c.startEdit(); c.draft.value = "<h1>updated</h1>"; await c.openCurrentHTMLBrowser(); await c.openHTMLBrowser(); expect(host.create).not.toHaveBeenCalled();
    await c.save(); await flush(); expect(host.save).toHaveBeenCalledExactlyOnceWith("reports/index.html", "<h1>updated</h1>", 10, false);
    expect(c.content.value?.text).toBe("<h1>updated</h1>"); expect(c.editing.value).toBe(false); expect(host.create).not.toHaveBeenCalled(); expect(host.revoke).not.toHaveBeenCalled();
    await c.openHTMLBrowser(); expect(host.create).toHaveBeenCalledExactlyOnceWith("conversation-a", "reports/index.html");
  });
  it("keeps source and retry actions after an explicitly requested capability fails", async () => {
    host.create.mockRejectedValueOnce(new Error("Preview unavailable")); const { c, props } = mount(); await flush(); expect(c.htmlError.value).toBe(""); expect(host.create).not.toHaveBeenCalled();
    await c.openCurrentHTMLBrowser(); expect(c.htmlError.value).toBe("Preview unavailable"); const output = await render(c, props); expect(output).toContain('data-testid="source-code"'); expect(output).toContain("Preview unavailable");
    await c.openCurrentHTMLBrowser(); expect(c.htmlError.value).toBe(""); expect(host.create).toHaveBeenCalledTimes(2);
  });
  it("disables browser actions without a session while source remains available", async () => {
    const { c, props } = mount({ sessionId: null }); await flush(); await c.openCurrentHTMLBrowser(); await c.openHTMLBrowser(); expect(host.create).not.toHaveBeenCalled(); expect(c.content.value?.text).toBe(html);
    const output = await render(c, props); expect(output).toContain('data-testid="source-code"'); expect(output.match(/disabled/g)?.length).toBeGreaterThanOrEqual(2);
  });
  it.each(["current", "native"] as const)("reclaims late %s issuance after close", async kind => {
    const { c, close, emit } = mount(); await flush(); const pending = deferred<WorkspaceHTMLPreview>(); host.create.mockReturnValueOnce(pending.promise);
    const request = open(c, kind); close(); const late = capability(`close-${kind}`); pending.resolve(late); await request; await flush();
    expect(host.revoke).toHaveBeenCalledWith("conversation-a", late.id); expect(emit.mock.calls.filter(call => String(call[0]).startsWith("open"))).toEqual([]);
  });
  it.each(["current", "native"] as const)("reclaims late %s issuance after a path switch", async kind => {
    const { c, props, emit } = mount(); await flush(); const pending = deferred<WorkspaceHTMLPreview>(); host.create.mockReturnValueOnce(pending.promise);
    const request = open(c, kind); props.path = "reports/new.html"; await flush(); const late = capability(`path-${kind}`); pending.resolve(late); await request;
    expect(host.revoke).toHaveBeenCalledWith("conversation-a", late.id); expect(emit.mock.calls.filter(call => String(call[0]).startsWith("open"))).toEqual([]);
    expect(c.content.value?.path).toBe("reports/new.html"); await open(c, kind); expect(host.create).toHaveBeenLastCalledWith("conversation-a", "reports/new.html");
  });
  it.each(["current", "native"] as const)("reclaims late %s issuance with the old session owner", async kind => {
    const { c, props, emit } = mount(); await flush(); const pending = deferred<WorkspaceHTMLPreview>(); host.create.mockReturnValueOnce(pending.promise);
    const request = open(c, kind); props.sessionId = "conversation-b"; await flush(); const late = capability(`session-${kind}`); pending.resolve(late); await request;
    expect(host.revoke).toHaveBeenCalledWith("conversation-a", late.id); expect(host.revoke).not.toHaveBeenCalledWith("conversation-b", late.id); expect(emit.mock.calls.filter(call => String(call[0]).startsWith("open"))).toEqual([]);
    await open(c, kind); expect(host.create).toHaveBeenLastCalledWith("conversation-b", "reports/index.html");
  });
  it.each(["current", "native"] as const)("reclaims late %s issuance if editing began in flight", async kind => {
    const { c, emit } = mount(); await flush(); const pending = deferred<WorkspaceHTMLPreview>(); host.create.mockReturnValueOnce(pending.promise);
    const request = open(c, kind); c.startEdit(); const late = capability(`editing-${kind}`); pending.resolve(late); await request;
    expect(c.editing.value).toBe(true); expect(host.revoke).toHaveBeenCalledWith("conversation-a", late.id); expect(emit.mock.calls.filter(call => String(call[0]).startsWith("open"))).toEqual([]);
  });
  it.each(["current", "native"] as const)("coalesces repeated %s clicks while pending", async kind => {
    const { c, emit } = mount(); await flush(); const pending = deferred<WorkspaceHTMLPreview>(); host.create.mockReturnValueOnce(pending.promise); const request = open(c, kind);
    await open(c, kind); expect(host.create).toHaveBeenCalledTimes(1); pending.resolve(capability(`once-${kind}`)); await request; expect(emit.mock.calls.filter(call => String(call[0]).startsWith("open"))).toHaveLength(1);
  });
});
