<script setup lang="ts">
// MCP call monitor panel ("调用监控"), embedded by SettingsDialog as its own
// sheet. Unlike the legacy inbound HTTP diagnostics (McpLogsPanel), this page
// covers both directions — Pogo calling external MCP servers and external
// clients calling Pogo — backed by the mcpobserve store:
//   - stats cards share the exact filter set with the list (one serializer),
//   - keyset pagination via an opaque cursor; page size is never a total,
//   - auto-refresh replaces the first page; historical paging pauses it,
//   - a generation gate drops slow responses that arrive after the filters
//     changed, so stale data can never overwrite a newer view.
// Monitoring responses contain metadata only; arguments, results and credentials
// are omitted, and unknown/interrupted statuses are never presented
// as success.
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Refresh } from "@element-plus/icons-vue";
import { api } from "@/api/client";
import {
  asMonitorBridge,
  asAgentMonitorBridge,
  createAgentBridgeLoaders,
  createBridgeLoaders,
  createDefaultFilter,
  createGenerationGate,
  formatDurationMs,
  formatErrorRate,
  mergeCalls,
  normalizeMcpEntrypoint,
  normalizeMcpStatus,
  snapshotMonitorFilter,
} from "@/components/mcpMonitor";
import { MCP_ENTRYPOINTS } from "@/components/mcpMonitor";
import ObserveStatusCard from "@/components/ObserveStatusCard.vue";
import McpMatchText from "@/components/McpMatchText.vue";
import { mcpCallFailed, mcpHighlightQueries, mcpHighlightSegments } from "@/components/mcpMonitorPresentation";
import type {
  McpCall,
  McpCallsPage,
  McpCallStats,
  McpCallStatsToolRow,
  McpMonitorFilter,
  McpMonitorLoaders,
  McpCallStatus,
  McpMonitorWindow,
} from "@/components/mcpMonitor";

const props = withDefaults(defineProps<{
  active: boolean;
  scope?: "global" | "agent";
  agentId?: string;
  compact?: boolean;
  initialTraceId?: string;
  // Injectable data sources (tests, embedding). When absent, the panel talks
  // to api.mcpCalls/mcpCallStats/mcpCall — wired into api/client.ts by the
  // main agent; until then the panel renders the "not available" notice.
  loadCalls?: (filter: McpMonitorFilter, cursor?: string) => Promise<McpCallsPage>;
  loadStats?: (filter: McpMonitorFilter) => Promise<McpCallStats>;
  loadDetail?: (id: string) => Promise<McpCall>;
}>(), { scope: "global", agentId: "", compact: false });

const { t, d } = useI18n();

const PAGE_SIZE = 100;
const AUTO_REFRESH_MS = 8000;

const filter = ref<McpMonitorFilter>({
  ...createDefaultFilter(),
  traceId: props.initialTraceId ?? "",
  window: props.initialTraceId ? "7d" : "24h",
});
const calls = ref<McpCall[]>([]);
const stats = ref<McpCallStats | null>(null);
const nextCursor = ref("");
const storage = ref<"sqlite" | "memory">("sqlite");
const dropped = ref(0);
const writeErrors = ref(0);
const loading = ref(false);
const loadingMore = ref(false);
const errorKey = ref("");
const selected = ref<McpCall | null>(null);
const detailLoading = ref(false);
const detailError = ref(false);
const auto = ref(true);
const pagedHistory = ref(false);
const knownAgents = ref<Record<string, string>>({});
const agentChoices = computed(() => Object.entries(knownAgents.value).map(([id, name]) => ({ id, name })).sort((a, b) => a.name.localeCompare(b.name)));
const scopeReady = computed(() => props.scope !== "agent" || /^[A-Za-z0-9_-]{1,160}$/.test(props.agentId));
const committedFilter = ref(snapshotMonitorFilter(filter.value));
let timer: number | undefined;

const gate = createGenerationGate();
const detailGate = createGenerationGate();

const STATUS_OPTIONS: McpCallStatus[] = [
  "running",
  "ok",
  "error",
  "cancelled",
  "timeout",
  "interrupted",
];
const WINDOW_OPTIONS: McpMonitorWindow[] = ["1h", "24h", "7d"];

function loaders(): McpMonitorLoaders {
  if (!scopeReady.value) throw new Error("Agent MCP history scope unavailable");
  if (props.loadCalls && props.loadStats && props.loadDetail) {
    return { loadCalls: props.loadCalls, loadStats: props.loadStats, loadDetail: props.loadDetail };
  }
  if (props.scope === "agent") return createAgentBridgeLoaders(asAgentMonitorBridge(api), props.agentId, PAGE_SIZE);
  return createBridgeLoaders(asMonitorBridge(api), PAGE_SIZE);
}

function applyPage(page: McpCallsPage, mode: "replace" | "append") {
  if (props.scope === "agent" && page.calls.some(call => call.agent_id !== props.agentId)) throw new Error("MCP history response belongs to another Agent");
  for (const call of page.calls) if (call.agent_id) knownAgents.value[call.agent_id] = call.agent_name || call.agent_id;
  if (mode === "replace") calls.value = page.calls;
  else calls.value = mergeCalls(calls.value, page.calls);
  nextCursor.value = page.next_cursor;
  if (selected.value) {
    const updated = page.calls.find(c => c.id === selected.value?.id);
    if (updated) {
      // Session fallback exists only in detail. A list refresh must not erase
      // the verified reason or present it as newly captured list metadata.
      const detail = selected.value;
      selected.value = detail.error_source === "session" && detail.error_summary?.trim() && !updated.error_summary?.trim() && updated.status === detail.status
        ? { ...updated, error_summary: detail.error_summary, error_code: detail.error_code, error_source: "session" }
        : updated;
    }
  }
  storage.value = page.storage;
  dropped.value = page.dropped;
  writeErrors.value = page.write_errors;
}

function noteError(err: unknown) {
  errorKey.value =
    err instanceof Error && err.message.includes("not wired")
      ? "mcpMonitor.notAvailable"
      : "mcpMonitor.loadError";
}

// Full reload from the first page: manual refresh and every filter change.
async function reload(snapshot: McpMonitorFilter) {
  if (!props.active) return;
  if (!scopeReady.value) { errorKey.value = "mcpMonitor.scopeMissing"; return; }
  const g = gate.next();
  committedFilter.value = snapshot;
  loading.value = true;
  loadingMore.value = false;
  nextCursor.value = "";
  errorKey.value = "";
  try {
    const src = loaders();
    const [page, s] = await Promise.all([
      src.loadCalls(snapshot),
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
  if (!props.active) return;
  pagedHistory.value = false;
  closeDetail();
  calls.value = [];
  stats.value = null;
  return reload(snapshotMonitorFilter(filter.value));
}

// Historical pages use a fixed query window and pause polling.
async function tick() {
  if (!props.active || loading.value || loadingMore.value) return;
  if (pagedHistory.value) return;
  await reload(snapshotMonitorFilter(committedFilter.value));
}

async function loadMore() {
  if (!props.active || !scopeReady.value || loadingMore.value || loading.value || !nextCursor.value) return;
  const g = gate.current();
  const cursor = nextCursor.value;
  auto.value = false;
  pagedHistory.value = true;
  loadingMore.value = true;
  try {
    const page = await loaders().loadCalls(committedFilter.value, cursor);
    if (!gate.isLatest(g)) return;
    applyPage(page, "append");
  } catch (err) {
    if (gate.isLatest(g)) noteError(err);
  } finally {
    if (gate.isLatest(g)) loadingMore.value = false;
  }
}

async function openDetail(call: McpCall) {
  if (!props.active || !scopeReady.value || (props.scope === "agent" && call.agent_id !== props.agentId)) return;
  const g = gate.current();
  const dg = detailGate.next();
  selected.value = call;
  detailLoading.value = true;
  detailError.value = false;
  try {
    const full = await loaders().loadDetail(call.id);
    if (!gate.isLatest(g) || !detailGate.isLatest(dg) || selected.value?.id !== call.id) return;
    if (full.id !== call.id || (props.scope === "agent" && full.agent_id !== props.agentId)) throw new Error("MCP history detail scope changed");
    selected.value = full;
  } catch {
    if (gate.isLatest(g) && detailGate.isLatest(dg) && selected.value?.id === call.id) detailError.value = true;
  } finally {
    if (detailGate.isLatest(dg) && selected.value?.id === call.id) detailLoading.value = false;
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
  filter.value = { ...createDefaultFilter(), agentId: filter.value.agentId, window: filter.value.window, traceId: selected.value.trace_id };
  void refresh();
}

function filterSameSession() {
  if (!selected.value?.session_id) return;
  filter.value = { ...createDefaultFilter(), agentId: filter.value.agentId, window: filter.value.window, sessionId: selected.value.session_id };
  void refresh();
}

function clearCorrelationFilters() {
  filter.value = { ...filter.value, traceId: "", sessionId: "", runId: "", taskId: "" };
  void refresh();
}

function filterToolGroup(row: McpCallStatsToolRow) {
  if (row.direction !== "inbound" && row.direction !== "outbound") return;
  filter.value = { ...filter.value, direction: row.direction, server: row.server, tool: row.tool };
  void refresh();
}

function correlationActive(): boolean {
  const f = filter.value;
  return Boolean(f.traceId || f.sessionId || f.runId || f.taskId);
}

function statusLabel(status: string): string {
  const s = normalizeMcpStatus(status);
  const key =
    s === "unknown" ? "mcpMonitor.statusUnknown" : `mcpMonitor.status${s.charAt(0).toUpperCase()}${s.slice(1)}`;
  return t(key);
}

function statusClass(status: string): string {
  return normalizeMcpStatus(status);
}

function directionLabel(direction: string): string {
  if (direction === "inbound") return t("mcpMonitor.inboundShort");
  if (direction === "outbound") return t("mcpMonitor.outboundShort");
  return t("mcpMonitor.directionUnknown");
}

function entrypointLabel(entrypoint: string | undefined): string {
  const e = normalizeMcpEntrypoint(entrypoint);
  if (e === "pogo") return t("mcpMonitor.entrypointPogo");
  if (e === "mcp") return t("mcpMonitor.entrypointMcp");
  if (e === "cli") return t("mcpMonitor.entrypointCli");
  return t("mcpMonitor.entrypointUnknown");
}

function durationText(call: McpCall): string {
  if (call.status === "running" || call.status === "interrupted") return t("mcpMonitor.dash");
  return formatDurationMs(call.duration_ms) ?? t("mcpMonitor.dash");
}

function clientSessionText(call: McpCall): string {
  if (call.client) return call.client;
  if (call.session_id) return call.session_id;
  if (call.mcp_session_id) return call.mcp_session_id;
  return t("mcpMonitor.dash");
}

function agentLabel(call: McpCall): string {
  return call.agent_id ? call.agent_name || call.agent_id : t("mcpMonitor.agentUnknown");
}

function highlightQueries(field = "text"): string[] {
  return mcpHighlightQueries(committedFilter.value, field);
}

function failureSummary(call: McpCall): string {
  return call.error_summary?.trim() || t("mcpMonitor.failureMissingShort");
}

function matchingContext(call: McpCall): string[] {
  const queries = highlightQueries();
  if (!queries.length) return [];
  const displayed = new Set([call.server, call.tool || call.wire_tool, ...(mcpCallFailed(call) ? [call.error_code] : []), ...(props.compact ? [] : [agentLabel(call), clientSessionText(call)])]);
  return [...new Set([call.agent_name, call.agent_id, call.client, call.session_id, call.mcp_session_id, call.wire_tool, call.client_id, call.id, call.trace_id, call.parent_id, call.request_id, call.run_id, call.task_id, call.tool_call_id, call.error_kind, call.error_code, call.entrypoint])]
    .filter((value): value is string => !!value && !displayed.has(value) && mcpHighlightSegments(value, queries).some(part => part.hit));
}

function technicalFields(call: McpCall) {
  return [
    { key: "fId", value: call.id },
    { key: "fAgentId", value: call.agent_id },
    { key: "fDirection", value: directionLabel(call.direction) },
    { key: "fWireTool", value: call.wire_tool },
    { key: "fTransport", value: call.transport },
    { key: "fEntrypoint", value: entrypointLabel(call.entrypoint) },
    { key: "fTrace", value: call.trace_id },
    { key: "fParent", value: call.parent_id },
    { key: "fSpanId", value: call.span_id },
    { key: "fParentSpanId", value: call.parent_span_id },
    { key: "fTraceFlags", value: call.trace_flags },
    { key: "fClient", value: call.client },
    { key: "fClientId", value: call.client_id },
    { key: "fMcpSession", value: call.mcp_session_id },
    { key: "fRequestId", value: call.request_id },
    { key: "fRun", value: call.run_id },
    { key: "fTask", value: call.task_id },
    { key: "fToolCall", value: call.tool_call_id },
  ];
}

function statText(v: string | number | null | undefined): string {
  if (v === null || v === undefined) return t("mcpMonitor.dash");
  return String(v);
}

function fmtTime(ms: number): string {
  if (!ms) return t("mcpMonitor.dash");
  return d(new Date(ms), "short");
}

function field(v: string | number | null | undefined): string {
  if (v === null || v === undefined || v === "") return t("mcpMonitor.dash");
  return String(v);
}

function startTimer() {
  stopTimer();
  if (!auto.value || !props.active || !scopeReady.value) return;
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
      void reload(snapshotMonitorFilter(committedFilter.value));
    }
    startTimer();
  }
});

watch(() => props.initialTraceId, (traceId) => {
  filter.value = { ...createDefaultFilter(), traceId: traceId ?? "", window: traceId ? "7d" : "24h" };
  if (props.active) void refresh();
});

watch(
  [() => props.active, () => props.scope, () => props.agentId],
  ([on, scope, agentId], previous) => {
    gate.next();
    closeDetail();
    calls.value = [];
    stats.value = null;
    nextCursor.value = "";
    dropped.value = 0;
    writeErrors.value = 0;
    storage.value = "sqlite";
    errorKey.value = "";
    loading.value = false;
    loadingMore.value = false;
    if (previous && (scope !== previous[1] || agentId !== previous[2])) {
      filter.value = { ...createDefaultFilter(), traceId: props.initialTraceId ?? "", window: props.initialTraceId ? "7d" : "24h" };
      knownAgents.value = {};
      pagedHistory.value = false;
    }
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
  { immediate: true, flush: "sync" },
);

onBeforeUnmount(() => {
  stopTimer();
  gate.next(); // invalidate any in-flight responses
});
</script>

<template>
  <div class="mm-panel" :class="{ compact }" data-testid="mcp-history" :data-agent-id="scope === 'agent' ? agentId : undefined">
    <p class="mm-subtitle">{{ t(scope === "agent" ? "mcpMonitor.agentSubtitle" : "mcpMonitor.subtitle") }}</p>

    <ObserveStatusCard v-if="scope === 'global'" :active="active" />

    <div class="mm-stats" aria-label="statistics">
      <div class="mm-stat"><span class="mm-stat-label">{{ t("mcpMonitor.statTotal") }}</span><span class="mm-stat-value">{{ statText(stats?.total) }}</span></div>
      <div class="mm-stat"><span class="mm-stat-label">{{ t("mcpMonitor.statRunning") }}</span><span class="mm-stat-value running">{{ statText(stats?.running) }}</span></div>
      <div class="mm-stat"><span class="mm-stat-label">{{ t("mcpMonitor.statOk") }}</span><span class="mm-stat-value ok">{{ statText(stats?.ok) }}</span></div>
      <div class="mm-stat"><span class="mm-stat-label">{{ t("mcpMonitor.statErrorRate") }}</span><span class="mm-stat-value" :class="{ error: (stats?.errors ?? 0) > 0 }">{{ statText(formatErrorRate(stats?.error_rate)) }}</span></div>
      <div v-if="!compact" class="mm-stat"><span class="mm-stat-label">{{ t("mcpMonitor.statP50") }}</span><span class="mm-stat-value">{{ statText(formatDurationMs(stats?.p50_ms)) }}</span></div>
      <div v-if="!compact" class="mm-stat"><span class="mm-stat-label">{{ t("mcpMonitor.statP95") }}</span><span class="mm-stat-value">{{ statText(formatDurationMs(stats?.p95_ms)) }}</span></div>
      <div v-if="!compact" class="mm-stat"><span class="mm-stat-label">{{ t("mcpMonitor.statCancelled") }}</span><span class="mm-stat-value">{{ statText(stats?.cancelled) }}</span></div>
      <div v-if="!compact" class="mm-stat"><span class="mm-stat-label">{{ t("mcpMonitor.statInterrupted") }}</span><span class="mm-stat-value">{{ statText(stats?.interrupted) }}</span></div>
    </div>

    <div v-if="storage === 'memory'" class="mm-notice" role="status">{{ t("mcpMonitor.storageMemory") }}</div>
    <div v-if="dropped > 0" class="mm-notice warn" role="status">{{ t("mcpMonitor.droppedHint", { n: dropped }) }}</div>
    <div v-if="writeErrors > 0" class="mm-notice warn" role="status">{{ t("mcpMonitor.writeErrorsHint", { n: writeErrors }) }}</div>

    <p v-if="pagedHistory" class="mm-subtitle">{{ t("mcpMonitor.historyPaused") }}</p>
    <div class="mm-toolbar">
      <el-select
        v-if="!compact"
        v-model="filter.direction"
        :empty-values="[null, undefined]"
        size="small"
        style="width: 150px"
        :aria-label="t('mcpMonitor.colDirection')"
        @change="refresh"
      >
        <el-option :label="t('mcpMonitor.directionAll')" value="" />
        <el-option :label="t('mcpMonitor.directionInbound')" value="inbound" />
        <el-option :label="t('mcpMonitor.directionOutbound')" value="outbound" />
      </el-select>
      <el-select
        v-model="filter.window"
        size="small"
        style="width: 130px"
        :aria-label="t('mcpMonitor.timeWindow')"
        data-testid="mcp-time-window"
        @change="refresh"
      >
        <el-option v-for="w in WINDOW_OPTIONS" :key="w" :label="t(`mcpMonitor.window${w}`)" :value="w" />
      </el-select>
      <el-select
        v-model="filter.status"
        :empty-values="[null, undefined]"
        size="small"
        style="width: 120px"
        :aria-label="t('mcpMonitor.colStatus')"
        data-testid="mcp-status-filter"
        @change="refresh"
      >
        <el-option :label="t('mcpMonitor.statusAll')" value="" />
        <el-option
          v-for="s in STATUS_OPTIONS"
          :key="s"
          :label="t(`mcpMonitor.status${s.charAt(0).toUpperCase()}${s.slice(1)}`)"
          :value="s"
        />
      </el-select>
      <el-input
        v-model="filter.server"
        size="small"
        style="width: 120px"
        clearable
        :placeholder="t('mcpMonitor.serverPlaceholder')"
        :aria-label="t('mcpMonitor.serverPlaceholder')"
        data-testid="mcp-server-filter"
        @keyup.enter="refresh"
        @clear="refresh"
      />
      <el-input
        v-model="filter.tool"
        size="small"
        style="width: 120px"
        clearable
        :placeholder="t('mcpMonitor.toolPlaceholder')"
        :aria-label="t('mcpMonitor.toolPlaceholder')"
        data-testid="mcp-tool-filter"
        @keyup.enter="refresh"
        @clear="refresh"
      />
      <el-select
        v-if="!compact"
        v-model="filter.entrypoint"
        :empty-values="[null, undefined]"
        size="small"
        style="width: 130px"
        :aria-label="t('mcpMonitor.fEntrypoint')"
        @change="refresh"
      >
        <el-option :label="t('mcpMonitor.entrypointAll')" value="" />
        <el-option v-for="e in MCP_ENTRYPOINTS" :key="e" :label="entrypointLabel(e)" :value="e" />
      </el-select>
      <el-select
        v-if="scope === 'global'"
        v-model="filter.agentId"
        class="mm-agent-filter"
        size="small"
        style="width: 180px"
        filterable
        allow-create
        clearable
        :empty-values="[null, undefined]"
        :placeholder="t('mcpMonitor.agentFilter')"
        :aria-label="t('mcpMonitor.agentFilter')"
        data-testid="mcp-agent-filter"
        @change="refresh"
        @clear="refresh"
      >
        <el-option :label="t('mcpMonitor.agentAll')" value="" />
        <el-option v-for="agent in agentChoices" :key="agent.id" :value="agent.id" :label="agent.name" />
      </el-select>
      <el-input
        v-model="filter.q"
        class="mm-search"
        size="small"
        style="width: 220px"
        clearable
        :placeholder="t('mcpMonitor.searchPlaceholder')"
        :aria-label="t('mcpMonitor.searchPlaceholder')"
        data-testid="mcp-search-filter"
        @keyup.enter="refresh"
        @clear="refresh"
      />
      <div class="mm-spacer" />
      <span class="mm-auto">{{ t("mcpMonitor.autoRefresh") }}</span>
      <el-switch v-model="auto" size="small" :aria-label="t('mcpMonitor.autoRefresh')" />
      <el-button size="small" :icon="Refresh" :loading="loading" @click="refresh">
        {{ t("mcpMonitor.refresh") }}
      </el-button>
    </div>

    <div v-if="correlationActive()" class="mm-corr">
      <span>{{ t("mcpMonitor.corrActive") }}</span>
      <el-button size="small" text @click="clearCorrelationFilters">
        {{ t("mcpMonitor.clearCorrFilters") }}
      </el-button>
    </div>

    <div v-if="errorKey" class="mm-error" role="alert">
      <span>{{ t(errorKey) }}</span>
      <el-button v-if="scopeReady" size="small" text @click="refresh">{{ t("mcpMonitor.retry") }}</el-button>
    </div>

    <details v-if="!compact && stats?.by_tool.length" class="mm-groups">
      <summary>{{ t("mcpMonitor.toolGroups") }}</summary>
      <div class="mm-group-scroll">
        <table>
          <thead><tr><th>{{ t("mcpMonitor.colDirection") }}</th><th>{{ t("mcpMonitor.colServer") }}</th><th>{{ t("mcpMonitor.colTool") }}</th><th>{{ t("mcpMonitor.statTotal") }}</th><th>{{ t("mcpMonitor.groupErrors") }}</th><th>p95</th></tr></thead>
          <tbody><tr v-for="row in stats.by_tool" :key="JSON.stringify([row.direction, row.server, row.tool])">
            <td>{{ directionLabel(row.direction) }}</td><td><McpMatchText :text="row.server" :queries="highlightQueries('server')" /></td>
            <td><el-button size="small" text @click="filterToolGroup(row)"><McpMatchText :text="row.tool" :queries="highlightQueries('tool')" /></el-button></td>
            <td>{{ row.total }}</td><td>{{ row.errors }}</td><td>{{ statText(formatDurationMs(row.p95_ms)) }}</td>
          </tr></tbody>
        </table>
      </div>
    </details>

    <div class="mm-body" :class="{ 'detail-open': selected }">
      <div class="mm-list" role="list" :aria-busy="loading">
        <div v-if="loading && !calls.length" class="mm-empty">{{ t("mcpMonitor.loading") }}</div>
        <div v-else-if="!calls.length && !errorKey" class="mm-empty">
          <el-empty :description="t('mcpMonitor.empty')" :image-size="56" />
          <div class="mm-empty-hint">{{ t(scope === 'agent' ? 'mcpMonitor.agentEmptyHint' : 'mcpMonitor.emptyHint') }}</div>
        </div>
        <button
          v-for="call in calls"
          :key="call.id"
          type="button"
          role="listitem"
          class="mm-row"
          :class="[statusClass(call.status), { selected: selected?.id === call.id }]"
          :aria-label="`${fmtTime(call.started_at)} ${agentLabel(call)} ${directionLabel(call.direction)} ${call.server} ${call.tool} ${statusLabel(call.status)}${mcpCallFailed(call) ? ` ${failureSummary(call)}` : ''}`"
          :data-call-id="call.id"
          @click="openDetail(call)"
        >
          <span class="mm-row-main">
          <span class="mm-time">{{ fmtTime(call.started_at) }}</span>
          <span class="mm-direction" :class="call.direction">{{ directionLabel(call.direction) }}</span>
          <span class="mm-server" :title="call.server"><McpMatchText :text="field(call.server)" :queries="highlightQueries('server')" /></span>
          <span class="mm-tool" :title="call.tool || call.wire_tool"><McpMatchText :text="field(call.tool || call.wire_tool)" :queries="highlightQueries('tool')" /></span>
          <span v-if="scope === 'global'" class="mm-agent" :class="{ unknown: !call.agent_id }" :title="call.agent_id || t('mcpMonitor.agentUnknownHint')"><McpMatchText :text="agentLabel(call)" :queries="highlightQueries()" /></span>
          <span class="mm-status" :class="statusClass(call.status)">
            {{ statusLabel(call.status) }}
          </span>
          <span v-if="normalizeMcpStatus(call.status) === 'running'" class="mm-running-badge">
            {{ t("mcpMonitor.runningBadge") }}
          </span>
          <span class="mm-spacer" />
          <span class="mm-duration">{{ durationText(call) }}</span>
          <span class="mm-client" :title="clientSessionText(call)"><McpMatchText :text="clientSessionText(call)" :queries="highlightQueries()" /></span>
          </span>
          <span v-if="matchingContext(call).length" class="mm-match-context" data-testid="mcp-match-context"><span v-for="text in matchingContext(call)" :key="text"><McpMatchText :text="text" :queries="highlightQueries()" /></span></span>
          <span v-if="mcpCallFailed(call)" class="mm-row-reason" data-testid="mcp-error-summary" :title="failureSummary(call)"><span class="mm-reason-text"><McpMatchText :text="failureSummary(call)" :queries="highlightQueries()" /></span><code v-if="call.error_code" class="mm-reason-code"><McpMatchText :text="call.error_code" :queries="highlightQueries()" /></code></span>
        </button>
        <div v-if="calls.length" class="mm-list-foot">
          <span v-if="stats" class="mm-total">{{ t("mcpMonitor.listTotal", { total: stats.total }) }}</span>
          <el-button
            v-if="nextCursor"
            size="small"
            :loading="loadingMore"
            :disabled="loadingMore"
            @click="loadMore"
          >
            {{ t("mcpMonitor.loadMore") }}
          </el-button>
        </div>
      </div>

      <div v-if="selected" class="mm-detail">
        <div class="mm-detail-head">
          <el-button v-if="compact" size="small" text @click="closeDetail">← {{ t("mcpMonitor.backToCalls") }}</el-button>
          <span class="mm-detail-title">{{ t("mcpMonitor.detailTitle") }}</span>
          <el-button size="small" text :aria-label="t('mcpMonitor.closeDetail')" @click="closeDetail">×</el-button>
        </div>
        <div v-if="detailLoading" class="mm-detail-loading">{{ t("mcpMonitor.loading") }}</div>
        <div v-else-if="detailError" class="mm-error" role="alert">
          <span>{{ t("mcpMonitor.detailError") }}</span>
          <el-button size="small" text @click="openDetail(selected)">{{ t("mcpMonitor.retry") }}</el-button>
        </div>
        <section v-if="mcpCallFailed(selected)" class="mm-failure" data-testid="mcp-failure-reason" :aria-label="t('mcpMonitor.failureReason')">
          <h3>{{ t("mcpMonitor.failureReason") }}</h3>
          <p v-if="selected.error_summary?.trim()"><McpMatchText :text="selected.error_summary" :queries="highlightQueries()" /></p>
          <p v-else class="mm-failure-missing">{{ t(detailLoading ? 'mcpMonitor.failureLoading' : detailError ? 'mcpMonitor.failureLoadFailed' : 'mcpMonitor.failureMissing') }}</p>
          <p v-if="selected.error_summary?.trim() && selected.error_source === 'session'" class="mm-failure-source" data-testid="mcp-error-source">{{ t("mcpMonitor.failureFromSession") }}</p>
          <p v-if="selected.error_code" class="mm-failure-code"><span>{{ t("mcpMonitor.fErrorCode") }}</span><code><McpMatchText :text="selected.error_code" :queries="highlightQueries()" /></code></p>
          <el-button v-if="selected.session_id" text size="small" @click="filterSameSession">{{ t("mcpMonitor.filterSameSession") }}</el-button>
          <small v-else-if="!selected.error_summary?.trim() && !detailLoading">{{ t("mcpMonitor.failureNoSession") }}</small>
        </section>
        <dl class="mm-fields">
          <div class="mm-field"><dt>{{ t("mcpMonitor.fAgent") }}</dt><dd><McpMatchText :text="agentLabel(selected)" :queries="highlightQueries()" /><small v-if="!compact && selected.agent_id && selected.agent_name" class="mm-agent-id mono"><McpMatchText :text="selected.agent_id" :queries="highlightQueries()" /></small><small v-else-if="!selected.agent_id" class="mm-agent-id">{{ t("mcpMonitor.agentUnknownHint") }}</small></dd></div>
          <div class="mm-field"><dt>{{ t("mcpMonitor.fStatus") }}</dt><dd><span class="mm-status" :class="statusClass(selected.status)">{{ statusLabel(selected.status) }}</span></dd></div>
          <div class="mm-field"><dt>{{ t("mcpMonitor.fServer") }}</dt><dd><McpMatchText :text="field(selected.server)" :queries="highlightQueries('server')" /></dd></div>
          <div class="mm-field"><dt>{{ t("mcpMonitor.fTool") }}</dt><dd><McpMatchText :text="field(selected.tool)" :queries="highlightQueries('tool')" /></dd></div>
          <div class="mm-field"><dt>{{ t("mcpMonitor.fStarted") }}</dt><dd>{{ fmtTime(selected.started_at) }}</dd></div>
          <div class="mm-field"><dt>{{ t("mcpMonitor.fFinished") }}</dt><dd>{{ selected.finished_at ? fmtTime(selected.finished_at) : t("mcpMonitor.dash") }}</dd></div>
          <div class="mm-field"><dt>{{ t("mcpMonitor.fDuration") }}</dt><dd>{{ durationText(selected) }}</dd></div>
          <div class="mm-field"><dt>{{ t("mcpMonitor.fSession") }}</dt><dd class="mono"><McpMatchText :text="field(selected.session_id)" :queries="highlightQueries()" /></dd></div>
          <div v-if="selected.error_kind" class="mm-field"><dt>{{ t("mcpMonitor.fErrorKind") }}</dt><dd><McpMatchText :text="selected.error_kind" :queries="highlightQueries()" /></dd></div>
        </dl>
        <component :is="compact ? 'details' : 'div'" class="mm-technical" data-testid="mcp-technical-details">
          <summary v-if="compact">{{ t("mcpMonitor.technicalDetails") }}</summary>
          <dl class="mm-fields">
            <div v-for="item in technicalFields(selected)" :key="item.key" class="mm-field"><dt>{{ t(`mcpMonitor.${item.key}`) }}</dt><dd class="mono"><McpMatchText :text="field(item.value)" :queries="highlightQueries()" /></dd></div>
          </dl>
          <el-button v-if="compact" size="small" :disabled="!selected.trace_id" @click="filterSameTrace">{{ t("mcpMonitor.filterSameTrace") }}</el-button>
        </component>
        <div class="mm-detail-actions">
          <el-button v-if="!compact" size="small" :disabled="!selected.trace_id" @click="filterSameTrace">
            {{ t("mcpMonitor.filterSameTrace") }}
          </el-button>
          <el-button v-if="!mcpCallFailed(selected)" size="small" :disabled="!selected.session_id" @click="filterSameSession">
            {{ t("mcpMonitor.filterSameSession") }}
          </el-button>
        </div>
      </div>
    </div>
    <p v-if="scope === 'agent'" class="mm-subtitle mm-history-note">{{ t("mcpMonitor.agentHistoryNote") }}</p>
  </div>
</template>

<style scoped lang="scss">
.mm-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 0;
  height: 100%;
  min-width: 0;
  overflow: hidden;
  container: mcp-history / inline-size;
}
.mm-subtitle {
  margin: 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.mm-groups {
  font-size: 12px;
  summary { cursor: pointer; padding: 4px 0; }
  table { width: 100%; border-collapse: collapse; text-align: left; }
  th, td { padding: 3px 8px; border-bottom: 1px solid var(--el-border-color-lighter); }
}
.mm-group-scroll { max-height: 160px; overflow: auto; }
.mm-stats {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.mm-stat {
  display: flex;
  flex-direction: column;
  padding: 6px 10px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  min-width: 72px;
}
.mm-stat-label {
  font-size: 11px;
  color: var(--el-text-color-secondary);
}
.mm-stat-value {
  font-size: 15px;
  font-weight: 600;
  &.running { color: var(--el-color-primary); }
  &.ok { color: var(--el-color-success); }
  &.error { color: var(--el-color-danger); }
}
.mm-notice {
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
.mm-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.mm-spacer { flex: 1; }
.mm-auto {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.mm-corr {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--el-color-primary);
}
.mm-error {
  font-size: 12px;
  padding: 6px 8px;
  border-radius: 4px;
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
}
.mm-body {
  display: flex;
  gap: 12px;
  min-height: 0;
  flex: 1;
  overflow: hidden;
}
.mm-list {
  flex: 1;
  min-width: 0;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.mm-empty {
  padding: 24px 0;
  text-align: center;
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
.mm-empty-hint {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.mm-row {
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 6px;
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
.mm-row-main { display: flex; align-items: center; gap: 8px; min-width: 0; }
.mm-row-reason { display: flex; align-items: flex-start; gap: 8px; min-width: 0; color: var(--el-text-color-regular); line-height: 1.55; }
.mm-reason-text { flex: 1; min-width: 0; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden; overflow-wrap: anywhere; }
.mm-reason-code { flex: 0 1 auto; max-width: 42%; font-size: 10px; color: var(--el-color-danger); overflow-wrap: anywhere; }
.mm-match-context { display: flex; flex-wrap: wrap; gap: 3px 10px; color: var(--el-text-color-secondary); font-size: 11px; overflow-wrap: anywhere; }
.mm-match-context > span { min-width: 0; }
.mm-time {
  flex-shrink: 0;
  color: var(--el-text-color-secondary);
  font-variant-numeric: tabular-nums;
}
.mm-direction {
  flex-shrink: 0;
  padding: 0 6px;
  border-radius: 3px;
  font-size: 11px;
  &.inbound { background: var(--el-color-success-light-9); color: var(--el-color-success-dark-2); }
  &.outbound { background: var(--el-color-primary-light-9); color: var(--el-color-primary-dark-2); }
}
.mm-server, .mm-tool {
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mm-status {
  flex-shrink: 0;
  padding: 0 6px;
  border-radius: 3px;
  font-size: 11px;
  &.ok { background: var(--el-color-success-light-9); color: var(--el-color-success-dark-2); }
  &.running { background: var(--el-color-primary-light-9); color: var(--el-color-primary-dark-2); }
  &.error, &.timeout { background: var(--el-color-danger-light-9); color: var(--el-color-danger-dark-2); }
  &.cancelled, &.interrupted, &.unknown { background: var(--el-color-info-light-9); color: var(--el-text-color-secondary); }
}
.mm-running-badge {
  flex-shrink: 0;
  font-size: 11px;
  color: var(--el-color-primary);
}
.mm-duration {
  flex-shrink: 0;
  font-variant-numeric: tabular-nums;
  color: var(--el-text-color-secondary);
}
.mm-client {
  flex-shrink: 0;
  max-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--el-text-color-secondary);
}
.mm-agent {
  flex: 0 1 150px;
  min-width: 64px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--el-color-primary);
  &.unknown { color: var(--el-text-color-placeholder); }
}
.mm-agent-id { display: block; margin-top: 3px; color: var(--el-text-color-secondary); overflow-wrap: anywhere; }
.mm-technical summary { cursor: pointer; color: var(--el-text-color-secondary); font-size: 12px; padding: 10px 0; }
.mm-list-foot {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 6px 4px;
}
.mm-total {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.mm-detail {
  width: 320px;
  flex-shrink: 0;
  overflow-y: auto;
  border-left: 1px solid var(--el-border-color-lighter);
  padding-left: 12px;
  min-width: 0;
  max-width: 100%;
  box-sizing: border-box;
}
.mm-detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.mm-detail-title {
  font-weight: 600;
  font-size: 13px;
}
.mm-detail-loading {
  padding: 12px 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.mm-failure { margin: 10px 0 14px; padding: 11px 12px; border: 1px solid var(--el-color-danger-light-7); border-radius: 9px; background: var(--el-color-danger-light-9); font-size: 12px; line-height: 1.65; overflow-wrap: anywhere; }
.mm-failure h3 { margin: 0 0 6px; color: var(--el-color-danger); font-size: 12px; font-weight: 600; }
.mm-failure p { margin: 0 0 7px; white-space: pre-wrap; }
.mm-failure p:last-child { margin-bottom: 0; }
.mm-failure-missing, .mm-failure-source, .mm-failure small { color: var(--el-text-color-secondary); }
.mm-failure-source { font-size: 11px; }
.mm-failure-code { display: flex; flex-wrap: wrap; gap: 4px 8px; }
.mm-failure-code span { color: var(--el-text-color-secondary); }
.mm-failure :deep(.el-button) { padding: 0; height: auto; margin-top: 2px; }
.mm-fields {
  margin: 8px 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.mm-field {
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
.mm-detail-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.compact {
  .mm-subtitle { line-height: 1.6; }
  .mm-stats { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 6px; }
  .mm-stat { min-width: 0; padding: 7px 8px; border-radius: 9px; }
  .mm-stat-label { white-space: nowrap; font-size: 10px; }
  .mm-stat-value { font-size: 16px; margin-top: 3px; }
  .mm-toolbar { gap: 6px; }
  .mm-toolbar > :deep(.el-select), .mm-toolbar > :deep(.el-input) { flex: 1 1 calc(50% - 3px); min-width: 0; width: calc(50% - 3px) !important; }
  .mm-toolbar > :deep(.mm-search) { flex-basis: 100%; width: 100% !important; }
  .mm-toolbar > .mm-spacer { display: none; }
  .mm-toolbar > :deep(.el-button) { margin-left: auto; }
  .mm-list { gap: 7px; }
  .mm-row { gap: 8px; padding: 11px; border: 1px solid var(--el-border-color-lighter); border-radius: 10px; }
  .mm-row-main { display: grid; grid-template-columns: minmax(0, 1fr) auto; grid-template-areas: "tool status" "server duration" "time time"; gap: 5px 10px; }
  .mm-tool { grid-area: tool; max-width: none; font-size: 13px; font-weight: 600; }
  .mm-server { grid-area: server; max-width: none; color: var(--el-text-color-secondary); }
  .mm-status { grid-area: status; justify-self: end; }
  .mm-duration { grid-area: duration; justify-self: end; }
  .mm-time { grid-area: time; white-space: normal; font-size: 11px; }
  .mm-row .mm-direction, .mm-row .mm-running-badge, .mm-row .mm-spacer, .mm-row .mm-client { display: none; }
  .mm-body.detail-open .mm-list { display: none; }
  .mm-detail { flex: 1; width: 100%; border: 0; padding: 0 2px; }
  .mm-detail-head { gap: 8px; position: sticky; top: 0; background: var(--el-bg-color); padding-bottom: 8px; }
  .mm-detail-title { margin-left: auto; }
  .mm-fields { gap: 10px; }
  .mm-field dt { width: 80px; }
}

@container mcp-history (max-width: 640px) {
  .mm-row-main { flex-wrap: wrap; }
  .mm-body.detail-open .mm-list { display: none; }
  .mm-detail { flex: 1; width: 100%; border: 0; padding: 0; }
}
</style>
