import { afterEach, describe, expect, it, vi } from "vitest";
import { nextTick } from "vue";
import { createFloatingChatMotion, floatingChatMotionPlan } from "./floatingChatMotion";
describe("floating conversation collection geometry", () => {
  it("uses a nearby fixed restore anchor for a card at its default right-bottom position", () => {
    expect(floatingChatMotionPlan({ x: 988, y: 328, width: 440, height: 660 }, { x: 1264, y: 936, width: 160, height: 44 }))
      .toEqual({ near: true, transform: "translate3d(-4px, -8px, 0) scale(0.36363636363636365, 0.06666666666666667)" });
  });
  it("keeps a dragged card's far-away motion short without travelling across the browser", () => {
    expect(floatingChatMotionPlan({ x: 20, y: 100, width: 440, height: 500 }, { x: 1264, y: 936, width: 160, height: 44 }))
      .toEqual({ near: false, transform: "translate3d(0, 16px, 0) scale(0.98)" });
  });
  it("keeps hidden or zero-size anchors out of geometry collection", () => {
    for (const pill of [null, { x: 0, y: 0, width: 0, height: 0 }])
      expect(floatingChatMotionPlan({ x: 0, y: 0, width: 440, height: 660 }, pill).near).toBe(false);
  });
});

afterEach(() => vi.unstubAllGlobals());
function motionFixture(reduced = false) {
  class FakeElement {}
  vi.stubGlobal("HTMLElement", FakeElement);
  const element = (bounds: { x: number; y: number; width: number; height: number }) => {
    const requests: { frames: Keyframe[]; options: KeyframeAnimationOptions; finish: () => void; cancel: ReturnType<typeof vi.fn> }[] = [];
    const value = Object.assign(new FakeElement(), { style: { transformOrigin: "50% 50%" }, children: [] as FakeElement[], drawn: { transform: "none", opacity: "1" },
      getClientRects: () => [bounds], getBoundingClientRect: () => bounds,
      animate: (frames: Keyframe[], options: KeyframeAnimationOptions) => {
        let finish!: () => void, reject!: (reason: Error) => void;
        const finished = new Promise<void>((resolve, failure) => { finish = resolve; reject = failure; });
        const cancel = vi.fn(() => reject(new Error("cancelled")));
        requests.push({ frames, options, finish, cancel });
        return { finished, cancel } as unknown as Animation;
      } });
    return { value, requests };
  };
  const card = element({ x: 988, y: 328, width: 440, height: 660 }), pill = element({ x: 1264, y: 936, width: 160, height: 44 });
  const child = element({ x: 988, y: 328, width: 440, height: 60 });
  card.value.children.push(child.value);
  vi.stubGlobal("window", { matchMedia: () => ({ matches: reduced }), getComputedStyle: (value: typeof card.value) => value.drawn });
  vi.stubGlobal("document", { hidden: false });
  const phase = vi.fn(), motion = createFloatingChatMotion(() => card.value as unknown as HTMLElement, () => pill.value as unknown as HTMLElement, phase);
  return { card, pill, child, phase, motion };
}
describe("floating conversation animation lifecycle", () => {
  it("uses separate short collapse/restore motions while keeping real card dimensions unchanged", async () => {
    const f = motionFixture(), complete = vi.fn();
    f.motion.run("minimizing", () => {}, complete); await nextTick();
    expect(f.card.requests[0].options.duration).toBe(220);
    expect(f.card.requests[0].frames[0].transform).toBe("none");
    f.card.requests[0].finish(); f.pill.requests[0].finish(); f.child.requests[0].finish(); await Promise.resolve(); await Promise.resolve();
    expect(complete).toHaveBeenCalledOnce();
    expect(f.card.value.style.transformOrigin).toBe("50% 50%");
    f.motion.run("restoring", () => {}, () => {}); await nextTick();
    expect(f.card.requests[1].options.duration).toBe(260);
    expect(f.card.requests[1].frames[1]).toMatchObject({ transform: "none", opacity: 1 });
    f.motion.cancel();
  });
  it("keeps readable content at full scale and collects only the outline toward a nearby anchor", async () => {
    const f = motionFixture();
    f.motion.run("minimizing", () => {}, () => {}); await nextTick();
    const collapse = f.card.requests[0], fadeOut = f.child.requests[0];
    expect(collapse.options.easing).toBe("linear");
    expect(collapse.frames[1]).toMatchObject({ transform: "none", opacity: 1 });
    expect(Number(collapse.frames[1].offset) * Number(collapse.options.duration)).toBe(Number(fadeOut.options.duration));
    f.motion.cancel();
    f.motion.run("restoring", () => {}, () => {}); await nextTick();
    const expand = f.card.requests[1], fadeIn = f.child.requests[1];
    expect(expand.frames[1]).toMatchObject({ transform: "none", opacity: 1 });
    expect(fadeIn.options.delay).toBe(Number(expand.frames[1].offset) * Number(expand.options.duration));
    expect(Number(fadeIn.options.delay) + Number(fadeIn.options.duration)).toBe(expand.options.duration);
    f.motion.cancel();
  });
  it("continues a rapid reversal from the drawn pose and refuses stale completion callbacks", async () => {
    const f = motionFixture(), minimized = vi.fn(), restored = vi.fn();
    f.motion.run("minimizing", () => {}, minimized); await nextTick();
    f.card.value.drawn = { transform: "matrix(0.7, 0, 0, 0.7, 0, 8)", opacity: "0.7" };
    f.motion.run("restoring", () => {}, restored); await nextTick();
    expect(f.card.requests[0].cancel).toHaveBeenCalled();
    expect(f.card.requests[1].frames[0]).toMatchObject({ transform: "matrix(0.7, 0, 0, 0.7, 0, 8)", opacity: .7 });
    f.card.requests[1].finish(); f.pill.requests[1].finish(); f.child.requests[1].finish(); await Promise.resolve(); await Promise.resolve();
    expect(minimized).not.toHaveBeenCalled(); expect(restored).toHaveBeenCalledOnce();
  });
  it("cancellation removes temporary animation geometry and cannot replay an old view", async () => {
    const f = motionFixture(), complete = vi.fn();
    f.motion.run("minimizing", () => {}, complete); await nextTick();
    f.motion.cancel(); await Promise.resolve(); await Promise.resolve();
    expect(complete).not.toHaveBeenCalled();
    expect(f.card.value.style.transformOrigin).toBe("50% 50%");
    expect(f.phase).toHaveBeenLastCalledWith(undefined);
  });
  it("reduced-motion preference commits visibility without creating compositor animations", async () => {
    const f = motionFixture(true), prepare = vi.fn(), complete = vi.fn();
    f.motion.run("minimizing", prepare, complete); await nextTick();
    expect(prepare).toHaveBeenCalledOnce(); expect(complete).toHaveBeenCalledOnce();
    expect(f.card.requests).toHaveLength(0); expect(f.pill.requests).toHaveLength(0);
  });
});
