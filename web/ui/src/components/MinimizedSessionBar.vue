<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { fitMinimizedSession } from "@/agent/minimizedSessionLayout";
import { sessionProgressTone, type SessionProgressEvent } from "@/agent/sessionProgress";
import type { FloatingRect } from "@/agent/immersiveLayout";

const props = defineProps<{
  title: string;
  controlsId: string;
  events: readonly SessionProgressEvent[];
  busy: boolean;
  attention: boolean;
  disconnected?: boolean;
}>();
const emit = defineEmits<{ restore: []; exit: [] }>();
const { t } = useI18n();
const root = ref<HTMLElement | null>(null);
const eventLine = ref<HTMLElement | null>(null);
const historyOpen = ref(false);
const dragging = ref(false);
const rect = ref<FloatingRect | null>(null);
const viewHeight = ref(0);
const viewportOffset = ref({ x: 0, y: 0 });
const positionKey = "pogo:immersive-minibar-position:v1";
let stopGesture: (() => void) | undefined;
let safeProbe: HTMLElement | undefined;
let lastPointerDrag = false;

function viewport() {
  const style = safeProbe ? getComputedStyle(safeProbe) : undefined;
  const inset = (key: "paddingTop" | "paddingRight" | "paddingBottom" | "paddingLeft") => parseFloat(style?.[key] ?? "0") || 0;
  return { width: window.visualViewport?.width ?? window.innerWidth, height: window.visualViewport?.height ?? window.innerHeight,
    safeTop: inset("paddingTop"), safeRight: inset("paddingRight"), safeBottom: inset("paddingBottom"), safeLeft: inset("paddingLeft") };
}
function remember() {
  if (!rect.value) return;
  try { sessionStorage.setItem(positionKey, JSON.stringify({ x: rect.value.x, y: rect.value.y })); } catch { /* Tab-local memory is optional. */ }
}
function fit() {
  stopGesture?.();
  rect.value = fitMinimizedSession(rect.value, viewport());
  viewHeight.value = viewport().height;
  viewportOffset.value = { x: window.visualViewport?.offsetLeft ?? 0, y: window.visualViewport?.offsetTop ?? 0 };
}
const position = computed(() => rect.value ? {
  left: `${rect.value.x + viewportOffset.value.x}px`, top: `${rect.value.y + viewportOffset.value.y}px`,
  width: `${rect.value.width}px`, height: `${rect.value.height}px`,
} : undefined);
const historyAbove = computed(() => !!rect.value && rect.value.y > viewHeight.value - rect.value.y - rect.value.height);
const historyHeight = computed(() => !rect.value ? 250 : Math.max(1, Math.min(250, (historyAbove.value
  ? rect.value.y : viewHeight.value - rect.value.y - rect.value.height) - 19)));

function beginDrag(event: PointerEvent) {
  if (event.button !== 0 || !rect.value) return;
  event.preventDefault();
  historyOpen.value = false;
  stopGesture?.();
  const handle = event.currentTarget as HTMLElement;
  const start = { ...rect.value }, origin = { x: event.clientX, y: event.clientY };
  lastPointerDrag = false;
  handle.focus({ preventScroll: true });
  handle.setPointerCapture(event.pointerId);
  const move = (e: PointerEvent) => {
    if (e.pointerId !== event.pointerId) return;
    const dx = e.clientX - origin.x, dy = e.clientY - origin.y;
    if (!lastPointerDrag && Math.hypot(dx, dy) < 4) return;
    lastPointerDrag = true;
    dragging.value = true;
    rect.value = fitMinimizedSession({ x: start.x + dx, y: start.y + dy }, viewport());
  };
  const finish = () => {
    handle.removeEventListener("pointermove", move);
    handle.removeEventListener("pointerup", finish);
    handle.removeEventListener("pointercancel", finish);
    handle.removeEventListener("lostpointercapture", finish);
    window.removeEventListener("blur", finish);
    stopGesture = undefined;
    dragging.value = false;
    if (handle.hasPointerCapture(event.pointerId)) handle.releasePointerCapture(event.pointerId);
    remember();
  };
  stopGesture = finish;
  handle.addEventListener("pointermove", move);
  handle.addEventListener("pointerup", finish);
  handle.addEventListener("pointercancel", finish);
  handle.addEventListener("lostpointercapture", finish);
  window.addEventListener("blur", finish);
}
function adjustKey(event: KeyboardEvent) {
  if (event.target !== event.currentTarget || event.isComposing || !rect.value) return;
  const step = event.shiftKey ? 40 : 12;
  const delta: Record<string, [number, number]> = { ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step] };
  if (!delta[event.key]) return;
  event.preventDefault(); event.stopPropagation();
  stopGesture?.();
  rect.value = fitMinimizedSession({ x: rect.value.x + delta[event.key]![0], y: rect.value.y + delta[event.key]![1] }, viewport());
  remember();
}
function movedClick(event: MouseEvent) {
  if (lastPointerDrag) { event.preventDefault(); event.stopPropagation(); lastPointerDrag = false; }
}
const latest = computed(() => props.events[props.events.length - 1]);
const tone = computed(() => props.disconnected ? "error" : props.attention ? "attention" : sessionProgressTone(latest.value?.kind));
const status = computed(() => t(`immersiveBrowser.mini.${props.disconnected ? "disconnected" : props.attention ? "attention" : props.busy ? "working"
  : latest.value?.kind === "failed" || latest.value?.kind === "toolFailed" ? "needsReview" : latest.value?.kind === "limited" ? "limitReached"
  : latest.value?.kind === "stopped" ? "stoppedShort" : latest.value?.kind === "ended" ? "endedShort" : "ready"}`));
function label(event?: SessionProgressEvent) {
  if (!event) return t(`immersiveBrowser.mini.${props.busy ? "started" : "idle"}`);
  const key = event.tool?.startsWith("mcp") ? "mcp" : event.tool?.startsWith("docs_") ? "office" : event.tool;
  const known = key && ["browser", "office", "read", "write", "edit", "bash", "subagent", "todo", "synth", "skill", "mcp"].includes(key);
  const tool = known ? t(`immersiveBrowser.mini.tools.${key}`) : event.tool || t("immersiveBrowser.mini.tools.unknown");
  return t(`immersiveBrowser.mini.${event.kind}`, { tool });
}
const latestText = computed(() => props.disconnected ? t("immersiveBrowser.mini.connectionLost") : props.attention ? t("immersiveBrowser.mini.approval") : label(latest.value));
watch(() => [latest.value?.id, latestText.value], () => { void nextTick(() => { if (eventLine.value) eventLine.value.scrollLeft = 0; }); });
const recent = computed(() => [...props.events].reverse());
const time = (ts: number) => ts > 0 ? new Date(ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }) : "";
function outside(event: PointerEvent) { if (event.target instanceof Node && !root.value?.contains(event.target)) historyOpen.value = false; }
function escape(event: KeyboardEvent) {
  if (event.key === "Escape" && !event.shiftKey && historyOpen.value && root.value?.contains(event.target as Node)) {
    event.stopPropagation(); event.preventDefault(); historyOpen.value = false;
    root.value?.querySelector<HTMLButtonElement>(".mini-event-toggle")?.focus({ preventScroll: true });
  }
}
onMounted(() => {
  safeProbe = document.createElement("div");
  safeProbe.style.cssText = "position:fixed;visibility:hidden;pointer-events:none;padding:env(safe-area-inset-top) env(safe-area-inset-right) env(safe-area-inset-bottom) env(safe-area-inset-left)";
  document.body.appendChild(safeProbe);
  try { const saved = JSON.parse(sessionStorage.getItem(positionKey) || "null"); if (saved && Number.isFinite(saved.x) && Number.isFinite(saved.y)) rect.value = saved; } catch { /* Ignore malformed preferences. */ }
  fit();
  window.addEventListener("resize", fit);
  window.visualViewport?.addEventListener("resize", fit);
  window.visualViewport?.addEventListener("scroll", fit);
  window.addEventListener("pointerdown", outside, true);
});
onBeforeUnmount(() => {
  stopGesture?.();
  window.removeEventListener("resize", fit);
  window.visualViewport?.removeEventListener("resize", fit);
  window.visualViewport?.removeEventListener("scroll", fit);
  window.removeEventListener("pointerdown", outside, true);
  safeProbe?.remove();
});
</script>

<template>
  <section ref="root" class="immersive-minibar" data-testid="minimized-session" :class="{ dragging }" :style="position" :aria-label="t('immersiveBrowser.mini.label')" @keydown="escape">
    <div class="mini-head">
      <button type="button" class="mini-move" data-testid="minimized-session-grip" :title="t('immersiveBrowser.mini.move')" :aria-label="t('immersiveBrowser.mini.move')"
        @pointerdown="beginDrag" @keydown="adjustKey" @click="movedClick">
        <svg class="mini-grip" width="10" height="16" viewBox="0 0 10 16" aria-hidden="true"><path d="M2 3h.01M7 3h.01M2 8h.01M7 8h.01M2 13h.01M7 13h.01" stroke="currentColor" stroke-width="2.8" stroke-linecap="round" /></svg>
        <span class="mini-title" :title="title">{{ title }}</span>
      </button>
      <span class="mini-state" :data-tone="disconnected ? 'error' : attention ? 'attention' : busy ? 'working' : tone">{{ status }}</span>
      <button type="button" class="mini-action immersive-chat-restore" :title="t('immersiveBrowser.restore')" :aria-label="t('immersiveBrowser.restore')" :aria-controls="controlsId" aria-expanded="false" @click="emit('restore')">
        <svg width="16" height="16" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true"><rect x="3" y="3" width="14" height="14" rx="2" /><path d="M3 8h14m-7 3v4m-2-2 2-2 2 2" /></svg>
      </button>
      <button type="button" class="mini-action" :title="t('immersiveBrowser.exitTip')" :aria-label="t('immersiveBrowser.exit')" @click="emit('exit')">
        <svg width="15" height="15" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><path d="m5 5 10 10M5 15 15 5" /></svg>
      </button>
    </div>
    <div class="mini-event-row">
      <span class="mini-event-dot" :data-tone="tone" aria-hidden="true" />
      <span ref="eventLine" class="mini-event-text" data-testid="minimized-session-event" role="status" aria-live="polite" aria-atomic="true" :title="latestText" tabindex="0">{{ latestText }}</span>
      <button type="button" class="mini-event-toggle" :title="t('immersiveBrowser.mini.recent')" :aria-label="t('immersiveBrowser.mini.recent')" :aria-expanded="historyOpen" :disabled="!events.length" @click="historyOpen = !historyOpen">
        <svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" aria-hidden="true"><path :d="historyOpen ? 'm4 10 4-4 4 4' : 'm4 6 4 4 4-4'" /></svg>
      </button>
    </div>
    <div v-if="historyOpen" class="mini-history" :class="{ above: historyAbove }" :style="{ maxHeight: `${historyHeight}px` }" role="region" :aria-label="t('immersiveBrowser.mini.recent')">
      <div class="mini-history-head">{{ t('immersiveBrowser.mini.recent') }}<span>{{ t('immersiveBrowser.mini.recentHint') }}</span></div>
      <ol><li v-for="event in recent" :key="event.id"><span class="mini-event-dot" :data-tone="sessionProgressTone(event.kind)" aria-hidden="true" /><span>{{ label(event) }}</span><time>{{ time(event.ts) }}</time></li></ol>
    </div>
  </section>
</template>

<style scoped>
.immersive-minibar { position: fixed; right: 16px; bottom: 20px; z-index: 120; width: 332px; height: 72px; box-sizing: border-box; padding: 5px 8px 6px; border: 1px solid var(--el-border-color); border-radius: 14px; background: var(--el-bg-color); color: var(--el-text-color-primary); box-shadow: 0 6px 24px #0002; font-size: 12px; }
.mini-head { display: flex; align-items: center; gap: 5px; height: 31px; }
.mini-move { display: flex; flex: 1; align-items: center; gap: 7px; min-width: 0; height: 30px; padding: 0 3px; border: 0; border-radius: 5px; color: inherit; background: none; text-align: left; font: inherit; cursor: grab; touch-action: none; user-select: none; }
.dragging .mini-move { cursor: grabbing; }
.mini-grip { flex-shrink: 0; opacity: .4; }
.mini-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; font-weight: 600; }
.mini-state { flex-shrink: 0; font-size: 10px; color: var(--el-text-color-secondary); }
.mini-state[data-tone="attention"] { color: var(--el-color-warning); }
.mini-state[data-tone="error"] { color: var(--el-color-danger); }
.mini-action { display: grid; flex: 0 0 27px; place-items: center; width: 27px; height: 28px; padding: 0; border: 0; border-radius: 6px; background: none; color: var(--el-text-color-secondary); cursor: pointer; }
.mini-action:hover, .mini-event-toggle:hover:not(:disabled) { background: var(--el-fill-color); color: var(--el-text-color-primary); }
.mini-action:focus-visible, .mini-move:focus-visible, .mini-event-toggle:focus-visible, .mini-event-text:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 1px; }
.mini-event-row { display: flex; align-items: center; gap: 8px; height: 25px; margin: 1px 1px 0 5px; border-top: 1px solid var(--el-border-color-lighter); padding-top: 2px; }
.mini-event-dot { display: block; width: 6px; height: 6px; flex: 0 0 6px; border-radius: 50%; background: var(--el-text-color-placeholder); }
.mini-event-dot[data-tone="working"] { background: var(--el-color-primary); }
.mini-event-dot[data-tone="done"] { background: var(--el-color-success); }
.mini-event-dot[data-tone="attention"] { background: var(--el-color-warning); }
.mini-event-dot[data-tone="error"] { background: var(--el-color-danger); }
.mini-event-text { display: block; flex: 1; min-width: 0; overflow-x: auto; overflow-y: hidden; white-space: nowrap; scrollbar-width: none; color: var(--el-text-color-secondary); font-size: 11px; line-height: 20px; }
.mini-event-text::-webkit-scrollbar { display: none; }
.mini-event-toggle { display: grid; flex: 0 0 24px; width: 24px; height: 22px; padding: 0; place-items: center; border: 0; border-radius: 5px; background: none; color: var(--el-text-color-secondary); cursor: pointer; }
.mini-event-toggle:disabled { opacity: .35; cursor: default; }
.mini-history { position: absolute; top: calc(100% + 7px); left: 0; right: 0; box-sizing: border-box; max-height: min(250px, 45dvh); overflow-y: auto; padding: 10px 11px; border: 1px solid var(--el-border-color); border-radius: 12px; background: var(--el-bg-color); box-shadow: 0 6px 24px #0002; }
.mini-history.above { top: auto; bottom: calc(100% + 7px); }
.mini-history-head { display: flex; justify-content: space-between; margin: 0 0 8px; font-weight: 600; font-size: 11px; }
.mini-history-head span { font-weight: 400; color: var(--el-text-color-placeholder); font-size: 10px; }
.mini-history ol { margin: 0; padding: 0; list-style: none; }
.mini-history li { display: flex; align-items: baseline; gap: 8px; padding: 6px 0; font-size: 11px; line-height: 1.5; }
.mini-history li > span:nth-child(2) { flex: 1; min-width: 0; overflow-wrap: anywhere; }
.mini-history time { font-size: 10px; color: var(--el-text-color-placeholder); white-space: nowrap; }
@media (pointer: coarse) { .mini-action { flex-basis: 30px; width: 30px; height: 31px; } .mini-event-toggle { flex-basis: 30px; width: 30px; } }
</style>
