import { describe, expect, it, vi } from "vitest";
import { createDesktopSurface, desktopBounds, desktopSurfaceGeometry, desktopBrowserBridge, desktopElementPick, desktopEventMatches, type DesktopBrowserBridge, type DesktopBrowserEvent, type DesktopBrowserPresentation } from "./desktopBrowser";
import type { NativeBrowserPick, NativeBrowserState } from "./nativeBrowser";

const binding = { session_id: "session-a", context_ref: `ctx_${"a".repeat(24)}`, tab_id: "t1" };
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (error: Error) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
function bridge(): DesktopBrowserBridge {
  return { version: 1, attach: vi.fn(async () => ({ surface_id: "surface-1" })), update: vi.fn(async () => ({ interactive: false })), detach: vi.fn(async () => {}), setPicker: vi.fn(async () => {}), openDevTools: vi.fn(async () => {}), onEvent: vi.fn(() => () => {}) };
}
const shown: DesktopBrowserPresentation = { bounds: { x: 501.5, y: 88, width: 913, height: 612 }, visible: true, blocked: false, lease_token: "window-only-lease", control_epoch: 7, generation: 2 };

describe("desktop browser bridge capability and bindings", () => {
  it("requires the complete v1 interface, without treating shape checks as authorization", () => {
    const api = bridge(); expect(desktopBrowserBridge(api)).toBe(api);
    for (const candidate of [null, {}, { ...api, version: 2 }, { ...api, detach: undefined }, { ...api, onEvent: "event" }]) expect(desktopBrowserBridge(candidate)).toBeUndefined();
  });
  it("rejects late events from another surface, session, context or tab", () => {
    const event: DesktopBrowserEvent = { ...binding, surface_id: "surface-1", type: "picker-ended" };
    expect(desktopEventMatches(event, binding, "surface-1")).toBe(true);
    for (const changed of [{ session_id: "session-b" }, { context_ref: `ctx_${"b".repeat(24)}` }, { tab_id: "t2" }, { surface_id: "surface-old" }]) expect(desktopEventMatches({ ...event, ...changed }, binding, "surface-1")).toBe(false);
  });
  it("keeps CSS viewport geometry without substituting the old screenshot dimensions", () => {
    expect(desktopBounds(shown.bounds)).toEqual(shown.bounds);
    expect(desktopBounds({ ...shown.bounds, width: 0 })).toBeNull();
    expect(desktopBounds({ ...shown.bounds, x: NaN })).toBeNull();
  });
  it("hides dock slivers and off-window pages without clipping their full viewport", () => {
    const viewport = { width: 1440, height: 900 };
    expect(desktopSurfaceGeometry({ x: 1426, y: 90, width: 600, height: 598 }, viewport).visible).toBe(false);
    expect(desktopSurfaceGeometry({ x: 100, y: 90, width: 14, height: 598 }, viewport).visible).toBe(false);
    expect(desktopSurfaceGeometry({ x: 1441, y: 90, width: 600, height: 598 }, viewport).visible).toBe(false);
    const partial = { x: 1100, y: 90, width: 600, height: 598 };
    expect(desktopSurfaceGeometry(partial, viewport)).toEqual({ bounds: partial, visible: true });
    expect(desktopSurfaceGeometry(partial, viewport, [{ x: 1100, y: 90, width: 300, height: 600, clipX: true }]).visible).toBe(false);
  });
  it("accepts bounded read-only evidence only for the freshly confirmed target/version", () => {
    const state: NativeBrowserState = { backend: "native", control: "human", context_ref: binding.context_ref, viewed_tab: "t1", active_tab: "t2", url: "https://example.test/", title: "Example", tabs: [], viewport: { width: 913, height: 612 }, version: "2:t1:3:4" };
    const pick: NativeBrowserPick = { version: state.version!, tab_id: "t1", url: state.url, title: state.title, viewport: state.viewport, element: { ref: "p1", tag: "button", role: "button", name: "Save", text: "Save", rect: { x: 40, y: 40, width: 60, height: 30 } } };
    expect(desktopElementPick(pick, state, binding)?.block).toContain("read-only observation");
    expect(desktopElementPick({ ...pick, tab_id: "t2" }, state, binding)).toBeNull();
    expect(desktopElementPick(pick, { ...state, version: "2:t1:4:0" }, binding)).toBeNull();
    expect(desktopElementPick(pick, { ...state, context_ref: `ctx_${"b".repeat(24)}` }, binding)).toBeNull();
  });
});

describe("desktop surface lifecycle", () => {
  it("attaches an existing target hidden and sends only presentation claims", async () => {
    const api = bridge(), callbacks = { interactive: vi.fn(), error: vi.fn() };
    const surface = createDesktopSurface(api, binding, "view-1", callbacks);
    await surface.update({ bounds: shown.bounds, visible: false, blocked: true });
    await surface.attach();
    expect(api.attach).toHaveBeenCalledWith({ ...binding, view_id: "view-1" });
    expect(api.update).toHaveBeenLastCalledWith(expect.objectContaining({ visible: false, blocked: true, revision: 1 }));
    await surface.update(shown);
    expect(api.update).toHaveBeenLastCalledWith({ ...shown, surface_id: "surface-1", revision: 2 });
    expect(callbacks.interactive).toHaveBeenLastCalledWith(false); // Host declined the claim.
    await surface.close(); expect(api.detach).toHaveBeenCalledWith({ surface_id: "surface-1" });
  });
  it("sends hide immediately and ignores an older interactive response", async () => {
    const api = bridge(), callbacks = { interactive: vi.fn(), error: vi.fn() };
    const surface = createDesktopSurface(api, binding, "view-1", callbacks);
    await surface.attach();
    expect(api.update).not.toHaveBeenCalled(); // No invented 1×1 viewport at attach.
    const pending = deferred<{ interactive: boolean }>();
    vi.mocked(api.update).mockReturnValueOnce(pending.promise);
    const oldUpdate = surface.update(shown);
    await surface.update({ bounds: shown.bounds, visible: false, blocked: true });
    expect(api.update).toHaveBeenLastCalledWith(expect.objectContaining({ visible: false, blocked: true, revision: 2 }));
    pending.resolve({ interactive: true }); await oldUpdate;
    expect(callbacks.interactive).not.toHaveBeenCalledWith(true);
  });
  it("detaches a late attachment after unmount without ever showing it", async () => {
    const api = bridge(), pending = deferred<{ surface_id: string }>();
    vi.mocked(api.attach).mockReturnValue(pending.promise);
    const surface = createDesktopSurface(api, binding, "view-1", { interactive: vi.fn(), error: vi.fn() });
    await surface.update(shown);
    const opening = surface.attach(); await surface.close();
    pending.resolve({ surface_id: "late-surface" }); await opening;
    expect(api.update).not.toHaveBeenCalled();
    expect(api.detach).toHaveBeenCalledExactlyOnceWith({ surface_id: "late-surface" });
  });
  it("omits revoked lease claims and cannot restore interactive state after close", async () => {
    const api = bridge(), callbacks = { interactive: vi.fn(), error: vi.fn() };
    const surface = createDesktopSurface(api, binding, "view-1", callbacks); await surface.attach();
    const pending = deferred<{ interactive: boolean }>(); vi.mocked(api.update).mockReturnValueOnce(pending.promise);
    const update = surface.update(shown); await surface.close();
    pending.resolve({ interactive: true }); await update;
    expect(callbacks.interactive).not.toHaveBeenCalledWith(true);
    const before = vi.mocked(api.update).mock.calls.length;
    await surface.update({ bounds: shown.bounds, visible: true, blocked: true });
    expect(api.update).toHaveBeenCalledTimes(before);
  });
});
