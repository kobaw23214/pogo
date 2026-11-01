import { nextTick, onBeforeUnmount, onMounted, watch, type Ref } from "vue";
import { gsap } from "gsap";

type DetailsOptions = {
  root: Ref<HTMLElement | null>;
  enabled: Readonly<Ref<boolean>>;
  reduced: Readonly<Ref<boolean>>;
  paused: Readonly<Ref<boolean>>;
  onLayoutChange: () => void;
};

// Decorative motion owns these properties only. ScrollTrigger retains ownership
// of --motion-* so entrance, pointer and scroll effects compose without jumps.
const detailProperties = ["opacity", "--detail-y", "--arrival-y"];

export function useLandingDetails(options: DetailsOptions) {
  const animations = new Set<gsap.core.Tween>();
  const styled = new Set<HTMLElement>();
  const pending = new Set<HTMLElement>();
  const foldFinishes = new Map<Element, () => void>();
  let revealObserver: IntersectionObserver | undefined;
  let chapterObserver: IntersectionObserver | undefined;
  let pointerTween: gsap.core.Tween | undefined;
  let pointerFrame: number | undefined;
  let pointerCleanup: (() => void) | undefined;
  let entered = false;
  let mounted = false;
  let disposed = false;
  const paused = () => options.paused.value || document.hidden;

  function clearProperties(target: HTMLElement) {
    for (const property of detailProperties) target.style.removeProperty(property);
  }

  function reveal(targets: HTMLElement[], offset = 20, stagger = 0.065, arrival = false) {
    if (!targets.length || disposed) return;
    for (const target of targets) styled.add(target);
    const property = arrival ? "--arrival-y" : "--detail-y";
    gsap.killTweensOf(targets, `opacity,${property}`);
    const tween = gsap.fromTo(targets, {
      opacity: 0, [property]: `${options.reduced.value ? 0 : offset}px`,
    }, {
      opacity: 1, [property]: "0px",
      duration: options.reduced.value ? 0.24 : arrival ? 0.95 : 0.72,
      stagger: options.reduced.value ? 0.015 : stagger,
      ease: "power3.out",
      onComplete: () => {
        animations.delete(tween);
        for (const target of targets) {
          clearProperties(target);
          target.dataset.detailReveal = "visible";
        }
      },
      onInterrupt: () => { animations.delete(tween); },
    });
    animations.add(tween);
    if (paused()) tween.pause();
  }

  function syncPause() {
    const stopped = paused();
    options.root.value?.classList.toggle("motion-paused", stopped);
    for (const tween of animations) tween.paused(stopped);
    pointerTween?.paused(stopped);
    if (!stopped && pending.size) {
      reveal([...pending]);
      pending.clear();
    }
  }

  function cleanup() {
    revealObserver?.disconnect();
    chapterObserver?.disconnect();
    pointerCleanup?.();
    pointerCleanup = undefined;
    if (pointerFrame !== undefined) cancelAnimationFrame(pointerFrame);
    pointerFrame = undefined;
    pointerTween?.kill();
    pointerTween = undefined;
    for (const tween of animations) tween.kill();
    animations.clear();
    for (const finish of [...foldFinishes.values()]) finish();
    for (const target of styled) {
      clearProperties(target);
      target.classList.remove("detail-reveal");
      delete target.dataset.detailReveal;
    }
    styled.clear();
    pending.clear();
    options.root.value?.querySelectorAll(".is-in-view").forEach(node => node.classList.remove("is-in-view"));
  }

  function build() {
    if (!mounted || disposed) return;
    cleanup();
    const root = options.root.value;
    if (!root || !options.enabled.value) return;
    const all = (selector: string) => [...root.querySelectorAll<HTMLElement>(selector)];

    chapterObserver = new IntersectionObserver(entries => {
      for (const entry of entries) entry.target.classList.toggle("is-in-view", entry.isIntersecting);
    }, { root, threshold: 0.05 });
    all(".chapter").forEach(chapter => chapterObserver!.observe(chapter));

    const targets = all(".section-heading > *, .workspace-heading > div, .possibility-heading > *, .capability-fold, .editorial-card, .workflow-caption > p, .begin-content > *, .section-end, .possibility-footer");
    revealObserver = new IntersectionObserver(entries => {
      const visible: HTMLElement[] = [];
      for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        const target = entry.target as HTMLElement;
        revealObserver?.unobserve(target);
        if (paused()) pending.add(target);
        else visible.push(target);
      }
      reveal(visible);
    }, { root, rootMargin: "-40px 0px -24px 0px", threshold: 0.08 });
    for (const target of targets) {
      target.classList.add("detail-reveal");
      styled.add(target);
      // Never hide content already being read during a preference change.
      if (entered && target.getBoundingClientRect().top < root.getBoundingClientRect().bottom) continue;
      target.dataset.detailReveal = "pending";
      target.style.opacity = "0";
      revealObserver.observe(target);
    }

    if (!entered && root.scrollTop < 30) {
      const planes = all(".hero-art .plate");
      reveal(planes, 42, 0.045, true);
      const copy = all(".hero-title-line, .hero-intro, .section-meta, .hero-baseline");
      copy.forEach(target => target.classList.add("detail-reveal"));
      reveal(copy, 18, 0.055);
    }
    entered = true;

    const art = root.querySelector<HTMLElement>(".hero-art");
    if (art && !options.reduced.value && matchMedia("(hover: hover) and (pointer: fine)").matches) {
      let pointerX = 0;
      let pointerY = 0;
      const move = (event: PointerEvent) => {
        if (paused() || event.pointerType === "touch") return;
        const box = art.getBoundingClientRect();
        pointerX = ((event.clientX - box.left) / box.width - 0.5) * 14;
        pointerY = ((event.clientY - box.top) / box.height - 0.5) * 10;
        if (pointerFrame !== undefined) return;
        pointerFrame = requestAnimationFrame(() => {
          pointerFrame = undefined;
          if (paused()) return;
          pointerTween?.kill();
          pointerTween = gsap.to(art, { "--pointer-x": `${pointerX}px`, "--pointer-y": `${pointerY}px`, duration: 0.65, ease: "power3.out" });
        });
      };
      const leave = () => {
        if (pointerFrame !== undefined) cancelAnimationFrame(pointerFrame);
        pointerFrame = undefined;
        pointerTween?.kill();
        pointerTween = gsap.to(art, { "--pointer-x": "0px", "--pointer-y": "0px", duration: 0.8, ease: "power3.out" });
      };
      art.addEventListener("pointermove", move, { passive: true });
      art.addEventListener("pointerleave", leave);
      pointerCleanup = () => {
        art.removeEventListener("pointermove", move);
        art.removeEventListener("pointerleave", leave);
        art.style.removeProperty("--pointer-x");
        art.style.removeProperty("--pointer-y");
      };
    }
    syncPause();
  }

  function focusContent(event: FocusEvent) {
    if (!(event.target instanceof HTMLElement)) return;
    const target = event.target.closest<HTMLElement>(".detail-reveal");
    if (target?.dataset.detailReveal === "pending") {
      revealObserver?.unobserve(target);
      pending.delete(target);
      clearProperties(target);
      target.dataset.detailReveal = "visible";
    }
  }

  function animateFold(element: Element, done: () => void, opening: boolean) {
    cancelFold(element);
    if (!options.enabled.value || paused()) { done(); options.onLayoutChange(); return; }
    const target = element as HTMLElement;
    const css = getComputedStyle(target);
    const height = target.getBoundingClientRect().height;
    const paddingTop = css.paddingTop;
    const paddingBottom = css.paddingBottom;
    let completed = false;
    let tween: gsap.core.Tween | undefined;
    const finish = () => {
      if (completed) return;
      completed = true;
      tween?.kill();
      if (tween) animations.delete(tween);
      for (const property of ["height", "opacity", "overflow", "padding-top", "padding-bottom"]) target.style.removeProperty(property);
      foldFinishes.delete(element);
      done();
      if (!disposed) options.onLayoutChange();
    };
    foldFinishes.set(element, finish);
    target.style.overflow = "hidden";
    const expanded = { height, opacity: 1, paddingTop, paddingBottom };
    const collapsed = { height: 0, opacity: 0, paddingTop: "0px", paddingBottom: "0px" };
    tween = gsap.fromTo(target, opening ? collapsed : expanded, {
      ...(opening ? expanded : collapsed), duration: options.reduced.value ? 0.16 : 0.42,
      ease: "power2.inOut", onComplete: finish,
    });
    animations.add(tween);
  }

  function cancelFold(element: Element) { foldFinishes.get(element)?.(); }
  function enterFold(element: Element, done: () => void) { animateFold(element, done, true); }
  function leaveFold(element: Element, done: () => void) { animateFold(element, done, false); }

  function animateScenario() {
    if (!options.enabled.value || paused()) return;
    const targets = [...(options.root.value?.querySelectorAll<HTMLElement>(".config-paper > div, .task-header, .task-question, .task-answer, .file-result, .result-paper h3, .result-summary, .result-outline") ?? [])];
    targets.forEach(target => target.classList.add("detail-reveal"));
    reveal(targets, 10, 0.025);
  }

  watch([options.enabled, options.reduced], () => { void nextTick(build); });
  // Vue also updates the root class when the dialog opens. Apply our pause
  // class afterwards so its class patch cannot restart ambient animations.
  watch(options.paused, syncPause, { flush: "post" });
  onMounted(() => {
    mounted = true;
    document.addEventListener("visibilitychange", syncPause);
    options.root.value?.addEventListener("focusin", focusContent);
    build();
  });
  onBeforeUnmount(() => {
    disposed = true;
    mounted = false;
    document.removeEventListener("visibilitychange", syncPause);
    options.root.value?.removeEventListener("focusin", focusContent);
    cleanup();
  });
  return { enterFold, leaveFold, cancelFold, animateScenario };
}
