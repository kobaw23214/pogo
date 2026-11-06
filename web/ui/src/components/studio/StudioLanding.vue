<script setup lang="ts">
import { useI18n } from "vue-i18n";
const { t: tr } = useI18n();
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import { cinemaPlanes } from "./landingSceneConfig";
import { useOpeningCinematics } from "./useOpeningCinematics";
import Logo from "@/components/Logo.vue";
import { landingScenarios, anatomyFocusContent, type CapabilityKey, type LandingScenarioKey } from "./studioLandingContent";
import { useLandingMotion } from "./useLandingMotion";
import { useLandingSnap } from "./useLandingSnap";
import { useLandingDetails } from "./useLandingDetails";
import { landingMotionPreference, landingMotionPreferenceKey } from "./landingMotionConfig";

const props = defineProps<{ loginOpen: boolean }>();
const emit = defineEmits<{ login: [opener: HTMLElement] }>();
const scroller = ref<HTMLElement | null>(null);
const header = ref<HTMLElement | null>(null);
const hero = ref<HTMLElement | null>(null);
const mobileMenu = ref<HTMLDetailsElement | null>(null);
const reducedMotion = ref(window.matchMedia("(prefers-reduced-motion: reduce)").matches);
const motionEnabled = ref(readMotionPreference());
const readingProgress = ref<HTMLElement | null>(null);
const activeChapter = ref("overview");
const activeCapability = ref<CapabilityKey | null>("agent");
const activeScenario = ref<LandingScenarioKey>("research");
const scenario = computed(() => landingScenarios[activeScenario.value]);
const anatomyCaption = computed(() => activeCapability.value ? anatomyFocusContent[activeCapability.value].caption : tr('studioLandingLabels.anatomyTitle'));
const anatomyLines = computed(() => activeCapability.value ? anatomyFocusContent[activeCapability.value].lines : anatomyFocusContent.agent.lines);
let frame: number | undefined;
let motionQuery: MediaQueryList | undefined;
let observer: ResizeObserver | undefined;
let disposed = false;
const chapterIds = ["overview", "capabilities", "workspace", "possibilities", "begin"];
const clamp = (value: number) => Math.min(1, Math.max(0, value));
const { refreshLayout, preservePosition } = useLandingMotion({
  root: scroller, header, hero, enabled: motionEnabled,
  reduced: reducedMotion,
  paused: computed(() => props.loginOpen),
});
const { enterFold, leaveFold, cancelFold, animateScenario } = useLandingDetails({
  root: scroller, enabled: motionEnabled, reduced: reducedMotion,
  paused: computed(() => props.loginOpen), onLayoutChange: refreshLayout,
});

useOpeningCinematics({
  root: scroller, header, hero, enabled: motionEnabled,
  paused: computed(() => props.loginOpen),
});

const { scrollToChapter, stepScene, currentStop, currentIndex, chapterScenes, sceneIndex, storyStops } = useLandingSnap({
  root: scroller, header, enabled: motionEnabled,
  paused: computed(() => props.loginOpen),
  reduced: reducedMotion,
  chapterIds,
});

function readMotionPreference() {
  try { return landingMotionPreference(localStorage.getItem(landingMotionPreferenceKey)); }
  catch { return true; }
}

function syncScroll() {
  frame = undefined;
  const root = scroller.value;
  if (!root) return;
  const top = root.getBoundingClientRect().top;
  root.classList.toggle("has-scrolled", root.scrollTop > 18);
  const headerHeight = header.value?.offsetHeight ?? 76;
  const reading = root.scrollHeight > root.clientHeight
    ? clamp(root.scrollTop / (root.scrollHeight - root.clientHeight)) : 0;
  if (readingProgress.value) readingProgress.value.style.transform = `scaleX(${reading})`;
  const marker = top + headerHeight + root.clientHeight * 0.32;
  let current = "overview";
  for (const id of chapterIds) {
    const chapter = root.querySelector<HTMLElement>("#" + id);
    if (chapter && chapter.getBoundingClientRect().top <= marker) current = id;
  }
  activeChapter.value = motionEnabled.value ? currentStop.value?.chapterId ?? current : current;
}

function requestSync() {
  if (!disposed && frame === undefined) frame = window.requestAnimationFrame(syncScroll);
}

function syncPreferences() {
  reducedMotion.value = motionQuery?.matches ?? false;
  void nextTick(requestSync);
}

function scrollTo(id: string) {
  scrollToChapter(id);
  if (mobileMenu.value) mobileMenu.value.open = false;
}

function requestLogin(event: MouseEvent) {
  if (event.currentTarget instanceof HTMLElement) emit("login", event.currentTarget);
}

function toggleMotion() {
  preservePosition();
  motionEnabled.value = !motionEnabled.value;
  try { localStorage.setItem(landingMotionPreferenceKey, motionEnabled.value ? "on" : "off"); }
  catch { /* The toggle remains usable when browser storage is unavailable. */ }
  void nextTick(requestSync);
}

function toggleCapability(key: CapabilityKey) {
  activeCapability.value = activeCapability.value === key ? null : key;
  void nextTick(() => { requestSync(); refreshLayout(); });
}

function selectScenario(key: LandingScenarioKey, navigate = false) {
  const changed = activeScenario.value !== key;
  activeScenario.value = key;
  void nextTick(() => { if (changed) animateScenario(); refreshLayout(); });
  if (navigate) void nextTick(() => scrollTo("workspace"));
}

onMounted(() => {
  motionQuery = window.matchMedia("(prefers-reduced-motion: reduce)");
  motionQuery.addEventListener("change", syncPreferences);
  window.addEventListener("resize", requestSync);
  observer = new ResizeObserver(requestSync);
  if (scroller.value) observer.observe(scroller.value);
  if (hero.value) observer.observe(hero.value);
  syncPreferences();
});

onBeforeUnmount(() => {
  disposed = true;
  if (frame !== undefined) window.cancelAnimationFrame(frame);
  motionQuery?.removeEventListener("change", syncPreferences);
  window.removeEventListener("resize", requestSync);
  observer?.disconnect();
});
</script>

<template>
  <div ref="scroller" class="studio-landing cinema-hybrid" :class="{ 'motion-enabled': motionEnabled, 'motion-reduced': reducedMotion, 'login-open': loginOpen }" @scroll.passive="requestSync">

  <a class="skip-link" href="#capabilities" @click.prevent="scrollTo('capabilities')">{{ tr('studioManagement.text366') }}</a>
  <header ref="header" class="site-header">
    <a href="#overview" @click.prevent="scrollTo('overview')" class="brand" :aria-label="tr('studioManagement.text367')"><Logo :size="36" /><span>pogo</span><small>{{ tr('studioLandingLabels.foundation') }}</small></a>
    <nav class="desktop-nav" :aria-label="tr('studioManagement.text368')"><a href="#capabilities" :aria-current="activeChapter === 'capabilities' ? 'location' : undefined" @click.prevent="scrollTo('capabilities')">{{ tr('studioManagement.text369') }}</a><a href="#workspace" :aria-current="activeChapter === 'workspace' ? 'location' : undefined" @click.prevent="scrollTo('workspace')">{{ tr('studioManagement.text370') }}</a><a href="#possibilities" :aria-current="activeChapter === 'possibilities' ? 'location' : undefined" @click.prevent="scrollTo('possibilities')">{{ tr('studioManagement.text371') }}</a></nav>
    <button class="button button-dark header-login" type="button" @click="requestLogin">{{ tr('studioManagement.text372') }} <span>↗</span></button>
    <details ref="mobileMenu" class="mobile-menu"><summary :aria-label="tr('studioManagement.text373')">☰</summary><nav :aria-label="tr('studioManagement.text374')"><a href="#capabilities" @click.prevent="scrollTo('capabilities')">{{ tr('studioManagement.text369') }}</a><a href="#workspace" @click.prevent="scrollTo('workspace')">{{ tr('studioManagement.text370') }}</a><a href="#possibilities" @click.prevent="scrollTo('possibilities')">{{ tr('studioManagement.text371') }}</a></nav></details>
    <span ref="readingProgress" class="reading-progress" aria-hidden="true"></span>
  </header>

  <aside class="chapter-nav" :aria-label="tr('studioManagement.text375')"><a href="#overview" @click.prevent="scrollTo('overview')" :aria-label="tr('studioManagement.text376')" :class="{ 'is-active': activeChapter === 'overview' }" :aria-current="activeChapter === 'overview' ? 'location' : undefined"><span>01</span><i></i></a><a href="#capabilities" @click.prevent="scrollTo('capabilities')" :aria-label="tr('studioManagement.text377')" :class="{ 'is-active': activeChapter === 'capabilities' }" :aria-current="activeChapter === 'capabilities' ? 'location' : undefined"><span>02</span><i></i></a><a href="#workspace" @click.prevent="scrollTo('workspace')" :aria-label="tr('studioManagement.text378')" :class="{ 'is-active': activeChapter === 'workspace' }" :aria-current="activeChapter === 'workspace' ? 'location' : undefined"><span>03</span><i></i></a><a href="#possibilities" @click.prevent="scrollTo('possibilities')" :aria-label="tr('studioManagement.text379')" :class="{ 'is-active': activeChapter === 'possibilities' }" :aria-current="activeChapter === 'possibilities' ? 'location' : undefined"><span>04</span><i></i></a><a href="#begin" @click.prevent="scrollTo('begin')" :aria-label="tr('studioManagement.text380')" :class="{ 'is-active': activeChapter === 'begin' }" :aria-current="activeChapter === 'begin' ? 'location' : undefined"><span>05</span><i></i></a></aside>

  <main>
      <section ref="hero" id="overview" class="chapter opening-scene" aria-labelledby="opening-title">
        <div class="cinema-stage opening-stage">
          <div class="opening-grid" aria-hidden="true"></div><span class="opening-watermark" aria-hidden="true">pogo</span>
          <div class="cinema-meta opening-meta"><span><i></i> THE POGO ATLAS</span><span>{{ tr('studioLandingLabels.unfolded') }}</span><span>{{ tr('studioManagement.text381') }}</span></div>
          <div class="opening-art" role="img" :aria-label="tr('studioManagement.text382')">
            <svg class="opening-wires" viewBox="0 0 1000 650" fill="none" aria-hidden="true"><g stroke="currentColor"><path d="M110 390 270 130 700 76 920 460 660 570 110 390Z"/><path d="m270 130 390 440M110 390h810M700 76 420 530" stroke-dasharray="4 8"/><circle cx="530" cy="340" r="160"/><circle cx="530" cy="340" r="220" stroke-dasharray="1 12"/></g><g fill="currentColor"><rect x="104" y="384" width="12" height="12"/><rect x="264" y="124" width="12" height="12"/><rect x="694" y="70" width="12" height="12"/><rect x="914" y="454" width="12" height="12"/></g></svg>
            <div v-for="plane in cinemaPlanes" :key="plane.id" class="cinema-paper" :class="[`paper-${plane.kind}`,`atlas-${plane.id}`]" :data-plane="plane.id" :style="{left:`${plane.x}%`,top:`${plane.y}%`,width:`${plane.w}%`,'--paper-rotation':`${plane.r}deg`,'--depth':plane.z,zIndex:plane.z}">
              <template v-if="plane.kind === 'archive'"><small>{{ tr('studioLandingLabels.archive') }}</small><div class="archive-lines"></div><em>f(x) → ∞</em><span>{{ tr('studioLandingLabels.observeConnectCreate') }}</span></template>
              <template v-else-if="plane.kind === 'blueprint'"><small>{{ tr('studioLandingLabels.connections') }}</small><svg viewBox="0 0 240 180" fill="none" stroke="currentColor"><circle cx="120" cy="90" r="62"/><ellipse cx="120" cy="90" rx="104" ry="35" transform="rotate(-26 120 90)"/><path d="M16 90h208M120 10v160M38 25h164v130H38z"/><circle cx="120" cy="90" r="12"/></svg><span>{{ tr('studioLandingLabels.modelSkillMcp') }}</span></template>
              <template v-else-if="plane.kind === 'vellum'"><small>{{ tr('studioLandingLabels.openEnded') }}</small><strong>∞</strong><span>{{ tr('studioLandingLabels.yourIdeas') }}</span></template>
              <template v-else-if="plane.kind === 'ticket'"><b>pogo</b><small style="white-space:pre-line">{{ tr('studioLandingLabels.buildPossibility') }}</small><i>↗</i></template>
              <template v-else-if="plane.kind === 'tag'"><i></i><span style="white-space:pre-line">{{ tr('studioLandingLabels.connectComposeCreate') }}</span></template>
              <span v-else class="paper-register">{{ String(plane.z).padStart(2,'0') }} / POGO ATLAS</span>
            </div>
          </div>
          <div class="opening-copy"><p class="cinema-eyebrow">{{ tr('studioLandingLabels.nextFoundation') }}</p><h1 id="opening-title">{{ tr('studioManagement.text383') }}<br>{{ tr('studioManagement.text384') }}<span>{{ tr('studioManagement.text385') }}<i>✳</i></span>。</h1><div class="opening-intro"><p>{{ tr('studioManagement.text386') }}<br>{{ tr('studioManagement.text387') }}</p><button class="cinema-button" type="button" @click="requestLogin">{{ tr('studioManagement.text388') }} <span>↗</span></button></div></div>
          <div class="opening-discovery" aria-hidden="true"><span class="cinema-eyebrow">{{ tr('studioLandingLabels.lookCloser') }}</span><h2>{{ tr('studioManagement.text389') }}<br>{{ tr('studioManagement.text390') }}<span>{{ tr('studioManagement.text391') }}</span></h2><p>{{ tr('studioManagement.text392') }}<br>{{ tr('studioManagement.text393') }}</p></div>
          <div class="opening-portal" aria-hidden="true"><Logo :size="52"/><i></i></div>
          <div class="opening-bottom"><span>{{ tr('studioLandingLabels.selfHosted') }}</span><a href="#capabilities" @click.prevent="scrollTo('capabilities')"><span>{{ tr('studioManagement.text394') }}</span><i>↓</i></a><button class="motion-button" type="button" :aria-pressed="motionEnabled" @click="toggleMotion"><span class="motion-indicator" aria-hidden="true"></span>{{ motionEnabled ? tr('studioManagement.text395') : tr('studioManagement.text396') }}</button></div>
          <div class="scene-scrub opening-scrub" aria-hidden="true"><span>{{ tr('studioManagement.text397') }}</span><i><b></b></i><span>{{ tr('studioManagement.text398') }}</span></div>
        </div>
      </section>

    <section class="capabilities section-pad chapter" id="capabilities" aria-labelledby="capability-title">
      <div class="section-heading"><p class="eyebrow">{{ tr('studioLandingLabels.foundationChapter') }}</p><h2 id="capability-title">{{ tr('studioManagement.text399') }}<br>{{ tr('studioManagement.text400') }}<span class="outline-word">{{ tr('studioManagement.text401') }}</span></h2><p class="section-lead">{{ tr('studioManagement.text402') }}<br>{{ tr('studioManagement.text403') }}</p></div>
      <div class="capability-layout">
        <div class="anatomy-art" aria-hidden="true" :data-focus="activeCapability"><Transition name="anatomy-swap" mode="out-in"><span class="anatomy-caption" :key="anatomyCaption">{{ anatomyCaption }}</span></Transition><div class="anatomy-back"><span>{{ tr('studioLandingLabels.skillMethods') }}</span><em class="anatomy-tag">{{ anatomyFocusContent.skill.tag }}</em></div><div class="anatomy-mid art-iris"><em class="anatomy-tag">{{ anatomyFocusContent.model.tag }}</em></div><div class="anatomy-front"><Logo :size="76" /><Transition name="anatomy-swap" mode="out-in"><span :key="anatomyLines[0]">{{ anatomyLines[0] }}<br>{{ anatomyLines[1] }}</span></Transition><i>↗</i><em class="anatomy-tag">{{ anatomyFocusContent.agent.tag }}</em></div><div class="anatomy-corner art-shell"><em class="anatomy-tag">{{ anatomyFocusContent.mcp.tag }}</em></div><div class="anatomy-axis"><i></i><span>{{ tr('studioLandingLabels.definePossibilities') }}</span></div><div class="anatomy-ruler">+ ───── + ───── + ───── +</div></div>
        <div class="capability-folds">
          <article class="capability-fold" :class="{ 'is-open': activeCapability === 'agent' }"><button class="fold-trigger" type="button" :aria-expanded="activeCapability === 'agent'" aria-controls="fold-agent" @click="toggleCapability('agent')"><span class="fold-no">01</span><span class="fold-name">Agent <small>{{ tr('studioManagement.text404') }}</small></span><span class="fold-icon">{{ activeCapability === 'agent' ? '−' : '＋' }}</span></button><Transition :css="false" @enter="enterFold" @leave="leaveFold" @enter-cancelled="cancelFold" @leave-cancelled="cancelFold"><div class="fold-body" id="fold-agent" v-show="activeCapability === 'agent'"><p>{{ tr('studioManagement.text405') }}</p><div class="fold-illustration"><span class="mini-agent">P</span><span class="fold-line"></span><div class="role-stack"><span>{{ tr('studioManagement.text406') }}</span><span>{{ tr('studioManagement.text407') }}</span><span>{{ tr('studioManagement.text408') }}</span></div></div><span class="fold-foot">{{ tr('studioLandingLabels.bringsTogether') }} ↗</span></div></Transition></article>
          <article class="capability-fold" :class="{ 'is-open': activeCapability === 'model' }"><button class="fold-trigger" type="button" :aria-expanded="activeCapability === 'model'" aria-controls="fold-model" @click="toggleCapability('model')"><span class="fold-no">02</span><span class="fold-name">{{ tr('studioLandingLabels.models') }} <small>{{ tr('studioManagement.text409') }}</small></span><span class="fold-icon">{{ activeCapability === 'model' ? '−' : '＋' }}</span></button><Transition :css="false" @enter="enterFold" @leave="leaveFold" @enter-cancelled="cancelFold" @leave-cancelled="cancelFold"><div class="fold-body" id="fold-model" v-show="activeCapability === 'model'"><p>{{ tr('studioManagement.text410') }}</p><div class="fold-keywords"><span>{{ tr('studioManagement.text411') }}</span><span>{{ tr('studioManagement.text412') }}</span><span>{{ tr('studioManagement.text095') }}</span></div><span class="fold-foot">{{ tr('studioLandingLabels.chooseThinking') }} ↗</span></div></Transition></article>
          <article class="capability-fold" :class="{ 'is-open': activeCapability === 'skill' }"><button class="fold-trigger" type="button" :aria-expanded="activeCapability === 'skill'" aria-controls="fold-skill" @click="toggleCapability('skill')"><span class="fold-no">03</span><span class="fold-name">Skills <small>{{ tr('studioManagement.text413') }}</small></span><span class="fold-icon">{{ activeCapability === 'skill' ? '−' : '＋' }}</span></button><Transition :css="false" @enter="enterFold" @leave="leaveFold" @enter-cancelled="cancelFold" @leave-cancelled="cancelFold"><div class="fold-body" id="fold-skill" v-show="activeCapability === 'skill'"><p>{{ tr('studioManagement.text414') }}</p><div class="fold-keywords"><span>{{ tr('studioManagement.text415') }}</span><span>{{ tr('studioManagement.text416') }}</span><span>{{ tr('studioManagement.text417') }}</span></div><span class="fold-foot">{{ tr('studioLandingLabels.giveMethod') }} ↗</span></div></Transition></article>
          <article class="capability-fold" :class="{ 'is-open': activeCapability === 'mcp' }"><button class="fold-trigger" type="button" :aria-expanded="activeCapability === 'mcp'" aria-controls="fold-mcp" @click="toggleCapability('mcp')"><span class="fold-no">04</span><span class="fold-name">MCP <small>{{ tr('studioManagement.text418') }}</small></span><span class="fold-icon">{{ activeCapability === 'mcp' ? '−' : '＋' }}</span></button><Transition :css="false" @enter="enterFold" @leave="leaveFold" @enter-cancelled="cancelFold" @leave-cancelled="cancelFold"><div class="fold-body" id="fold-mcp" v-show="activeCapability === 'mcp'"><p>{{ tr('studioManagement.text419') }}</p><div class="fold-keywords"><span>{{ tr('studioManagement.text420') }}</span><span>{{ tr('studioManagement.text421') }}</span><span>{{ tr('studioManagement.text422') }}</span></div><span class="fold-foot">{{ tr('studioLandingLabels.connectWorld') }} ↗</span></div></Transition></article>
        </div>
      </div>
      <div class="section-end"><span>{{ tr('studioLandingLabels.yourWay') }}</span><span>POGO / 02</span></div>
    </section>

    <section class="workspace section-pad chapter" id="workspace" aria-labelledby="workspace-title">
      <div class="workspace-heading"><div><p class="eyebrow">{{ tr('studioLandingLabels.intentAction') }}</p><h2 id="workspace-title">{{ tr('studioManagement.text423') }}<br>{{ tr('studioManagement.text424') }}<span>{{ tr('studioManagement.text425') }}</span></h2></div><div class="workspace-aside"><p>{{ tr('studioManagement.text426') }}<br>{{ tr('studioManagement.text427') }}</p><div class="scenario-switch" role="group" :aria-label="tr('studioManagement.text428')"><button type="button" :aria-pressed="activeScenario === 'research'" @click="selectScenario('research')">{{ tr('studioManagement.text429') }}</button><button type="button" :aria-pressed="activeScenario === 'engineering'" @click="selectScenario('engineering')">{{ tr('studioManagement.text430') }}</button><button type="button" :aria-pressed="activeScenario === 'analysis'" @click="selectScenario('analysis')">{{ tr('studioManagement.text431') }}</button></div></div></div>
      <div class="workspace-story"><div class="workspace-stage">
      <div class="workspace-progress" aria-hidden="true"><span>{{ tr('studioManagement.text432') }}</span><i></i><span>{{ tr('studioManagement.text433') }}</span><i></i><span>{{ tr('studioManagement.text434') }}</span></div>
      <div class="workspace-collage">
        <div class="config-paper"><span class="paper-code">{{ tr('studioManagement.text435') }}</span><h3>{{ tr('studioManagement.text436') }}<br>{{ tr('studioManagement.text437') }}</h3><div><span>{{ tr('studioLandingLabels.role') }}</span><b id="sample-role">{{ scenario.role }}</b></div><div><span>SKILL</span><b id="sample-skill">{{ scenario.skill }}</b></div><div><span>{{ tr('studioLandingLabels.tools') }}</span><b id="sample-tools">{{ scenario.tools }}</b></div><i class="config-mark">✳</i></div>
        <div class="workbench-window"><div class="window-chrome"><span class="window-dots"><i></i><i></i><i></i></span><span>pogo / {{ tr('studioManagement.text370') }}</span><span>{{ tr('studioManagement.text438') }}</span></div><div class="workbench-content"><aside class="workbench-sidebar"><Logo :size="32" /><span class="sidebar-caption">{{ tr('studioManagement.text370') }}</span><span class="sidebar-active">{{ tr('studioManagement.text439') }}</span><span>{{ tr('studioManagement.text440') }}</span><span>{{ tr('studioManagement.text441') }}</span><span>{{ tr('studioManagement.text442') }}</span><span class="sidebar-bottom" style="white-space:pre-line">{{ tr('studioLandingLabels.workspacePossibilities') }}</span></aside><div class="task-conversation"><div class="task-header"><span class="avatar">R</span><div><b id="sample-agent">{{ scenario.agent }}</b><small>{{ tr('studioManagement.text443') }}</small></div><span class="task-header-star">✳</span></div><div class="task-question" id="sample-question">{{ scenario.question }}</div><div class="task-answer"><span class="answer-icon">↳</span><div><p id="sample-answer">{{ scenario.answer }}</p><ol id="sample-steps"><li v-for="step in scenario.steps" :key="step"><i>✓</i> {{ step }}</li></ol></div></div><div class="file-result"><span>▤</span><div><b id="sample-file">{{ scenario.file }}</b><small>{{ tr('studioManagement.text444') }}</small></div><span>↗</span></div><div class="mock-composer">{{ tr('studioManagement.text445') }} <span>↑</span></div></div></div></div>
        <div class="result-paper"><div class="result-meta"><span>{{ tr('studioLandingLabels.outputNumbered') }}</span><span>↗</span></div><h3 id="result-title"><template v-for="(line, index) in scenario.title" :key="line"><br v-if="index > 0" />{{ line }}</template></h3><p class="result-summary" id="result-summary"><template v-for="(line, index) in scenario.summary" :key="line"><br v-if="index > 0" />{{ line }}</template></p><div class="result-divider"></div><div class="result-outline" id="result-outline"><span v-for="(line, index) in scenario.outline" :key="line"><b>{{ String(index + 1).padStart(2, '0') }}</b> {{ line }}</span></div><div class="result-seal">P<br>G</div></div>
        <span class="workspace-note">{{ tr('studioManagement.text446') }}</span>
      </div>
      </div></div>
      <div class="workflow-caption"><p><span>{{ tr('studioLandingLabels.input') }}</span> {{ tr('studioManagement.text447') }}</p><i>→</i><p><span>{{ tr('studioLandingLabels.process') }}</span> {{ tr('studioManagement.text448') }}</p><i>→</i><p><span>{{ tr('studioLandingLabels.output') }}</span> {{ tr('studioManagement.text449') }}</p></div>
    </section>

    <section class="possibilities section-pad chapter" id="possibilities" aria-labelledby="possibilities-title">
      <div class="possibility-heading"><div><p class="eyebrow">{{ tr('studioLandingLabels.openCanvas') }}</p><h2 id="possibilities-title">{{ tr('studioManagement.text450') }}</h2></div><p>{{ tr('studioManagement.text451') }}<br>{{ tr('studioManagement.text452') }}</p></div>
      <div class="editorial-grid">
        <button class="editorial-card card-research" type="button" @click="selectScenario('research', true)"><div class="editorial-art"><div class="editorial-image art-mountain" aria-hidden="true"></div><span class="card-coordinates">{{ tr('studioLandingLabels.exploreUnknown') }}</span><div class="mountain-cross"></div></div><div class="editorial-copy"><span class="eyebrow">{{ tr('studioManagement.text570') }}</span><h3>{{ tr('studioManagement.text453') }}<br>{{ tr('studioManagement.text454') }}</h3><p>{{ tr('studioManagement.text455') }}<br>{{ tr('studioManagement.text456') }}</p><span class="card-link">{{ tr('studioManagement.text457') }} <i>↗</i></span></div><span class="card-index">01</span></button>
        <button class="editorial-card card-engineering" type="button" @click="selectScenario('engineering', true)"><div class="editorial-art"><div class="editorial-image art-iris" aria-hidden="true"></div><span class="card-coordinates">{{ tr('studioLandingLabels.buildNext') }}</span></div><div class="editorial-copy"><span class="eyebrow">{{ tr('studioManagement.text571') }}</span><h3>{{ tr('studioManagement.text458') }}<br>{{ tr('studioManagement.text459') }}</h3><p>{{ tr('studioManagement.text460') }}<br>{{ tr('studioManagement.text461') }}</p><span class="card-link">{{ tr('studioManagement.text462') }} <i>↗</i></span></div><span class="card-index">02</span></button>
        <button class="editorial-card card-analysis" type="button" @click="selectScenario('analysis', true)"><div class="editorial-art"><div class="editorial-image art-leaf" aria-hidden="true"></div><span class="card-coordinates">{{ tr('studioLandingLabels.findPattern') }}</span></div><div class="editorial-copy"><span class="eyebrow">{{ tr('studioManagement.text572') }}</span><h3>{{ tr('studioManagement.text463') }}<br>{{ tr('studioManagement.text464') }}</h3><p>{{ tr('studioManagement.text465') }}<br>{{ tr('studioManagement.text466') }}</p><span class="card-link">{{ tr('studioManagement.text467') }} <i>↗</i></span></div><span class="card-index">03</span></button>
      </div>
      <div class="possibility-footer"><span>{{ tr('studioLandingLabels.foundationRemains') }}</span><span>{{ tr('studioManagement.text468') }}</span></div>
    </section>

    <section class="begin section-pad chapter" id="begin" aria-labelledby="begin-title">
      <div class="begin-art" aria-hidden="true"><div class="begin-plane bp1 art-neuron"></div><div class="begin-plane bp2 art-shell"></div><div class="begin-plane bp3 art-wave"></div><div class="begin-plane bp4 art-leaf"></div><div class="begin-plane bp5"><span>pogo</span><i>↗</i></div></div>
      <div class="begin-content"><p class="eyebrow">{{ tr('studioLandingLabels.nextChapter') }}</p><h2 id="begin-title">{{ tr('studioManagement.text469') }}<br>{{ tr('studioManagement.text470') }}</h2><p>{{ tr('studioManagement.text471') }}<br>{{ tr('studioManagement.text472') }}</p><button class="button button-dark" type="button" @click="requestLogin">{{ tr('studioManagement.text372') }} <span>↗</span></button></div>
      <footer class="site-footer"><a href="#overview" @click.prevent="scrollTo('overview')" class="footer-brand">pogo <span>{{ tr('studioLandingLabels.foundation') }}</span></a><div><a href="#capabilities" @click.prevent="scrollTo('capabilities')">{{ tr('studioManagement.text369') }}</a><a href="#workspace" @click.prevent="scrollTo('workspace')">{{ tr('studioManagement.text370') }}</a><a href="#possibilities" @click.prevent="scrollTo('possibilities')">{{ tr('studioManagement.text371') }}</a></div><small>POGO / {{ tr('studioLandingLabels.foundation') }}</small></footer>
    </section>
  </main>
  <nav v-if="motionEnabled" class="scene-nav" :aria-label="tr('studioManagement.text473')" :class="{ 'is-hidden': loginOpen }">
    <button type="button" :aria-label="tr('studioManagement.text474')" :disabled="currentIndex <= 0" @click="stepScene(-1)"><span aria-hidden="true">↑</span></button>
    <div class="scene-caption"><div><span>{{ currentStop?.chapterTitle || tr('studioManagement.text475') }}</span><small>{{ sceneIndex + 1 }} / {{ chapterScenes.length || 1 }}</small></div><strong>{{ currentStop?.label || tr('studioManagement.text397') }}</strong><span class="scene-meter" aria-hidden="true"><i :style="{ transform: `scaleX(${(sceneIndex + 1) / (chapterScenes.length || 1)})` }"></i></span></div>
    <button type="button" :aria-label="tr('studioManagement.text476')" :disabled="currentIndex >= storyStops.length - 1" @click="stepScene(1)"><span aria-hidden="true">↓</span></button>
  </nav>
  </div>
</template>

<style scoped src="./studioLanding.css"></style>
<style scoped src="./studioLandingDetails.css"></style>
<style scoped src="./studioOpening.css"></style>
<style scoped src="./studioStory.css"></style>
