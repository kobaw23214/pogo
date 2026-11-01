import { nextTick, onBeforeUnmount, onMounted, watch, type Ref } from "vue";
import { gsap } from "gsap";
import { cinemaPlanes, sceneLayout, sceneProgress, sceneTravel } from "./landingSceneConfig";

type Options = {
  root: Ref<HTMLElement | null>;
  header: Ref<HTMLElement | null>;
  hero: Ref<HTMLElement | null>;
  enabled: Readonly<Ref<boolean>>;
  paused: Readonly<Ref<boolean>>;
};

/** A native scroll position seeks the opening timeline in either direction. */
export function useOpeningCinematics(options: Options) {
  let context: gsap.Context | undefined;
  let timeline: gsap.core.Timeline | undefined;
  let pointerTween: gsap.core.Tween | undefined;
  let observer: ResizeObserver | undefined;
  let frame: number | undefined;
  let buildFrame: number | undefined;
  let mounted = false;
  let disposed = false;
  let staticMode = false;
  let size = "";
  let start = 0;
  let distance = 1;
  let progress = 0;
  const stopped = () => options.paused.value || document.hidden;

  function render() {
    frame = undefined;
    const root = options.root.value;
    const hero = options.hero.value;
    if (!root || !hero || !timeline || disposed || stopped()) return;
    const target = staticMode ? 0 : sceneProgress(root.scrollTop, start, distance);
    // Catch up over a few frames so this scene responds like the scrubbed
    // GSAP chapters instead of snapping 1:1 with the scroll position.
    if (staticMode || matchMedia("(prefers-reduced-motion: reduce)").matches) progress = target;
    else {
      progress += (target - progress) * 0.24;
      if (Math.abs(target - progress) < 0.0006) progress = target;
    }
    timeline.progress(progress, true);
    hero.style.setProperty("--scene-progress", String(progress));
    hero.classList.toggle("is-scene-active", !staticMode && root.scrollTop < start + hero.offsetHeight);
    if (progress !== target) frame = requestAnimationFrame(render);
  }

  function requestRender() {
    if (!disposed && !stopped() && frame === undefined) frame = requestAnimationFrame(render);
  }

  function clear() {
    pointerTween?.kill();
    pointerTween = undefined;
    context?.revert();
    context = undefined;
    timeline = undefined;
    const hero = options.hero.value;
    hero?.querySelector<HTMLElement>(".opening-art")?.style.removeProperty("--pointer-x");
    hero?.querySelector<HTMLElement>(".opening-art")?.style.removeProperty("--pointer-y");
  }

  function build() {
    buildFrame = undefined;
    const root = options.root.value;
    const hero = options.hero.value;
    if (!mounted || disposed || !root || !hero) return;
    const oldProgress = progress;
    const preserve = !!timeline && !staticMode && options.enabled.value && root.scrollTop > start && root.scrollTop < start + distance;
    clear();
    const layout = sceneLayout(root.clientWidth, root.clientHeight);
    hero.dataset.layout = layout;
    staticMode = !options.enabled.value || layout === "compact";
    const travel = sceneTravel(options.enabled.value, layout);
    const width = root.clientWidth;
    const height = root.clientHeight;

    context = gsap.context(() => {
      const opening = gsap.timeline({ paused: true, defaults: { ease: "none" } });
      opening.to({ progress: 0 }, { progress: 1, duration: 1 }, 0);
      gsap.set(".opening-discovery,.opening-portal,.opening-scrub", { autoAlpha: 0 });
      gsap.set(".opening-discovery", { y: 44 });
      gsap.set(".opening-portal", { scale: 0.35 });
      for (const plane of cinemaPlanes) {
        const target = hero.querySelector<HTMLElement>(`[data-plane="${plane.id}"]`)!;
        gsap.set(target, { x: 0, y: 0, rotation: plane.r, scale: 1 });
        opening.to(target, {
          x: plane.dx * width * travel,
          y: plane.dy * height * travel,
          rotation: plane.r + plane.spin * travel,
          scale: 1 + (plane.z > 8 ? 0.35 : 0.08) * travel,
          duration: 0.65, ease: "power1.inOut",
        }, 0.025);
      }
      opening.to(".opening-art", { x: -width * 0.08 * travel, scale: 1 + 0.22 * travel, duration: 0.67 }, 0.02)
        .to(".opening-copy", { autoAlpha: 0, y: -52 * travel, duration: 0.15 }, 0.1)
        .to(".opening-bottom", { autoAlpha: 0, duration: 0.12 }, 0.07)
        .to(".opening-watermark", { scale: 1.3, autoAlpha: 0, duration: 0.4 }, 0)
        .to(".opening-discovery", { autoAlpha: 1, y: 0, duration: 0.16 }, 0.25)
        .to(".opening-discovery", { autoAlpha: 0, y: -26, duration: 0.13 }, 0.56)
        .to(".opening-scrub", { autoAlpha: 1, duration: 0.13 }, 0.25)
        .to(".opening-scrub", { autoAlpha: 0, duration: 0.1 }, 0.64)
        .to(".opening-portal", { autoAlpha: 1, scale: 1, duration: 0.2 }, 0.37)
        .to(".opening-portal>svg,.opening-portal>i", { autoAlpha: 0, duration: 0.1 }, 0.56)
        .to(".opening-portal", { scale: layout === "mobile" ? 19 : 24, duration: 0.3, ease: "power2.in" }, 0.6)
        .to(".opening-art,.opening-meta", { autoAlpha: 0, duration: 0.15 }, 0.66)
        .to(".opening-portal", { backgroundColor: "#eceee5", duration: 0.14 }, 0.76);
      timeline = opening;
    }, hero);

    start = hero.getBoundingClientRect().top - root.getBoundingClientRect().top + root.scrollTop - (options.header.value?.offsetHeight ?? 76);
    // The timeline spans exactly the sticky range (release - start cancels the
    // header offset out): the portal finishes opening right as the stage
    // releases, so the filled sheet itself wipes up into the next chapter.
    // A longer distance leaves the final zoom playing while the stage is
    // already sliding away, which reads as a broken handoff.
    const stage = hero.querySelector<HTMLElement>(".opening-stage");
    const stageHeight = stage?.offsetHeight || root.clientHeight - (options.header.value?.offsetHeight ?? 76);
    distance = Math.max(1, hero.offsetHeight - stageHeight);
    if (preserve && !staticMode) root.scrollTo({ top: start + oldProgress * distance, behavior: "instant" });
    render();
  }

  function scheduleBuild() {
    if (mounted && !disposed && buildFrame === undefined) buildFrame = requestAnimationFrame(build);
  }

  function pointerMove(event: PointerEvent) {
    const hero = options.hero.value;
    const art = hero?.querySelector<HTMLElement>(".opening-art");
    if (!hero || !art || stopped() || staticMode || event.pointerType !== "mouse" || progress > 0.75 || matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    const bounds = options.root.value!.getBoundingClientRect();
    pointerTween?.kill();
    pointerTween = gsap.to(art, {
      "--pointer-x": `${((event.clientX - bounds.left) / bounds.width - 0.5) * 24}px`,
      "--pointer-y": `${((event.clientY - bounds.top) / bounds.height - 0.5) * 18}px`,
      duration: 0.55, ease: "power2.out", overwrite: "auto",
    });
  }

  function pointerLeave() {
    pointerTween?.kill();
    const art = options.hero.value?.querySelector<HTMLElement>(".opening-art");
    if (art) pointerTween = gsap.to(art, { "--pointer-x": "0px", "--pointer-y": "0px", duration: stopped() ? 0 : 0.4 });
  }

  function suspend() {
    if (stopped()) {
      pointerTween?.kill();
      options.hero.value?.classList.remove("is-scene-active");
      if (frame !== undefined) cancelAnimationFrame(frame);
      frame = undefined;
    } else requestRender();
  }

  watch(options.enabled, () => { void nextTick(scheduleBuild); });
  watch(options.paused, suspend);
  onMounted(() => {
    mounted = true;
    const root = options.root.value;
    root?.addEventListener("scroll", requestRender, { passive: true });
    root?.addEventListener("pointermove", pointerMove, { passive: true });
    root?.addEventListener("pointerleave", pointerLeave);
    document.addEventListener("visibilitychange", suspend);
    observer = new ResizeObserver(() => {
      const next = `${root?.clientWidth}:${root?.clientHeight}:${options.header.value?.offsetHeight}`;
      if (next !== size) { size = next; scheduleBuild(); }
    });
    if (root) observer.observe(root);
    if (options.header.value) observer.observe(options.header.value);
    scheduleBuild();
  });

  onBeforeUnmount(() => {
    disposed = true;
    mounted = false;
    if (frame !== undefined) cancelAnimationFrame(frame);
    if (buildFrame !== undefined) cancelAnimationFrame(buildFrame);
    observer?.disconnect();
    const root = options.root.value;
    root?.removeEventListener("scroll", requestRender);
    root?.removeEventListener("pointermove", pointerMove);
    root?.removeEventListener("pointerleave", pointerLeave);
    document.removeEventListener("visibilitychange", suspend);
    clear();
  });
}
