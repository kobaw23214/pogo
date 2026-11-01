import { nextTick, onBeforeUnmount, onMounted, watch, type Ref } from "vue";
import { gsap } from "gsap";
import { ScrollTrigger } from "gsap/ScrollTrigger";
import { heroLayerTravel, heroMotionLayers, landingMotionLayout, landingMotionScale } from "./landingMotionConfig";

type ElementRef = Ref<HTMLElement | null>;
type MotionOptions = {
  root: ElementRef;
  header: ElementRef;
  hero: ElementRef;
  enabled: Readonly<Ref<boolean>>;
  reduced: Readonly<Ref<boolean>>;
  paused: Readonly<Ref<boolean>>;
};
type ViewAnchor = { element: HTMLElement; top: number; scrollTop: number; inHero: boolean };

export function useLandingMotion(options: MotionOptions) {
  let context: gsap.Context | undefined;
  let triggers: ScrollTrigger[] = [];
  const activityTargets = new Map<ScrollTrigger, HTMLElement[]>();
  let planes: HTMLElement[] = [];
  let resizeObserver: ResizeObserver | undefined;
  let buildFrame: number | undefined;
  let refreshFrame: number | undefined;
  let pendingAnchor: ViewAnchor | undefined;
  let mounted = false;
  let disposed = false;
  let rootSize = "";

  const isPaused = () => options.paused.value || document.hidden;

  function preservePosition() {
    const root = options.root.value;
    if (!root) return;
    const rect = root.getBoundingClientRect();
    const top = rect.top + (options.header.value?.offsetHeight ?? 76);
    const anchors = Array.from(root.querySelectorAll<HTMLElement>("h1, .section-heading, .workspace-stage, .possibility-heading, .begin-content"));
    const element = anchors.find(node => {
      const box = node.getBoundingClientRect();
      return box.bottom > top + 40 && box.top < rect.bottom - 30;
    });
    pendingAnchor = element ? {
      element, top: element.getBoundingClientRect().top,
      scrollTop: root.scrollTop, inHero: !!element.closest(".hero, .opening-scene"),
    } : undefined;
  }

  function updateActivity(trigger: ScrollTrigger, targets: HTMLElement[]) {
    const active = trigger.isActive && !isPaused();
    for (const target of targets) target.classList.toggle("is-motion-active", active);
  }

  function suspend() {
    const paused = isPaused();
    for (const trigger of triggers) {
      if (paused) trigger.disable(false);
      else {
        trigger.enable(false, false);
        trigger.update();
      }
    }
    for (const plane of planes) plane.classList.remove("is-motion-active");
    if (!paused) {
      for (const trigger of triggers) {
        updateActivity(trigger, activityTargets.get(trigger) ?? []);
      }
    }
  }

  function clearScene() {
    context?.revert();
    context = undefined;
    triggers = [];
    activityTargets.clear();
    for (const plane of planes) plane.classList.remove("is-motion-active");
    planes = [];
    const root = options.root.value;
    root?.querySelector(".workspace-stage")?.removeAttribute("data-workflow-step");
    root?.removeAttribute("data-motion-layout");
  }

  function refreshLayout() {
    if (!mounted || disposed || refreshFrame !== undefined) return;
    refreshFrame = requestAnimationFrame(() => {
      refreshFrame = undefined;
      if (disposed || isPaused()) return;
      for (const trigger of triggers) trigger.refresh();
    });
  }

  function build() {
    buildFrame = undefined;
    if (disposed) return;
    const root = options.root.value;
    const hero = options.hero.value;
    clearScene();
    if (!root || !hero) return;

    if (options.enabled.value) {
      const layout = landingMotionLayout(root.clientWidth, root.clientHeight);
      const factor = landingMotionScale(layout, options.reduced.value);
      root.dataset.motionLayout = layout;
      const headerHeight = () => options.header.value?.offsetHeight ?? 76;

      context = gsap.context(() => {
        const all = (selector: string) => Array.from(root.querySelectorAll<HTMLElement>(selector));
        const one = (selector: string) => root.querySelector<HTMLElement>(selector);
        const timeline = (
          trigger: HTMLElement,
          targets: HTMLElement[],
          start: ScrollTrigger.Vars["start"] = "top bottom",
          end: ScrollTrigger.Vars["end"] = "bottom top",
          onUpdate?: (self: ScrollTrigger) => void,
        ) => {
          planes.push(...targets);
          const scene = gsap.timeline({
            defaults: { ease: "none" },
            scrollTrigger: {
              trigger, scroller: root, start, end, scrub: 0.22,
              invalidateOnRefresh: true,
              onToggle: self => updateActivity(self, targets),
              onUpdate,
            },
          });
          if (scene.scrollTrigger) {
            triggers.push(scene.scrollTrigger);
            activityTargets.set(scene.scrollTrigger, targets);
          }
          return scene;
        };

        const artwork = one(".hero-art");
        if (artwork) {
          const heroPlanes = Array.from(artwork.querySelectorAll<HTMLElement>(".plate"));
          gsap.set(heroPlanes, { "--motion-x": "0px", "--motion-y": "0px" });
          gsap.set(artwork, { "--fold-progress": 0 });
          const scene = timeline(hero, heroPlanes,
            layout === "full" ? () => `top top+=${headerHeight()}` : "top top",
            layout === "full"
              ? () => `+=${Math.max(1, hero.offsetHeight - root.clientHeight + headerHeight())}`
              : "bottom top",
          );
          scene.to(artwork, { "--fold-progress": layout === "mobile" ? 0 : options.reduced.value ? 0.36 : 0.8, duration: 0.28 }, 0);
          for (const layer of heroMotionLayers) {
            const plane = artwork.querySelector<HTMLElement>(layer.selector);
            if (!plane) continue;
            scene.to(plane, {
              "--motion-y": () => `${-heroLayerTravel(layer.y, plane.offsetTop, factor)}px`,
              "--motion-x": `${layer.x * factor}px`,
              duration: 1,
            }, 0);
          }
        }

        const anatomy = one(".anatomy-art");
        if (anatomy) {
          const anatomyPlanes = all(".anatomy-back, .anatomy-mid, .anatomy-front, .anatomy-corner");
          const scene = timeline(anatomy, anatomyPlanes);
          anatomyPlanes.forEach((plane, index) => {
            const distance = [20, 34, 56, 74][index] * factor;
            scene.fromTo(plane, { "--motion-y": `${distance * 0.65}px` }, { "--motion-y": `${-distance}px`, duration: 1 }, 0);
          });
        }

        const story = one(".workspace-story");
        const stage = one(".workspace-stage");
        const config = one(".config-paper");
        const workbench = one(".workbench-window");
        const result = one(".result-paper");
        if (story && stage && config && workbench && result) {
          let step = -1;
          stage.dataset.workflowStep = "0";
          const scene = timeline(story, [config, workbench, result],
            layout === "full" ? () => `top top+=${headerHeight() + 16}` : "top 85%",
            layout === "full" ? () => `+=${Math.max(1, story.offsetHeight - stage.offsetHeight)}` : "bottom 30%",
            self => {
              const next = self.progress < 0.3 ? 0 : self.progress < 0.68 ? 1 : 2;
              if (next !== step) {
                step = next;
                stage.dataset.workflowStep = String(step);
              }
            },
          );
          scene.fromTo(config, { "--motion-y": `${55 * factor}px`, "--motion-x": `${-12 * factor}px` },
            { "--motion-y": `${-18 * factor}px`, "--motion-x": "0px", duration: 0.48 }, 0);
          scene.fromTo(workbench, { "--motion-y": `${76 * factor}px`, "--motion-scale": 1 - 0.025 * factor },
            { "--motion-y": "0px", "--motion-scale": 1, duration: 0.48 }, 0.2);
          scene.fromTo(result, { "--motion-y": `${120 * factor}px`, "--motion-x": `${16 * factor}px` },
            { "--motion-y": `${-8 * factor}px`, "--motion-x": "0px", duration: 0.42 }, 0.58);
        }

        for (const image of all(".editorial-image")) {
          const frame = image.parentElement;
          if (!frame) continue;
          const distance = () => Math.min(28, frame.clientHeight * 0.09) * factor;
          timeline(frame, [image]).fromTo(image,
            { "--motion-y": () => `${distance()}px` },
            { "--motion-y": () => `${-distance()}px`, duration: 1 }, 0);
        }

        const ending = one(".begin-art");
        if (ending) {
          const endPlanes = all(".begin-plane");
          const scene = timeline(ending, endPlanes, "top bottom", "bottom bottom");
          endPlanes.forEach((plane, index) => {
            const distance = (32 + index * 13) * factor;
            scene.fromTo(plane, { "--motion-y": `${distance}px` }, { "--motion-y": `${-distance * 0.3}px`, duration: 1 }, 0);
          });
        }
      }, root);
    }

    for (const trigger of triggers) trigger.refresh();
    if (pendingAnchor?.element.isConnected) {
      const difference = pendingAnchor.element.getBoundingClientRect().top - pendingAnchor.top;
      const top = options.enabled.value && pendingAnchor.inHero
        ? pendingAnchor.scrollTop : Math.max(0, root.scrollTop + difference);
      root.scrollTo({ top, behavior: "instant" });
      ScrollTrigger.update();
    }
    pendingAnchor = undefined;
    for (const trigger of triggers) {
      // Restore the correct pose immediately after a preference/viewport change.
      trigger.animation?.totalProgress(trigger.progress, true);
    }
    suspend();
  }

  function scheduleBuild() {
    if (!mounted || disposed || buildFrame !== undefined) return;
    buildFrame = requestAnimationFrame(build);
  }

  watch(options.enabled, () => { void nextTick(scheduleBuild); });
  watch(options.reduced, () => { void nextTick(scheduleBuild); });
  watch(options.paused, suspend);

  onMounted(() => {
    mounted = true;
    gsap.registerPlugin(ScrollTrigger);
    document.addEventListener("visibilitychange", suspend);
    const root = options.root.value;
    if (root) {
      resizeObserver = new ResizeObserver(() => {
        const size = `${root.clientWidth}:${root.clientHeight}`;
        if (size !== rootSize) {
          rootSize = size;
          scheduleBuild();
        }
      });
      resizeObserver.observe(root);
    }
    void document.fonts?.ready.then(() => { if (!disposed) refreshLayout(); });
    scheduleBuild();
  });

  onBeforeUnmount(() => {
    disposed = true;
    mounted = false;
    if (buildFrame !== undefined) cancelAnimationFrame(buildFrame);
    if (refreshFrame !== undefined) cancelAnimationFrame(refreshFrame);
    resizeObserver?.disconnect();
    document.removeEventListener("visibilitychange", suspend);
    clearScene();
  });

  return { refreshLayout, preservePosition };
}
