<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Histogram, Warning } from "@element-plus/icons-vue";
import Chevron from "./Chevron.vue";
import { compactionReduction, compactionSummaryExcerpt, compactionTokens } from "@/agent/compactionPresentation";

const props = defineProps<{
  state: "running" | "done" | "info" | "error";
  startedAt: number;
  finishedAt?: number;
  instructions?: string;
  result?: string;
  message?: string;
  summary?: string;
  beforeTokens?: number;
  afterTokens?: number;
}>();

const { t } = useI18n();
const expanded = ref(false);
const now = ref(Date.now());
let timer: ReturnType<typeof setInterval> | undefined;
const running = computed(() => props.state === "running");
const title = computed(() => t(`agentView.compact.${props.state === "running" ? "running" : props.state === "done" ? "doneTitle" : props.state === "info" ? "infoTitle" : "errorTitle"}`));
const elapsed = computed(() => {
  const end = running.value ? now.value : props.finishedAt ?? props.startedAt;
  const seconds = Math.max(0, Math.floor((end - props.startedAt) / 1000));
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
});
const reduction = computed(() => compactionReduction(props.beforeTokens ?? 0, props.afterTokens ?? 0));
const hasMetrics = computed(() => reduction.value !== null);
const remaining = computed(() => 100 - (reduction.value ?? 0));
const beforeLabel = computed(() => compactionTokens(props.beforeTokens ?? 0));
const afterLabel = computed(() => compactionTokens(props.afterTokens ?? 0));
const summaryExcerpt = computed(() => compactionSummaryExcerpt(props.summary ?? ""));

function syncTimer() {
  if (timer !== undefined) clearInterval(timer);
  timer = undefined;
  now.value = Date.now();
  if (running.value) timer = setInterval(() => { now.value = Date.now(); }, 1000);
}

watch(() => props.state, state => {
  if (state === "done") expanded.value = false;
  syncTimer();
});
onMounted(syncTimer);
onBeforeUnmount(() => { if (timer !== undefined) clearInterval(timer); });
</script>

<template>
  <section class="compaction-card" :class="state" :role="state === 'error' ? 'alert' : 'status'" aria-live="polite" :aria-busy="running">
    <div class="compaction-head">
      <span class="compaction-mark" aria-hidden="true">
        <span v-if="running" class="compact-lines"><i /><i /><i /></span>
        <Histogram v-else-if="state === 'done'" />
        <Warning v-else />
      </span>
      <div class="compaction-copy">
        <div class="compaction-title">
          <span>{{ title }}</span><span v-if="running" class="compaction-dots" aria-hidden="true"><i /><i /><i /></span>
        </div>
        <div v-if="running" class="compaction-hint">{{ t("agentView.compact.runningHint") }}</div>
        <div v-else-if="state === 'done' && result" class="compaction-result">{{ result }}</div>
        <div v-else-if="message" class="compaction-message">{{ message }}</div>
      </div>
      <span class="compaction-elapsed">{{ elapsed }}</span>
    </div>
    <div v-if="state === 'done' && hasMetrics" class="compaction-metrics">
      <div class="metric-reduction">
        <strong>-{{ reduction }}%</strong>
        <span>{{ t("agentView.compact.reduction") }}</span>
      </div>
      <div class="metric-usage">
        <div class="metric-tokens">
          <Histogram aria-hidden="true" />
          <strong>{{ beforeLabel }}<span>→</span>{{ afterLabel }}<small>tok</small></strong>
        </div>
        <div class="metric-bar" role="meter" :aria-label="t('agentView.compact.remaining', { pct: remaining })" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="remaining"><i :style="{ width: `${remaining}%` }" /></div>
        <div class="metric-remaining">{{ t("agentView.compact.remaining", { pct: remaining }) }}</div>
      </div>
    </div>
    <div v-if="state === 'done' && summaryExcerpt" class="compaction-overview">
      <span>{{ t("agentView.compact.overview") }}</span>
      <p>{{ summaryExcerpt }}</p>
    </div>
    <div v-if="instructions" class="compaction-focus">
      <span>{{ t("agentView.compact.focus") }}</span>{{ instructions }}
    </div>
    <div v-if="running" class="compaction-progress" aria-hidden="true"><i /></div>
    <div v-if="state === 'done' && summary" class="compaction-summary-wrap">
      <button type="button" class="compaction-summary-toggle" :aria-expanded="expanded" @click="expanded = !expanded">
        <Chevron :open="expanded" />
        {{ t(expanded ? "agentView.compact.hideSummary" : "agentView.compact.showSummary") }}
      </button>
      <Transition name="compaction-summary">
        <div v-show="expanded" class="compaction-summary">{{ summary }}</div>
      </Transition>
    </div>
  </section>
</template>

<style scoped lang="scss">
.compaction-card {
  position: relative;
  margin: 18px 0 10px;
  overflow: hidden;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 12px;
  background: var(--el-fill-color-extra-light, var(--el-fill-color-light));
  color: var(--el-text-color-primary);
  animation: compaction-enter .24s ease-out;
}

.compaction-card.running { border-color: var(--el-color-primary-light-7, var(--el-border-color)); }
.compaction-card.error { border-color: var(--el-color-danger-light-7, var(--el-border-color)); }
.compaction-head { display: flex; align-items: flex-start; gap: 11px; padding: 13px 14px 12px; }
.compaction-mark { display: grid; place-items: center; flex: 0 0 28px; width: 28px; height: 28px; border-radius: 8px; background: var(--el-color-primary-light-9, var(--el-fill-color)); color: var(--el-color-primary); }
.compaction-mark > svg { width: 16px; height: 16px; }
.done .compaction-mark { background: var(--el-color-success-light-9, var(--el-fill-color)); color: var(--el-color-success); }
.error .compaction-mark { background: var(--el-color-danger-light-9, var(--el-fill-color)); color: var(--el-color-danger); }
.info .compaction-mark { background: var(--el-fill-color); color: var(--el-text-color-secondary); }
.compaction-copy { flex: 1; min-width: 0; }
.compaction-title { display: flex; align-items: center; gap: 7px; min-height: 20px; font-size: 13px; font-weight: 600; line-height: 1.5; }
.running .compaction-title > span:first-child { background: linear-gradient(100deg, var(--el-text-color-primary) 30%, var(--el-color-primary) 50%, var(--el-text-color-primary) 70%); background-size: 220% 100%; background-clip: text; color: transparent; animation: compaction-shimmer 2.2s linear infinite; }
.compaction-hint, .compaction-result, .compaction-message { margin-top: 2px; color: var(--el-text-color-secondary); font-size: 12px; line-height: 1.55; }
.compaction-message { white-space: pre-wrap; overflow-wrap: anywhere; }
.error .compaction-message { color: var(--el-color-danger); }
.compaction-elapsed { flex: 0 0 auto; padding-top: 2px; color: var(--el-text-color-placeholder); font-size: 11px; font-variant-numeric: tabular-nums; }
.compaction-dots { display: inline-flex; align-items: center; gap: 3px; }
.compaction-dots i { width: 3px; height: 3px; border-radius: 50%; background: var(--el-color-primary); animation: compaction-dot 1.2s ease-in-out infinite; }
.compaction-dots i:nth-child(2) { animation-delay: .16s; }
.compaction-dots i:nth-child(3) { animation-delay: .32s; }
.compact-lines { display: flex; flex-direction: column; align-items: center; gap: 3px; width: 17px; }
.compact-lines i { display: block; width: 17px; height: 1.5px; border-radius: 2px; background: currentcolor; animation: compaction-lines 1.7s ease-in-out infinite; }
.compact-lines i:nth-child(2) { width: 12px; animation-delay: .12s; }
.compact-lines i:nth-child(3) { width: 15px; animation-delay: .24s; }
.compaction-metrics { display: grid; grid-template-columns: 132px minmax(0, 1fr); gap: 16px; margin: 0 14px 12px 53px; padding: 12px; border: 1px solid var(--el-border-color-extra-light, var(--el-border-color-lighter)); border-radius: 9px; background: var(--el-bg-color); }
.metric-reduction { display: flex; flex-direction: column; justify-content: center; border-right: 1px solid var(--el-border-color-extra-light, var(--el-border-color-lighter)); padding-right: 16px; }
.metric-reduction strong { color: var(--el-color-success); font-size: 25px; font-weight: 650; line-height: 1.15; letter-spacing: -.03em; font-variant-numeric: tabular-nums; }
.metric-reduction span { margin-top: 4px; color: var(--el-text-color-secondary); font-size: 11px; }
.metric-usage { min-width: 0; }
.metric-tokens { display: flex; align-items: center; gap: 7px; font-size: 13px; }
.metric-tokens > svg { width: 15px; height: 15px; color: var(--el-text-color-secondary); }
.metric-tokens strong { display: flex; align-items: baseline; gap: 6px; font-weight: 600; font-variant-numeric: tabular-nums; }
.metric-tokens strong > span { color: var(--el-text-color-placeholder); font-weight: 400; }
.metric-tokens small { margin-left: 2px; color: var(--el-text-color-secondary); font-size: 10px; font-weight: 400; }
.metric-bar { height: 5px; margin: 9px 0 5px; overflow: hidden; border-radius: 3px; background: var(--el-fill-color); }
.metric-bar i { display: block; height: 100%; border-radius: inherit; background: var(--el-color-success); transition: width .35s ease; }
.metric-remaining { color: var(--el-text-color-secondary); font-size: 11px; font-variant-numeric: tabular-nums; }
.compaction-overview { display: flex; align-items: flex-start; gap: 10px; margin: 0 14px 12px 53px; line-height: 1.65; }
.compaction-overview > span { flex: 0 0 auto; padding-top: 1px; color: var(--el-text-color-placeholder); font-size: 11px; }
.compaction-overview p { margin: 0; color: var(--el-text-color-regular); font-size: 12px; overflow-wrap: anywhere; }
.compaction-focus { display: flex; gap: 7px; padding: 9px 14px 10px 53px; border-top: 1px solid var(--el-border-color-extra-light, var(--el-border-color-lighter)); color: var(--el-text-color-regular); font-size: 12px; line-height: 1.55; white-space: pre-wrap; overflow-wrap: anywhere; }
.compaction-focus > span { flex: 0 0 auto; color: var(--el-text-color-placeholder); }
.compaction-progress { position: absolute; right: 0; bottom: 0; left: 0; height: 2px; overflow: hidden; background: var(--el-color-primary-light-8, var(--el-fill-color)); }
.compaction-progress i { display: block; width: 36%; height: 100%; border-radius: 2px; background: var(--el-color-primary); animation: compaction-progress 1.65s ease-in-out infinite; }
.compaction-summary-wrap { padding: 0 14px 13px 53px; }
.compaction-summary-toggle { display: inline-flex; align-items: center; gap: 3px; padding: 3px 0; border: 0; background: transparent; color: var(--el-text-color-secondary); font-size: 12px; cursor: pointer; }
.compaction-summary-toggle:hover { color: var(--el-color-primary); }
.compaction-summary { max-height: 40vh; margin-top: 6px; padding: 11px 12px; overflow: auto; border-left: 2px solid var(--el-color-primary-light-7, var(--el-border-color)); border-radius: 0 7px 7px 0; background: var(--el-bg-color); color: var(--el-text-color-regular); font-size: 12px; line-height: 1.7; white-space: pre-wrap; overflow-wrap: anywhere; }
.compaction-summary-enter-active, .compaction-summary-leave-active { transition: opacity .18s ease, transform .18s ease; transform-origin: top; }
.compaction-summary-enter-from, .compaction-summary-leave-to { opacity: 0; transform: translateY(-4px); }

@keyframes compaction-enter { from { opacity: 0; transform: translateY(6px); } }
@keyframes compaction-shimmer { to { background-position: -220% 0; } }
@keyframes compaction-dot { 0%, 70%, 100% { opacity: .3; transform: translateY(0); } 35% { opacity: 1; transform: translateY(-2px); } }
@keyframes compaction-lines { 0%, 100% { transform: scaleX(1); opacity: .65; } 50% { transform: scaleX(.55); opacity: 1; } }
@keyframes compaction-progress { from { transform: translateX(-110%); } to { transform: translateX(390%); } }

@media (max-width: 600px) {
  .compaction-focus, .compaction-summary-wrap { padding-left: 14px; }
  .compaction-metrics, .compaction-overview { margin-left: 14px; }
  .compaction-metrics { grid-template-columns: 1fr; gap: 10px; }
  .metric-reduction { border-right: 0; border-bottom: 1px solid var(--el-border-color-extra-light, var(--el-border-color-lighter)); padding: 0 0 10px; }
}

@media (prefers-reduced-motion: reduce) {
  .compaction-card, .compact-lines i, .compaction-dots i, .compaction-progress i, .running .compaction-title > span:first-child { animation: none; }
  .running .compaction-title > span:first-child { color: var(--el-text-color-primary); }
  .compaction-summary-enter-active, .compaction-summary-leave-active { transition: none; }
}
</style>
