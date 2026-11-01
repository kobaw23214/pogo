<script setup lang="ts">
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
const anatomyCaption = computed(() => activeCapability.value ? anatomyFocusContent[activeCapability.value].caption : "ANATOMY OF AN AGENT");
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

  <a class="skip-link" href="#capabilities" @click.prevent="scrollTo('capabilities')">跳到基座能力</a>
  <header ref="header" class="site-header">
    <a href="#overview" @click.prevent="scrollTo('overview')" class="brand" aria-label="Pogo 首页"><Logo :size="36" /><span>pogo</span><small>AGENT FOUNDATION</small></a>
    <nav class="desktop-nav" aria-label="主导航"><a href="#capabilities" :aria-current="activeChapter === 'capabilities' ? 'location' : undefined" @click.prevent="scrollTo('capabilities')">基座能力</a><a href="#workspace" :aria-current="activeChapter === 'workspace' ? 'location' : undefined" @click.prevent="scrollTo('workspace')">工作空间</a><a href="#possibilities" :aria-current="activeChapter === 'possibilities' ? 'location' : undefined" @click.prevent="scrollTo('possibilities')">应用场景</a></nav>
    <button class="button button-dark header-login" type="button" @click="requestLogin">进入工作空间 <span>↗</span></button>
    <details ref="mobileMenu" class="mobile-menu"><summary aria-label="展开导航">☰</summary><nav aria-label="移动导航"><a href="#capabilities" @click.prevent="scrollTo('capabilities')">基座能力</a><a href="#workspace" @click.prevent="scrollTo('workspace')">工作空间</a><a href="#possibilities" @click.prevent="scrollTo('possibilities')">应用场景</a></nav></details>
    <span ref="readingProgress" class="reading-progress" aria-hidden="true"></span>
  </header>

  <aside class="chapter-nav" aria-label="章节导航"><a href="#overview" @click.prevent="scrollTo('overview')" aria-label="01 首页" :class="{ 'is-active': activeChapter === 'overview' }" :aria-current="activeChapter === 'overview' ? 'location' : undefined"><span>01</span><i></i></a><a href="#capabilities" @click.prevent="scrollTo('capabilities')" aria-label="02 基座能力" :class="{ 'is-active': activeChapter === 'capabilities' }" :aria-current="activeChapter === 'capabilities' ? 'location' : undefined"><span>02</span><i></i></a><a href="#workspace" @click.prevent="scrollTo('workspace')" aria-label="03 工作空间" :class="{ 'is-active': activeChapter === 'workspace' }" :aria-current="activeChapter === 'workspace' ? 'location' : undefined"><span>03</span><i></i></a><a href="#possibilities" @click.prevent="scrollTo('possibilities')" aria-label="04 应用场景" :class="{ 'is-active': activeChapter === 'possibilities' }" :aria-current="activeChapter === 'possibilities' ? 'location' : undefined"><span>04</span><i></i></a><a href="#begin" @click.prevent="scrollTo('begin')" aria-label="05 开始使用" :class="{ 'is-active': activeChapter === 'begin' }" :aria-current="activeChapter === 'begin' ? 'location' : undefined"><span>05</span><i></i></a></aside>

  <main>
      <section ref="hero" id="overview" class="chapter opening-scene" aria-labelledby="opening-title">
        <div class="cinema-stage opening-stage">
          <div class="opening-grid" aria-hidden="true"></div><span class="opening-watermark" aria-hidden="true">pogo</span>
          <div class="cinema-meta opening-meta"><span><i></i> THE POGO ATLAS</span><span>INTELLIGENCE, UNFOLDED / 001</span><span>模型 · 技能 · 工具 · 行动</span></div>
          <div class="opening-art" role="img" aria-label="十一张科学纸页聚合为一个系统，随滚动向四周拆解并让出中央镜头">
            <svg class="opening-wires" viewBox="0 0 1000 650" fill="none" aria-hidden="true"><g stroke="currentColor"><path d="M110 390 270 130 700 76 920 460 660 570 110 390Z"/><path d="m270 130 390 440M110 390h810M700 76 420 530" stroke-dasharray="4 8"/><circle cx="530" cy="340" r="160"/><circle cx="530" cy="340" r="220" stroke-dasharray="1 12"/></g><g fill="currentColor"><rect x="104" y="384" width="12" height="12"/><rect x="264" y="124" width="12" height="12"/><rect x="694" y="70" width="12" height="12"/><rect x="914" y="454" width="12" height="12"/></g></svg>
            <div v-for="plane in cinemaPlanes" :key="plane.id" class="cinema-paper" :class="[`paper-${plane.kind}`,`atlas-${plane.id}`]" :data-plane="plane.id" :style="{left:`${plane.x}%`,top:`${plane.y}%`,width:`${plane.w}%`,'--paper-rotation':`${plane.r}deg`,'--depth':plane.z,zIndex:plane.z}">
              <template v-if="plane.kind === 'archive'"><small>FIG. 01 / THE ARCHIVE</small><div class="archive-lines"></div><em>f(x) → ∞</em><span>OBSERVE / CONNECT / CREATE</span></template>
              <template v-else-if="plane.kind === 'blueprint'"><small>A SYSTEM OF CONNECTIONS</small><svg viewBox="0 0 240 180" fill="none" stroke="currentColor"><circle cx="120" cy="90" r="62"/><ellipse cx="120" cy="90" rx="104" ry="35" transform="rotate(-26 120 90)"/><path d="M16 90h208M120 10v160M38 25h164v130H38z"/><circle cx="120" cy="90" r="12"/></svg><span>MODEL ↔ SKILL ↔ MCP</span></template>
              <template v-else-if="plane.kind === 'vellum'"><small>OPEN ENDED, BY DESIGN</small><strong>∞</strong><span>YOUR IDEAS. YOUR AGENTS.</span></template>
              <template v-else-if="plane.kind === 'ticket'"><b>pogo</b><small>BUILD YOUR<br>NEXT POSSIBILITY.</small><i>↗</i></template>
              <template v-else-if="plane.kind === 'tag'"><i></i><span>CONNECT.<br>COMPOSE.<br>CREATE.</span></template>
              <span v-else class="paper-register">{{ String(plane.z).padStart(2,'0') }} / POGO ATLAS</span>
            </div>
          </div>
          <div class="opening-copy"><p class="cinema-eyebrow">A FOUNDATION FOR WHAT COMES NEXT</p><h1 id="opening-title">万千能力，<br>在此<span>展开<i>✳</i></span>。</h1><div class="opening-intro"><p>你的 Agent 基座。<br>让模型、技能与工具，成为行动的起点。</p><button class="cinema-button" type="button" @click="requestLogin">开始构建 <span>↗</span></button></div></div>
          <div class="opening-discovery" aria-hidden="true"><span class="cinema-eyebrow">LOOK CLOSER / 01</span><h2>拆开能力。<br>看见<span>可能。</span></h2><p>每一层各有所长，<br>每一次连接，都为你所用。</p></div>
          <div class="opening-portal" aria-hidden="true"><Logo :size="52"/><i></i></div>
          <div class="opening-bottom"><span>SELF-HOSTED. MODULAR. YOURS.</span><a href="#capabilities" @click.prevent="scrollTo('capabilities')"><span>向下滚动，展开整个系统</span><i>↓</i></a><button class="motion-button" type="button" :aria-pressed="motionEnabled" @click="toggleMotion"><span class="motion-indicator" aria-hidden="true"></span>{{ motionEnabled ? '关闭视差' : '开启视差' }}</button></div>
          <div class="scene-scrub opening-scrub" aria-hidden="true"><span>聚合</span><i><b></b></i><span>拆解</span></div>
        </div>
      </section>

    <section class="capabilities section-pad chapter" id="capabilities" aria-labelledby="capability-title">
      <div class="section-heading"><p class="eyebrow">02 / THE FOUNDATION</p><h2 id="capability-title">复杂的能力，<br>清晰的<span class="outline-word">组合。</span></h2><p class="section-lead">每一层，做好一件事。<br>组合在一起，成为你的工作方式。</p></div>
      <div class="capability-layout">
        <div class="anatomy-art" aria-hidden="true" :data-focus="activeCapability"><Transition name="anatomy-swap" mode="out-in"><span class="anatomy-caption" :key="anatomyCaption">{{ anatomyCaption }}</span></Transition><div class="anatomy-back"><span>SKILLS / METHODS</span><em class="anatomy-tag">{{ anatomyFocusContent.skill.tag }}</em></div><div class="anatomy-mid art-iris"><em class="anatomy-tag">{{ anatomyFocusContent.model.tag }}</em></div><div class="anatomy-front"><Logo :size="76" /><Transition name="anatomy-swap" mode="out-in"><span :key="anatomyLines[0]">{{ anatomyLines[0] }}<br>{{ anatomyLines[1] }}</span></Transition><i>↗</i><em class="anatomy-tag">{{ anatomyFocusContent.agent.tag }}</em></div><div class="anatomy-corner art-shell"><em class="anatomy-tag">{{ anatomyFocusContent.mcp.tag }}</em></div><div class="anatomy-axis"><i></i><span>YOU DEFINE THE POSSIBILITIES</span></div><div class="anatomy-ruler">+ ───── + ───── + ───── +</div></div>
        <div class="capability-folds">
          <article class="capability-fold" :class="{ 'is-open': activeCapability === 'agent' }"><button class="fold-trigger" type="button" :aria-expanded="activeCapability === 'agent'" aria-controls="fold-agent" @click="toggleCapability('agent')"><span class="fold-no">01</span><span class="fold-name">Agent <small>定义你的任务角色</small></span><span class="fold-icon">{{ activeCapability === 'agent' ? '−' : '＋' }}</span></button><Transition :css="false" @enter="enterFold" @leave="leaveFold" @enter-cancelled="cancelFold" @leave-cancelled="cancelFold"><div class="fold-body" id="fold-agent" v-show="activeCapability === 'agent'"><p>把目标、角色与执行方式组织起来。从一次探索，到持续开展的工作，都有清楚的起点。</p><div class="fold-illustration"><span class="mini-agent">P</span><span class="fold-line"></span><div class="role-stack"><span>目标与上下文</span><span>能力与执行方式</span><span>会话与结果</span></div></div><span class="fold-foot">THE ONE THAT BRINGS IT TOGETHER ↗</span></div></Transition></article>
          <article class="capability-fold" :class="{ 'is-open': activeCapability === 'model' }"><button class="fold-trigger" type="button" :aria-expanded="activeCapability === 'model'" aria-controls="fold-model" @click="toggleCapability('model')"><span class="fold-no">02</span><span class="fold-name">Models <small>连接适合的推理能力</small></span><span class="fold-icon">{{ activeCapability === 'model' ? '−' : '＋' }}</span></button><Transition :css="false" @enter="enterFold" @leave="leaveFold" @enter-cancelled="cancelFold" @leave-cancelled="cancelFold"><div class="fold-body" id="fold-model" v-show="activeCapability === 'model'"><p>集中配置模型服务，让任务与合适的模型相遇。连接由你选择，工作方式由你定义。</p><div class="fold-keywords"><span>服务商</span><span>可用模型</span><span>默认模型</span></div><span class="fold-foot">CHOOSE HOW YOUR AGENT THINKS ↗</span></div></Transition></article>
          <article class="capability-fold" :class="{ 'is-open': activeCapability === 'skill' }"><button class="fold-trigger" type="button" :aria-expanded="activeCapability === 'skill'" aria-controls="fold-skill" @click="toggleCapability('skill')"><span class="fold-no">03</span><span class="fold-name">Skills <small>把经验沉淀为方法</small></span><span class="fold-icon">{{ activeCapability === 'skill' ? '−' : '＋' }}</span></button><Transition :css="false" @enter="enterFold" @leave="leaveFold" @enter-cancelled="cancelFold" @leave-cancelled="cancelFold"><div class="fold-body" id="fold-skill" v-show="activeCapability === 'skill'"><p>将研究、分析与执行经验整理为可复用的技能，让下一次任务从已有方法开始。</p><div class="fold-keywords"><span>专业流程</span><span>执行指引</span><span>复用与迭代</span></div><span class="fold-foot">GIVE YOUR AGENT A WAY OF WORKING ↗</span></div></Transition></article>
          <article class="capability-fold" :class="{ 'is-open': activeCapability === 'mcp' }"><button class="fold-trigger" type="button" :aria-expanded="activeCapability === 'mcp'" aria-controls="fold-mcp" @click="toggleCapability('mcp')"><span class="fold-no">04</span><span class="fold-name">MCP <small>让工具与数据成为能力</small></span><span class="fold-icon">{{ activeCapability === 'mcp' ? '−' : '＋' }}</span></button><Transition :css="false" @enter="enterFold" @leave="leaveFold" @enter-cancelled="cancelFold" @leave-cancelled="cancelFold"><div class="fold-body" id="fold-mcp" v-show="activeCapability === 'mcp'"><p>通过 MCP 连接外部服务与工具。让 Agent 的工作触达资料、文件和业务系统。</p><div class="fold-keywords"><span>外部工具</span><span>数据与服务</span><span>能力发现</span></div><span class="fold-foot">CONNECT TO THE WORLD YOU WORK IN ↗</span></div></Transition></article>
        </div>
      </div>
      <div class="section-end"><span>NOT ONE WAY TO WORK. YOUR WAY TO WORK.</span><span>POGO / 02</span></div>
    </section>

    <section class="workspace section-pad chapter" id="workspace" aria-labelledby="workspace-title">
      <div class="workspace-heading"><div><p class="eyebrow">03 / FROM INTENT TO ACTION</p><h2 id="workspace-title">想法有了，<br>让工作<span>发生。</span></h2></div><div class="workspace-aside"><p>在一个工作空间里，<br>连接能力、推进任务、看见结果。</p><div class="scenario-switch" role="group" aria-label="工作流示例"><button type="button" :aria-pressed="activeScenario === 'research'" @click="selectScenario('research')">研究</button><button type="button" :aria-pressed="activeScenario === 'engineering'" @click="selectScenario('engineering')">工程</button><button type="button" :aria-pressed="activeScenario === 'analysis'" @click="selectScenario('analysis')">分析</button></div></div></div>
      <div class="workspace-story"><div class="workspace-stage">
      <div class="workspace-progress" aria-hidden="true"><span>01 / 连接能力</span><i></i><span>02 / 推进任务</span><i></i><span>03 / 看见结果</span></div>
      <div class="workspace-collage">
        <div class="config-paper"><span class="paper-code">CAPABILITY PROFILE / 示例</span><h3>为任务，<br>准备好能力。</h3><div><span>ROLE</span><b id="sample-role">{{ scenario.role }}</b></div><div><span>SKILL</span><b id="sample-skill">{{ scenario.skill }}</b></div><div><span>TOOLS</span><b id="sample-tools">{{ scenario.tools }}</b></div><i class="config-mark">✳</i></div>
        <div class="workbench-window"><div class="window-chrome"><span class="window-dots"><i></i><i></i><i></i></span><span>pogo / workspace</span><span>交互示例 ↗</span></div><div class="workbench-content"><aside class="workbench-sidebar"><Logo :size="32" /><span class="sidebar-caption">工作空间</span><span class="sidebar-active">◉ 我的 Agents</span><span>◈ 模型配置</span><span>▤ Skill 管理</span><span>⌘ MCP 管理</span><span class="sidebar-bottom">Your workspace.<br>Your possibilities.</span></aside><div class="task-conversation"><div class="task-header"><span class="avatar">R</span><div><b id="sample-agent">{{ scenario.agent }}</b><small>工作流演示</small></div><span class="task-header-star">✳</span></div><div class="task-question" id="sample-question">{{ scenario.question }}</div><div class="task-answer"><span class="answer-icon">↳</span><div><p id="sample-answer">{{ scenario.answer }}</p><ol id="sample-steps"><li v-for="step in scenario.steps" :key="step"><i>✓</i> {{ step }}</li></ol></div></div><div class="file-result"><span>▤</span><div><b id="sample-file">{{ scenario.file }}</b><small>结果材料 · 示例</small></div><span>↗</span></div><div class="mock-composer">继续探索你的想法… <span>↑</span></div></div></div></div>
        <div class="result-paper"><div class="result-meta"><span>OUTPUT / 01</span><span>↗</span></div><h3 id="result-title"><template v-for="(line, index) in scenario.title" :key="line"><br v-if="index > 0" />{{ line }}</template></h3><p class="result-summary" id="result-summary"><template v-for="(line, index) in scenario.summary" :key="line"><br v-if="index > 0" />{{ line }}</template></p><div class="result-divider"></div><div class="result-outline" id="result-outline"><span v-for="(line, index) in scenario.outline" :key="line"><b>{{ String(index + 1).padStart(2, '0') }}</b> {{ line }}</span></div><div class="result-seal">P<br>G</div></div>
        <span class="workspace-note">ILLUSTRATIVE WORKFLOW / 非真实运行数据</span>
      </div>
      </div></div>
      <div class="workflow-caption"><p><span>INPUT</span> 一个明确的目标</p><i>→</i><p><span>PROCESS</span> 能力协同与执行</p><i>→</i><p><span>OUTPUT</span> 可以继续使用的成果</p></div>
    </section>

    <section class="possibilities section-pad chapter" id="possibilities" aria-labelledby="possibilities-title">
      <div class="possibility-heading"><div><p class="eyebrow">04 / AN OPEN CANVAS</p><h2 id="possibilities-title">不止一种可能。</h2></div><p>同一个基座，<br>为不同的问题展开。</p></div>
      <div class="editorial-grid">
        <button class="editorial-card card-research" type="button" @click="selectScenario('research', true)"><div class="editorial-art"><div class="editorial-image art-mountain" aria-hidden="true"></div><span class="card-coordinates">FIG. R / EXPLORE THE UNKNOWN</span><div class="mountain-cross"></div></div><div class="editorial-copy"><span class="eyebrow">RESEARCH & INSIGHT</span><h3>从线索，<br>到洞见。</h3><p>整理资料、比较方案、保留依据。<br>让探索形成可以复核的成果。</p><span class="card-link">查看研究示例 <i>↗</i></span></div><span class="card-index">01</span></button>
        <button class="editorial-card card-engineering" type="button" @click="selectScenario('engineering', true)"><div class="editorial-art"><div class="editorial-image art-iris" aria-hidden="true"></div><span class="card-coordinates">FIG. E / BUILD WHAT'S NEXT</span></div><div class="editorial-copy"><span class="eyebrow">CODE & ENGINEERING</span><h3>把思路，<br>接到行动。</h3><p>理解代码与上下文，<br>整理实现和验证的路径。</p><span class="card-link">查看工程示例 <i>↗</i></span></div><span class="card-index">02</span></button>
        <button class="editorial-card card-analysis" type="button" @click="selectScenario('analysis', true)"><div class="editorial-art"><div class="editorial-image art-leaf" aria-hidden="true"></div><span class="card-coordinates">FIG. A / FIND THE PATTERN</span></div><div class="editorial-copy"><span class="eyebrow">ANALYSIS & KNOWLEDGE</span><h3>让复杂，<br>逐渐清晰。</h3><p>梳理记录、发现关联，<br>把信息变成下一步的判断。</p><span class="card-link">查看分析示例 <i>↗</i></span></div><span class="card-index">03</span></button>
      </div>
      <div class="possibility-footer"><span>THE TOOLS CHANGE. THE FOUNDATION REMAINS.</span><span>继续拓展你的工作方式 ↗</span></div>
    </section>

    <section class="begin section-pad chapter" id="begin" aria-labelledby="begin-title">
      <div class="begin-art" aria-hidden="true"><div class="begin-plane bp1 art-neuron"></div><div class="begin-plane bp2 art-shell"></div><div class="begin-plane bp3 art-wave"></div><div class="begin-plane bp4 art-leaf"></div><div class="begin-plane bp5"><span>pogo</span><i>↗</i></div></div>
      <div class="begin-content"><p class="eyebrow">05 / YOUR NEXT CHAPTER</p><h2 id="begin-title">你的下一位 Agent，<br>从这里开始。</h2><p>一个基座。你的模型、技能与工具。<br>剩下的，交给你的想象。</p><button class="button button-dark" type="button" @click="requestLogin">进入工作空间 <span>↗</span></button></div>
      <footer class="site-footer"><a href="#overview" @click.prevent="scrollTo('overview')" class="footer-brand">pogo <span>AGENT FOUNDATION</span></a><div><a href="#capabilities" @click.prevent="scrollTo('capabilities')">基座能力</a><a href="#workspace" @click.prevent="scrollTo('workspace')">工作空间</a><a href="#possibilities" @click.prevent="scrollTo('possibilities')">应用场景</a></div><small>POGO / AGENT FOUNDATION</small></footer>
    </section>
  </main>
  <nav v-if="motionEnabled" class="scene-nav" aria-label="当前章节分镜" :class="{ 'is-hidden': loginOpen }">
    <button type="button" aria-label="上一分镜" :disabled="currentIndex <= 0" @click="stepScene(-1)"><span aria-hidden="true">↑</span></button>
    <div class="scene-caption"><div><span>{{ currentStop?.chapterTitle || '首页' }}</span><small>{{ sceneIndex + 1 }} / {{ chapterScenes.length || 1 }}</small></div><strong>{{ currentStop?.label || '聚合' }}</strong><span class="scene-meter" aria-hidden="true"><i :style="{ transform: `scaleX(${(sceneIndex + 1) / (chapterScenes.length || 1)})` }"></i></span></div>
    <button type="button" aria-label="下一分镜" :disabled="currentIndex >= storyStops.length - 1" @click="stepScene(1)"><span aria-hidden="true">↓</span></button>
  </nav>
  </div>
</template>

<style scoped src="./studioLanding.css"></style>
<style scoped src="./studioLandingDetails.css"></style>
<style scoped src="./studioOpening.css"></style>
<style scoped src="./studioStory.css"></style>
