import { afterEach, describe, expect, it, vi } from "vitest";
import { observeOfficeInput } from "./officeInput";
class Frame extends EventTarget { constructor(public contentDocument: FrameDocument | null) { super(); } }
class FrameDocument extends EventTarget { frames: Frame[] = []; querySelectorAll() { return this.frames; } }
function key(value: string, extra: Record<string, unknown> = {}) { const event = new Event("keydown", { cancelable: true }); Object.assign(event, { key: value, ctrlKey: true, ...extra }); return event; }
function fixture() {
  vi.useFakeTimers(); const inner = new FrameDocument(), outer = new FrameDocument(), root = new FrameDocument();
  outer.frames.push(new Frame(inner)); root.frames.push(new Frame(outer));
  const callbacks = { activity: vi.fn(), composition: vi.fn(), save: vi.fn() };
  const stop = observeOfficeInput(root as unknown as HTMLElement, callbacks);
  return { root, outer, inner, callbacks, stop };
}
afterEach(() => vi.useRealTimers());
describe("Office frame input save scheduling", () => {
  it("routes Ctrl+S and Cmd+S in nested frames through confirmed workspace save", () => {
    const { inner, callbacks, stop } = fixture();
    const ctrl = key("s"), cmd = key("S", { ctrlKey: false, metaKey: true });
    inner.dispatchEvent(ctrl); inner.dispatchEvent(cmd);
    expect(ctrl.defaultPrevented).toBe(true); expect(cmd.defaultPrevented).toBe(true);
    expect(callbacks.save).toHaveBeenCalledTimes(2); stop();
  });
  it("does not save or consume the IME shortcut until composition has ended", () => {
    const { inner, callbacks, stop } = fixture();
    inner.dispatchEvent(new Event("compositionstart")); const shortcut = key("s"); inner.dispatchEvent(shortcut);
    expect(callbacks.composition).toHaveBeenLastCalledWith(true); expect(shortcut.defaultPrevented).toBe(false); expect(callbacks.save).not.toHaveBeenCalled();
    inner.dispatchEvent(new Event("beforeinput")); inner.dispatchEvent(new Event("compositionend")); inner.dispatchEvent(key("s"));
    expect(callbacks.composition).toHaveBeenLastCalledWith(false); expect(callbacks.save).toHaveBeenCalledOnce(); stop();
  });
  it("tracks delayed nested frames without reading or storing their input text", () => {
    const { outer, callbacks, stop } = fixture(); const later = new FrameDocument(); outer.frames.push(new Frame(later));
    vi.advanceTimersByTime(1000); later.dispatchEvent(new Event("beforeinput"));
    expect(callbacks.activity).toHaveBeenCalledOnce(); stop();
  });
  it("retires a reloaded document and releases its unfinished IME composition", () => {
    const { outer, inner, callbacks, stop } = fixture(); inner.dispatchEvent(new Event("compositionstart"));
    const replacement = new FrameDocument(); outer.frames[0]!.contentDocument = replacement; outer.frames[0]!.dispatchEvent(new Event("load"));
    expect(callbacks.composition).toHaveBeenLastCalledWith(false);
    inner.dispatchEvent(key("s")); expect(callbacks.save).not.toHaveBeenCalled(); replacement.dispatchEvent(key("s")); expect(callbacks.save).toHaveBeenCalledOnce(); stop();
  });
  it("ignores foreign embedded frames and removes observers and timers on disposal", () => {
    const { root, inner, callbacks, stop } = fixture(); const foreign = new Frame(null);
    Object.defineProperty(foreign, "contentDocument", { get() { throw new Error("cross origin"); } }); root.frames.push(foreign);
    expect(() => vi.advanceTimersByTime(1000)).not.toThrow(); stop();
    inner.dispatchEvent(key("s")); expect(callbacks.save).not.toHaveBeenCalled(); expect(vi.getTimerCount()).toBe(0);
  });
});
