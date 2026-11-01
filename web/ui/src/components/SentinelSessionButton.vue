<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Connection, Refresh } from "@element-plus/icons-vue";
import { sentinelSessionsApi, type SentinelSessionSummary } from "@/api/sentinelSessions";

const props = defineProps<{ parentSession: string | null; agentId: string; enabled: boolean }>();
const emit = defineEmits<{ open: [sentinelId: string, label: string, agentId: string, parentSessionId?: string] }>();
const { t } = useI18n();
const watches = ref<SentinelSessionSummary[]>([]);
const expanded = ref(false), loading = ref(false), error = ref(false);
let disposed = false, sequence = 0;
let timer: ReturnType<typeof setTimeout> | undefined, controller: AbortController | undefined;
const context = () => JSON.stringify([props.parentSession, props.agentId, props.enabled]);
const available = () => !disposed && props.enabled && !!props.parentSession && !!props.agentId;
const pageVisible = () => typeof document === "undefined" || !document.hidden;
const active = (item: SentinelSessionSummary) => !!item.busy || item.enabled && !["expired", "removed", "completed"].includes(item.state);
const hasActive = computed(() => watches.value.some(active));
const title = computed(() => t(error.value ? "sentinelAccess.stale" : watches.value.length === 1 ? "sentinelAccess.openOne" : "sentinelAccess.openMany", { n: watches.value.length }));

function linked(item: SentinelSessionSummary): boolean {
  return !!item && typeof item.id === "string" && /^[a-zA-Z0-9_-]{1,160}$/.test(item.id) &&
    item.task_id === `sentinel:${item.id}` && item.agent_id === props.agentId && item.parent_session_id === props.parentSession;
}
function state(item: SentinelSessionSummary): string {
  if (["paused", "expired", "removed", "completed", "error", "blocked", "unavailable", "waiting_capacity", "waiting_human"].includes(item.state)) return item.state;
  return item.busy ? "running" : item.enabled ? "listening" : "paused";
}
function schedule() {
  clearTimeout(timer);
  if (available() && pageVisible()) timer = setTimeout(() => void load(), 5000);
}
async function load() {
  if (!available() || !pageVisible()) return;
  const ticket = ++sequence, origin = context(), parent = props.parentSession!;
  clearTimeout(timer); controller?.abort(); controller = new AbortController();
  loading.value = true;
  try {
    const value = await sentinelSessionsApi.list(parent, controller.signal);
    if (!available() || ticket !== sequence || origin !== context()) return;
    if (!value || !Array.isArray(value.watches)) throw new Error("Invalid Sentinel list");
    const unique = new Map<string, SentinelSessionSummary>();
    for (const item of value.watches) if (linked(item)) unique.set(item.id, item);
    watches.value = [...unique.values()].sort((a, b) => Number(active(b)) - Number(active(a)) || a.title.localeCompare(b.title));
    error.value = false;
    if (watches.value.length < 2) expanded.value = false;
  } catch (failure) {
    if (available() && ticket === sequence && origin === context() && (failure as Error)?.name !== "AbortError") error.value = true;
  } finally {
    if (ticket === sequence && origin === context()) { loading.value = false; schedule(); }
  }
}
function open(item: SentinelSessionSummary) {
  if (!available() || !linked(item) || !watches.value.some(value => value.id === item.id)) return;
  expanded.value = false;
  emit("open", item.id, item.title, item.agent_id, item.parent_session_id);
}
function visibilityChanged() {
  ++sequence; controller?.abort(); clearTimeout(timer); loading.value = false;
  if (pageVisible() && available()) void load();
}
watch(() => [props.parentSession, props.agentId, props.enabled] as const, () => {
  ++sequence; controller?.abort(); clearTimeout(timer);
  watches.value = []; expanded.value = false; loading.value = false; error.value = false;
  if (available() && pageVisible()) void load();
}, { immediate: true });
onMounted(() => { if (typeof document !== "undefined") document.addEventListener("visibilitychange", visibilityChanged); });
onBeforeUnmount(() => {
  disposed = true; ++sequence; controller?.abort(); clearTimeout(timer);
  if (typeof document !== "undefined") document.removeEventListener("visibilitychange", visibilityChanged);
});
</script>

<template>
  <template v-if="watches.length">
    <button v-if="watches.length === 1" type="button" class="sentinel-access" :class="{ 'is-active': hasActive }" :title="title" :aria-label="title" data-testid="sentinel-access" @click="open(watches[0]!)">
      <el-icon><Connection /></el-icon><span>{{ t('sentinelAccess.label') }}</span><span class="sa-count">{{ watches.length }}</span>
    </button>
    <el-popover v-else v-model:visible="expanded" trigger="click" placement="top-end" :width="320" popper-class="sentinel-access-popover">
      <template #reference>
        <button type="button" class="sentinel-access" :class="{ 'is-active': hasActive }" :title="title" :aria-label="title" :aria-expanded="expanded" data-testid="sentinel-access">
          <el-icon><Connection /></el-icon><span>{{ t('sentinelAccess.label') }}</span><span class="sa-count">{{ watches.length }}</span>
        </button>
      </template>
      <header class="sa-heading"><span>{{ t('sentinelAccess.heading') }}</span><button type="button" class="sa-refresh" :disabled="loading" :title="t('sentinelAccess.refresh')" :aria-label="t('sentinelAccess.refresh')" @click="load"><el-icon :class="{ 'is-loading': loading }"><Refresh /></el-icon></button></header>
      <p v-if="error" class="sa-error" role="status">{{ t('sentinelAccess.refreshFailed') }}</p>
      <div class="sa-list">
        <button v-for="item in watches" :key="item.id" type="button" class="sa-row" :title="item.title" @click="open(item)">
          <span class="sa-dot" :class="{ 'is-active': active(item) }" aria-hidden="true" /><span class="sa-name">{{ item.title }}</span><span class="sa-status">{{ t(`sentinelAccess.state_${state(item)}`) }}</span>
        </button>
      </div>
    </el-popover>
  </template>
</template>

<style scoped>
.sentinel-access { display: inline-flex; align-items: center; gap: 5px; height: 30px; padding: 0 10px; border: 1px solid var(--el-border-color-lighter); border-radius: 8px; background: var(--el-bg-color); color: var(--el-text-color-secondary); cursor: pointer; font: inherit; font-size: 12px; line-height: 1; box-shadow: 0 2px 8px rgb(0 0 0 / 8%); transition: color var(--pg-transition), border-color var(--pg-transition), background-color var(--pg-transition); }
.sentinel-access:hover { color: var(--el-color-primary); border-color: var(--el-color-primary-light-5); background: var(--el-color-primary-light-9); }
.sentinel-access:focus-visible, .sa-refresh:focus-visible, .sa-row:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
.sentinel-access > .el-icon { width: 16px; height: 16px; flex: none; font-size: 16px; }
.sa-count { min-width: 16px; height: 16px; padding: 0 5px; border-radius: 8px; background: var(--el-fill-color); color: var(--el-text-color-secondary); font-size: 10px; font-weight: 600; line-height: 16px; text-align: center; }
.is-active .sa-count { background: var(--el-color-primary); color: var(--el-color-white); }
.sa-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 7px; color: var(--el-text-color-secondary); font-size: 12px; }
.sa-refresh { display: grid; place-items: center; width: 25px; height: 25px; border: 0; border-radius: 6px; background: none; color: var(--el-text-color-secondary); cursor: pointer; }
.sa-refresh:hover { background: var(--el-fill-color-light); }
.sa-refresh:disabled { opacity: .45; cursor: default; }
.sa-list { display: flex; flex-direction: column; gap: 3px; max-height: 280px; overflow: auto; }
.sa-row { display: flex; align-items: center; gap: 8px; width: 100%; padding: 9px 7px; border: 0; border-radius: 7px; background: none; color: var(--el-text-color-primary); text-align: left; cursor: pointer; font: inherit; font-size: 12px; }
.sa-row:hover { background: var(--el-fill-color-light); }
.sa-dot { width: 5px; height: 5px; flex: none; border-radius: 50%; background: var(--el-text-color-placeholder); }
.sa-dot.is-active { background: var(--el-color-primary); }
.sa-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sa-status { flex: none; color: var(--el-text-color-secondary); font-size: 10px; }
.sa-error { margin: 0 0 8px; color: var(--el-color-warning); font-size: 11px; line-height: 1.6; }
@media (prefers-reduced-motion: reduce) { .sentinel-access { transition: none; } }
</style>
