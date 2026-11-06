<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { activityItemNeedsAttention, activityRunDisclosureKey, buildActivityRuns, type ActivityCallItem, type ActivityRun, type ActivityRunItem } from "@/agent/activityRuns";
import { historicalFailureGroups, summarizeActivityCalls, type HistoricalFailureGroup } from "@/agent/runSummary";
import { useActivityExecution } from "@/agent/activityExecutionContext";
import { useCardDisclosureState } from "@/agent/cardDisclosure";
import { activityContainsSelection, canAutoFoldActivity, useActivityReading } from "@/agent/activityReading";
import { executionDetailMode, worklogRevealRow, trackWorklog } from "@/agent/worklogPrefs";
import { answerReviewNoticeText } from "@/agent/answerReview";
import { shortcutCallTool } from "@/agent/shortcutToolNames";
import { formatDuration, matchSkillRead, type TimelineCall, type TurnItem } from "@/agent/timeline";
import { presentToolCall, toolCallState, toolControlOutcome } from "@/agent/toolPresentation";
import { useToolIdentityCatalog } from "@/agent/toolIdentity";
import { incomingPath } from "@/agent/argsPreview";
import type { Exchange } from "@/agent/exchanges";
import type { OpenFileTarget, OpenSheetRequest } from "./dockSheets";
import ThinkingBlock from "./ThinkingBlock.vue";
import ToolCall from "./ToolCall.vue";
import AnswerText from "./AnswerText.vue";
import AnswerReviewStatus from "./AnswerReviewStatus.vue";
import SourcesPanel from "./SourcesPanel.vue";
import SteeringMessage from "./SteeringMessage.vue";
import Chevron from "./Chevron.vue";
import ActivityWaiting from "./ActivityWaiting.vue";

const props = defineProps<{
  exchange: Exchange; runActive: boolean; workFolded?: boolean;
  skills?: { name: string; path: string }[]; cwd?: string; todoPinned?: boolean;
  serverOffset?: number | null; revealId?: string; waitingForModel?: boolean;
}>();
const emit = defineEmits<{
  suggest: [string]; decide: [{ gateId: string; allow: boolean; args?: unknown; remember?: "tool" | "command" }];
  freeze: [string]; thaw: [string]; "open-sheet": [OpenSheetRequest]; "open-file": [OpenFileTarget];
}>();
const { t } = useI18n();
const disclosure = useCardDisclosureState(), reading = useActivityReading(), catalog = useToolIdentityCatalog();
const rootElement = ref<HTMLElement | null>(null);
const full = computed(() => executionDetailMode.value === "full");
const execution = useActivityExecution();
const retryGroups = ref<ReadonlyMap<string, HistoricalFailureGroup>>(new Map());
const retryExpanded = ref<ReadonlySet<string>>(new Set());
const scope = computed(() => (disclosure?.scopeId?.() ?? "") + ":" + props.exchange.id);
const reserved = ref<ReadonlySet<string>>(new Set());
const automatic = ref<ReadonlyMap<string, boolean>>(new Map());
const localChoices = ref<ReadonlyMap<string, boolean>>(new Map());
const temporary = ref<ReadonlySet<string>>(new Set());
const revealVersions = ref<ReadonlyMap<string, number>>(new Map());
const clocks = ref<ReadonlyMap<string, { start: number; end?: number }>>(new Map());
const now = ref(Date.now());
const elements = new Map<string, HTMLElement>();
let ticker: ReturnType<typeof setInterval> | undefined;
watch(scope, () => {
  automatic.value = new Map(); localChoices.value = new Map();
  temporary.value = new Set(); clocks.value = new Map(); elements.clear();
  revealVersions.value = new Map();
  retryGroups.value = new Map(); retryExpanded.value = new Set();
  reserved.value = new Set();
}, { flush: "sync" });
watch(() => props.exchange.turns.flatMap(turn => turn.incoming ?? []), incoming => {
  const next = new Set(reserved.value);
  for (const item of incoming) if (!item.name) next.add(item.call_id);
  if (next.size !== reserved.value.size) reserved.value = next;
}, { immediate: true, flush: "sync" });
const nodes = computed(() => {
  void scope.value;
  return buildActivityRuns(props.exchange, { reservedCallIds: reserved.value });
});
const runs = computed(() => nodes.value.filter((node): node is ActivityRun => node.kind === "run"));
const summaries = computed(() => new Map(runs.value.map(run => [run.id, summarizeActivityCalls(run.calls)])));
const summary = (run: ActivityRun) => summaries.value.get(run.id)!;
const retryByCall = computed(() => {
  const out = new Map<string, HistoricalFailureGroup>();
  for (const run of runs.value) for (const candidate of historicalFailureGroups(run, execution?.value.callOrigins)) {
    const saved = retryGroups.value.get(candidate.id);
    if (!saved || saved.calls.length !== candidate.calls.length ||
      saved.calls.some((call, index) => call.callId !== candidate.calls[index]?.callId)) continue;
    for (const call of candidate.calls) out.set(call.callId, candidate);
  }
  return out;
});
const runKey = (run: ActivityRun) => activityRunDisclosureKey(props.exchange.id, run.id);
const choice = (run: ActivityRun) => disclosure?.available() ? disclosure.choice(runKey(run)) : localChoices.value.get(run.id);
const itemKeys = (run: ActivityRun) => run.items.map(item => item.kind === "call" ? item.id + ":details" : item.id);
const hasOpenDescendant = (run: ActivityRun) => disclosure?.hasOpen(itemKeys(run)) ?? false;
const hasGate = (run: ActivityRun) => run.calls.some(call => !!call.gate || !!call.childGates?.length);
const activeItem = (item: ActivityRunItem) => item.kind === "thinking" ? item.turn.streaming :
  !!item.incoming || !!item.call && (toolCallState(item.call) === "running" || !!item.call.gate || !!item.call.childGates?.length);
const grouped = (run: ActivityRun) => run.items.filter(item => item.kind === "call").length > 1;
const openRun = (run: ActivityRun) => full.value || hasGate(run) || temporary.value.has(run.id) ||
  (choice(run) ?? (hasOpenDescendant(run) || automatic.value.get(run.id) !== false));
const visibleRun = (run: ActivityRun) => full.value || !props.workFolded || run.items.some(item => activityItemNeedsAttention(item, props.exchange.finished));
const itemRetry = (item: ActivityRunItem) => item.kind === "call" && item.call ? retryByCall.value.get(item.call.callId) : undefined;
const retryIsOpen = (item: ActivityRunItem) => {
  const group = itemRetry(item);
  return !!group && (retryExpanded.value.has(group.id) || group.calls.some(call => temporary.value.has("call:" + call.callId)));
};
const retryFirst = (item: ActivityRunItem) => item.kind === "call" && itemRetry(item)?.calls[0]?.callId === item.call?.callId;
const visibleItem = (run: ActivityRun, item: ActivityRunItem) => full.value || activityItemNeedsAttention(item, props.exchange.finished) ||
  (!props.workFolded && (!grouped(run) || openRun(run)) && (!itemRetry(item) || retryFirst(item) || retryIsOpen(item)));
function toggleRetry(item: ActivityRunItem) {
  const group = itemRetry(item);
  if (!group || full.value) return;
  const next = new Set(retryExpanded.value);
  if (retryIsOpen(item)) {
    next.delete(group.id);
    temporary.value = new Set([...temporary.value].filter(id => !group.calls.some(call => id === "call:" + call.callId)));
  } else { next.add(group.id); reading?.pause(); }
  retryExpanded.value = next;
}
const visibleProse = (turn: TurnItem) => full.value || !props.workFolded ||
  (props.exchange.finished && props.exchange.answer?.id === turn.id) || turn.streaming;
function persistentReceipt(item: ActivityCallItem): boolean {
  const call = item.call;
  if (!call?.result || call.result.is_error) return false;
  const name = shortcutCallTool(call.name, call.args);
  return ["skill_save", "shortcut_create", "shortcut_update", "shortcut_folder_create", "shortcut_folder_rename",
    "shortcut_move", "shortcut_delete", "shortcut_merge", "agent_workflow_enable"].includes(name) ||
    name === "background_task" && !!call.args && typeof call.args === "object" && "action" in call.args && call.args.action === "create";
}
function itemCall(item: ActivityCallItem): TimelineCall {
  return item.call ?? { callId: item.incoming!.call_id, name: item.incoming!.name, args: item.incoming!.head ?? item.incoming!.tail ?? "", running: false, orphaned: false };
}
function summaryText(run: ActivityRun): string {
  const facts = summary(run);
  const clauses = facts.clauses.slice(0, 3).map(clause => t("activityRun.summary." + clause.kind, { n: clause.count, object: clause.object ?? "" }));
  const skills = new Set(run.calls.filter(call => !!call.result && toolCallState(call) === "done" && !toolControlOutcome(call))
    .map(call => matchSkillRead(call.name, call.args, props.skills ?? [], props.cwd ?? "")).filter((name): name is string => !!name));
  clauses.unshift(...[...skills].map(name => t("activityRun.loadedSkill", { name })));
  if (!openRun(run)) {
    const current = run.items.find(activeItem);
    if (current?.kind === "call") {
      if (current.incoming) {
        const name = current.incoming.name;
        const label = name === "write" ? "toolDisclosure.generatingWrite" : name === "edit" ? "toolDisclosure.generatingEdit" : "toolDisclosure.generatingArguments";
        clauses.unshift([t(label), incomingPath(current.incoming) || t("toolDisclosure.objectPending"), t("toolDisclosure.notExecuted")].join(" · "));
      } else {
        const p = presentToolCall(itemCall(current), catalog.value);
        clauses.unshift([p.statusLabel || t("activityRun.running"), p.name, p.target].filter(Boolean).join(" · "));
      }
    } else if (current) clauses.unshift(t("thinkingBlock.title"));
  }
  return clauses.join(" · ") || t("activityRun.calls", { n: facts.total });
}
function lastFailure(run: ActivityRun): string | undefined {
  return [...run.calls].reverse().find(call => toolCallState(call) === "error" && !call.inputInvalid && !toolControlOutcome(call))?.callId;
}
function reveal(run: ActivityRun | undefined, itemId?: string) {
  temporary.value = new Set([...temporary.value, ...(run ? [run.id] : []), ...(itemId ? [itemId] : [])]);
  if (itemId) revealVersions.value = new Map(revealVersions.value).set(itemId, (revealVersions.value.get(itemId) ?? 0) + 1);
}
function toggle(run: ActivityRun) {
  if (full.value || hasGate(run) || activityContainsSelection(elements.get(run.id) ?? null)) return;
  const next = !openRun(run);
  temporary.value = new Set([...temporary.value].filter(id => id !== run.id && !run.items.some(item => item.id === id)));
  if (disclosure?.available()) disclosure.set(runKey(run), next);
  else localChoices.value = new Map(localChoices.value).set(run.id, next);
  if (next) { reading?.pause(); trackWorklog("worklog.rowOpen"); }
}
function protectReading(event: Event) {
  if (typeof Element === "undefined" || !(event.target instanceof Element)) return;
  if (event.type === "focusin" || event.target.closest("button[aria-expanded='false'], .body, .thinking-full, pre")) reading?.pause();
}
function protectSelection() {
  if (activityContainsSelection(rootElement.value)) reading?.pause();
}
onMounted(() => { if (typeof document !== "undefined") document.addEventListener?.("selectionchange", protectSelection); });
onBeforeUnmount(() => { if (typeof document !== "undefined") document.removeEventListener?.("selectionchange", protectSelection); });
function captureElement(id: string, element: unknown) {
  if (element) elements.set(id, element as HTMLElement);
  else elements.delete(id);
}
function maybeFold() {
  const next = new Map(automatic.value);
  const nextRetries = new Map(retryGroups.value);
  let changed = false;
  for (const run of runs.value) {
    const element = elements.get(run.id);
    const focused = typeof document !== "undefined" && !!document.activeElement && !!element?.contains?.(document.activeElement);
    const safe = canAutoFoldActivity({ closed: run.closed, following: reading?.following.value === true,
      active: run.items.some(activeItem), full: full.value, userChoice: choice(run),
      openDescendant: hasOpenDescendant(run) || temporary.value.has(run.id),
      focused, selected: activityContainsSelection(element ?? null) ||
        !!element?.querySelector?.("[data-reading-output='true']") });
    if (safe) {
      if (next.get(run.id) !== false) { next.set(run.id, false); changed = true; }
      for (const group of historicalFailureGroups(run, execution?.value.callOrigins)) {
        if (!nextRetries.has(group.id)) nextRetries.set(group.id, group);
      }
    }
  }
  if (changed) automatic.value = next;
  if (nextRetries.size !== retryGroups.value.size) retryGroups.value = nextRetries;
}
watch(runs, value => {
  const defaults = new Map(automatic.value), times = new Map(clocks.value);
  const observed = Date.now();
  for (const run of value) {
    if (!defaults.has(run.id)) defaults.set(run.id, !props.exchange.finished);
    const clock = times.get(run.id);
    if (!clock && !props.exchange.finished && (!run.closed || run.items.some(activeItem))) times.set(run.id, { start: observed });
    else if (clock && clock.end === undefined && run.closed && !run.items.some(activeItem)) times.set(run.id, { ...clock, end: observed });
  }
  automatic.value = defaults; clocks.value = times;
}, { immediate: true, flush: "sync" });
watch([runs, full, () => reading?.following.value, () => disclosure?.version.value, () => execution?.value], () => { void nextTick(maybeFold); }, { flush: "post" });
watch(() => !props.exchange.finished, active => {
  if (ticker) clearInterval(ticker);
  ticker = undefined;
  if (active && typeof window !== "undefined") ticker = setInterval(() => { now.value = Date.now(); maybeFold(); }, 1000);
}, { immediate: true });
onBeforeUnmount(() => { if (ticker) clearInterval(ticker); });
function observedDuration(run: ActivityRun): string | undefined {
  const clock = clocks.value.get(run.id);
  return clock ? t("activityRun.observed", { d: formatDuration(Math.max(0, (clock.end ?? now.value) - clock.start)) }) : undefined;
}
function revealRow(value: string) {
  const prefix = props.revealId ? props.revealId + ":" : "";
  if (value.startsWith("wl-body-") && (!prefix || !value.startsWith(prefix))) return;
  const id = prefix && value.startsWith(prefix) ? value.slice(prefix.length) : value;
  for (const node of nodes.value) {
    if (node.kind === "run") {
      if (node.id === id) { reveal(node); return; }
      const item = node.items.find(item => item.id === id || item.kind === "call" &&
        [itemCall(item).callId, itemCall(item).callId + ":explore", "activity:" + item.turn.id + ":" + itemCall(item).callId].includes(id));
      if (item) { reveal(node, item.id); return; }
    } else if (node.kind === "card" && [node.id, itemCall(node.item).callId].includes(id)) { reveal(undefined, node.item.id); return; }
  }
}
watch(worklogRevealRow, revealRow);
</script>

<template>
  <div ref="rootElement" class="activity-runs" @click.capture="protectReading" @focusin="protectReading" @wheel.capture.passive="protectReading" @pointerup="protectSelection">
    <template v-for="node in nodes" :key="node.id">
      <SteeringMessage v-if="node.kind === 'steering'" :message="node.message" @open-file="emit('open-file', $event)" />
      <div v-else-if="node.kind === 'notice'" v-show="full || !workFolded" class="activity-notice" :data-mid="node.notice.id">{{ answerReviewNoticeText(node.notice.text) }}</div>
      <div v-else-if="node.kind === 'review'" v-show="visibleProse(node.turn) || node.turn.answerReview?.state === 'running'" :data-mid="node.turn.id">
        <AnswerReviewStatus :review="node.turn.answerReview!" :server-offset="serverOffset" />
      </div>
      <div v-else-if="node.kind === 'prose'" v-show="visibleProse(node.turn)" class="activity-prose" :data-mid="node.turn.id" :data-prose-mid="node.turn.id">
        <div v-if="full" class="turn-index">{{ t('turnCard.turnIndex', { n: node.turn.index }) }}</div>
        <AnswerText :source="node.turn.text" :streaming="node.turn.streaming" :sources="node.turn.sources" />
        <SourcesPanel v-if="node.turn.sources?.length" :sources="node.turn.sources" :text="node.turn.text" />
      </div>
      <div v-else-if="node.kind === 'card'" v-show="full || !workFolded || (node.independent && persistentReceipt(node.item)) || activityItemNeedsAttention(node.item, exchange.finished)"
        class="activity-card" :data-rid="node.id" :data-cite-labels="node.item.call?.result?.source?.label" @pogo-reveal-tool="reveal(undefined, node.item.id)">
        <ToolCall :call="itemCall(node.item)" :incoming="node.item.incoming" :run-active="runActive" :layered="node.independent !== true"
          :full-details="full" :reveal="temporary.has(node.item.id)" :skills="skills" :cwd="cwd" :todo-pinned="todoPinned" :server-offset="serverOffset"
          @suggest="emit('suggest', $event)" @decide="emit('decide', $event)" @freeze="emit('freeze', $event)"
          @thaw="emit('thaw', $event)" @open-sheet="emit('open-sheet', $event)" @open-file="emit('open-file', $event)" />
      </div>
      <div v-else-if="node.kind === 'run'" v-show="visibleRun(node)" :ref="el => captureElement(node.id, el)"
        class="activity-run" :class="{ grouped: grouped(node), open: openRun(node), live: !node.closed }"
        :data-rid="node.id" :data-cite-labels="node.citeLabels.join(' ') || undefined">
        <button v-if="grouped(node)" type="button" class="activity-group-head" :aria-expanded="openRun(node)"
          :aria-controls="(revealId || exchange.id) + ':' + node.id + ':items'" :disabled="full || hasGate(node)" @click="toggle(node)">
          <Chevron :open="openRun(node)" />
          <span v-if="!node.closed" class="live-dot" aria-hidden="true" />
          <span class="run-summary">{{ summaryText(node) }}</span>
          <span v-if="summary(node).failuresObserved" class="run-state failure">{{ t('activityRun.failed', { n: summary(node).failuresObserved }) }}</span>
          <span v-for="code in summary(node).environmentErrors" :key="code" class="run-state">{{ t('activityRun.environment.' + code) }}</span>
          <span v-if="summary(node).gated" class="run-state">{{ t('activityGroup.gate', { n: summary(node).gated }) }}</span>
          <span v-if="summary(node).denied" class="run-state">{{ t('activityRun.denied', { n: summary(node).denied }) }}</span>
          <span v-if="summary(node).timedOut" class="run-state">{{ t('activityRun.timedOut', { n: summary(node).timedOut }) }}</span>
          <span v-if="summary(node).controlled" class="run-state">{{ t('activityRun.controlled', { n: summary(node).controlled }) }}</span>
          <span v-if="observedDuration(node)" class="run-time">{{ observedDuration(node) }}</span>
        </button>
        <div :id="(revealId || exchange.id) + ':' + node.id + ':items'" class="run-items">
          <div v-for="item in node.items" :key="item.id" v-show="visibleItem(node, item)" class="activity-item" :data-rid="item.id" :data-mid="item.turn.id"
            :data-cite-labels="item.kind === 'call' ? item.call?.result?.source?.label : undefined" @pogo-reveal-tool="reveal(node, item.id)">
            <div v-if="full && !item.turn.text && node.items.find(value => value.turn.id === item.turn.id)?.id === item.id" class="turn-index">{{ t('turnCard.turnIndex', { n: item.turn.index }) }}</div>
            <ThinkingBlock v-if="item.kind === 'thinking'" :id="item.turn.id" :text="item.turn.thinking"
              :streaming="item.turn.streaming && !item.turn.text && !item.turn.calls.length && !item.turn.incoming?.length"
              :duration-ms="item.turn.thinkingMs" :full-details="full" :reveal="temporary.has(item.id)" :reveal-version="revealVersions.get(item.id)" />
            <template v-else>
            <button v-if="itemRetry(item) && retryFirst(item)" v-show="!full" type="button" class="retry-head"
              :aria-expanded="retryIsOpen(item)" @click="toggleRetry(item)">
              <Chevron :open="retryIsOpen(item)" />
              <span>{{ presentToolCall(itemCall(item), catalog).name }} · {{ presentToolCall(itemCall(item), catalog).target }}</span>
              <span class="failure">{{ t('activityRun.failed', { n: itemRetry(item)!.calls.length }) }}</span>
            </button>
            <ToolCall v-show="full || !itemRetry(item) || retryIsOpen(item)" :call="itemCall(item)" :incoming="item.incoming" :run-active="runActive" layered
              :full-details="full || retryIsOpen(item)" :reveal="temporary.has(item.id)" :default-expanded="openRun(node) && lastFailure(node) === item.call?.callId"
              :skills="skills" :cwd="cwd" :todo-pinned="todoPinned" :server-offset="serverOffset"
              @suggest="emit('suggest', $event)" @decide="emit('decide', $event)" @freeze="emit('freeze', $event)"
              @thaw="emit('thaw', $event)" @open-sheet="emit('open-sheet', $event)" @open-file="emit('open-file', $event)" />
            </template>
          </div>
        </div>
      </div>
    </template>
    <ActivityWaiting :key="scope" :active="!!waitingForModel" :reserve="!exchange.finished" />
  </div>
</template>

<style scoped lang="scss">
.activity-runs { min-width: 0; color: var(--pg-activity-text, var(--el-text-color-regular)); }
.activity-prose, .activity-card { margin: 8px 0; min-width: 0; }
.activity-run { min-width: 0; margin: 6px 0; }
.activity-run.grouped > .run-items { border-left: 1px solid var(--el-border-color-lighter); padding-left: 9px; margin-left: 5px; }
.activity-group-head { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; min-height: 28px; width: 100%; padding: 3px 0; border: 0; border-radius: 5px; background: transparent; text-align: left; color: inherit; font: inherit; font-size: 12px; cursor: pointer; }
.activity-group-head:disabled { cursor: default; }
.activity-group-head:hover:not(:disabled) { background: var(--el-fill-color-light); }
.activity-group-head:focus-visible { outline: 2px solid var(--pg-activity-live, var(--el-color-primary)); outline-offset: 2px; }
.activity-group-head > :first-child { flex: 0 0 auto; }
.run-summary { min-width: 0; overflow-wrap: anywhere; }
.run-state, .run-time { font-size: 11px; font-variant-numeric: tabular-nums; }
.run-time { margin-left: auto; }
.failure::before { content: "×"; color: var(--pg-activity-error, var(--el-color-danger)); margin-right: 4px; font-weight: 600; }
.live-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--pg-activity-live, var(--el-color-primary)); flex: 0 0 auto; }
.activity-item, .run-items { min-width: 0; }
.turn-index, .activity-notice { color: var(--pg-activity-text, var(--el-text-color-regular)); font-size: 12px; margin: 6px 0; overflow-wrap: anywhere; }
.activity-notice { white-space: pre-wrap; }
.retry-head { display: flex; flex-wrap: wrap; align-items: center; gap: 7px; min-height: 28px; padding: 3px 0; background: transparent; border: 0; color: inherit; font: inherit; font-size: 12px; text-align: left; cursor: pointer; }
.retry-head:focus-visible { outline: 2px solid var(--pg-activity-live, var(--el-color-primary)); outline-offset: 2px; border-radius: 3px; }
@media (max-width: 480px) { .activity-group-head { min-height: 32px; } .run-time { flex-basis: 100%; margin-left: 20px; } }
</style>
