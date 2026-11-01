import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch, type Ref } from "vue";
import { gsap } from "gsap";
import { adjacentStoryStop, buildStoryStops, commitsStorySwipe, nearestStoryStop, storyTransitionDuration, type ScrollDirection, type StoryAnchor, type StoryChapter, type StoryStop } from "./landingSnapConfig";

type Options = {
  root: Ref<HTMLElement | null>; header: Ref<HTMLElement | null>;
  enabled: Readonly<Ref<boolean>>; paused: Readonly<Ref<boolean>>; reduced: Readonly<Ref<boolean>>;
  chapterIds: readonly string[];
};
type TouchGesture = { x: number; y: number; at: number; origin: number; delta: number; dragging: boolean; target?: StoryStop };
const chapterTitles: Record<string, string> = { overview: "首页", capabilities: "基座能力", workspace: "工作空间", possibilities: "应用场景", begin: "开始使用" };

/** A single scroller and timeline position for reading, scenes and chapter cuts. */
export function useLandingSnap(options: Options) {
  const storyStops = shallowRef<StoryStop[]>([]);
  const currentIndex = ref(0);
  const currentStop = computed(() => storyStops.value[currentIndex.value]);
  const chapterScenes = computed(() => storyStops.value.filter(stop => stop.chapterId === currentStop.value?.chapterId));
  const sceneIndex = computed(() => Math.max(0, chapterScenes.value.findIndex(stop => stop.id === currentStop.value?.id)));
  let tween: gsap.core.Tween | undefined;
  let observer: ResizeObserver | undefined;
  let measureFrame: number | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let disposed = false;
  let pointerHeld = false;
  let pointerOrigin = 0;
  let touch: TouchGesture | undefined;
  let lastPosition = 0;
  let nativeOrigin: number | undefined;
  let nativeDirection: ScrollDirection = 0;
  let ignoreUntil = 0;
  let wheelAt = -Infinity;
  let wheelDirection: ScrollDirection = 0;
  let wheelIntent = 0;
  let wheelConsumed = false;
  let animationDirection: ScrollDirection = 0;
  let rootSize = "";
  let preserveOnMeasure = false;
  const sign = (value: number): ScrollDirection => value === 0 ? 0 : value > 0 ? 1 : -1;
  const available = () => !disposed && options.enabled.value && !options.paused.value && !document.hidden;

  function clearTimer() { if (timer !== undefined) clearTimeout(timer); timer = undefined; }

  function syncPosition() {
    const root = options.root.value;
    if (!root) return;
    const index = nearestStoryStop(storyStops.value, root.scrollTop);
    if (index >= 0) currentIndex.value = index;
    root.dataset.storyStop = currentStop.value?.id ?? "";
  }

  function cancel() {
    clearTimer();
    tween?.kill(); tween = undefined;
    animationDirection = 0; nativeOrigin = undefined; nativeDirection = 0;
    wheelConsumed = false; wheelIntent = 0;
    ignoreUntil = performance.now() + 100;
    const root = options.root.value;
    if (root) { root.dataset.snapState = "idle"; delete root.dataset.snapTarget; lastPosition = root.scrollTop; }
  }

  // offsetTop ignores the artwork's animated transforms. Sticky stages are
  // measured from their non-sticky story wrapper, never from their live rect.
  function layoutTop(node: HTMLElement): number {
    let value = 0;
    let cursor: HTMLElement | null = node;
    while (cursor && cursor !== options.root.value) { value += cursor.offsetTop; cursor = cursor.offsetParent as HTMLElement | null; }
    return value;
  }

  function measure() {
    measureFrame = undefined;
    const root = options.root.value;
    if (!root || disposed) return;
    const previous = currentStop.value?.id;
    const header = options.header.value?.offsetHeight ?? 76;
    const bottomInset = options.enabled.value ? 84 : 20;
    const readable = Math.max(120, root.clientHeight - header - bottomInset - 20);
    const maxScroll = Math.max(0, root.scrollHeight - root.clientHeight);
    const chapters: StoryChapter[] = [];
    for (const id of options.chapterIds) {
      const node = root.querySelector<HTMLElement>("#" + id);
      if (!node) continue;
      const start = Math.max(0, layoutTop(node) - header);
      const end = Math.min(maxScroll, Math.max(start, layoutTop(node) + node.offsetHeight - root.clientHeight + bottomInset));
      const anchors: StoryAnchor[] = [];
      const add = (selector: string, key: string, label: string) => {
        const element = node.querySelector<HTMLElement>(selector);
        if (element) anchors.push({ key, label, top: layoutTop(element) - header - 20 });
      };
      let cinematic = false;
      if (id === "overview" && options.enabled.value && node.dataset.layout !== "compact") {
        const stage = node.querySelector<HTMLElement>(".opening-stage");
        const distance = Math.max(1, node.offsetHeight - (stage?.offsetHeight ?? root.clientHeight - header));
        anchors.push({ key: "gather", label: "聚合", top: start }, { key: "unfold", label: "拆解", top: start + distance * 0.4 }, { key: "enter", label: "穿入", top: start + distance * 0.62 });
        cinematic = true;
      } else if (id === "capabilities") {
        add(".anatomy-art", "anatomy", "能力组合");
        add(".capability-folds", "details", "能力详解");
      } else if (id === "workspace") {
        const story = node.querySelector<HTMLElement>(".workspace-story");
        const stage = node.querySelector<HTMLElement>(".workspace-stage");
        if (story && stage && root.dataset.motionLayout === "full" && options.enabled.value) {
          const stageStart = layoutTop(story) - header - 16;
          const travel = Math.max(0, story.offsetHeight - stage.offsetHeight);
          anchors.push({ key: "configure", label: "配置能力", top: stageStart }, { key: "execute", label: "推进任务", top: stageStart + travel * 0.5 }, { key: "result", label: "看见结果", top: stageStart + travel });
        } else {
          add(".config-paper", "configure", "配置能力");
          add(".workbench-window", "execute", "推进任务");
          add(".result-paper", "result", "看见结果");
        }
      } else if (id === "possibilities") {
        if (root.clientWidth <= 650) {
          add(".card-research", "research", "研究与洞见");
          add(".card-engineering", "engineering", "代码与工程");
          add(".card-analysis", "analysis", "分析与知识");
        } else add(".editorial-grid", "directions", "探索应用方向");
      }
      chapters.push({ id, title: chapterTitles[id] ?? id, start, end, anchors, cinematic });
    }
    storyStops.value = buildStoryStops(chapters, readable, maxScroll);
    root.dataset.storyCount = String(storyStops.value.length);
    const preserved = preserveOnMeasure && previous ? storyStops.value.find(stop => stop.id === previous) : undefined;
    preserveOnMeasure = false;
    if (preserved && available() && !touch && !pointerHeld) {
      cancel();
      root.scrollTo({ top: preserved.top, behavior: "instant" });
    }
    syncPosition();
    lastPosition = root.scrollTop;
  }

  function refreshStoryLayout(preserve = false) {
    preserveOnMeasure ||= preserve;
    if (!disposed && measureFrame === undefined) measureFrame = requestAnimationFrame(measure);
  }

  function moveTo(stop: StoryStop) {
    const root = options.root.value;
    if (!root || options.paused.value || document.hidden) return;
    const previousChapter = currentStop.value?.chapterId;
    cancel();
    const top = Math.max(0, Math.min(root.scrollHeight - root.clientHeight, stop.top));
    const distance = top - root.scrollTop;
    if (!available() || Math.abs(distance) < 2) {
      root.scrollTo({ top, behavior: "instant" }); syncPosition(); return;
    }
    animationDirection = sign(distance);
    root.dataset.snapState = "transition";
    root.dataset.snapTarget = stop.id;
    tween = gsap.to(root, {
      scrollTop: top,
      duration: storyTransitionDuration(distance, root.clientHeight, previousChapter !== stop.chapterId, options.reduced.value),
      ease: "power3.inOut",
      onComplete: () => {
        tween = undefined; nativeOrigin = undefined; nativeDirection = 0;
        wheelConsumed = true; ignoreUntil = performance.now() + 100;
        lastPosition = root.scrollTop;
        root.dataset.snapState = "idle"; delete root.dataset.snapTarget;
        syncPosition();
      },
    });
  }

  function stepScene(direction: -1 | 1) {
    const root = options.root.value;
    if (!root) return;
    const target = adjacentStoryStop(storyStops.value, root.scrollTop, direction);
    if (target) moveTo(target);
  }

  function scrollToChapter(id: string) {
    measure();
    const target = storyStops.value.find(stop => stop.chapterId === id);
    if (target) moveTo(target);
  }

  function settle() {
    timer = undefined;
    const root = options.root.value;
    if (!root || !available() || tween || pointerHeld || touch) return;
    const target = nativeOrigin !== undefined
      ? adjacentStoryStop(storyStops.value, nativeOrigin, nativeDirection)
      : storyStops.value[nearestStoryStop(storyStops.value, root.scrollTop)];
    nativeOrigin = undefined;
    if (target) moveTo(target);
  }

  function onScroll() {
    const root = options.root.value;
    if (!root) return;
    const before = lastPosition;
    lastPosition = root.scrollTop;
    syncPosition();
    if (!available() || tween || touch || pointerHeld || performance.now() < ignoreUntil) return;
    if (Math.abs(lastPosition - before) <= 0.5) return;
    nativeOrigin ??= before;
    nativeDirection = sign(lastPosition - before);
    clearTimer(); timer = setTimeout(settle, 140);
  }

  function nestedScroll(target: EventTarget | null, direction: ScrollDirection) {
    let node = target instanceof Element ? target : null;
    while (node && node !== options.root.value) {
      if (node.matches("input,textarea,select,[contenteditable]:not([contenteditable=false]),[role=slider]")) return true;
      if (node instanceof HTMLElement && node.scrollHeight > node.clientHeight + 2 && /auto|scroll/.test(getComputedStyle(node).overflowY)) {
        if (direction < 0 ? node.scrollTop > 0 : node.scrollTop + node.clientHeight < node.scrollHeight - 1) return true;
      }
      node = node.parentElement;
    }
    return false;
  }

  function onWheel(event: WheelEvent) {
    const root = options.root.value;
    if (!root || !available() || event.ctrlKey || event.metaKey || event.shiftKey || Math.abs(event.deltaX) > Math.abs(event.deltaY)) return;
    const delta = event.deltaY * (event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? root.clientHeight : 1);
    const direction = sign(delta);
    if (!direction || nestedScroll(event.target, direction) || !event.cancelable) return;
    event.preventDefault();
    const now = performance.now();
    const fresh = now - wheelAt > 200 || direction !== wheelDirection;
    wheelAt = now; wheelDirection = direction;
    if (fresh) { wheelIntent = 0; wheelConsumed = false; }
    if (tween && direction !== animationDirection) cancel();
    if (tween || wheelConsumed) return;
    wheelIntent += Math.abs(delta);
    if (wheelIntent < 24) return;
    stepScene(direction);
    wheelConsumed = true;
  }

  function onPointerDown(event: PointerEvent) {
    if (event.pointerType === "touch") return;
    cancel(); pointerHeld = true; pointerOrigin = options.root.value?.scrollTop ?? 0;
  }

  function onPointerUp() {
    if (!pointerHeld) return;
    pointerHeld = false;
    if (Math.abs((options.root.value?.scrollTop ?? 0) - pointerOrigin) > 2 && available()) {
      nativeOrigin = undefined; clearTimer(); timer = setTimeout(settle, 100);
    }
  }

  function onTouchStart(event: TouchEvent) {
    if (!available() || event.touches.length !== 1) { touch = undefined; return; }
    cancel();
    const finger = event.touches[0]!;
    touch = { x: finger.clientX, y: finger.clientY, at: performance.now(), origin: options.root.value?.scrollTop ?? 0, delta: 0, dragging: false };
  }

  function onTouchMove(event: TouchEvent) {
    const root = options.root.value;
    if (!touch || !root || event.touches.length !== 1 || !available()) { touch = undefined; return; }
    const finger = event.touches[0]!;
    const delta = touch.y - finger.clientY;
    if (!touch.dragging && (Math.abs(delta) < 10 || Math.abs(delta) < Math.abs(finger.clientX - touch.x))) return;
    const direction = sign(delta);
    if (nestedScroll(event.target, direction) || !event.cancelable) { touch = undefined; return; }
    event.preventDefault();
    touch.dragging = true; touch.delta = delta;
    touch.target = adjacentStoryStop(storyStops.value, touch.origin, direction);
    const travel = touch.target ? Math.min(Math.abs(delta), Math.abs(touch.target.top - touch.origin)) : Math.min(24, Math.abs(delta) * 0.15);
    root.scrollTo({ top: touch.origin + direction * travel, behavior: "instant" });
  }

  function onTouchEnd(event: TouchEvent) {
    const gesture = touch; touch = undefined;
    if (!gesture?.dragging) return;
    const commit = event.type !== "touchcancel" && commitsStorySwipe(gesture.delta, performance.now() - gesture.at);
    const target = commit && gesture.target ? gesture.target : storyStops.value[nearestStoryStop(storyStops.value, gesture.origin)];
    if (target) moveTo(target);
  }

  function onKeydown(event: KeyboardEvent) {
    const root = options.root.value;
    if (!root || !available() || event.ctrlKey || event.metaKey || event.altKey) return;
    const active = document.activeElement;
    if (active && active !== document.body && !root.contains(active)) return;
    if (active?.closest("input,textarea,select,[contenteditable]:not([contenteditable=false])")) return;
    if (event.key === "Tab" || event.key === "Escape") { cancel(); ignoreUntil = performance.now() + 500; return; }
    if (event.key === " " && active?.closest("button,a,summary,[role=button]")) return;
    const direction: ScrollDirection = ["PageDown", "ArrowDown"].includes(event.key) || (event.key === " " && !event.shiftKey) ? 1
      : ["PageUp", "ArrowUp"].includes(event.key) || (event.key === " " && event.shiftKey) ? -1 : 0;
    if (!direction && event.key !== "Home" && event.key !== "End") return;
    if (nestedScroll(event.target, direction)) return;
    event.preventDefault();
    if (event.repeat) return;
    if (direction) stepScene(direction);
    else {
      const target = event.key === "Home" ? storyStops.value[0] : storyStops.value[storyStops.value.length - 1];
      if (target) moveTo(target);
    }
  }

  function suspend() { touch = undefined; pointerHeld = false; cancel(); }
  watch([options.enabled, options.reduced], () => { suspend(); void nextTick(() => refreshStoryLayout()); });
  watch(options.paused, suspend);

  onMounted(() => {
    const root = options.root.value;
    if (!root) return;
    root.dataset.snapState = "idle"; lastPosition = root.scrollTop;
    root.addEventListener("scroll", onScroll, { passive: true });
    root.addEventListener("wheel", onWheel, { passive: false });
    root.addEventListener("pointerdown", onPointerDown, { passive: true });
    root.addEventListener("touchstart", onTouchStart, { passive: true });
    root.addEventListener("touchmove", onTouchMove, { passive: false });
    root.addEventListener("touchend", onTouchEnd, { passive: true });
    root.addEventListener("touchcancel", onTouchEnd, { passive: true });
    window.addEventListener("pointerup", onPointerUp, { passive: true });
    window.addEventListener("pointercancel", onPointerUp, { passive: true });
    window.addEventListener("keydown", onKeydown);
    document.addEventListener("visibilitychange", suspend);
    observer = new ResizeObserver(() => {
      const size = `${root.clientWidth}:${root.clientHeight}`;
      const changed = !!rootSize && rootSize !== size;
      rootSize = size;
      if (changed) cancel();
      refreshStoryLayout(changed);
    });
    observer.observe(root);
    if (options.header.value) observer.observe(options.header.value);
    for (const node of root.querySelectorAll<HTMLElement>(".chapter,.workspace-story,.workspace-stage")) observer.observe(node);
    void document.fonts.ready.then(() => { if (!disposed) refreshStoryLayout(); });
    refreshStoryLayout();
  });

  onBeforeUnmount(() => {
    disposed = true; suspend(); observer?.disconnect();
    if (measureFrame !== undefined) cancelAnimationFrame(measureFrame);
    const root = options.root.value;
    root?.removeEventListener("scroll", onScroll); root?.removeEventListener("wheel", onWheel);
    root?.removeEventListener("pointerdown", onPointerDown);
    root?.removeEventListener("touchstart", onTouchStart); root?.removeEventListener("touchmove", onTouchMove);
    root?.removeEventListener("touchend", onTouchEnd); root?.removeEventListener("touchcancel", onTouchEnd);
    window.removeEventListener("pointerup", onPointerUp); window.removeEventListener("pointercancel", onPointerUp);
    window.removeEventListener("keydown", onKeydown); document.removeEventListener("visibilitychange", suspend);
    for (const name of ["data-snap-state", "data-story-stop", "data-story-count"]) root?.removeAttribute(name);
  });

  return { scrollToChapter, stepScene, refreshStoryLayout, currentStop, currentIndex, chapterScenes, sceneIndex, storyStops };
}
