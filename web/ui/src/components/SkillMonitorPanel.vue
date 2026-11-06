<script setup lang="ts">
// Skill monitor panel ("Skill 监控"), embedded by SettingsDialog as its own
// sheet (main agent wires the sheet). It observes skill operations recorded
// by the skillobserve store — discovery, instruction reads and declared-tool
// executions as three separate stages:
//   - stats cards share the exact filter set with the list (one serializer),
//     and cover the whole filtered range, never just the current page,
//   - outcome/latency cards describe declared-tool executions only and never
//     claim a whole skill succeeded,
//   - keyset pagination via an opaque cursor; page size is never a total,
//   - auto-refresh replaces the first page against the committed filter
//     snapshot; historical paging pauses it,
//   - a generation gate drops slow responses that arrive after the filters
//     changed, so stale data can never overwrite a newer view.
// The UI shows metadata only; skill contents, arguments, results and raw
// error text never leave the server, and unknown/interrupted statuses are
// never presented as success.
import { onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Refresh } from "@element-plus/icons-vue";
import { api } from "@/api/client";
import {
  asSkillMonitorBridge,
  createDefaultSkillFilter,
  createGenerationGate,
  createSkillBridgeLoaders,
  formatDurationMs,
  formatErrorRate,
  hasKnownDuration,
  mergeEvents,
  normalizeSkillCaller,
  normalizeSkillEntrypoint,
  normalizeSkillStage,
  normalizeSkillStatus,
  skillMechanismLabelKey,
  skillSourceLabelKey,
  snapshotSkillFilter,
} from "@/components/skillMonitor";
import ObserveStatusCard from "@/components/ObserveStatusCard.vue";
import StudioEntrySkeleton from "@/components/studio/StudioEntrySkeleton.vue";
import type {
  SkillEvent,
  SkillEventsPage,
  SkillEventStats,
  SkillEventStatsSkillRow,
  SkillEventStage,
  SkillEventStatus,
  SkillMonitorFilter,
  SkillMonitorLoaders,
  SkillMonitorWindow,
} from "@/components/skillMonitor";

const props = defineProps<{
  active: boolean;
  // Injectable data sources (tests, embedding). When absent, the panel talks
  // to api.skillEvents/skillEventStats/skillEvent — wired into api/client.ts
  // by the main agent; until then the panel renders the "not available"
  // notice.
  loadEvents?: (filter: SkillMonitorFilter, cursor?: string) => Promise<SkillEventsPage>;
  loadStats?: (filter: SkillMonitorFilter) => Promise<SkillEventStats>;
  loadDetail?: (id: string) => Promise<SkillEvent>;
}>();

// Optional cross-page jump: the main agent may listen and switch to the MCP
// call monitor filtered by this trace. The in-panel trace filter below works
// regardless.
const emit = defineEmits<{
  (e: "showMCPTrace", traceId: string): void;
}>();

const { t, d } = useI18n();

const PAGE_SIZE = 100;
const AUTO_REFRESH_MS = 8000;

const filter = ref<SkillMonitorFilter>(createDefaultSkillFilter());
const events = ref<SkillEvent[]>([]);
const stats = ref<SkillEventStats | null>(null);
const nextCursor = ref("");
const storage = ref<"sqlite" | "memory">("sqlite");
const dropped = ref(0);
const writeErrors = ref(0);
const loading = ref(false);
const loadingMore = ref(false);
const errorKey = ref("");
const selected = ref<SkillEvent | null>(null);
const detailLoading = ref(false);
const detailError = ref(false);
const auto = ref(true);
const pagedHistory = ref(false);
let committedFilter = snapshotSkillFilter(filter.value);
let timer: number | undefined;

const gate = createGenerationGate();
const detailGate = createGenerationGate();

const STAGE_OPTIONS: SkillEventStage[] = ["discovery", "load", "execute"];
const STATUS_OPTIONS: SkillEventStatus[] = [
  "running",
  "ok",
  "error",
  "cancelled",
  "timeout",
  "interrupted",
];
const WINDOW_OPTIONS: SkillMonitorWindow[] = ["1h", "24h", "7d"];

function loaders(): SkillMonitorLoaders {
  if (props.loadEvents && props.loadStats && props.loadDetail) {
    return { loadEvents: props.loadEvents, loadStats: props.loadStats, loadDetail: props.loadDetail };
  }
  return createSkillBridgeLoaders(asSkillMonitorBridge(api), PAGE_SIZE);
}

function applyPage(page: SkillEventsPage, mode: "replace" | "append") {
  if (mode === "replace") events.value = page.events;
  else events.value = mergeEvents(events.value, page.events);
  nextCursor.value = page.next_cursor;
  if (selected.value) selected.value = page.events.find((e) => e.id === selected.value?.id) ?? selected.value;
  storage.value = page.storage;
  dropped.value = page.dropped;
  writeErrors.value = page.write_errors;
}

function noteError(err: unknown) {
  errorKey.value =
    err instanceof Error && err.message.includes("not wired")
      ? "skillMonitor.notAvailable"
      : "skillMonitor.loadError";
}

// Full reload from the first page: manual refresh and every filter change.
async function reload(snapshot: SkillMonitorFilter) {
  const g = gate.next();
  committedFilter = snapshot;
  loading.value = true;
  loadingMore.value = false;
  nextCursor.value = "";
  errorKey.value = "";
  try {
    const src = loaders();
    const [page, s] = await Promise.all([
      src.loadEvents(snapshot),
      src.loadStats(snapshot),
    ]);
    if (!gate.isLatest(g)) return;
    applyPage(page, "replace");
    stats.value = s;
  } catch (err) {
    if (gate.isLatest(g)) noteError(err);
  } finally {
    if (gate.isLatest(g)) loading.value = false;
  }
}

function refresh() {
  const snapshot = snapshotSkillFilter(filter.value);
  pagedHistory.value = false;
  closeDetail();
  // Keep a resolved result (including an empty result) in place when the
  // query is unchanged. New filters get their own loading placeholders.
  if (JSON.stringify(snapshot) !== JSON.stringify(committedFilter)) {
    events.value = [];
    stats.value = null;
  }
  return reload(snapshot);
}

// Auto-refresh reuses the committed filter snapshot — uncommitted input box
// text never leaks into polling — and historical pages keep their fixed
// query window with polling paused.
async function tick() {
  if (!props.active || loading.value || loadingMore.value) return;
  if (pagedHistory.value) return;
  await reload(snapshotSkillFilter(committedFilter));
}

async function loadMore() {
  if (loadingMore.value || loading.value || !nextCursor.value) return;
  const g = gate.current();
  const cursor = nextCursor.value;
  auto.value = false;
  pagedHistory.value = true;
  loadingMore.value = true;
  try {
    const page = await loaders().loadEvents(committedFilter, cursor);
    if (!gate.isLatest(g)) return;
    applyPage(page, "append");
  } catch (err) {
    if (gate.isLatest(g)) noteError(err);
  } finally {
    if (gate.isLatest(g)) loadingMore.value = false;
  }
}

async function openDetail(event: SkillEvent) {
  const g = gate.current();
  const dg = detailGate.next();
  selected.value = event;
  detailLoading.value = true;
  detailError.value = false;
  try {
    const full = await loaders().loadDetail(event.id);
    if (!gate.isLatest(g) || !detailGate.isLatest(dg) || selected.value?.id !== event.id) return;
    selected.value = full;
  } catch {
    if (gate.isLatest(g) && detailGate.isLatest(dg) && selected.value?.id === event.id) detailError.value = true;
  } finally {
    if (detailGate.isLatest(dg) && selected.value?.id === event.id) detailLoading.value = false;
  }
}

function closeDetail() {
  detailGate.next();
  selected.value = null;
  detailError.value = false;
  detailLoading.value = false;
}

// Correlation quick-filters stay inside this panel — they re-run the same
// filtered query rather than navigating anywhere.
function filterSameTrace() {
  if (!selected.value?.trace_id) return;
  filter.value = { ...createDefaultSkillFilter(), window: filter.value.window, traceId: selected.value.trace_id };
  void refresh();
}

function filterSameSession() {
  if (!selected.value?.session_id) return;
  filter.value = { ...createDefaultSkillFilter(), window: filter.value.window, sessionId: selected.value.session_id };
  void refresh();
}

function clearCorrelationFilters() {
  filter.value = { ...filter.value, traceId: "", sessionId: "", skillId: "" };
  void refresh();
}

function filterSkillGroup(row: SkillEventStatsSkillRow) {
  filter.value = { ...filter.value, skillName: row.skill_name, skillId: row.skill_id };
  void refresh();
}

function correlationActive(): boolean {
  const f = filter.value;
  return Boolean(f.traceId || f.sessionId || f.skillId);
}

function jumpToMcpTrace() {
  if (!selected.value?.trace_id) return;
  emit("showMCPTrace", selected.value.trace_id);
}

function stageLabel(stage: string): string {
  const s = normalizeSkillStage(stage);
  const key =
    s === "unknown" ? "skillMonitor.stageUnknown" : `skillMonitor.stage${s.charAt(0).toUpperCase()}${s.slice(1)}`;
  return t(key);
}

function stageClass(stage: string): string {
  return normalizeSkillStage(stage);
}

function statusLabel(status: string): string {
  const s = normalizeSkillStatus(status);
  const key =
    s === "unknown" ? "skillMonitor.statusUnknown" : `skillMonitor.status${s.charAt(0).toUpperCase()}${s.slice(1)}`;
  return t(key);
}

function statusClass(status: string): string {
  return normalizeSkillStatus(status);
}

function callerLabel(caller: string): string {
  const c = normalizeSkillCaller(caller);
  if (c === "internal") return t("skillMonitor.callerInternal");
  if (c === "external") return t("skillMonitor.callerExternal");
  return t("skillMonitor.callerUnknown");
}

function callerShort(caller: string): string {
  const c = normalizeSkillCaller(caller);
  if (c === "internal") return t("skillMonitor.callerShortInternal");
  if (c === "external") return t("skillMonitor.callerShortExternal");
  return t("skillMonitor.callerShortUnknown");
}

function entrypointLabel(entrypoint: string): string {
  const e = normalizeSkillEntrypoint(entrypoint);
  if (e === "pogo") return t("skillMonitor.entrypointPogo");
  if (e === "mcp") return t("skillMonitor.entrypointMcp");
  if (e === "cli") return t("skillMonitor.entrypointCli");
  return t("skillMonitor.entrypointUnknown");
}

function mechanismLabel(mechanism: string): string {
  return t(skillMechanismLabelKey(mechanism));
}

function skillSourceLabel(source: string): string {
  return t(skillSourceLabelKey(source));
}

// Real zero durations stay visible ("0 ms"); only running / interrupted /
// unknown outcomes have an unknown duration and show the dash.
function durationText(event: SkillEvent): string {
  if (!hasKnownDuration(event)) return t("skillMonitor.dash");
  return formatDurationMs(event.duration_ms) ?? t("skillMonitor.dash");
}

function statText(v: string | number | null | undefined): string {
  if (v === null || v === undefined) return t("skillMonitor.dash");
  return String(v);
}

function fmtTime(ms: number): string {
  if (!ms) return t("skillMonitor.dash");
  return d(new Date(ms), "short");
}

function field(v: string | number | null | undefined): string {
  if (v === null || v === undefined || v === "") return t("skillMonitor.dash");
  return String(v);
}

function startTimer() {
  stopTimer();
  if (!auto.value) return;
  timer = window.setInterval(() => void tick(), AUTO_REFRESH_MS);
}

function stopTimer() {
  if (timer !== undefined) {
    window.clearInterval(timer);
    timer = undefined;
  }
}

watch(auto, () => {
  if (props.active) {
    if (auto.value && pagedHistory.value) {
      pagedHistory.value = false;
      void reload(snapshotSkillFilter(committedFilter));
    }
    startTimer();
  }
});

watch(
  () => props.active,
  (on) => {
    if (on) {
      void refresh();
      startTimer();
    } else {
      stopTimer();
      gate.next();
      closeDetail();
      loading.value = false;
      loadingMore.value = false;
    }
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  stopTimer();
  gate.next(); // invalidate any in-flight responses
});
</script>

<template>
  <div class="sm-panel">
    <p class="sm-subtitle">{{ t("skillMonitor.subtitle") }}</p>

    <ObserveStatusCard :active="active" />

    <div class="sm-stats" :aria-label="t('uiCoverage.statistics')">
      <div class="sm-stat"><span class="sm-stat-label">{{ t("skillMonitor.statTotal") }}</span><span class="sm-stat-value">{{ statText(stats?.total) }}</span></div>
      <div class="sm-stat"><span class="sm-stat-label">{{ t("skillMonitor.statDiscovery") }}</span><span class="sm-stat-value">{{ statText(stats?.discoveries) }}</span></div>
      <div class="sm-stat"><span class="sm-stat-label">{{ t("skillMonitor.statLoad") }}</span><span class="sm-stat-value">{{ statText(stats?.loads) }}</span></div>
      <div class="sm-stat"><span class="sm-stat-label">{{ t("skillMonitor.statExecute") }}</span><span class="sm-stat-value">{{ statText(stats?.executions) }}</span></div>
      <div class="sm-stat"><span class="sm-stat-label">{{ t("skillMonitor.statExecOk") }}</span><span class="sm-stat-value ok">{{ statText(stats?.execution_ok) }}</span></div>
      <div class="sm-stat"><span class="sm-stat-label">{{ t("skillMonitor.statExecErrorRate") }}</span><span class="sm-stat-value" :class="{ error: ((stats?.execution_errors ?? 0) + (stats?.execution_timeouts ?? 0)) > 0 }">{{ statText(formatErrorRate(stats?.error_rate)) }}</span></div>
      <div class="sm-stat"><span class="sm-stat-label">{{ t("skillMonitor.statExecP50") }}</span><span class="sm-stat-value">{{ statText(formatDurationMs(stats?.p50_ms)) }}</span></div>
      <div class="sm-stat"><span class="sm-stat-label">{{ t("skillMonitor.statExecP95") }}</span><span class="sm-stat-value">{{ statText(formatDurationMs(stats?.p95_ms)) }}</span></div>
      <div class="sm-stat"><span class="sm-stat-label">{{ t("skillMonitor.statCallerInternal") }}</span><span class="sm-stat-value">{{ statText(stats?.by_caller?.internal) }}</span></div>
      <div class="sm-stat"><span class="sm-stat-label">{{ t("skillMonitor.statCallerExternal") }}</span><span class="sm-stat-value">{{ statText(stats?.by_caller?.external) }}</span></div>
      <div class="sm-stat"><span class="sm-stat-label">{{ t("skillMonitor.statCallerUnknown") }}</span><span class="sm-stat-value">{{ statText(stats?.by_caller?.unknown) }}</span></div>
    </div>

    <div v-if="storage === 'memory'" class="sm-notice" role="status">{{ t("skillMonitor.storageMemory") }}</div>
    <div v-if="dropped > 0" class="sm-notice warn" role="status">{{ t("skillMonitor.droppedHint", { n: dropped }) }}</div>
    <div v-if="writeErrors > 0" class="sm-notice warn" role="status">{{ t("skillMonitor.writeErrorsHint", { n: writeErrors }) }}</div>

    <p v-if="pagedHistory" class="sm-subtitle">{{ t("skillMonitor.historyPaused") }}</p>
    <div class="sm-toolbar">
      <el-select
        v-model="filter.stage"
        :empty-values="[null, undefined]"
        size="small"
        style="width: 130px"
        :aria-label="t('skillMonitor.colStage')"
        @change="refresh"
      >
        <el-option :label="t('skillMonitor.stageAll')" value="" />
        <el-option v-for="s in STAGE_OPTIONS" :key="s" :label="stageLabel(s)" :value="s" />
      </el-select>
      <el-select
        v-model="filter.caller"
        :empty-values="[null, undefined]"
        size="small"
        style="width: 170px"
        :aria-label="t('skillMonitor.colCaller')"
        @change="refresh"
      >
        <el-option :label="t('skillMonitor.callerAll')" value="" />
        <el-option :label="t('skillMonitor.callerInternal')" value="internal" />
        <el-option :label="t('skillMonitor.callerExternal')" value="external" />
        <el-option :label="t('skillMonitor.callerUnknown')" value="unknown" />
      </el-select>
      <el-select v-model="filter.entrypoint" :empty-values="[null, undefined]" size="small" style="width: 150px" :aria-label="t('skillMonitor.fEntrypoint')" @change="refresh">
        <el-option :label="t('skillMonitor.entrypointAll')" value="" />
        <el-option :label="t('skillMonitor.entrypointPogo')" value="pogo" />
        <el-option :label="t('skillMonitor.entrypointMcp')" value="mcp" />
        <el-option :label="t('skillMonitor.entrypointCli')" value="cli" />
        <el-option :label="t('skillMonitor.entrypointUnknown')" value="unknown" />
      </el-select>
      <el-select
        v-model="filter.status"
        :empty-values="[null, undefined]"
        size="small"
        style="width: 120px"
        :aria-label="t('skillMonitor.colStatus')"
        @change="refresh"
      >
        <el-option :label="t('skillMonitor.statusAll')" value="" />
        <el-option v-for="s in STATUS_OPTIONS" :key="s" :label="statusLabel(s)" :value="s" />
      </el-select>
      <el-select
        v-model="filter.window"
        :empty-values="[null, undefined]"
        size="small"
        style="width: 130px"
        :aria-label="t('skillMonitor.colStarted')"
        @change="refresh"
      >
        <el-option v-for="w in WINDOW_OPTIONS" :key="w" :label="t(`skillMonitor.window${w}`)" :value="w" />
      </el-select>
      <el-input
        v-model="filter.skillName"
        size="small"
        style="width: 140px"
        :placeholder="t('skillMonitor.skillPlaceholder')"
        :aria-label="t('skillMonitor.colSkill')"
        clearable
        @input="filter.skillId = ''"
        @clear="refresh"
        @keyup.enter="refresh"
      />
      <el-input
        v-model="filter.tool"
        size="small"
        style="width: 120px"
        :placeholder="t('skillMonitor.toolPlaceholder')"
        :aria-label="t('skillMonitor.colTool')"
        clearable
        @clear="refresh"
        @keyup.enter="refresh"
      />
      <el-input v-model="filter.q" size="small" style="width: 185px" clearable :placeholder="t('skillMonitor.searchPlaceholder')" :aria-label="t('skillMonitor.searchPlaceholder')" @keyup.enter="refresh" @clear="refresh" />
      <el-button size="small" :icon="Refresh" :loading="loading && !!stats" :disabled="loading" @click="refresh">
        {{ t("skillMonitor.refresh") }}
      </el-button>
      <span class="sm-spacer" />
      <el-checkbox v-model="auto" size="small" class="sm-auto">{{ t("skillMonitor.autoRefresh") }}</el-checkbox>
    </div>

    <div v-if="correlationActive()" class="sm-corr">
      <span>{{ t("skillMonitor.corrActive") }}</span>
      <code v-if="filter.skillId">{{ filter.skillId.slice(0, 12) }}</code>
      <el-button size="small" text @click="clearCorrelationFilters">
        {{ t("skillMonitor.clearCorrFilters") }}
      </el-button>
    </div>

    <div v-if="errorKey" class="sm-error" role="alert">{{ t(errorKey) }}</div>

    <details v-if="stats?.by_skill?.length" class="sm-groups">
      <summary>{{ t("skillMonitor.skillGroups") }}</summary>
      <div class="sm-group-scroll">
        <table>
          <thead><tr>
            <th>{{ t("skillMonitor.colSkill") }}</th>
            <th>{{ t("skillMonitor.fSkillSource") }}</th>
            <th>{{ t("skillMonitor.statDiscovery") }}</th>
            <th>{{ t("skillMonitor.statLoad") }}</th>
            <th>{{ t("skillMonitor.statExecute") }}</th>
            <th>{{ t("skillMonitor.groupErrors") }}</th>
            <th>{{ t("skillMonitor.groupP95") }}</th>
          </tr></thead>
          <tbody><tr v-for="row in stats.by_skill" :key="row.skill_id || row.skill_name">
            <td><el-button size="small" text :title="row.skill_id" @click="filterSkillGroup(row)">{{ row.skill_name }}</el-button></td>
            <td>{{ skillSourceLabel(row.skill_source) }}</td>
            <td>{{ row.discoveries }}</td>
            <td>{{ row.loads }}</td>
            <td>{{ row.executions }}</td>
            <td>{{ row.execution_errors }}</td>
            <td>{{ statText(formatDurationMs(row.p95_ms)) }}</td>
          </tr></tbody>
        </table>
      </div>
    </details>

    <div class="sm-body">
      <div class="sm-list" role="list" :aria-busy="loading">
        <div v-if="loading && !stats" role="status" :aria-label="t('skillMonitor.loading')"><StudioEntrySkeleton area="sessions" /></div>
        <div v-else-if="!events.length && !errorKey" class="sm-empty">
          <el-empty :description="t('skillMonitor.empty')" :image-size="56" />
          <div class="sm-empty-hint">{{ t("skillMonitor.emptyHint") }}</div>
        </div>
        <button
          v-for="event in events"
          :key="event.id"
          type="button"
          role="listitem"
          class="sm-row"
          :class="[statusClass(event.status), { selected: selected?.id === event.id }]"
          :aria-label="`${fmtTime(event.started_at)} ${stageLabel(event.stage)} ${event.skill_name} ${event.tool ?? ''} ${statusLabel(event.status)}`"
          @click="openDetail(event)"
        >
          <span class="sm-time">{{ fmtTime(event.started_at) }}</span>
          <span class="sm-stage" :class="stageClass(event.stage)">{{ stageLabel(event.stage) }}</span>
          <span class="sm-skill">{{ field(event.skill_name) }}</span>
          <span class="sm-tool">{{ field(event.tool || event.wire_tool) }}</span>
          <span class="sm-status" :class="statusClass(event.status)">
            {{ statusLabel(event.status) }}
          </span>
          <span v-if="normalizeSkillStatus(event.status) === 'running'" class="sm-running-badge">
            {{ t("skillMonitor.runningBadge") }}
          </span>
          <span class="sm-spacer" />
          <span class="sm-duration">{{ durationText(event) }}</span>
          <span class="sm-caller">{{ callerShort(event.caller) }}</span>
        </button>
        <div v-if="events.length" class="sm-list-foot">
          <span v-if="stats" class="sm-total">{{ t("skillMonitor.listTotal", { total: stats.total }) }}</span>
          <el-button
            v-if="nextCursor"
            size="small"
            :loading="loadingMore"
            :disabled="loadingMore"
            @click="loadMore"
          >
            {{ t("skillMonitor.loadMore") }}
          </el-button>
        </div>
      </div>

      <div v-if="selected" class="sm-detail" :aria-busy="detailLoading">
        <div class="sm-detail-head">
          <span class="sm-detail-title">{{ t("skillMonitor.detailTitle") }}</span>
          <el-button size="small" text :aria-label="t('skillMonitor.closeDetail')" @click="closeDetail">×</el-button>
        </div>
        <div v-if="detailLoading" class="sm-detail-loading" role="status" :aria-label="t('skillMonitor.loading')"><StudioEntrySkeleton area="form" /></div>
        <template v-else>
          <div v-if="detailError" class="sm-error" role="alert">{{ t("skillMonitor.detailError") }}</div>
          <dl class="sm-fields">
            <div class="sm-field"><dt>{{ t("skillMonitor.fId") }}</dt><dd class="mono">{{ selected.id }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fStatus") }}</dt><dd><span class="sm-status" :class="statusClass(selected.status)">{{ statusLabel(selected.status) }}</span></dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fStage") }}</dt><dd><span class="sm-stage" :class="stageClass(selected.stage)">{{ stageLabel(selected.stage) }}</span></dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fMechanism") }}</dt><dd>{{ mechanismLabel(selected.mechanism) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fSkill") }}</dt><dd>{{ field(selected.skill_name) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fSkillSource") }}</dt><dd>{{ skillSourceLabel(selected.skill_source) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fTool") }}</dt><dd>{{ field(selected.tool) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fWireTool") }}</dt><dd>{{ field(selected.wire_tool) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fCaller") }}</dt><dd>{{ callerLabel(selected.caller) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fEntrypoint") }}</dt><dd>{{ entrypointLabel(selected.entrypoint) }}</dd></div>
            <div v-if="selected.client_id" class="sm-field"><dt>{{ t("skillMonitor.fClientId") }}</dt><dd class="mono">{{ selected.client_id }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fStarted") }}</dt><dd>{{ fmtTime(selected.started_at) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fFinished") }}</dt><dd>{{ selected.finished_at ? fmtTime(selected.finished_at) : t("skillMonitor.dash") }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fDuration") }}</dt><dd>{{ durationText(selected) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fErrorKind") }}</dt><dd>{{ field(selected.error_kind) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fTrace") }}</dt><dd class="mono">{{ field(selected.trace_id) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fParent") }}</dt><dd class="mono">{{ field(selected.parent_id) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fSpanId") }}</dt><dd class="mono">{{ field(selected.span_id) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fParentSpanId") }}</dt><dd class="mono">{{ field(selected.parent_span_id) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fTraceFlags") }}</dt><dd class="mono">{{ field(selected.trace_flags) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fSkillId") }}</dt><dd class="mono">{{ field(selected.skill_id) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fSession") }}</dt><dd class="mono">{{ field(selected.session_id) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fRun") }}</dt><dd class="mono">{{ field(selected.run_id) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fTask") }}</dt><dd class="mono">{{ field(selected.task_id) }}</dd></div>
            <div class="sm-field"><dt>{{ t("skillMonitor.fToolCall") }}</dt><dd class="mono">{{ field(selected.tool_call_id) }}</dd></div>
          </dl>
          <div class="sm-detail-actions">
            <el-button size="small" :disabled="!selected.trace_id" @click="filterSameTrace">
              {{ t("skillMonitor.filterSameTrace") }}
            </el-button>
            <el-button size="small" :disabled="!selected.session_id" @click="filterSameSession">
              {{ t("skillMonitor.filterSameSession") }}
            </el-button>
            <el-button size="small" :disabled="!selected.trace_id" @click="jumpToMcpTrace">
              {{ t("skillMonitor.showMCPTrace") }}
            </el-button>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped lang="scss">
.sm-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 0;
  height: 100%;
}
.sm-subtitle {
  margin: 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.sm-groups {
  font-size: 12px;
  summary { cursor: pointer; padding: 4px 0; }
  table { width: 100%; border-collapse: collapse; text-align: left; }
  th, td { padding: 3px 8px; border-bottom: 1px solid var(--el-border-color-lighter); }
}
.sm-group-scroll { max-height: 160px; overflow: auto; }
.sm-stats {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.sm-stat {
  display: flex;
  flex-direction: column;
  padding: 6px 10px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  min-width: 72px;
}
.sm-stat-label {
  font-size: 11px;
  color: var(--el-text-color-secondary);
}
.sm-stat-value {
  font-size: 15px;
  font-weight: 600;
  &.ok { color: var(--el-color-success); }
  &.error { color: var(--el-color-danger); }
}
.sm-notice {
  font-size: 12px;
  padding: 4px 8px;
  border-radius: 4px;
  background: var(--el-color-info-light-9);
  color: var(--el-text-color-secondary);
  &.warn {
    background: var(--el-color-warning-light-9);
    color: var(--el-color-warning-dark-2);
  }
}
.sm-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.sm-spacer { flex: 1; }
.sm-auto {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.sm-corr {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--el-color-primary);
}
.sm-error {
  font-size: 12px;
  padding: 6px 8px;
  border-radius: 4px;
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}
.sm-body {
  display: flex;
  gap: 12px;
  min-height: 0;
  flex: 1;
}
.sm-list {
  flex: 1;
  min-width: 0;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.sm-empty {
  padding: 24px 0;
  text-align: center;
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
.sm-empty-hint {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.sm-row {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 5px 8px;
  border: none;
  border-radius: 4px;
  background: transparent;
  font: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;
  color: var(--el-text-color-primary);
  &:hover { background: var(--el-fill-color-light); }
  &.selected { background: var(--el-color-primary-light-9); }
}
.sm-time {
  flex-shrink: 0;
  color: var(--el-text-color-secondary);
  font-variant-numeric: tabular-nums;
}
.sm-stage {
  flex-shrink: 0;
  padding: 0 6px;
  border-radius: 3px;
  font-size: 11px;
  &.discovery { background: var(--el-color-info-light-9); color: var(--el-text-color-secondary); }
  &.load { background: var(--el-color-primary-light-9); color: var(--el-color-primary-dark-2); }
  &.execute { background: var(--el-color-success-light-9); color: var(--el-color-success-dark-2); }
  &.unknown { background: var(--el-color-info-light-9); color: var(--el-text-color-secondary); }
}
.sm-skill, .sm-tool {
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sm-skill { font-weight: 600; }
.sm-status {
  flex-shrink: 0;
  padding: 0 6px;
  border-radius: 3px;
  font-size: 11px;
  &.ok { background: var(--el-color-success-light-9); color: var(--el-color-success-dark-2); }
  &.running { background: var(--el-color-primary-light-9); color: var(--el-color-primary-dark-2); }
  &.error, &.timeout { background: var(--el-color-danger-light-9); color: var(--el-color-danger-dark-2); }
  &.cancelled, &.interrupted, &.unknown { background: var(--el-color-info-light-9); color: var(--el-text-color-secondary); }
}
.sm-running-badge {
  flex-shrink: 0;
  font-size: 11px;
  color: var(--el-color-primary);
}
.sm-duration {
  flex-shrink: 0;
  font-variant-numeric: tabular-nums;
  color: var(--el-text-color-secondary);
}
.sm-caller {
  flex-shrink: 0;
  max-width: 72px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--el-text-color-secondary);
}
.sm-list-foot {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 6px 4px;
}
.sm-total {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.sm-detail {
  width: 320px;
  flex-shrink: 0;
  overflow-y: auto;
  border-left: 1px solid var(--el-border-color-lighter);
  padding-left: 12px;
}
// Narrow sheets: stack the detail under the list instead of squeezing the
// rows until the columns collide.
@media (max-width: 720px) {
  .sm-body { flex-direction: column; overflow-y: auto; }
  .sm-list { overflow-y: visible; flex: none; }
  .sm-detail {
    width: 100%;
    border-left: none;
    border-top: 1px solid var(--el-border-color-lighter);
    padding-left: 0;
    padding-top: 8px;
  }
}
.sm-detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.sm-detail-title {
  font-weight: 600;
  font-size: 13px;
}
.sm-detail-loading {
  padding: 12px 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.sm-fields {
  margin: 8px 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.sm-field {
  display: flex;
  gap: 8px;
  font-size: 12px;
  dt {
    flex-shrink: 0;
    width: 92px;
    color: var(--el-text-color-secondary);
  }
  dd {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
  }
}
.mono { font-family: var(--el-font-family-mono, monospace); }
.sm-detail-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
</style>
