<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { TopRight } from "@element-plus/icons-vue";
import CodeBlock from "../CodeBlock.vue";
import Chevron from "../Chevron.vue";
import WorkLogGlyph from "../WorkLogGlyph.vue";
import SubagentStepRow from "./SubagentStepRow.vue";
import { formatDuration, subagentActivityRows, subagentSteps, type SubagentRow, type SubagentStep, type TaskNotice } from "@/agent/timeline";
import { subagentSummary } from "@/agent/subagentSummary";
import { useCardDisclosure, useCardDisclosureState } from "@/agent/cardDisclosure";
import { resultTextPreview } from "./resultPresentation";
import type { OpenSheetRequest } from "../dockSheets";
import type { BgTask, PendingGate, SubagentDetails, SubagentFrame, ToolResult } from "@/api/types";

const props = defineProps<{
  callId?: string; frames?: SubagentFrame[]; result?: ToolResult; args?: unknown;
  details?: Partial<SubagentDetails>; running: boolean; bgTask?: BgTask;
  startedAt?: number; gates?: PendingGate[]; notice?: TaskNotice; orphaned?: boolean; serverOffset?: number | null;
}>();
const emit = defineEmits<{ "open-sheet": [OpenSheetRequest] }>();
const { t } = useI18n();
const summary = computed(() => subagentSummary(props));
const parentCallId = computed(() => props.callId ?? props.result?.call_id);
const expanded = useCardDisclosure(() => parentCallId.value ? `call:${parentCallId.value}:details` : undefined, false);
const resultOpen = useCardDisclosure(() => parentCallId.value ? `call:${parentCallId.value}:subagent:result` : undefined, false);
const disclosure = useCardDisclosureState();
const frames = computed(() => props.bgTask?.frames ?? props.frames);
const steps = computed(() => subagentSteps(frames.value));
const rows = computed(() => subagentActivityRows(steps.value));
const excerpt = computed(() => resultTextPreview(summary.value.resultText ?? ""));

const now = ref(Date.now());
let timer: ReturnType<typeof setInterval> | undefined;
let mounted = false;
function updateTimer() {
  if (timer !== undefined) clearInterval(timer);
  timer = undefined; now.value = Date.now();
  if (mounted && summary.value.live && summary.value.startedAt !== undefined) timer = setInterval(() => { now.value = Date.now(); }, 1000);
}
watch([() => summary.value.live, () => summary.value.startedAt], updateTimer);
onMounted(() => { mounted = true; updateTimer(); });
onBeforeUnmount(() => { mounted = false; if (timer !== undefined) clearInterval(timer); });
const elapsed = computed(() => {
  const value = summary.value;
  if (value.durationMs !== undefined) return formatDuration(value.durationMs);
  return value.live && value.startedAt !== undefined ? formatDuration(Math.max(0, now.value + (props.serverOffset ?? 0) - value.startedAt)) : undefined;
});

function openSheet() {
  const value = summary.value;
  if (!value.childSession) return;
  emit("open-sheet", { childSession: value.childSession, label: value.task.split("\n")[0].slice(0, 60), ...(value.agentId ? { agentId: value.agentId } : {}) });
}

type ExploreRow = Extract<SubagentRow, { kind: "explore" }>;
// The parent invocation already identifies this run and exists before result
// metadata. Switching to a newly-arrived run_id on completion would lose the
// choices made while the child was still running.
const runScope = computed(() => parentCallId.value);
const nestedKey = (kind: "tool" | "group", identity?: string) => parentCallId.value && runScope.value && identity ? `call:${parentCallId.value}:subagent:${runScope.value}:${kind}:${identity}` : undefined;
const storedOpen = (key: string | undefined) => key && disclosure?.available() ? disclosure.choice(key) : undefined;
const localRows = ref<Set<SubagentStep>>(new Set());
const localGroups = ref<Set<ExploreRow>>(new Set());
const automaticGroups = ref<ReadonlyMap<string | ExploreRow, boolean>>(new Map());
const rowDisclosureId = (step: SubagentStep) => nestedKey("tool", step.identity);
const isOpen = (step: SubagentStep) => storedOpen(rowDisclosureId(step)) ?? localRows.value.has(step);
function toggleRow(step: SubagentStep) {
  const value = !isOpen(step);
  disclosure?.set(rowDisclosureId(step), value);
  const next = new Set(localRows.value); if (value) next.add(step); else next.delete(step); localRows.value = next;
}
const groupKey = (row: ExploreRow) => nestedKey("group", row.steps[0]?.identity);
const groupForced = (row: ExploreRow) => row.steps.some(step => !step.done && summary.value.live || step.bad);
const isGroupOpen = (row: ExploreRow) => row.steps.length === 1 || groupForced(row) ||
  (storedOpen(groupKey(row)) ?? (row.steps.some(isOpen) || localGroups.value.has(row) || automaticGroups.value.get(groupKey(row) ?? row) === true));
function toggleGroup(row: ExploreRow) {
  if (groupForced(row)) return;
  const value = !isGroupOpen(row); disclosure?.set(groupKey(row), value);
  const next = new Set(localGroups.value); if (value) next.add(row); else next.delete(row); localGroups.value = next;
}
watch(rows, values => {
  const next = new Map(automaticGroups.value);
  for (const row of values) if (row.kind === "explore") {
    const key = groupKey(row) ?? row;
    if (!next.has(key)) next.set(key, summary.value.live);
  }
  automaticGroups.value = next;
}, { immediate: true });

// A stable identity is only supplied when the child turn is known. Sparse
// legacy frames deliberately use fresh local keys rather than sharing a bare
// call id with a different turn that happened to reuse it.
const localKeys = new WeakMap<object, number>();
let nextLocalKey = 0;
function rowKey(row: SubagentRow): string | number {
  const step = row.kind === "explore" ? row.steps[0] : "step" in row ? row.step : undefined;
  if (step?.identity) return `${runScope.value ?? ""}:${row.kind}:${step.identity}`;
  let key = localKeys.get(row); if (key === undefined) { key = ++nextLocalKey; localKeys.set(row, key); } return key;
}
const listEl = ref<HTMLElement | null>(null);
watch(() => steps.value.length, async () => {
  const element = listEl.value;
  if (!expanded.value || !element || element.scrollHeight - element.scrollTop - element.clientHeight >= 24) return;
  await nextTick(); element.scrollTop = element.scrollHeight;
});
</script>

<template>
  <section class="subagent-summary" :class="summary.state" :data-subagent-state="summary.state">
    <div class="summary-head">
      <button type="button" class="summary-title" :aria-expanded="expanded" :title="summary.task" @click="expanded = !expanded">
        <WorkLogGlyph class="agent-glyph" kind="subagent" />
        <span class="task">{{ summary.task }}</span>
        <Chevron class="caret" :open="expanded" />
      </button>
      <span class="summary-state" role="status">{{ t(`subagentResult.states.${summary.state}`) }}</span>
      <span v-if="elapsed" class="elapsed">{{ elapsed }}</span>
    </div>

    <div v-if="summary.live || summary.state === 'awaitingApproval'" class="current-action">
      <span v-if="summary.activeCount !== undefined && summary.activeCount > 1">{{ t('subagentResult.parallelActions', { n: summary.activeCount }) }}<template v-if="summary.action"> · </template></span>
      <span>{{ summary.state === 'awaitingApproval' ? t('subagentResult.approvalInParent') : summary.action || (summary.recent ? t(`subagentResult.recent.${summary.recent.kind}`) : t('subagentResult.waitingUpdate')) }}</span>
    </div>
    <div v-if="summary.recent" class="recent-stream" :aria-label="t(`subagentResult.recent.${summary.recent.kind}`)">{{ summary.recent.text }}</div>
    <pre v-if="summary.error" class="summary-error">{{ summary.error }}</pre>
    <div v-else-if="summary.state === 'failed'" class="summary-error">{{ t('subagentResult.noFailureReason') }}</div>
    <div v-if="summary.state === 'syncing' || summary.state === 'unavailable'" class="result-note">{{ t(`subagentResult.notes.${summary.state}`) }}</div>

    <template v-if="summary.resultText && !summary.error">
      <div v-if="!resultOpen" class="result-excerpt">{{ excerpt.text }}</div>
      <button type="button" class="text-action" :aria-expanded="resultOpen" @click="resultOpen = !resultOpen">{{ t(resultOpen ? 'subagentResult.hideResult' : 'subagentResult.fullResult') }}</button>
      <CodeBlock v-if="resultOpen" :code="summary.resultText" />
    </template>

    <div class="summary-foot">
      <button type="button" class="text-action" :aria-expanded="expanded" @click="expanded = !expanded">{{ t(expanded ? 'subagentResult.hideActivity' : 'subagentResult.recentActivity') }}</button>
      <span v-if="summary.recordedCalls > 0">{{ t('subagentResult.recordedCalls', { n: summary.recordedCalls }) }}</span>
      <span v-if="summary.recordedFailures > 0" class="has-failures">{{ t('subagentResult.recordedFailures', { n: summary.recordedFailures }) }}</span>
      <button v-if="summary.childSession" type="button" class="text-action open-transcript" @click="openSheet">{{ t('subagentResult.openSheet') }} <el-icon><TopRight /></el-icon></button>
    </div>

    <div v-if="expanded" class="summary-details">
      <div class="secondary-meta">
        <span v-if="summary.mode">{{ t('subagentResult.mode') }}: {{ summary.mode }}</span>
        <span v-if="summary.model">{{ t('subagentResult.model') }}: {{ summary.model }}</span>
        <span v-if="summary.turns !== undefined">{{ t('subagentResult.turns', { n: summary.turns }) }}</span>
        <span v-if="summary.inputTokens !== undefined || summary.outputTokens !== undefined">{{ t('subagentResult.tokens', { input: summary.inputTokens ?? '—', output: summary.outputTokens ?? '—' }) }}</span>
        <span v-if="summary.retained">{{ t('subagentResult.retained') }}</span>
      </div>
      <pre v-if="summary.task.includes('\n')" class="task-details">{{ summary.task }}</pre>
      <div ref="listEl" class="activity-list">
        <template v-for="row in rows" :key="rowKey(row)">
          <div v-if="row.kind === 'turn'" class="turn-label">{{ row.label }}</div>
          <div v-else-if="row.kind === 'explore'" class="activity-group">
            <button v-show="row.steps.length > 1" type="button" class="group-head" :aria-expanded="isGroupOpen(row)" :disabled="groupForced(row)" @click="toggleGroup(row)"><Chevron :open="isGroupOpen(row)" /> {{ t('subagentResult.viewMaterials', { n: row.steps.length }) }}</button>
            <div v-show="isGroupOpen(row)" class="group-body">
              <SubagentStepRow v-for="step in row.steps" :key="step.identity ?? rowKey({ kind: 'tool', step })" :step="step" :open="isOpen(step)" :disclosure-id="rowDisclosureId(step)" @toggle="toggleRow" />
            </div>
          </div>
          <SubagentStepRow v-else-if="row.kind === 'tool'" :step="row.step" :open="isOpen(row.step)" :disclosure-id="rowDisclosureId(row.step)" @toggle="toggleRow" />
          <div v-else class="end-label" :class="{ bad: row.step.bad }">{{ row.step.label }}<span v-if="row.step.detail"> · {{ row.step.detail }}</span></div>
        </template>
        <div v-if="!rows.length" class="result-note">{{ t('subagentResult.noEvents') }}</div>
      </div>
    </div>
  </section>
</template>

<style scoped lang="scss">
.subagent-summary { border: 1px solid var(--el-border-color-lighter); border-radius: 10px; padding: 10px 12px; background: var(--el-bg-color); margin: 4px 0; min-width: 0; }
.summary-head { display: flex; align-items: flex-start; gap: 8px; flex-wrap: wrap; }
.summary-title { display: flex; align-items: flex-start; flex: 1; min-width: 150px; gap: 8px; padding: 0; border: 0; background: transparent; color: var(--el-text-color-primary); font: inherit; text-align: left; cursor: pointer; }
.agent-glyph { width: 16px; height: 16px; flex: 0 0 16px; margin-top: 2px; color: var(--el-text-color-secondary); }
.task { font-size: 12px; font-weight: 600; line-height: 1.6; white-space: pre-wrap; overflow-wrap: anywhere; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden; }
.caret { flex: 0 0 auto; margin-top: 4px; }
.summary-state { font-size: 10px; padding: 2px 7px; border-radius: 999px; color: var(--el-text-color-secondary); background: var(--el-fill-color-light); white-space: nowrap; }
.running .summary-state { color: var(--el-color-primary); background: var(--el-color-primary-light-9); }
.running .summary-state::before { content: ''; display: inline-block; width: 5px; height: 5px; margin-right: 5px; border-radius: 50%; background: currentColor; animation: pulse 1.2s ease-in-out infinite; }
.awaitingApproval .summary-state { color: var(--el-color-warning-dark-2); background: var(--el-color-warning-light-9); }
.failed .summary-state { color: var(--el-color-danger); background: var(--el-color-danger-light-9); }
.elapsed { font-size: 11px; color: var(--el-text-color-secondary); font-variant-numeric: tabular-nums; white-space: nowrap; padding-top: 2px; }
.current-action { margin-top: 7px; font-size: 11px; color: var(--el-text-color-secondary); overflow-wrap: anywhere; }
.recent-stream { margin-top: 5px; max-height: 3.4em; overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; font-size: 12px; line-height: 1.7; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--el-text-color-secondary); }
.summary-error { margin: 8px 0; padding: 8px 10px; white-space: pre-wrap; overflow-wrap: anywhere; font: 12px/1.6 var(--pg-mono); color: var(--el-color-danger); background: var(--el-color-danger-light-9); border-radius: 6px; max-height: 260px; overflow: auto; }
.result-note, .result-excerpt { margin-top: 7px; font-size: 12px; line-height: 1.6; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--el-text-color-secondary); }
.result-excerpt { max-height: 4.8em; overflow: hidden; }
.summary-foot { display: flex; align-items: center; gap: 8px 12px; flex-wrap: wrap; margin-top: 7px; font-size: 11px; color: var(--el-text-color-placeholder); }
.text-action { border: 0; background: transparent; color: var(--el-color-primary); padding: 2px 0; font: inherit; font-size: 11px; cursor: pointer; }
button:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
.open-transcript { margin-left: auto; display: inline-flex; align-items: center; gap: 4px; }
.has-failures, .bad { color: var(--el-color-danger); }
.summary-details { margin-top: 8px; padding-top: 8px; border-top: 1px solid var(--el-border-color-lighter); }
.secondary-meta { display: flex; flex-wrap: wrap; gap: 5px 12px; color: var(--el-text-color-secondary); font-size: 11px; }
.task-details { font: inherit; font-size: 12px; line-height: 1.6; white-space: pre-wrap; overflow-wrap: anywhere; }
.activity-list { max-height: min(45vh, 420px); overflow: auto; overscroll-behavior: contain; margin-top: 8px; }
.turn-label, .end-label { padding: 4px 0; font-size: 11px; color: var(--el-text-color-secondary); }
.group-head { display: flex; align-items: center; gap: 6px; padding: 4px 0; border: 0; background: transparent; font: inherit; font-size: 11px; color: var(--el-text-color-secondary); cursor: pointer; }
.group-head:disabled { cursor: default; opacity: 1; }
.group-body { padding-left: 8px; border-left: 1px solid var(--el-border-color-lighter); }
@keyframes pulse { 50% { opacity: .35; } }
@media (prefers-reduced-motion: reduce) { .running .summary-state::before { animation: none; } }
@media (max-width: 480px) { .subagent-summary { padding: 8px; } .summary-title { flex-basis: calc(100% - 80px); } .open-transcript { margin-left: 0; } }
</style>
