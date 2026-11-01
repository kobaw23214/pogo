import { nextTick } from "vue";
import { notifyImmersiveLayout } from "./immersiveViewportMotion";
import "./immersiveMotion.css";

export type ImmersiveMotionPhase = "entering" | "leaving";
let instanceSequence = 0;

function conversationPosition(chat: HTMLElement) {
  const scroller = chat.querySelector<HTMLElement>(".conversation");
  if (!scroller || !scroller.clientHeight) return () => {};
  const bounds = scroller.getBoundingClientRect();
  const atBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80;
  const paragraphs = [...scroller.querySelectorAll<HTMLElement>(".conv-inner p, .conv-inner li, .conv-inner pre, .conv-inner [data-mid]")];
  const anchor = paragraphs.find(element => {
    const box = element.getBoundingClientRect();
    return box.top >= bounds.top && box.top < bounds.bottom;
  });
  const offset = anchor ? anchor.getBoundingClientRect().top - bounds.top : 0;
  const top = scroller.scrollTop;
  return () => {
    if (!scroller.isConnected) return;
    if (atBottom) scroller.scrollTop = scroller.scrollHeight;
    else if (anchor?.isConnected) scroller.scrollTop += anchor.getBoundingClientRect().top - scroller.getBoundingClientRect().top - offset;
    else scroller.scrollTop = top;
  };
}

interface MotionRect { x: number; y: number; width: number; height: number }
export function immersiveSurfaceTransform(before: MotionRect, after: MotionRect): string | undefined {
  if (![before.x, before.y, before.width, before.height, after.x, after.y, after.width, after.height].every(Number.isFinite)
    || before.width <= 0 || before.height <= 0 || after.width <= 0 || after.height <= 0) return;
  return `translate3d(${before.x - after.x}px, ${before.y - after.y}px, 0) scale(${before.width / after.width}, ${before.height / after.height})`;
}

/** Move the existing live surfaces rather than frozen ViewTransition images.
 * The real layout reaches its final size at the start, so remote video can
 * resize and present during the motion instead of jumping afterward. */
export function createImmersiveMotion(
  main: () => HTMLElement | null,
  phase: (value: ImmersiveMotionPhase | undefined) => void,
) {
  let serial = 0;
  const instance = ++instanceSequence;
  let animations: Animation[] = [];
  let cleanup: (() => void) | undefined;

  function cancel() {
    serial++;
    for (const animation of animations) animation.cancel();
    animations = [];
    cleanup?.(); cleanup = undefined;
    phase(undefined);
  }

  function run(change: () => void, direction: ImmersiveMotionPhase, animate = true) {
    const chat = main(), shell = chat?.closest<HTMLElement>(".shell");
    const dock = shell?.querySelector<HTMLElement>(":scope > .dock");
    // A rapid reversal starts at the currently drawn position, before cancel
    // removes the preceding animation's temporary transform.
    const surfaces = [dock, chat].filter((element): element is HTMLElement => !!element);
    const before = new Map(surfaces.filter(element => element.getClientRects().length)
      .map(element => [element, element.getBoundingClientRect()]));
    if (!chat || !shell || !dock) {
      cancel();
      change();
      return;
    }
    const animated = animate && !document.hidden && !window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const duration = animated ? direction === "entering" ? 420 : 340 : 0;
    const transitionId = `immersive-${instance}-${serial + 1}`;
    const notify = (value: { phase: "start" | "commit" | "finish"; reason?: "geometry-complete" | "instant" | "cancelled" }) =>
      notifyImmersiveLayout(dock, { ...value, transitionId, direction, animated, duration });
    // Pixel bridges capture the real painted rectangle before an interrupted
    // animation or a CSS layout change removes its current compositor pose.
    notify({ phase: "start" });
    cancel();
    const id = serial;
    const restorePosition = conversationPosition(chat);
    let completed = false;
    if (animated) phase(direction);
    shell.dataset.immersiveMotion = animated ? direction : "instant";
    const previous = surfaces.map(element => ({ origin: element.style.transformOrigin, willChange: element.style.willChange }));
    cleanup = () => {
      surfaces.forEach((element, index) => {
        element.style.transformOrigin = previous[index]!.origin;
        element.style.willChange = previous[index]!.willChange;
      });
      delete shell.dataset.immersiveMotion;
      notify({ phase: "finish", reason: completed ? animated ? "geometry-complete" : "instant" : "cancelled" });
    };
    const finish = () => {
      if (id !== serial) return;
      completed = true;
      for (const animation of animations) animation.cancel();
      animations = [];
      cleanup?.(); cleanup = undefined;
      phase(undefined);
    };
    change();
    void nextTick().then(() => {
      if (id !== serial) return;
      restorePosition();
      if (animated) {
        for (const element of surfaces) {
          if (!element.getClientRects().length || typeof element.animate !== "function") continue;
          const start = before.get(element), end = element.getBoundingClientRect();
          const transform = start && immersiveSurfaceTransform(start, end);
          element.style.transformOrigin = "0 0";
          element.style.willChange = "transform, opacity";
          try {
            animations.push(element.animate([
              { transform: transform ?? "translate3d(0, 12px, 0)", opacity: start ? 1 : 0 },
              { transform: "none", opacity: 1 },
            ], { duration, easing: "cubic-bezier(.22, 1, .36, 1)", fill: "both" }));
          } catch { /* Unsupported compositor animation still commits usable layout. */ }
        }
      }
      // This event never navigates, claims a Page, or opens a live socket.
      notify({ phase: "commit" });
      // Geometry has one uninterrupted clock. New media can only replace
      // pixels; it never pauses, resumes, or starts another shell animation.
      void Promise.allSettled(animations.map(animation => animation.finished)).then(finish);
    });
  }

  return { run, cancel };
}
