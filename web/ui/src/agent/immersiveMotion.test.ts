import { afterEach, describe, expect, it, vi } from "vitest";
import { nextTick } from "vue";
import { createImmersiveMotion, immersiveSurfaceTransform } from "./immersiveMotion";
import { browserLayoutSize, IMMERSIVE_LAYOUT_EVENT, IMMERSIVE_PRESENTED_EVENT } from "./immersiveViewportMotion";

afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); });
const flush = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); };
function fixture(reduced = false) {
  const events: { transitionId: string; phase: string; animated: boolean; duration: number; reason?: string; layout: boolean }[] = [];
  const listeners = new Map<string, Set<(event: Event) => void>>();
  const pending: { frames: Keyframe[]; options: KeyframeAnimationOptions; finish: () => void; cancel: ReturnType<typeof vi.fn> }[] = [];
  let immersive = false;
  const make = (kind: "dock" | "chat") => ({
    style: { transformOrigin: "50% 50%", willChange: "" },
    getClientRects: () => [{}],
    getBoundingClientRect: () => kind === "dock"
      ? immersive ? { x: 0, y: 0, width: 1400, height: 900 } : { x: 900, y: 0, width: 500, height: 900 }
      : immersive ? { x: 940, y: 200, width: 440, height: 660 } : { x: 240, y: 0, width: 660, height: 900 },
    animate: (frames: Keyframe[], options: KeyframeAnimationOptions) => {
      let finish!: () => void;
      const finished = new Promise<void>(resolve => { finish = resolve; });
      const cancel = vi.fn(); pending.push({ frames, options, finish, cancel });
      return { finished, cancel } as unknown as Animation;
    },
  });
  const dock = { ...make("dock"), querySelectorAll: () => [{ getClientRects: () => [{}] }],
    addEventListener: (name: string, listener: (event: Event) => void) => { if (!listeners.has(name)) listeners.set(name, new Set()); listeners.get(name)!.add(listener); },
    removeEventListener: (name: string, listener: (event: Event) => void) => listeners.get(name)?.delete(listener),
    dispatchEvent: (event: CustomEvent) => { Object.assign(event, { target: dock }); if (event.type === IMMERSIVE_LAYOUT_EVENT) events.push({ ...event.detail, layout: immersive }); for (const listener of listeners.get(event.type) ?? []) listener(event); } };
  const shell = { dataset: {} as Record<string, string>, querySelector: () => dock };
  const chat = { ...make("chat"), closest: () => shell, querySelector: () => null };
  vi.stubGlobal("CustomEvent", class { constructor(public type: string, public options: { detail: unknown }) {} get detail() { return this.options.detail; } });
  const capture = vi.fn();
  vi.stubGlobal("document", { hidden: false, startViewTransition: capture });
  vi.stubGlobal("window", { matchMedia: () => ({ matches: reduced }) });
  const phase = vi.fn(), motion = createImmersiveMotion(() => chat as unknown as HTMLElement, phase);
  const ready = (transitionId = [...events].reverse().find(event => event.phase === "start")!.transitionId) => dock.dispatchEvent(new CustomEvent(IMMERSIVE_PRESENTED_EVENT, { detail: { transitionId, status: "ready", reason: "decoded-frame" } }));
  return { motion, phase, events, pending, capture, shell, chat, dock, ready, set: (value: boolean) => { immersive = value; } };
}

describe("uninterrupted immersive geometry", () => {
  it("captures before CSS changes and moves directly to the final rectangle once", async () => {
    const f = fixture(); f.motion.run(() => f.set(true), "entering"); await nextTick();
    expect(f.events.map(event => event.phase)).toEqual(["start", "commit"]);
    expect(f.events[0]).toMatchObject({ layout: false, duration: 420 });
    expect(f.events[1]).toMatchObject({ layout: true });
    expect(f.pending).toHaveLength(2); expect(f.capture).not.toHaveBeenCalled();
    for (const animation of f.pending) { expect(animation.options.duration).toBe(420); expect(animation.frames).toHaveLength(2); expect(animation.frames[1]!.transform).toBe("none"); animation.finish(); }
    await flush();
    expect(f.events.map(event => event.phase)).toEqual(["start", "commit", "finish"]);
    expect(f.events[2]).toMatchObject({ reason: "geometry-complete" });
    expect(f.shell.dataset.immersiveMotion).toBeUndefined();
  });
  it("finishes geometry without any decoded-frame event and cannot restart on a late frame", async () => {
    vi.useFakeTimers(); const f = fixture(); f.motion.run(() => f.set(true), "entering"); await nextTick();
    for (const animation of f.pending) animation.finish(); await flush();
    const count = f.events.length; f.ready(); vi.advanceTimersByTime(2000); await flush();
    expect(f.events).toHaveLength(count); expect(f.pending).toHaveLength(2); expect(vi.getTimerCount()).toBe(0);
  });
  it("an early frame cannot pause, shorten or introduce a second geometry segment", async () => {
    const f = fixture(); f.motion.run(() => f.set(true), "entering"); await nextTick(); f.ready();
    expect(f.events.map(event => event.phase)).toEqual(["start", "commit"]); expect(f.pending).toHaveLength(2);
    for (const animation of f.pending) animation.finish(); await flush();
    expect(f.pending).toHaveLength(2); expect(f.events[2]).toMatchObject({ phase: "finish" });
  });
  it("captures reversal pixels before cancelling the previous compositor pose and ignores its late completion", async () => {
    const f = fixture(); f.motion.run(() => f.set(true), "entering"); await nextTick();
    const old = f.pending.slice(); f.motion.run(() => f.set(false), "leaving"); await nextTick();
    expect(f.events.map(event => event.phase)).toEqual(["start", "commit", "start", "finish", "commit"]);
    expect(f.events[2]).toMatchObject({ layout: true, duration: 340 }); expect(f.events[3]).toMatchObject({ reason: "cancelled" });
    for (const animation of old) animation.finish(); await flush(); expect(f.events).toHaveLength(5);
    for (const animation of f.pending.slice(2)) animation.finish(); await flush();
    expect(f.events[5]).toMatchObject({ phase: "finish", reason: "geometry-complete" });
    expect(f.chat.style.transformOrigin).toBe("50% 50%");
  });
  it("commits reduced-motion layout without animation, timers or a media wait", async () => {
    const f = fixture(true); f.motion.run(() => f.set(true), "entering"); await nextTick(); await flush();
    expect(f.pending).toHaveLength(0); expect(f.events.map(event => event.phase)).toEqual(["start", "commit", "finish"]);
    expect(f.events[2]).toMatchObject({ reason: "instant", animated: false, duration: 0 });
  });
  it("keeps remote dimensions independent from presentation scale", () => {
    const before = { x: 900, y: 0, width: 500, height: 900 }, after = { x: 0, y: 0, width: 1400, height: 900 };
    expect(browserLayoutSize({ clientWidth: 1400, clientHeight: 900 })).toEqual({ width: 1400, height: 900 });
    expect(immersiveSurfaceTransform(before, after)).toBe("translate3d(900px, 0px, 0) scale(0.35714285714285715, 1)");
    expect(immersiveSurfaceTransform({ ...before, width: 0 }, after)).toBeUndefined();
  });
});
