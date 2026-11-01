import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, type Ref } from "vue";
import { adjustFloatingChat, fitFloatingChat, type FloatingRect } from "./immersiveLayout";
import { createImmersiveMotion, type ImmersiveMotionPhase } from "./immersiveMotion";
import { createFloatingChatMotion, type FloatingChatMotionPhase } from "./floatingChatMotion";

export interface ImmersiveConversationOptions {
  /** Another canvas can reuse the floating conversation without targeting a browser. */
  focusSurface?: () => HTMLElement | null;
  /** Browser fullscreen remains an explicit capability of the browser surface. */
  fullscreen?: boolean;
}

/** Layout-only: the conversation, work surface and their subscriptions stay mounted. */
export function useImmersiveBrowser(main: Ref<HTMLElement | null>, onManualExit?: () => void, options: ImmersiveConversationOptions = {}) {
  const active = ref(false);
  const minimized = ref(false);
  const chatVisible = ref(true);
  const restoreVisible = ref(false);
  const chatMotionPhase = ref<FloatingChatMotionPhase>();
  const chatAnimating = computed(() => !!chatMotionPhase.value);
  const manipulating = ref(false);
  const fullscreen = ref(false);
  const fullscreenAvailable = options.fullscreen !== false && typeof document !== "undefined" && Boolean(document.fullscreenEnabled);
  const rect = ref<FloatingRect | null>(null);
  const motionPhase = ref<ImmersiveMotionPhase>();
  const interacting = ref(false);
  const pointers = new Set<number>();
  let composing = false;
  let compositionTarget: EventTarget | null = null;
  let pendingMinimize = false;
  let pendingMinimizeVersion = 0;
  let pendingFocus: number | undefined;
  let manualFocusVersion: number | undefined;
  let focusFrame: number | undefined;
  let finishFocusWait: (() => void) | undefined;
  const syncInteraction = () => { interacting.value = composing || pointers.size > 0; };
  const interruptEntrance = () => {
    if (motionPhase.value !== "entering") return;
    if (!active.value) intendedActive = false;
    motion.cancel();
  };
  const pointerStarted = (event: PointerEvent) => {
    // A click aimed at an in-flight browser rectangle cannot be replayed at
    // its final position.
    if (motionPhase.value && event.target instanceof Element && event.target.closest(".dock .native-page")) {
      event.preventDefault(); event.stopPropagation();
      return;
    }
    cancelManualFocusFromTarget(event.target);
    pointers.add(event.pointerId); syncInteraction();
    // Keep activation controls under the pointer until their click handler
    // runs. Cancelling here moves the button between down/up and loses the
    // click; enter/exit handlers already reverse the motion themselves.
    if (!(event.target instanceof Element && event.target.closest("button, a, [role='button'], input[type='button'], input[type='submit']"))) interruptEntrance();
  };
  const focusChanged = (event: FocusEvent) => { cancelManualFocusFromTarget(event.target); };
  const pointerEnded = (event: PointerEvent) => { pointers.delete(event.pointerId); syncInteraction(); };
  const compositionStarted = (event: CompositionEvent) => { composing = true; compositionTarget = event.target; syncInteraction(); interruptEntrance(); };
  const compositionEnded = () => {
    composing = false; compositionTarget = null; syncInteraction();
    // The input's own compositionend handler commits the draft in this event.
    // Vue applies visibility afterward; never hide a half-composed field.
    if (pendingMinimize) commitMinimize(pendingMinimizeVersion);
    if (pendingFocus !== undefined) { const version = pendingFocus; pendingFocus = undefined; scheduleComposerFocus(version); }
  };
  const resetInteraction = () => {
    pointers.clear(); composing = false; compositionTarget = null; syncInteraction();
    // Losing the app window must not run a deferred focus or hide operation.
    if (manualFocusVersion !== undefined) focusSequence++;
    pendingMinimize = false; clearScheduledFocus();
  };
  const motion = createImmersiveMotion(() => main.value, value => { motionPhase.value = value; });
  const restoreButton = () => main.value?.closest(".shell")?.querySelector<HTMLButtonElement>(".immersive-chat-restore") ?? null;
  const restoreSurface = () => main.value?.closest(".shell")?.querySelector<HTMLElement>(".immersive-minibar") ?? restoreButton();
  function getComposerTarget(): HTMLElement | undefined {
    return Array.from(main.value?.querySelectorAll<HTMLElement>(".input-card textarea, .input-card .inline-composer-document[contenteditable='true']") ?? [])
      .find(input => input.getClientRects().length > 0 && !input.hasAttribute("disabled") && input.getAttribute("aria-disabled") !== "true" && !input.closest("[inert]"));
  }
  const chatMotion = createFloatingChatMotion(() => main.value,
    restoreSurface, value => { chatMotionPhase.value = value; });
  let intendedActive = false;
  let focusSequence = 0;
  let desktopRect: FloatingRect | null = null;
  let trigger: HTMLElement | null = null;
  let ownsFullscreen = false;
  let stopGesture: (() => void) | undefined;
  let fittedOffset = { x: 0, y: 0 };
  const viewport = () => ({ width: window.visualViewport?.width ?? window.innerWidth, height: window.visualViewport?.height ?? window.innerHeight });
  const narrow = () => viewport().width <= 720;
  function fit() {
    if (!active.value) return;
    const fitted = fitFloatingChat(narrow() ? null : desktopRect, viewport());
    const offset = { x: window.visualViewport?.offsetLeft ?? 0, y: window.visualViewport?.offsetTop ?? 0 };
    const changed = !rect.value || Object.keys(fitted).some(key => fitted[key as keyof FloatingRect] !== rect.value![key as keyof FloatingRect])
      || offset.x !== fittedOffset.x || offset.y !== fittedOffset.y;
    if (changed) {
      stopGesture?.();
      if (chatAnimating.value) settleChatMotion();
    }
    rect.value = fitted; fittedOffset = offset;
    if (!narrow()) desktopRect = { ...rect.value };
  }
  const style = computed(() => active.value && rect.value ? {
    left: `${rect.value.x + (window.visualViewport?.offsetLeft ?? 0)}px`,
    top: `${rect.value.y + (window.visualViewport?.offsetTop ?? 0)}px`,
    width: `${rect.value.width}px`, height: `${rect.value.height}px`,
  } : undefined);
  function enter() {
    if (intendedActive) return;
    intendedActive = true;
    focusSequence++;
    trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    motion.run(() => {
      minimized.value = false;
      active.value = true;
      fit();
    }, "entering");
  }
  function exit(restoreFocus = true) {
    const focusVersion = ++focusSequence;
    if (restoreFocus) onManualExit?.();
    intendedActive = false;
    stopGesture?.();
    pendingMinimize = false; clearScheduledFocus();
    chatMotion.cancel();
    chatVisible.value = !minimized.value; restoreVisible.value = minimized.value;
    const change = () => {
      active.value = false; minimized.value = false;
      if (restoreFocus) void nextTick(() => {
        if (intendedActive || focusVersion !== focusSequence) return;
        if (trigger?.isConnected && trigger.getClientRects().length) trigger.focus({ preventScroll: true });
        else getComposerTarget()?.focus({ preventScroll: true });
      });
    };
    // Session changes/unmounts are immediate and cannot replay a captured view
    // of the previous conversation into a different one.
    if (!active.value) { motion.cancel(); change(); }
    else motion.run(change, "leaving", restoreFocus);
    if (ownsFullscreen && document.fullscreenElement) void document.exitFullscreen().catch(() => {});
    ownsFullscreen = false;
  }
  function clearScheduledFocus() {
    pendingFocus = undefined; manualFocusVersion = undefined;
    if (focusFrame !== undefined) window.cancelAnimationFrame(focusFrame);
    focusFrame = undefined;
    finishFocusWait?.(); finishFocusWait = undefined;
  }
  function cancelManualFocusFromTarget(target: EventTarget | null) {
    if (manualFocusVersion === undefined || !(target instanceof Node)) return;
    const input = getComposerTarget();
    if (input?.contains(target) || restoreButton()?.contains(target)) return;
    // A later deliberate pointer/keyboard focus choice wins over a deferred
    // manual expansion, including one waiting for browser compositionend.
    focusSequence++; clearScheduledFocus();
  }
  function focusComposer(version: number): boolean {
    if (!active.value || minimized.value || version !== focusSequence || pendingMinimize) return true;
    if (composing) { pendingFocus = version; return true; }
    const chat = main.value, input = getComposerTarget();
    if (!chat?.getClientRects().length || chat.inert || !input) return false;
    input.focus({ preventScroll: true });
    const focused = document.activeElement === input;
    if (focused) manualFocusVersion = undefined;
    return focused;
  }
  async function scheduleComposerFocus(version: number) {
    await nextTick();
    if (focusComposer(version)) return;
    await new Promise<void>(resolve => {
      // The visibility/inert bindings can land one render after the logical
      // state changes. Only the latest manual intent gets a bounded retry.
      finishFocusWait = resolve;
      focusFrame = window.requestAnimationFrame(() => {
        focusFrame = undefined;
        void nextTick(() => {
          focusComposer(version);
          if (finishFocusWait === resolve) finishFocusWait = undefined;
          resolve();
        });
      });
    });
  }
  async function restore(focus = true) {
    if (!active.value || (!focus && !minimized.value && !pendingMinimize)) return;
    const version = ++focusSequence;
    pendingMinimize = false; clearScheduledFocus();
    if (focus) manualFocusVersion = version;
    stopGesture?.();
    fit();
    minimized.value = false;
    await nextTick();
    // Manual expansion is writable as soon as its real DOM is visible. A
    // gate/reference expansion keeps the browser's input focus untouched.
    if (focus) await scheduleComposerFocus(version);
  }
  function commitMinimize(version: number) {
    pendingMinimize = false;
    if (!active.value || version !== focusSequence) return;
    minimized.value = true;
    void nextTick(() => {
      if (!active.value || !minimized.value || version !== focusSequence) return;
      restoreButton()?.focus({ preventScroll: true });
    });
  }
  function minimize() {
    const version = ++focusSequence;
    stopGesture?.(); clearScheduledFocus();
    if (!active.value) return;
    if (composing && compositionTarget instanceof Node && main.value?.contains(compositionTarget)) {
      pendingMinimize = true; pendingMinimizeVersion = version;
      return;
    }
    commitMinimize(version);
  }
  function begin(event: PointerEvent, resize = false) {
    if (!active.value || minimized.value || narrow() || event.button !== 0 || !rect.value) return;
    if (!resize && (event.target as Element).closest("button, a, input")) return;
    event.preventDefault();
    settleChatMotion();
    stopGesture?.();
    const handle = event.currentTarget as HTMLElement;
    handle.setPointerCapture(event.pointerId);
    const start = { ...rect.value };
    const origin = { x: event.clientX, y: event.clientY };
    manipulating.value = true;
    const move = (e: PointerEvent) => {
      if (e.pointerId !== event.pointerId) return;
      rect.value = adjustFloatingChat(start, e.clientX - origin.x, e.clientY - origin.y, resize, viewport());
      desktopRect = { ...rect.value };
    };
    const finish = () => {
      manipulating.value = false;
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", finish);
      handle.removeEventListener("pointercancel", finish);
      handle.removeEventListener("lostpointercapture", finish);
      window.removeEventListener("blur", finish);
      if (handle.hasPointerCapture(event.pointerId)) handle.releasePointerCapture(event.pointerId);
      stopGesture = undefined;
    };
    stopGesture = finish;
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", finish);
    handle.addEventListener("pointercancel", finish);
    handle.addEventListener("lostpointercapture", finish);
    window.addEventListener("blur", finish);
  }
  function adjustKey(event: KeyboardEvent, resize = false) {
    if (!active.value || minimized.value || event.target !== event.currentTarget || !rect.value || narrow()) return;
    const step = event.shiftKey ? 40 : 12;
    const deltas: Record<string, [number, number]> = { ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step] };
    const delta = deltas[event.key];
    if (!delta) return;
    event.preventDefault();
    settleChatMotion();
    rect.value = adjustFloatingChat(rect.value, ...delta, resize, viewport());
    desktopRect = { ...rect.value };
  }
  function settleChatMotion() {
    chatMotion.cancel();
    chatVisible.value = !minimized.value; restoreVisible.value = minimized.value;
  }
  function keyboard(event: KeyboardEvent) {
    if ((!active.value && !motionPhase.value) || event.defaultPrevented || event.isComposing) return;
    // Plain Escape remains available to page dialogs, remote input and popovers.
    if (event.key === "Escape" && event.shiftKey) { event.preventDefault(); event.stopPropagation(); exit(); }
    if (event.key === "F6") {
      event.preventDefault();
      event.stopPropagation();
      if (!minimized.value && main.value?.contains(document.activeElement)) {
        const page = options.focusSurface ? options.focusSurface() : document.querySelector<HTMLElement>(".dock.immersive .native-page, .dock.immersive .urlbar")
          ?? document.querySelector<HTMLElement>(".dock.immersive .sheet-head button");
        page?.focus();
      } else void restore();
    }
  }
  async function toggleFullscreen(): Promise<boolean> {
    if (options.fullscreen === false) return false;
    try {
      if (document.fullscreenElement) await document.exitFullscreen();
      else {
        // Include body-mounted menus/dialogs in the browser's fullscreen top layer.
        await document.documentElement.requestFullscreen();
        ownsFullscreen = true;
        if (!active.value) { await document.exitFullscreen(); ownsFullscreen = false; }
      }
      return true;
    } catch { return false; }
  }
  function syncFullscreen() {
    if (options.fullscreen === false) { fit(); return; }
    fullscreen.value = Boolean(document.fullscreenElement);
    if (!fullscreen.value) ownsFullscreen = false;
    fit();
  }
  watch(minimized, value => {
    if (!active.value) return;
    chatMotion.run(value ? "minimizing" : "restoring", () => {
      // Keep the original card and pill available until this local motion
      // settles. AgentView makes the logically minimized card inert.
      chatVisible.value = true; restoreVisible.value = true;
    }, () => {
      if (!active.value) return;
      chatVisible.value = !minimized.value; restoreVisible.value = minimized.value;
    });
  }, { flush: "sync" });
  watch(active, value => {
    if (!value) {
      stopGesture?.(); chatMotion.cancel(); pendingMinimize = false; clearScheduledFocus();
      chatVisible.value = true; restoreVisible.value = false;
    }
  }, { flush: "sync" });
  onMounted(() => {
    window.addEventListener("pointerdown", pointerStarted, true);
    window.addEventListener("pointerup", pointerEnded, true);
    window.addEventListener("pointercancel", pointerEnded, true);
    window.addEventListener("focusin", focusChanged, true);
    window.addEventListener("compositionstart", compositionStarted, true);
    window.addEventListener("compositionend", compositionEnded, true);
    window.addEventListener("blur", resetInteraction);
    window.addEventListener("resize", fit);
    window.visualViewport?.addEventListener("resize", fit);
    window.visualViewport?.addEventListener("scroll", fit);
    window.addEventListener("keydown", keyboard, true);
    document.addEventListener("fullscreenchange", syncFullscreen);
  });
  onBeforeUnmount(() => {
    exit(false);
    chatMotion.cancel();
    motion.cancel();
    window.removeEventListener("pointerdown", pointerStarted, true);
    window.removeEventListener("pointerup", pointerEnded, true);
    window.removeEventListener("pointercancel", pointerEnded, true);
    window.removeEventListener("focusin", focusChanged, true);
    window.removeEventListener("compositionstart", compositionStarted, true);
    window.removeEventListener("compositionend", compositionEnded, true);
    window.removeEventListener("blur", resetInteraction);
    window.removeEventListener("resize", fit);
    window.visualViewport?.removeEventListener("resize", fit);
    window.visualViewport?.removeEventListener("scroll", fit);
    window.removeEventListener("keydown", keyboard, true);
    document.removeEventListener("fullscreenchange", syncFullscreen);
  });
  return { active, minimized, chatVisible, restoreVisible, chatAnimating, chatMotionPhase, manipulating, interacting, motionPhase,
    fullscreen, fullscreenAvailable, style, enter, exit, restore, minimize, begin, adjustKey, toggleFullscreen };
}
