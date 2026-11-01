<script setup lang="ts">
// The MCP call-log panel, embedded in SettingsDialog's "服务端" sheet: every
// request that touches /mcp or /sse, one row each, classified by transport
// and verdict. Three-level progressive disclosure, the shape every network
// inspector converged on (DevTools/Fiddler; see MCP-日志方案.md):
//   L1  a compact row per request — date+time, transport tag, status badge,
//       method/tool, duration, size; rejections carry their one-line
//       diagnosis on the row (red accent + phrase).
//   L2  clicking a row opens the side detail panel: overview facts, a
//       diagnosis card (problem + stage + fix, translated from the server's
//       diag code), the request frame, the captured rejection body.
//   L3  request bodies render as a collapsible JSON tree (JsonTree.vue).
//
// Polling runs only while `active` is true (the owning sheet is showing);
// the ring caps what the endpoint returns, so the list needs no
// virtualization at these sizes.
import { onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox } from "element-plus";
import { Delete, Refresh } from "@element-plus/icons-vue";
import { api } from "@/api/client";
import type { MCPCallLog, MCPLogOutcome, MCPLogStats } from "@/api/types";
import JsonTree from "@/components/JsonTree.vue";
import SseFrames from "@/components/SseFrames.vue";
import { looksLikeSse } from "@/components/sseFrames";

const props = defineProps<{ active: boolean }>();

const { t } = useI18n();

const logs = ref<MCPCallLog[]>([]);
const stats = ref<MCPLogStats | null>(null);
const selected = ref<MCPCallLog | null>(null);
const loading = ref(false);
const auto = ref(true);
const verdictFilter = ref("all");
const transportFilter = ref("");
const query = ref("");
let timer: number | undefined;

async function load() {
  loading.value = true;
  try {
    const res = await api.mcpLogs({
      limit: 300,
      verdict: verdictFilter.value === "all" ? "" : verdictFilter.value,
      transport: transportFilter.value,
      q: query.value.trim() || undefined,
    });
    logs.value = res.logs;
    // Keep the selection alive across refreshes (the ring re-serves it).
    if (selected.value) {
      selected.value = res.logs.find((e) => e.req_id === selected.value?.req_id) ?? selected.value;
    }
  } catch {
    /* transient — the next tick or manual refresh retries */
  } finally {
    loading.value = false;
  }
}

async function loadStats() {
  try {
    stats.value = await api.mcpLogStats();
  } catch {
    stats.value = null;
  }
}

async function refresh() {
  await Promise.all([load(), loadStats()]);
}

async function clearLogs(scope: "screen" | "all") {
  if (scope === "all") {
    try {
      await ElMessageBox.confirm(t("mcpLogs.clearAll"), t("mcpLogs.clear"), { type: "warning" });
    } catch {
      return;
    }
  }
  const res = await api.mcpLogClear(scope);
  ElMessage.success(t("mcpLogs.clearDone", { screen: "", history: res.history_removed }));
  selected.value = null;
  await refresh();
}

watch(
  () => props.active,
  (on) => {
    if (on) {
      void refresh();
      timer = window.setInterval(() => {
        if (auto.value) void refresh();
      }, 5000);
    } else if (timer !== undefined) {
      window.clearInterval(timer);
      timer = undefined;
    }
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  if (timer !== undefined) window.clearInterval(timer);
});

// The one-line diagnosis for a rejected row: the problem statement when we
// know the code, the raw error otherwise.
function diagLine(e: MCPCallLog): string {
  if (e.diag_code) {
    const key = `mcpLogs.diag.${e.diag_code}.problem`;
    const txt = t(key);
    return txt === key ? e.diag_code : txt;
  }
  return e.resp_body?.split("\n")[0] ?? String(e.status);
}
function stageLabel(e: MCPCallLog): string {
  if (!e.stage) return "";
  const key = `mcpLogs.stage${e.stage.charAt(0).toUpperCase()}${e.stage.slice(1)}`;
  const txt = t(key);
  return txt === key ? e.stage : txt;
}

// The date is always shown: entries outlive the day they were made (the
// history table keeps 30 days), and "08:17" alone stops identifying a row
// the moment yesterday's rejections are still relevant.
function fmtTime(ts: number): string {
  const d = new Date(ts);
  const now = new Date();
  const hm = `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}:${String(d.getSeconds()).padStart(2, "0")}`;
  const md = `${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  if (d.getFullYear() !== now.getFullYear()) return `${d.getFullYear()}-${md} ${hm}`;
  return `${md} ${hm}`;
}
function fmtMs(v: number): string {
  if (v < 1) return "<1ms";
  if (v < 1000) return `${Math.round(v)}ms`;
  return `${(v / 1000).toFixed(1)}s`;
}
function fmtBytes(n?: number): string {
  if (!n) return "—";
  if (n < 1024) return `${n}B`;
  return `${(n / 1024).toFixed(1)}KB`;
}
function verdictClass(e: MCPCallLog): string {
  return e.verdict === "ok" ? "ok" : e.verdict === "rejected" ? "rejected" : "error";
}
function verdictLabel(e: MCPCallLog): string {
  const v = e.verdict;
  const key = `mcpLogs.verdict${v.charAt(0).toUpperCase()}${v.slice(1)}`;
  const txt = t(key);
  return txt === key ? v : txt;
}
function toolOutcome(e: MCPCallLog): MCPLogOutcome | undefined {
  return e.tool_outcome ?? (e.jsonrpc_method === "tools/call" ? "unknown" : undefined);
}
function outcomeLabel(outcome?: MCPLogOutcome): string {
  if (outcome === "ok") return t("mcpLogs.outcomeOk");
  if (outcome === "error") return t("mcpLogs.outcomeError");
  return t("mcpLogs.outcomeUnknown");
}
function parsedReqHead(e: MCPCallLog): { parsed: unknown; ok: boolean } {
  if (!e.req_head) return { parsed: null, ok: false };
  try {
    return { parsed: JSON.parse(e.req_head), ok: true };
  } catch {
    return { parsed: e.req_head, ok: false };
  }
}
function parsedRespBody(e: MCPCallLog): { parsed: unknown; ok: boolean } {
  if (!e.resp_body) return { parsed: null, ok: false };
  try {
    return { parsed: JSON.parse(e.resp_body), ok: true };
  } catch {
    return { parsed: e.resp_body, ok: false };
  }
}
async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text);
    ElMessage.success(t("mcpLogs.copied"));
  } catch {
    /* clipboard denied — the text is visible for manual copy anyway */
  }
}
</script>

<template>
  <div class="ml-panel">
    <div class="ml-stats">
      <template v-if="stats">
        <span>{{ t("mcpLogs.statsLine", { total: stats.total, rejected: stats.rejected_24h, streamable: stats.by_transport["streamable"] ?? 0, sse: stats.by_transport["sse"] ?? 0 }) }}</span>
        <span v-if="stats.dropped > 0" class="ml-dropped">{{ t("mcpLogs.droppedHint", { n: stats.dropped }) }}</span>
      </template>
    </div>

    <div class="ml-toolbar">
      <el-select v-model="verdictFilter" size="small" style="width: 120px" @change="load">
        <el-option :label="t('mcpLogs.verdictAll')" value="all" />
        <el-option :label="t('mcpLogs.verdictOk')" value="ok" />
        <el-option :label="t('mcpLogs.verdictRejected')" value="rejected" />
        <el-option :label="t('mcpLogs.verdictError')" value="error" />
      </el-select>
      <el-select v-model="transportFilter" size="small" style="width: 132px" @change="load">
        <el-option :label="t('mcpLogs.transportAll')" value="" />
        <el-option :label="t('mcpLogs.transportStreamable')" value="streamable" />
        <el-option :label="t('mcpLogs.transportSse')" value="sse" />
      </el-select>
      <el-input v-model="query" size="small" style="width: 220px" clearable
                :placeholder="t('mcpLogs.searchPlaceholder')" @keyup.enter="load" @clear="load" />
      <div class="ml-spacer" />
      <span class="ml-auto">{{ t("mcpLogs.autoRefresh") }}</span>
      <el-switch v-model="auto" size="small" />
      <el-button size="small" :icon="Refresh" :loading="loading" @click="refresh">
        {{ t("mcpLogs.refresh") }}
      </el-button>
      <el-dropdown @command="clearLogs">
        <el-button size="small" :icon="Delete" type="danger" plain>{{ t("mcpLogs.clear") }}</el-button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="screen">{{ t("mcpLogs.clearScreen") }}</el-dropdown-item>
            <el-dropdown-item command="all" divided>{{ t("mcpLogs.clearAll") }}</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </div>

    <div class="ml-body">
      <div class="ml-list" role="list">
        <div v-if="!logs.length" class="ml-empty">
          <el-empty :description="t('mcpLogs.empty')" :image-size="56" />
          <div class="ml-empty-hint">{{ t("mcpLogs.emptyHint") }}</div>
        </div>
        <button v-for="e in logs" :key="e.req_id + e.ts" type="button" role="listitem"
                class="ml-row" :class="[verdictClass(e), { selected: selected?.req_id === e.req_id }]"
                :title="verdictClass(e) === 'ok' ? '' : diagLine(e)"
                @click="selected = e">
          <span class="ml-time">{{ fmtTime(e.ts) }}</span>
          <span class="ml-transport" :class="e.transport">
            {{ e.transport === "streamable" ? "STRM" : "SSE" }}
          </span>
          <span class="ml-status" :class="verdictClass(e)">{{ e.status }}</span>
          <span class="ml-method">{{ e.tool || e.jsonrpc_method || e.http_method }}</span>
          <span v-if="toolOutcome(e)" class="ml-outcome" :class="toolOutcome(e)"
                :title="toolOutcome(e) === 'unknown' ? t('mcpLogs.outcomeUnknownHint') : t('mcpLogs.fToolResult')">
            {{ outcomeLabel(toolOutcome(e)) }}
          </span>
          <span class="ml-diag" v-if="e.verdict !== 'ok'">{{ diagLine(e) }}</span>
          <span class="ml-spacer" />
          <span class="ml-duration">{{ fmtMs(e.duration_ms) }}</span>
          <span class="ml-size">{{ fmtBytes(e.req_bytes) }}</span>
        </button>
      </div>

      <div v-if="selected" class="ml-detail">
        <div class="ml-detail-title">
          {{ t("mcpLogs.detailTitle") }}
          <span class="ml-reqid mono">{{ selected.req_id }}</span>
        </div>

        <div class="ml-section">
          <div class="ml-section-title">{{ t("mcpLogs.sectionOverview") }}</div>
          <div class="ml-kv">
            <div class="ml-kv-row"><span>{{ t("mcpLogs.colTime") }}</span><b class="mono">{{ fmtTime(selected.ts) }}</b></div>
            <div class="ml-kv-row"><span>{{ t("mcpLogs.colTransport") }}</span><b :class="selected.transport">{{ selected.transport }}</b></div>
            <div class="ml-kv-row"><span>{{ t("mcpLogs.colStatus") }}</span><b :class="verdictClass(selected)">{{ selected.status }} · {{ verdictLabel(selected) }}</b></div>
            <div v-if="selected.proto_outcome" class="ml-kv-row"><span>{{ t("mcpLogs.fProtocolResult") }}</span><b>{{ outcomeLabel(selected.proto_outcome) }}</b></div>
            <div v-if="toolOutcome(selected)" class="ml-kv-row"><span>{{ t("mcpLogs.fToolResult") }}</span><b>{{ outcomeLabel(toolOutcome(selected)) }}</b></div>
            <div class="ml-kv-row"><span>{{ t("mcpLogs.colMethod") }}</span><b class="mono">{{ selected.tool || selected.jsonrpc_method || "—" }}</b></div>
            <div class="ml-kv-row"><span>{{ t("mcpLogs.fHttpMethod") }}</span><b class="mono">{{ selected.http_method }}</b></div>
            <div class="ml-kv-row"><span>{{ t("mcpLogs.fDuration") }}</span><b>{{ fmtMs(selected.duration_ms) }}</b></div>
            <div class="ml-kv-row"><span>{{ t("mcpLogs.fSize") }}</span><b>{{ selected.req_bytes ?? 0 }} B</b></div>
            <div class="ml-kv-row" v-if="selected.session_id"><span>{{ t("mcpLogs.fSession") }}</span><b class="mono">{{ selected.session_id }}</b></div>
            <div class="ml-kv-row" v-if="selected.client_name"><span>{{ t("mcpLogs.fClient") }}</span><b>{{ selected.client_name }}</b></div>
            <div class="ml-kv-row" v-if="selected.protocol_version"><span>{{ t("mcpLogs.fProtocol") }}</span><b class="mono">{{ selected.protocol_version }}</b></div>
            <div class="ml-kv-row" v-if="selected.remote_addr"><span>{{ t("mcpLogs.fRemote") }}</span><b class="mono">{{ selected.remote_addr }}</b></div>
          </div>
          <div v-if="toolOutcome(selected) === 'unknown' || selected.proto_outcome === 'unknown'" class="ml-note">
            {{ t("mcpLogs.outcomeUnknownHint") }}
          </div>
        </div>

        <div v-if="selected.verdict !== 'ok'" class="ml-section">
          <div class="ml-section-title">{{ t("mcpLogs.sectionDiagnosis") }}</div>
          <div class="ml-diagcard" :class="verdictClass(selected)">
            <div class="ml-diagrow">
              <span class="ml-diaglabel">{{ t("mcpLogs.diagProblemLabel") }}</span>
              <span>{{ diagLine(selected) }}</span>
            </div>
            <div class="ml-diagrow" v-if="selected.stage">
              <span class="ml-diaglabel">{{ t("mcpLogs.diagStageLabel") }}</span>
              <span>{{ stageLabel(selected) }}</span>
            </div>
            <div class="ml-diagrow" v-if="selected.diag_code">
              <span class="ml-diaglabel">{{ t("mcpLogs.diagFixLabel") }}</span>
              <span>{{ t(`mcpLogs.diag.${selected.diag_code}.fix`) }}</span>
            </div>
          </div>
          <div v-if="selected.resp_body" class="ml-raw">
            <div class="ml-raw-head">
              <span>{{ t("mcpLogs.rawText") }}</span>
              <el-button size="small" text @click="copyText(selected.resp_body)">{{ t("mcpLogs.copy") }}</el-button>
            </div>
            <pre class="mono">{{ selected.resp_body }}</pre>
          </div>
        </div>

        <div class="ml-section">
          <div class="ml-section-title">{{ t("mcpLogs.sectionRequest") }}</div>
          <JsonTree v-if="parsedReqHead(selected).ok" :value="parsedReqHead(selected).parsed" :deep="3" />
          <div v-else-if="selected.req_head" class="ml-raw">
            <pre class="mono">{{ selected.req_head }}</pre>
          </div>
          <div v-else-if="selected.req_bytes" class="ml-note">{{ t("mcpLogs.reqHeadOff") }}</div>
          <div v-else class="ml-note">—</div>
        </div>

        <div class="ml-section">
          <div class="ml-section-title">{{ t("mcpLogs.sectionResponse") }}</div>
          <template v-if="selected.resp_body">
            <div v-if="selected.resp_truncated" class="ml-note">{{ t("mcpLogs.respTruncated") }}</div>
            <JsonTree v-if="parsedRespBody(selected).ok" :value="parsedRespBody(selected).parsed" :deep="3" />
            <SseFrames v-else-if="looksLikeSse(selected.resp_body)" :text="selected.resp_body"
                       :truncated="selected.resp_truncated" />
            <div v-else class="ml-raw">
              <pre class="mono">{{ selected.resp_body }}</pre>
            </div>
          </template>
          <div v-else class="ml-note">{{ t("mcpLogs.respBodyUnavailable") }}</div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ml-panel { display: flex; flex-direction: column; gap: 6px; }
.ml-stats { color: var(--el-text-color-secondary); font-size: 12px; }
.ml-dropped { color: var(--el-color-warning); margin-left: 10px; }
.ml-toolbar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.ml-auto { color: var(--el-text-color-secondary); font-size: 12px; }
.ml-spacer { flex: 1; }
.ml-body { display: flex; gap: 10px; height: min(52vh, 520px); }
.ml-list {
  flex: 1.2; overflow-y: auto; border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px; padding: 4px; min-width: 0;
}
.ml-empty { padding: 16px 8px; text-align: center; }
.ml-empty-hint { color: var(--el-text-color-secondary); font-size: 12px; }
.ml-row {
  display: flex; align-items: center; gap: 8px; width: 100%;
  height: 30px; padding: 0 8px; border: none; border-left: 3px solid transparent;
  border-radius: 4px; background: none; cursor: pointer; font: inherit;
  font-size: 12px; color: var(--el-text-color-primary); text-align: left;
}
.ml-row:hover { background: var(--el-fill-color-light); }
.ml-row.selected { background: var(--el-fill-color); }
.ml-row.rejected { border-left-color: var(--el-color-error); }
.ml-row.error { border-left-color: var(--el-color-error); background: var(--el-color-error-light-9); }
.ml-row.ok .ml-status { color: var(--el-color-success); }
.ml-row.rejected .ml-status, .ml-row.error .ml-status { color: var(--el-color-error); font-weight: 700; }
.ml-time { color: var(--el-text-color-secondary); flex-shrink: 0; font-variant-numeric: tabular-nums; }
.ml-transport {
  flex-shrink: 0; font-size: 10px; padding: 0 5px; border-radius: 3px; line-height: 16px;
  border: 1px solid var(--el-border-color);
}
.ml-transport.streamable { color: #58a6ff; border-color: #58a6ff66; }
.ml-transport.sse { color: #d29922; border-color: #d2992266; }
.ml-status { flex-shrink: 0; width: 28px; text-align: right; }
.ml-outcome { flex-shrink: 0; font-size: 11px; color: var(--el-text-color-secondary); }
.ml-outcome.ok { color: var(--el-color-success); }
.ml-outcome.error { color: var(--el-color-error); }
.ml-method { font-family: var(--font-mono, monospace); flex-shrink: 1; min-width: 60px; max-width: 200px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ml-diag { color: var(--el-color-error); font-size: 11px; overflow: hidden;
  text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
.ml-duration { color: var(--el-text-color-secondary); flex-shrink: 0; }
.ml-size { color: var(--el-text-color-secondary); flex-shrink: 0; width: 48px; text-align: right; }
.ml-detail {
  flex: 1; overflow-y: auto; border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px; padding: 10px; min-width: 0;
}
.ml-detail-title { font-weight: 600; margin-bottom: 8px; display: flex; align-items: center; gap: 8px; }
.ml-reqid { color: var(--el-text-color-secondary); font-weight: 400; }
.ml-section { margin-bottom: 12px; }
.ml-section-title {
  font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em;
  color: var(--el-text-color-secondary); margin-bottom: 6px;
  border-bottom: 1px solid var(--el-border-color-lighter); padding-bottom: 2px;
}
.ml-kv { font-size: 12px; }
.ml-kv-row { display: flex; justify-content: space-between; padding: 2px 0; gap: 10px; }
.ml-kv-row span { color: var(--el-text-color-secondary); flex-shrink: 0; }
.ml-kv-row b { font-weight: 500; word-break: break-all; text-align: right; }
.ml-kv-row .streamable { color: #58a6ff; }
.ml-kv-row .sse { color: #d29922; }
.ml-diagcard {
  border: 1px solid var(--el-color-error-light-5); background: var(--el-color-error-light-9);
  border-radius: 6px; padding: 8px 10px; font-size: 12px;
}
.ml-diagrow { display: flex; gap: 8px; padding: 2px 0; }
.ml-diaglabel { color: var(--el-text-color-secondary); flex-shrink: 0; width: 3em; }
.ml-raw { margin-top: 8px; }
.ml-raw-head { display: flex; align-items: center; justify-content: space-between; font-size: 11px;
  color: var(--el-text-color-secondary); }
.ml-raw pre {
  margin: 4px 0 0; padding: 8px; background: var(--el-fill-color-darker, #1b1b1b);
  border-radius: 4px; font-size: 11px; white-space: pre-wrap; word-break: break-all;
  max-height: 200px; overflow-y: auto;
}
.ml-note { color: var(--el-text-color-secondary); font-size: 12px; }
.mono { font-family: var(--font-mono, ui-monospace, monospace); }
</style>
