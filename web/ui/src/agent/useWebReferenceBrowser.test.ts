import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRenderer, h, ref } from "vue";
import type { NativeBrowserState } from "./nativeBrowser";
import { retireBrowserControls } from "./browserSheets";
import { useWebReferenceBrowser } from "./useWebReferenceBrowser";

interface Node { children: Node[]; parent: Node | null }
const node = (): Node => ({ children: [], parent: null });
const renderer = createRenderer<Node, Node>({
  createElement: node, createText: node, createComment: node, patchProp() {}, setText() {}, setElementText() {},
  insert(target, parent, anchor) { const at = anchor ? parent.children.indexOf(anchor) : -1; parent.children.splice(at < 0 ? parent.children.length : at, 0, target); target.parent = parent; },
  remove(target) { if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1); },
  parentNode: target => target.parent, nextSibling: () => null,
});
const ctx = `ctx_${"a".repeat(24)}`, temporaryID = `browser-authoring-${"a".repeat(48)}`;
const base = (): NativeBrowserState => ({ backend: "native", context_ref: ctx, active_tab: "t1", viewed_tab: "t1", control: "agent", control_epoch: 0, generation: 1, document_version: 1, version: "1:t1:1", frame_identity: "1:t1:1", url: "https://example.test/original", title: "Original", tabs: [{ id: "t1", url: "https://example.test/original", title: "Original", control: "agent", control_epoch: 0 }], viewport: { width: 1280, height: 720 } });
const created = () => ({ context_ref: ctx, tab_id: "t2", lease_token: "own-t2-lease", state: { ...base(), viewed_tab: "t2", active_tab: "t1", control: "human" as const, control_epoch: 1, url: "about:blank", title: "", tabs: [...base().tabs, { id: "t2", url: "about:blank", title: "", control: "human" as const, control_epoch: 1 }] } });
function deferred<T>() { let resolve!: (value: T) => void, reject!: (reason: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { resolve, reject, promise }; }
const settle = async () => { for (let i = 0; i < 25; i++) await Promise.resolve(); };
const disposers: (() => void)[] = [];
function mount(source = "") {
  const active = ref(true), sid = ref(source), scope = ref("scope-a");
  const transport = {
    browserAuthoringCreate: vi.fn().mockResolvedValue({ session_id: temporaryID, expires_in: 1200 }),
    browserAuthoringClose: vi.fn().mockResolvedValue(undefined),
    browserState: vi.fn().mockResolvedValue(base()), browserTabs: vi.fn().mockResolvedValue(created()),
    browserControl: vi.fn().mockResolvedValue({ ...base(), control: "human", control_epoch: 1, lease_token: "temporary-lease" }),
  };
  let value!: ReturnType<typeof useWebReferenceBrowser>;
  const app = renderer.createApp({ setup() { value = useWebReferenceBrowser({ active: () => active.value, sourceSession: () => sid.value, scope: () => scope.value, transport }); return () => h("div"); } });
  let mounted = false;
  function start() { app.mount(node()); mounted = true; }
  function unmount() { if (mounted) { app.unmount(); mounted = false; } }
  disposers.push(() => { unmount(); retireBrowserControls(temporaryID, ""); if (source) retireBrowserControls(source, ""); });
  return { get value() { return value; }, active, sid, scope, transport, start, unmount };
}
beforeEach(() => vi.useFakeTimers());
afterEach(async () => { for (const dispose of disposers.splice(0)) dispose(); await settle(); vi.useRealTimers(); });

describe("native web reference browser lifetime", () => {
  it("Manager opens an owned browser-only session, adopts its initial blank page, then deletes it without a conversation", async () => {
    const c = mount(); c.start(); await settle();
    expect(c.transport.browserAuthoringCreate).toHaveBeenCalledOnce();
    expect(c.value.sessionId.value).toBe(temporaryID);
    expect(c.value.state.value?.viewed_tab).toBe("t1");
    expect(c.transport.browserControl).toHaveBeenCalledWith(temporaryID, "human", { context_ref: ctx, tab_id: "t1" });
    c.active.value = false; await settle();
    expect(c.transport.browserAuthoringClose).toHaveBeenCalledExactlyOnceWith(temporaryID);
    expect(c.transport.browserTabs).not.toHaveBeenCalled();
  });
  it("uses bounded adoption only for an initially unassigned HUMAN page", async () => {
    const c = mount(); c.transport.browserState.mockResolvedValue({ ...base(), control: "human" }); c.start(); await settle();
    expect(c.transport.browserControl).toHaveBeenCalledWith(temporaryID, "human", { context_ref: ctx, tab_id: "t1", generation: 1, control_epoch: 0, adopt_if_unowned: true });
  });
  it("an Agent uses a new page in its existing context, exposing and closing only that page and its proven child popup", async () => {
    const c = mount("conversation-own"); c.start(); await settle();
    expect(c.transport.browserAuthoringCreate).not.toHaveBeenCalled();
    expect(c.value.state.value?.tabs.map(page => page.id)).toEqual(["t2"]);
    c.value.observe({ ...created().state, tabs: [...created().state.tabs,
      { id: "t3", url: "https://example.test/popup", title: "Popup", control: "human", control_epoch: 1, opener_tab: "t2", inherited_control_epoch: 1 },
      { id: "t4", url: "https://example.test/unrelated", title: "Unrelated", control: "human", control_epoch: 1 } ] });
    expect(c.value.state.value?.tabs.map(page => page.id)).toEqual(["t2", "t3"]);
    c.active.value = false; await settle();
    expect(c.transport.browserTabs.mock.calls.filter(([_, request]) => request.action === "close").map(([_, request]) => request.tab_id)).toEqual(["t3", "t2"]);
    expect(c.transport.browserAuthoringClose).not.toHaveBeenCalled();
  });
  it("cleans a late Manager POST receipt after cancel without ever starting or publishing its browser", async () => {
    const c = mount(), pending = deferred<{ session_id: string; expires_in: number }>();
    c.transport.browserAuthoringCreate.mockReturnValue(pending.promise); c.start();
    c.active.value = false; pending.resolve({ session_id: temporaryID, expires_in: 1200 }); await settle();
    expect(c.value.sessionId.value).toBe(""); expect(c.value.state.value).toBeNull();
    expect(c.transport.browserState).not.toHaveBeenCalled();
    expect(c.transport.browserAuthoringClose).toHaveBeenCalledExactlyOnceWith(temporaryID);
  });
  it("cleans only the exact late new-tab receipt after the originating draft closes", async () => {
    const c = mount("conversation-late"), pending = deferred<ReturnType<typeof created>>();
    c.transport.browserTabs.mockReturnValueOnce(pending.promise).mockResolvedValue(created());
    c.start(); await settle(); c.active.value = false; pending.resolve(created()); await settle();
    const close = c.transport.browserTabs.mock.calls.filter(([_, request]) => request.action === "close");
    expect(close).toHaveLength(1); expect(close[0][1]).toMatchObject({ context_ref: ctx, tab_id: "t2", lease_token: "own-t2-lease" });
    expect(c.value.state.value).toBeNull();
  });
  it("resolves an uncertain Manager create response by the same UUID before cleaning a cancelled dialog", async () => {
    const c = mount();
    c.transport.browserAuthoringCreate.mockRejectedValueOnce(new Error("response lost after creation")).mockResolvedValue({ session_id: temporaryID, expires_in: 1200 });
    c.start(); await settle(); c.active.value = false; await settle();
    expect(c.transport.browserAuthoringCreate).toHaveBeenCalledTimes(2);
    expect(c.transport.browserAuthoringCreate.mock.calls[0][0]).toBe(c.transport.browserAuthoringCreate.mock.calls[1][0]);
    expect(c.transport.browserAuthoringClose).toHaveBeenCalledExactlyOnceWith(temporaryID);
    expect(c.transport.browserState).not.toHaveBeenCalled();
    expect(c.value.sessionId.value).toBe("");
  });
  it("discovers a late proven popup during close without exposing or closing an unrelated original page", async () => {
    const c = mount("conversation-late-popup"); c.start(); await settle();
    c.transport.browserState.mockResolvedValue({ ...created().state, tabs: [...created().state.tabs,
      { id: "t3", url: "https://example.test/popup", title: "Late popup", control: "human", control_epoch: 1, opener_tab: "t2", inherited_control_epoch: 1 } ] });
    c.active.value = false; await settle();
    expect(c.transport.browserState).toHaveBeenLastCalledWith("conversation-late-popup", undefined, { context_ref: ctx, tab_id: "t2" });
    expect(c.transport.browserTabs.mock.calls.filter(([_, request]) => request.action === "close").map(([_, request]) => request.tab_id)).toEqual(["t3", "t2"]);
  });
  it("retries an unknown close result once with identical operation, page and lease", async () => {
    const c = mount("conversation-close-retry"); c.start(); await settle();
    c.transport.browserTabs.mockRejectedValueOnce(new Error("close response lost")).mockResolvedValue(created());
    c.active.value = false; await settle();
    const closes = c.transport.browserTabs.mock.calls.filter(([_, request]) => request.action === "close");
    expect(closes).toHaveLength(2); expect(closes[0]).toEqual(closes[1]);
    expect(closes[0][1]).toMatchObject({ tab_id: "t2", lease_token: "own-t2-lease" });
  });
  it("retries an uncertain new-tab request with its original operation ID", async () => {
    const c = mount("conversation-retry"); c.transport.browserTabs.mockRejectedValueOnce(new Error("network response lost")).mockResolvedValue(created());
    c.start(); await settle(); expect(c.value.error.value).toContain("network response lost");
    c.value.retry(); await settle();
    const creates = c.transport.browserTabs.mock.calls.filter(([_, request]) => request.action === "new");
    expect(creates).toHaveLength(2); expect(creates[0][1].op_id).toBe(creates[1][1].op_id);
    expect(c.value.state.value?.viewed_tab).toBe("t2");
  });
  it("keeps the Manager operation UUID after an unknown response, but uses a new one after confirmed expiry", async () => {
    const c = mount();
    c.transport.browserAuthoringCreate.mockRejectedValueOnce(new Error("network response lost")).mockRejectedValueOnce(Object.assign(new Error("expired"), { status: 410 })).mockResolvedValue({ session_id: temporaryID, expires_in: 1200 });
    c.start(); await settle(); c.value.retry(); await settle(); c.value.retry(); await settle();
    const operations = c.transport.browserAuthoringCreate.mock.calls.map(call => call[0]);
    expect(operations[0]).toBe(operations[1]); expect(operations[2]).not.toBe(operations[1]);
    expect(c.value.sessionId.value).toBe(temporaryID);
  });
  it("switches only its own page by bound GET, ignores hidden old-page events and heartbeats without navigation", async () => {
    const c = mount("conversation-switch"); c.start(); await settle();
    const popup = { id: "t3", url: "https://example.test/popup", title: "Popup", control: "human" as const, control_epoch: 1, opener_tab: "t2", inherited_control_epoch: 1 };
    c.value.observe({ ...created().state, tabs: [...created().state.tabs, popup] });
    const next = { ...created().state, viewed_tab: "t3", url: popup.url, tabs: [...created().state.tabs, popup] };
    c.transport.browserState.mockResolvedValue(next);
    await c.value.switchTab("t1"); expect(c.transport.browserState).toHaveBeenCalledTimes(1);
    await c.value.switchTab("t3"); expect(c.value.state.value?.viewed_tab).toBe("t3");
    expect(c.transport.browserState).toHaveBeenLastCalledWith("conversation-switch", undefined, { context_ref: ctx, tab_id: "t3" });
    c.value.observe(created().state); expect(c.value.state.value?.viewed_tab).toBe("t3");
    await vi.advanceTimersByTimeAsync(60_000);
    expect(c.transport.browserState).toHaveBeenLastCalledWith("conversation-switch", undefined, { context_ref: ctx, tab_id: "t3" });
    expect(c.transport.browserTabs.mock.calls.filter(([_, request]) => request.action === "new")).toHaveLength(1);
  });
});
