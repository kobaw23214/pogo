import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, shallowRef } from "vue";
import { useImmersiveBrowser } from "./useImmersiveBrowser";

const lifecycle = vi.hoisted(() => ({ mounted: [] as (() => void)[], cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({
  ...await original<typeof import("vue")>(),
  onMounted: (callback: () => void) => lifecycle.mounted.push(callback),
  onBeforeUnmount: (callback: () => void) => lifecycle.cleanup.push(callback),
}));
// Motion rendering has its own compositor tests. These checks exercise the
// controller's visible/focus state while committing each local motion at once.
vi.mock("./immersiveMotion", () => ({ createImmersiveMotion: () => ({ run: (change: () => void) => change(), cancel: () => {} }) }));
vi.mock("./floatingChatMotion", () => ({ createFloatingChatMotion: () => ({ run: (_phase: string, start: () => void, finish: () => void) => { start(); finish(); }, cancel: () => {} }) }));

const scopes: ReturnType<typeof effectScope>[] = [];
const flush = async () => { await nextTick(); await nextTick(); };

class Surface extends EventTarget {
  parent: Surface | null = null;
  textContent = "";
  inert = false;
  isConnected = true;
  visible = true;
  focus = vi.fn(() => { fakeDocument.activeElement = this; });
  contains(node: unknown): boolean { return node === this || node instanceof Surface && !!node.parent && this.contains(node.parent); }
  getClientRects() { return this.visible ? [{}] : []; }
  getAttribute() { return null; }
  hasAttribute() { return false; }
  closest(selector: string): Surface | null {
    if (selector === "[inert]") return this.inert ? this : this.parent?.closest(selector) || null;
    return null;
  }
}

let fakeDocument: EventTarget & {
  activeElement: Surface | null;
  fullscreenEnabled: boolean;
  fullscreenElement: Surface | null;
  documentElement: { requestFullscreen: ReturnType<typeof vi.fn> };
  exitFullscreen: ReturnType<typeof vi.fn>;
  querySelector: ReturnType<typeof vi.fn>;
};
let fakeWindow: EventTarget & { innerWidth: number; innerHeight: number; requestAnimationFrame: ReturnType<typeof vi.fn>; cancelAnimationFrame: ReturnType<typeof vi.fn> };

beforeEach(() => {
  fakeDocument = Object.assign(new EventTarget(), {
    activeElement: null as Surface | null,
    fullscreenEnabled: true,
    fullscreenElement: null as Surface | null,
    documentElement: { requestFullscreen: vi.fn(async () => { fakeDocument.fullscreenElement = new Surface(); }) },
    exitFullscreen: vi.fn(async () => { fakeDocument.fullscreenElement = null; }),
    querySelector: vi.fn(),
  });
  fakeWindow = Object.assign(new EventTarget(), { innerWidth: 1280, innerHeight: 800,
    requestAnimationFrame: vi.fn(), cancelAnimationFrame: vi.fn() });
  vi.stubGlobal("document", fakeDocument);
  vi.stubGlobal("window", fakeWindow);
  vi.stubGlobal("Node", Surface);
  vi.stubGlobal("Element", Surface);
  vi.stubGlobal("HTMLElement", Surface);
});
afterEach(() => {
  for (const cleanup of lifecycle.cleanup.splice(0)) cleanup();
  lifecycle.mounted.length = 0;
  for (const scope of scopes.splice(0)) scope.stop();
  vi.unstubAllGlobals();
});

function mount(options?: Parameters<typeof useImmersiveBrowser>[2], manualExit = vi.fn()) {
  const composer = new Surface(), restore = new Surface(), canvas = new Surface();
  composer.textContent = "这段输入保持在原会话，不发送。";
  const shell = Object.assign(new Surface(), { querySelector: (selector: string) => selector === ".immersive-chat-restore" ? restore : null });
  const main = Object.assign(new Surface(), {
    querySelectorAll: () => [composer],
    closest: (selector: string) => selector === ".shell" ? shell : null,
  });
  composer.parent = main;
  const mainRef = shallowRef(main as unknown as HTMLElement);
  const scope = effectScope(); scopes.push(scope);
  const controller = scope.run(() => useImmersiveBrowser(mainRef, manualExit, options))!;
  for (const mounted of lifecycle.mounted.splice(0)) mounted();
  return { controller, main, mainRef, composer, restore, canvas, manualExit };
}

function key(value: string, shiftKey = false) {
  const event = Object.assign(new Event("keydown", { cancelable: true }), { key: value, shiftKey, isComposing: false });
  fakeWindow.dispatchEvent(event);
  return event;
}

describe("shared immersive conversation controller", () => {
  it("opens the existing conversation as a small visible window without system fullscreen", () => {
    const f = mount(); f.controller.enter();
    expect(f.controller.active.value).toBe(true);
    expect(f.controller.chatVisible.value).toBe(true);
    expect(f.controller.minimized.value).toBe(false);
    expect(f.controller.restoreVisible.value).toBe(false);
    expect(f.controller.style.value?.width).toBe("440px");
    expect(Number.parseFloat(f.controller.style.value!.height)).toBeLessThan(800);
    expect(f.mainRef.value).toBe(f.main);
    expect(fakeDocument.documentElement.requestFullscreen).not.toHaveBeenCalled();
  });

  it("collapses and restores the same conversation and focuses its retained input", async () => {
    const f = mount(); f.controller.enter(); const original = f.mainRef.value;
    f.controller.minimize(); await flush();
    expect(f.controller.chatVisible.value).toBe(false);
    expect(f.controller.restoreVisible.value).toBe(true);
    expect(fakeDocument.activeElement).toBe(f.restore);
    await f.controller.restore(); await flush();
    expect(f.controller.minimized.value).toBe(false);
    expect(f.controller.chatVisible.value).toBe(true);
    expect(f.controller.restoreVisible.value).toBe(false);
    expect(f.mainRef.value).toBe(original);
    expect(f.composer.textContent).toBe("这段输入保持在原会话，不发送。");
    expect(fakeDocument.activeElement).toBe(f.composer);
  });

  it("switches focus to a workflow canvas with F6 and exits only on Shift+Escape", async () => {
    const canvas = new Surface();
    const f = mount({ fullscreen: false, focusSurface: () => canvas as unknown as HTMLElement }); f.controller.enter();
    fakeDocument.activeElement = f.composer;
    expect(key("F6").defaultPrevented).toBe(true);
    expect(fakeDocument.activeElement).toBe(canvas);
    expect(fakeDocument.querySelector).not.toHaveBeenCalled();
    key("F6"); await flush();
    expect(fakeDocument.activeElement).toBe(f.composer);
    expect(key("Escape").defaultPrevented).toBe(false);
    expect(f.controller.active.value).toBe(true);
    expect(key("Escape", true).defaultPrevented).toBe(true);
    expect(f.controller.active.value).toBe(false);
    expect(f.manualExit).toHaveBeenCalledTimes(1);
  });

  it("automatically restores the workflow conversation for attention without taking canvas focus", async () => {
    const f = mount({ fullscreen: false }); f.controller.enter();
    const original = f.mainRef.value;
    f.controller.minimize(); await flush();
    fakeDocument.activeElement = f.canvas;
    await f.controller.restore(false); await flush();
    expect(f.controller.chatVisible.value).toBe(true);
    expect(f.controller.minimized.value).toBe(false);
    expect(f.controller.restoreVisible.value).toBe(false);
    expect(f.mainRef.value).toBe(original);
    expect(fakeDocument.activeElement).toBe(f.canvas);
    expect(f.composer.focus).not.toHaveBeenCalled();
    expect(fakeDocument.documentElement.requestFullscreen).not.toHaveBeenCalled();
    expect(fakeDocument.exitFullscreen).not.toHaveBeenCalled();
  });

  it("retains the browser default focus target and explicitly requested system fullscreen", async () => {
    const browserPage = new Surface(); fakeDocument.querySelector.mockReturnValue(browserPage);
    const f = mount(); f.controller.enter(); fakeDocument.activeElement = f.composer;
    key("F6"); expect(fakeDocument.activeElement).toBe(browserPage);
    expect(fakeDocument.querySelector).toHaveBeenCalledWith(".dock.immersive .native-page, .dock.immersive .urlbar");
    expect(f.controller.fullscreenAvailable).toBe(true);
    expect(await f.controller.toggleFullscreen()).toBe(true);
    expect(fakeDocument.documentElement.requestFullscreen).toHaveBeenCalledTimes(1);
    expect(await f.controller.toggleFullscreen()).toBe(true);
    expect(fakeDocument.exitFullscreen).toHaveBeenCalledTimes(1);
  });

  it("keeps workflow immersion out of another surface's system fullscreen state", async () => {
    const otherFullscreen = new Surface(); fakeDocument.fullscreenElement = otherFullscreen;
    const f = mount({ fullscreen: false }); f.controller.enter();
    expect(f.controller.fullscreenAvailable).toBe(false);
    expect(await f.controller.toggleFullscreen()).toBe(false);
    fakeDocument.dispatchEvent(new Event("fullscreenchange"));
    expect(f.controller.fullscreen.value).toBe(false);
    f.controller.exit(false);
    expect(fakeDocument.fullscreenElement).toBe(otherFullscreen);
    expect(fakeDocument.documentElement.requestFullscreen).not.toHaveBeenCalled();
    expect(fakeDocument.exitFullscreen).not.toHaveBeenCalled();
  });
});
