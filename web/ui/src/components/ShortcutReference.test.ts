import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";
const host = vi.hoisted(() => ({ cleanups: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useSSRContext: () => ({ modules: new Set() }), onBeforeUnmount: (fn: () => void) => host.cleanups.push(fn) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("./ShortcutDetails.vue", () => ({ default: {} }));
import ShortcutReference from "./ShortcutReference.vue";
const scopes: ReturnType<typeof effectScope>[] = [];
const observers: { callback: ResizeObserverCallback; observe: ReturnType<typeof vi.fn<(element: Element) => void>>; disconnect: ReturnType<typeof vi.fn<() => void>> }[] = [];
interface Controller {
  panel: Ref<HTMLDivElement | undefined>;
  popover: Ref<{ popperRef?: { popperInstanceRef?: { update: () => unknown } } } | undefined>;
}
function mount() {
  const scope = effectScope(); scopes.push(scope);
  return scope.run(() => (ShortcutReference as unknown as { setup(props: unknown, context: unknown): Controller }).setup(reactive({ title: "完整资料", content: "不能裁剪的长资料", detailsOnly: false }), { emit: vi.fn(), expose() {} }))!;
}
beforeEach(() => {
  vi.stubGlobal("document", { addEventListener: vi.fn(), removeEventListener: vi.fn() });
  vi.stubGlobal("ResizeObserver", class { value: typeof observers[number]; constructor(callback: ResizeObserverCallback) { this.value = { callback, observe: vi.fn<(element: Element) => void>(), disconnect: vi.fn<() => void>() }; observers.push(this.value); } observe(element: Element) { this.value.observe(element); } disconnect() { this.value.disconnect(); } });
});
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); for (const cleanup of host.cleanups.splice(0)) cleanup(); observers.length = 0; vi.unstubAllGlobals(); });

describe("Shortcut reference popover sizing lifetime", () => {
  it("repositions when a lazily mounted preview changes size and disconnects when it leaves", async () => {
    const c = mount(), update = vi.fn();
    c.popover.value = { popperRef: { popperInstanceRef: { update } } };
    const panel = {} as HTMLDivElement; c.panel.value = panel; await nextTick();
    expect(observers).toHaveLength(1); expect(observers[0].observe).toHaveBeenCalledWith(panel);
    observers[0].callback([], {} as ResizeObserver); expect(update).toHaveBeenCalledTimes(1);
    c.panel.value = undefined; await nextTick(); expect(observers[0].disconnect).toHaveBeenCalledTimes(1);
  });
  it("replaces the observer for a new preview and releases it on component teardown", async () => {
    const c = mount(); c.panel.value = {} as HTMLDivElement; await nextTick();
    c.panel.value = {} as HTMLDivElement; await nextTick();
    expect(observers).toHaveLength(2); expect(observers[0].disconnect).toHaveBeenCalledTimes(1);
    scopes[0].stop(); expect(observers[1].disconnect).toHaveBeenCalledTimes(1);
  });
});
