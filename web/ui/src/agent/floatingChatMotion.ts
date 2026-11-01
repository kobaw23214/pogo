import { nextTick } from "vue";
import type { FloatingRect } from "./immersiveLayout";
import "./floatingChatMotion.css";

export type FloatingChatMotionPhase = "minimizing" | "restoring";
export function floatingChatMotionPlan(card: FloatingRect, pill: FloatingRect | null) {
  const distance = pill ? Math.hypot(card.x + card.width - pill.x - pill.width, card.y + card.height - pill.y - pill.height) : Infinity;
  const near = !!pill && card.width > 1 && card.height > 1 && pill.width > 1 && pill.height > 1
    && distance <= Math.min(220, Math.max(120, pill.width * 1.25));
  if (!near || !pill) return { near: false, transform: "translate3d(0, 16px, 0) scale(0.98)" };
  const x = pill.x + pill.width - card.x - card.width, y = pill.y + pill.height - card.y - card.height;
  return { near: true, transform: `translate3d(${x}px, ${y}px, 0) scale(${Math.min(1, pill.width / card.width)}, ${Math.min(1, pill.height / card.height)})` };
}

interface Pose { transform: string; opacity: number }
function pose(element: HTMLElement | null): Pose | undefined {
  if (!element?.getClientRects().length || typeof window === "undefined") return;
  const style = window.getComputedStyle(element), opacity = Number(style.opacity);
  return { transform: style.transform || "none", opacity: Number.isFinite(opacity) ? opacity : 1 };
}
const rect = (element: HTMLElement): FloatingRect => {
  const value = element.getBoundingClientRect();
  return { x: value.x, y: value.y, width: value.width, height: value.height };
};

/** Only the existing chat card and restore button animate. No page snapshots,
 * duplicate transcript, layout-size tween or browser-surface overlay. */
export function createFloatingChatMotion(
  card: () => HTMLElement | null,
  pill: () => HTMLElement | null,
  phase: (value: FloatingChatMotionPhase | undefined) => void,
) {
  let serial = 0, animations: Animation[] = [];
  let cleanup: (() => void) | undefined;
  let running = false;
  function cancel() {
    serial++;
    for (const animation of animations) animation.cancel();
    animations = [];
    cleanup?.(); cleanup = undefined;
    running = false;
    phase(undefined);
  }
  function run(direction: FloatingChatMotionPhase, prepare: () => void, complete: () => void) {
    // Read the currently drawn pose before cancellation, so rapid reversals
    // start where the user sees the card rather than jumping to an endpoint.
    const element = card(), current = running ? pose(element) : undefined;
    const currentPill = running ? pose(pill()) : undefined;
    const childPoses = new Map<HTMLElement, number>();
    if (running && element) for (const child of element.children) {
      if (child instanceof HTMLElement) childPoses.set(child, pose(child)?.opacity ?? 1);
    }
    cancel();
    const id = serial;
    running = true; phase(direction); prepare();
    void nextTick().then(() => {
      if (id !== serial) return;
      const chat = card(), restore = pill();
      const finish = () => {
        if (id !== serial) return;
        for (const animation of animations) animation.cancel();
        animations = []; cleanup?.(); cleanup = undefined;
        running = false; phase(undefined); complete();
      };
      if (!chat?.getClientRects().length || typeof chat.animate !== "function" || document.hidden
        || window.matchMedia("(prefers-reduced-motion: reduce)").matches) { finish(); return; }
      const plan = floatingChatMotionPlan(rect(chat), restore?.getClientRects().length ? rect(restore) : null);
      const minimizing = direction === "minimizing", end: Pose = { transform: minimizing ? plan.transform : "none", opacity: minimizing ? 0 : 1 };
      const start = current ?? { transform: minimizing ? "none" : plan.transform, opacity: minimizing ? 1 : 0 };
      const duration = current ? Math.max(80, (minimizing ? 220 : 260) * Math.abs(end.opacity - start.opacity)) : minimizing ? 220 : 260;
      const childFade = Math.min(100, duration * .45);
      const geometryDuration = duration * .7;
      const origin = chat.style.transformOrigin;
      chat.style.transformOrigin = "100% 100%";
      cleanup = () => { chat.style.transformOrigin = origin; };
      try {
        const ease = "cubic-bezier(.22, 1, .36, 1)";
        // A near anchor collects only an empty outline. Hold full geometry
        // until text has vanished; on restore, reach full geometry first.
        // Segment easing keeps that hold at its real duration.
        const frames: Keyframe[] = plan.near ? minimizing ? [
          { ...start, offset: 0 }, { ...start, offset: childFade / duration, easing: ease }, { ...end, offset: 1 },
        ] : [
          { ...start, offset: 0, easing: ease }, { ...end, offset: geometryDuration / duration }, { ...end, offset: 1 },
        ] : [{ ...start }, { ...end }];
        animations.push(chat.animate(frames, { duration, easing: plan.near ? "linear" : ease, fill: "both" }));
        for (const child of chat.children) {
          if (!(child instanceof HTMLElement) || !child.getClientRects().length || typeof child.animate !== "function") continue;
          animations.push(child.animate([
            { opacity: childPoses.get(child) ?? (minimizing ? 1 : 0) }, { opacity: minimizing ? 0 : 1 },
          ], { duration: minimizing ? childFade : plan.near ? duration - geometryDuration : Math.min(180, duration * .75),
            delay: !minimizing && plan.near ? geometryDuration : 0, fill: "both" }));
        }
        if (restore && typeof restore.animate === "function") animations.push(restore.animate([
          { opacity: currentPill?.opacity ?? (minimizing ? 0 : 1) }, { opacity: minimizing ? 1 : 0 },
        ], { duration: minimizing ? Math.min(130, duration) : Math.min(100, duration),
          delay: minimizing && !current ? Math.max(0, duration - 130) : 0, fill: "both" }));
        void Promise.allSettled(animations.map(animation => animation.finished)).then(finish);
      } catch { finish(); }
    });
  }
  return { run, cancel };
}
