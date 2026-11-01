<script setup lang="ts">
// The shared collection-status card on the MCP and skill monitor sheets.
// It summarizes local recording (which streams are on) and the OTLP
// metadata exporter (queue, exported/failed/dropped, sampling) from
// GET /api/observability/status. Deliberately never shown: the collector
// endpoint, headers or any credential — those are environment-level
// configuration documented elsewhere, not UI input.
//
// Behavior contract: polling runs only while `active`; a generation gate
// drops responses that arrive after a newer request or after deactivation;
// a failed request renders "unavailable" — counters are never fabricated
// as 0-success.
import { onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { api } from "@/api/client";
import { createGenerationGate } from "@/components/mcpMonitor";
import {
  asObservabilityBridge,
  counterText,
  type ObservabilityStatus,
} from "@/components/observeStatus";

const props = defineProps<{
  active: boolean;
  // Injectable loader (tests/embedding); defaults to the api bridge.
  load?: () => Promise<ObservabilityStatus>;
}>();

const { t, d } = useI18n();

const POLL_MS = 15000;

const status = ref<ObservabilityStatus | null>(null);
const unavailable = ref(false);
const loading = ref(false);
let timer: number | undefined;
const gate = createGenerationGate();

function loader(): () => Promise<ObservabilityStatus> {
  if (props.load) return props.load;
  return () => asObservabilityBridge(api).observabilityStatus();
}

async function poll() {
  if (!props.active) return;
  const g = gate.next();
  loading.value = true;
  try {
    const s = await loader()();
    if (!gate.isLatest(g)) return;
    status.value = s;
    unavailable.value = false;
  } catch {
    if (!gate.isLatest(g)) return;
    status.value = null;
    unavailable.value = true;
  } finally {
    if (gate.isLatest(g)) loading.value = false;
  }
}

function stopTimer() {
  if (timer !== undefined) {
    window.clearInterval(timer);
    timer = undefined;
  }
}

watch(
  () => props.active,
  (on) => {
    if (on) {
      void poll();
      stopTimer();
      timer = window.setInterval(() => void poll(), POLL_MS);
    } else {
      stopTimer();
      gate.next();
      loading.value = false;
    }
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  stopTimer();
  gate.next();
});

function num(v: number | null | undefined): string {
  return counterText(v) ?? t("observeStatus.dash");
}

function fmtTime(ms: number | undefined): string {
  return ms ? d(new Date(ms), "short") : t("observeStatus.dash");
}
</script>

<template>
  <div class="obs-card" role="status" aria-live="polite">
    <div v-if="unavailable" class="obs-line obs-unavailable">
      {{ t("observeStatus.unavailable") }}
    </div>
    <template v-else-if="status">
      <div class="obs-line">
        <span class="obs-label">{{ t("observeStatus.local") }}</span>
        <span class="obs-chip" :class="{ off: !status.mcp_enabled }">
          {{ status.mcp_enabled ? t("observeStatus.mcpOn") : t("observeStatus.mcpOff") }}
        </span>
        <span class="obs-chip" :class="{ off: !status.skills_enabled }">
          {{ status.skills_enabled ? t("observeStatus.skillsOn") : t("observeStatus.skillsOff") }}
        </span>
        <span class="obs-chip" :class="{ off: !status.history_enabled }">
          {{ status.history_enabled ? t("observeStatus.historyOn") : t("observeStatus.historyOff") }}
        </span>
        <span v-if="status.heartbeat_errors > 0" class="obs-chip warn">
          {{ t("observeStatus.heartbeatErrors", { n: status.heartbeat_errors }) }}
        </span>
      </div>
      <div class="obs-line">
        <span class="obs-label">OTLP</span>
        <span v-if="!status.otlp.enabled" class="obs-chip off">{{ t("observeStatus.otlpOff") }}</span>
        <template v-else>
          <span class="obs-chip">{{ t("observeStatus.otlpOn") }}</span>
          <span class="obs-item">{{ t("observeStatus.queue") }} {{ num(status.otlp.queued) }}/{{ num(status.otlp.queue_capacity) }}</span>
          <span class="obs-item">{{ t("observeStatus.exported") }} {{ num(status.otlp.exported) }}</span>
          <span class="obs-item" :class="{ 'obs-bad': status.otlp.failed > 0 }">
            {{ t("observeStatus.failed") }} {{ num(status.otlp.failed) }}
          </span>
          <span class="obs-item" :class="{ 'obs-bad': status.otlp.dropped > 0 || status.otlp.rejected > 0 }">
            {{ t("observeStatus.dropped") }} {{ num(status.otlp.dropped + status.otlp.rejected) }}
          </span>
          <span class="obs-item">{{ t("observeStatus.sampledOut") }} {{ num(status.otlp.sampled_out) }}</span>
          <span v-if="status.otlp.invalid > 0" class="obs-item obs-bad">{{ t("observeStatus.invalid") }} {{ num(status.otlp.invalid) }}</span>
          <span v-if="status.otlp.last_error_kind" class="obs-item obs-bad">
            {{ t("observeStatus.lastError") }} {{ status.otlp.last_error_kind }}
          </span>
          <span class="obs-item">{{ t("observeStatus.lastSuccess") }} {{ fmtTime(status.otlp.last_success_at) }}</span>
        </template>
        <span v-if="status.otlp_config_error" class="obs-chip warn">{{ t("observeStatus.configError") }}</span>
      </div>
    </template>
    <div v-else class="obs-line obs-loading">{{ loading ? t("observeStatus.loading") : t("observeStatus.dash") }}</div>
  </div>
</template>

<style scoped lang="scss">
.obs-card {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 6px 8px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  font-size: 12px;
}
.obs-line {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.obs-label {
  color: var(--el-text-color-secondary);
  font-weight: 600;
}
.obs-chip {
  padding: 0 6px;
  border-radius: 3px;
  background: var(--el-color-success-light-9);
  color: var(--el-color-success-dark-2);
  &.off {
    background: var(--el-color-info-light-9);
    color: var(--el-text-color-secondary);
  }
  &.warn {
    background: var(--el-color-warning-light-9);
    color: var(--el-color-warning-dark-2);
  }
}
.obs-item {
  color: var(--el-text-color-secondary);
  font-variant-numeric: tabular-nums;
}
.obs-bad {
  color: var(--el-color-danger);
}
.obs-unavailable {
  color: var(--el-color-danger);
}
.obs-loading {
  color: var(--el-text-color-secondary);
}
</style>
