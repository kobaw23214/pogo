import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive } from "vue";
import type { WebServiceChange, WebServiceRecord } from "@/api/webServices";
const host = vi.hoisted(() => ({ list: vi.fn(), open: vi.fn() }));
vi.mock("@/api/client", () => ({ api: { webServices: host.list, openWebService: host.open } }));
vi.mock("@/i18n", () => ({ gt: (key: string) => key }));
import { useWebServices } from "./useWebServices";
import { useWebServiceLinkReveal, type WebServiceRevealHandle } from "./useWebServiceLinkReveal";

const record = (): WebServiceRecord => ({ service_id: "ws_one", name: "counter", generation: 1, revision: 1, state: "ready", profile: "python-web", project_root: "app", lease_expires_at: new Date(Date.now() + 7200000).toISOString() });
const link = { present: true, sessionId: "target", serviceId: "ws_one" };
const scopes: ReturnType<typeof effectScope>[] = [];
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r; }); return { promise, resolve }; }
async function flush() { for (let i = 0; i < 18; i++) { await Promise.resolve(); await nextTick(); } }
function mount(childInitiallyReady = false) {
  const scope = effectScope(); scopes.push(scope);
  const child = reactive({ sessionId: childInitiallyReady ? "target" : null as string | null, enabled: childInitiallyReady, connected: true, change: null as WebServiceChange | null });
  // Match the real component's context builder: reading one context field
  // also tracks change/connected, which must not reset the owner scope.
  const state = scope.run(() => useWebServices(() => ({ sessionId: child.sessionId, enabled: child.enabled, connected: child.connected, change: child.change })))!;
  const widget: WebServiceRevealHandle = { readyFor: state.readyFor, reveal: vi.fn((id, sid) => state.reveal(id, sid)) };
  const parent = reactive({ sessionId: "target", displaySessionId: "target", switching: false, widget: null as WebServiceRevealHandle | null, surfaceReady: true, restoreSurface: vi.fn<() => Promise<void>>().mockResolvedValue() });
  const revealed = scope.run(() => useWebServiceLinkReveal(link, () => parent))!;
  return { child, state, parent, widget, revealed };
}
beforeEach(() => { vi.useFakeTimers(); host.list.mockReset().mockResolvedValue({ services: [record()] }); host.open.mockReset(); });
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); vi.useRealTimers(); });
describe("owner website deep link across delayed stream and child mounting", () => {
  it("waits for the late child context and delayed owner list before consuming the link or expanding", async () => {
    const list = deferred<{ services: WebServiceRecord[] }>(); host.list.mockReturnValueOnce(list.promise);
    const { child, state, parent, widget, revealed } = mount();
    parent.widget = widget; await flush();
    expect(widget.reveal).not.toHaveBeenCalled(); expect(revealed.value).toBe(false);
    // The parent observed the snapshot first; the child's props arrive later.
    child.sessionId = "target"; child.enabled = true; await flush();
    expect(widget.reveal).toHaveBeenCalledOnce(); expect(revealed.value).toBe(false); expect(state.expanded.value).toBe(false);
    child.change = { sessionId: "target", record: { ...record(), revision: 2 } }; await flush();
    expect(widget.reveal).toHaveBeenCalledOnce(); expect(revealed.value).toBe(false);
    list.resolve({ services: [record()] }); await flush();
    expect(state.expanded.value).toBe(true); expect(state.selected.value).toBe("ws_one"); expect(revealed.value).toBe(true);
    expect(host.list).toHaveBeenCalledExactlyOnceWith("target", expect.any(AbortSignal)); expect(host.open).not.toHaveBeenCalled();
  });
  it("keeps the revealed card expanded across subsequent live SSE and connection updates", async () => {
    const { child, state, parent, widget, revealed } = mount(true); parent.widget = widget; await flush();
    expect(revealed.value).toBe(true); expect(state.expanded.value).toBe(true);
    child.change = { sessionId: "target", record: { ...record(), revision: 2 } }; await flush();
    child.connected = false; await flush(); child.connected = true; await flush();
    expect(revealed.value).toBe(true); expect(state.expanded.value).toBe(true); expect(state.selected.value).toBe("ws_one"); expect(state.records.value[0]?.revision).toBe(2);
    expect(widget.reveal).toHaveBeenCalledOnce(); expect(host.open).not.toHaveBeenCalled();
  });
  it("does not consume the intent when the same child's enabled context resets during an in-flight reveal", async () => {
    const first = deferred<{ services: WebServiceRecord[] }>(), restored = deferred<{ services: WebServiceRecord[] }>();
    host.list.mockReturnValueOnce(first.promise).mockReturnValueOnce(restored.promise);
    const { child, state, parent, widget, revealed } = mount(true); parent.widget = widget; await flush();
    expect(widget.reveal).toHaveBeenCalledOnce();
    parent.switching = true; child.enabled = false; await flush();
    parent.switching = false; child.enabled = true; await flush();
    first.resolve({ services: [record()] }); await flush();
    expect(revealed.value).toBe(false); expect(state.expanded.value).toBe(false);
    restored.resolve({ services: [record()] }); await flush();
    expect(widget.reveal).toHaveBeenCalledTimes(2); expect(revealed.value).toBe(true); expect(state.expanded.value).toBe(true); expect(state.selected.value).toBe("ws_one");
    expect(host.open).not.toHaveBeenCalled();
  });
  it("does not reveal or select another conversation when a late linked-session response arrives", async () => {
    const first = deferred<{ services: WebServiceRecord[] }>(); host.list.mockReturnValueOnce(first.promise).mockResolvedValue({ services: [] });
    const { child, state, parent, widget, revealed } = mount(true); parent.widget = widget; await flush();
    parent.sessionId = "other"; parent.displaySessionId = "other"; child.sessionId = "other"; await flush();
    first.resolve({ services: [record()] }); await flush();
    expect(revealed.value).toBe(false); expect(state.expanded.value).toBe(false); expect(state.selected.value).toBeNull(); expect(state.records.value).toEqual([]); expect(host.open).not.toHaveBeenCalled();
  });
  it("shows an owner-scoped missing result after mounting instead of opening an unrelated service", async () => {
    host.list.mockResolvedValue({ services: [] });
    const { state, parent, widget, revealed } = mount(true); await flush(); parent.widget = widget; await flush();
    expect(revealed.value).toBe(true); expect(state.visible.value).toBe(true); expect(state.expanded.value).toBe(true); expect(state.error.value).toBe("webServices.notFound");
    expect(state.selected.value).toBeNull(); expect(host.open).not.toHaveBeenCalled();
  });
  it("restores a hidden conversation alongside a retained Office view and waits for visible layout without opening a website", async () => {
    const restoring = deferred<void>(); const { state, parent, widget, revealed } = mount(true);
    parent.surfaceReady = false; parent.restoreSurface.mockReturnValueOnce(restoring.promise); parent.widget = widget;
    await flush(); expect(parent.restoreSurface).toHaveBeenCalledOnce(); expect(widget.reveal).not.toHaveBeenCalled(); expect(revealed.value).toBe(false);
    restoring.resolve(); await flush(); expect(widget.reveal).not.toHaveBeenCalled();
    // The Office iframe and selected dock remain mounted; only the chat's
    // visible/inert transition finishes after the restore promise.
    parent.surfaceReady = true; await flush();
    expect(widget.reveal).toHaveBeenCalledOnce(); expect(state.expanded.value).toBe(true); expect(revealed.value).toBe(true); expect(host.open).not.toHaveBeenCalled();
  });
  it("does not reveal a stale link when the session changes while an immersive restore is pending", async () => {
    const restoring = deferred<void>(); const { child, state, parent, widget, revealed } = mount(true);
    parent.surfaceReady = false; parent.restoreSurface.mockReturnValueOnce(restoring.promise); parent.widget = widget; await flush();
    parent.sessionId = "other"; parent.displaySessionId = "other"; child.sessionId = "other"; await flush();
    parent.surfaceReady = true; restoring.resolve(); await flush();
    expect(widget.reveal).not.toHaveBeenCalled(); expect(revealed.value).toBe(false); expect(state.expanded.value).toBe(false); expect(host.open).not.toHaveBeenCalled();
  });
});
