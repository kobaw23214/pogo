<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Clock, Connection, Refresh } from "@element-plus/icons-vue";
import { taskCenterApi, type BackgroundTask, type BackgroundTaskRun, type BackgroundTaskSource } from "@/api/taskCenterApi";
import { backgroundTaskState, isPendingTaskRun, taskRunState } from "@/agent/taskCenterPresentation";

export interface BackgroundTaskSelection {
  taskId: string;
  taskSource: BackgroundTaskSource;
  taskSourceId: string;
  agentId: string;
  taskRunSession?: string;
  label: string;
}
interface Entry extends BackgroundTaskSelection { state: string; active: boolean; historical: boolean; centerOnly: boolean; updated: number; }
const props = defineProps<{ agentId: string; enabled: boolean; opening?: boolean }>();
const emit = defineEmits<{ open: [selection: BackgroundTaskSelection]; center: [] }>();
const { t, locale } = useI18n();
const tasks = ref<BackgroundTask[]>([]), runs = ref<BackgroundTaskRun[]>([]);
const expanded = ref(false), loading = ref(false), failed = ref(false);
let disposed = false, sequence = 0;
let timer: ReturnType<typeof setTimeout> | undefined, controller: AbortController | undefined;
const validId = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_-]{1,160}$/.test(value);
const available = () => !disposed && props.enabled && validId(props.agentId);
const context = () => JSON.stringify([props.agentId, props.enabled]);
const pageVisible = () => typeof document === "undefined" || !document.hidden;
function scoped(item: { source: BackgroundTaskSource; source_id: string; agent_id: string; workflow_id?: string }, taskId: string) {
  const sourceValid = item.source === "schedule" ? typeof item.source_id === "string" && /^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$/.test(item.source_id) : item.source === "sentinel" && validId(item.source_id);
  return item.agent_id === props.agentId && !item.workflow_id && sourceValid && taskId === `${item.source}:${item.source_id}`;
}
const entries = computed(() => {
  const by = new Map<string, Entry>();
  for (const task of tasks.value) {
    if (!scoped(task, task.id)) continue;
    by.set(task.id, { taskId: task.id, taskSource: task.source, taskSourceId: task.source_id, agentId: task.agent_id, label: task.title || t("backgroundAccess.fallback"),
      state: backgroundTaskState(task), active: !!task.running_count || !!task.queued_count, historical: false, centerOnly: task.executor !== "agent" || !!task.workflow_id, updated: 0,
      ...(validId(task.session_id) ? { taskRunSession: task.session_id } : {}) });
  }
  // A retained execution remains discoverable after its definition is removed.
  // Group by exact host task identity, never by its display title.
  const orderedRuns = [...runs.value].sort((a, b) => Number(isPendingTaskRun(b)) - Number(isPendingTaskRun(a)) || b.created_at - a.created_at);
  const chosen = new Set<string>();
  for (const run of orderedRuns) {
    if (!scoped(run, run.task_id) || chosen.has(run.task_id)) continue;
    chosen.add(run.task_id);
    const existing = by.get(run.task_id);
    if (existing) {
      existing.updated = run.created_at; existing.active ||= isPendingTaskRun(run);
      existing.centerOnly ||= !!run.workflow_id;
      if (isPendingTaskRun(run)) existing.state = taskRunState(run);
      if (validId(run.session_id)) existing.taskRunSession = run.session_id;
    } else {
      by.set(run.task_id, { taskId: run.task_id, taskSource: run.source, taskSourceId: run.source_id, agentId: run.agent_id, label: run.title || t("backgroundAccess.fallback"),
        state: taskRunState(run), active: isPendingTaskRun(run), historical: true, centerOnly: !!run.workflow_id, updated: run.created_at, ...(validId(run.session_id) ? { taskRunSession: run.session_id } : {}) });
    }
  }
  return [...by.values()].sort((a, b) => Number(b.active) - Number(a.active) || b.updated - a.updated || a.label.localeCompare(b.label, locale.value));
});
const hasActive = computed(() => entries.value.some(entry => entry.active));
function stateLabel(entry: Entry) {
  const runStates = ["running", "queued", "waiting", "stopping", "completed", "failed", "cancelled", "interrupted"];
  return t(`taskCenter.${runStates.includes(entry.state) ? "state" : "taskState"}_${entry.state}`);
}
function schedule() {
  clearTimeout(timer);
  if (available() && pageVisible()) timer = setTimeout(() => { void load(); }, expanded.value && hasActive.value ? 2500 : 10000);
}
async function load() {
  if (!available() || !pageVisible()) return;
  const ticket = ++sequence, origin = context();
  clearTimeout(timer); controller?.abort(); const abort = new AbortController(); controller = abort; loading.value = true;
  try {
    const result = await taskCenterApi.list(props.agentId, abort.signal);
    if (!available() || ticket !== sequence || context() !== origin) return;
    if (!result || !Array.isArray(result.tasks) || !Array.isArray(result.runs)) throw new Error("Invalid task list");
    tasks.value = result.tasks.filter(task => scoped(task, task.id));
    runs.value = result.runs.filter(run => scoped(run, run.task_id));
    failed.value = !!result.queue_error;
  } catch (issue) {
    if (available() && ticket === sequence && context() === origin && (issue as Error)?.name !== "AbortError") failed.value = true;
  } finally {
    if (ticket === sequence && context() === origin) { loading.value = false; schedule(); }
  }
}
function open(entry: Entry) {
  if (!available() || props.opening || !entries.value.some(item => item.taskId === entry.taskId && item.agentId === props.agentId)) return;
  expanded.value = false;
  if (entry.centerOnly) { emit("center"); return; }
  emit("open", { taskId: entry.taskId, taskSource: entry.taskSource, taskSourceId: entry.taskSourceId, agentId: entry.agentId, label: entry.label,
    ...(entry.taskRunSession ? { taskRunSession: entry.taskRunSession } : {}) });
}
function visibilityChanged() {
  ++sequence; controller?.abort(); clearTimeout(timer); loading.value = false;
  if (available() && pageVisible()) void load();
}
watch(() => [props.agentId, props.enabled], () => {
  ++sequence; controller?.abort(); clearTimeout(timer); tasks.value = []; runs.value = []; expanded.value = false; loading.value = false; failed.value = false;
  if (available() && pageVisible()) void load();
}, { immediate: true });
watch(expanded, value => { if (value && available()) void load(); });
onMounted(() => document.addEventListener("visibilitychange", visibilityChanged));
onBeforeUnmount(() => { disposed = true; ++sequence; controller?.abort(); clearTimeout(timer); document.removeEventListener("visibilitychange", visibilityChanged); });
</script>

<template>
  <el-popover v-model:visible="expanded" trigger="click" placement="top-end" :width="340" popper-class="background-access-popover">
    <template #reference>
      <button type="button" class="background-access" :class="{ 'is-active': hasActive }" :disabled="!enabled || !agentId || opening" :title="t(!enabled || !agentId ? 'backgroundAccess.needsAgent' : opening ? 'backgroundAccess.opening' : 'backgroundAccess.open')" :aria-label="t('backgroundAccess.open')" :aria-expanded="expanded" data-testid="background-tasks-access">
        <el-icon><Clock /></el-icon><span>{{ t('backgroundAccess.label') }}</span><span class="ba-count">{{ entries.length }}</span>
      </button>
    </template>
    <header class="ba-heading"><span>{{ t('backgroundAccess.heading') }}</span><button type="button" class="ba-refresh" :disabled="loading" :aria-label="t('backgroundAccess.refresh')" @click="load"><el-icon :class="{ 'is-loading': loading }"><Refresh /></el-icon></button></header>
    <p v-if="failed" class="ba-error" role="status">{{ t('backgroundAccess.failed') }} <button type="button" :disabled="loading" @click="load">{{ t('backgroundAccess.refresh') }}</button></p>
    <p v-if="loading && !entries.length" class="ba-empty">{{ t('backgroundAccess.loading') }}</p>
    <p v-else-if="!failed && !entries.length" class="ba-empty">{{ t('backgroundAccess.empty') }}</p>
    <div class="ba-list">
      <button v-for="entry in entries" :key="entry.taskId" type="button" class="ba-row" :disabled="opening" :data-task-id="entry.taskId" @click="open(entry)">
        <el-icon class="ba-kind"><Connection v-if="entry.taskSource === 'sentinel'" /><Clock v-else /></el-icon>
        <span class="ba-copy"><span class="ba-title">{{ entry.label }}</span><small>{{ t(entry.taskSource === 'sentinel' ? 'backgroundAccess.sentinel' : 'backgroundAccess.schedule') }}<template v-if="entry.historical"> · {{ t('backgroundAccess.history') }}</template></small></span>
        <span class="ba-state" :class="{ active: entry.active }">{{ entry.centerOnly ? t('backgroundAccess.manage') : stateLabel(entry) }}</span>
      </button>
    </div>
    <button type="button" class="ba-center" @click="expanded = false; emit('center')">{{ t('backgroundAccess.manage') }}</button>
  </el-popover>
</template>

<style scoped>
.background-access{display:inline-flex;align-items:center;gap:5px;height:30px;padding:0 10px;border:1px solid var(--el-border-color-lighter);border-radius:8px;background:var(--el-bg-color);color:var(--el-text-color-secondary);cursor:pointer;font:inherit;font-size:12px;line-height:1;box-shadow:0 2px 8px rgb(0 0 0 / 8%)}.background-access:hover:not(:disabled){color:var(--el-color-primary);border-color:var(--el-color-primary-light-5);background:var(--el-color-primary-light-9)}button:disabled{opacity:.5;cursor:default}.background-access>.el-icon{width:16px;height:16px;font-size:16px}.ba-count{min-width:16px;height:16px;padding:0 5px;border-radius:8px;background:var(--el-fill-color);font-size:10px;font-weight:600;line-height:16px;text-align:center}.is-active .ba-count{background:var(--el-color-primary);color:var(--el-color-white)}.ba-heading{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:7px;font-size:12px;color:var(--el-text-color-secondary)}.ba-refresh{display:grid;place-items:center;width:25px;height:25px;border:0;border-radius:6px;background:none;color:var(--el-text-color-secondary);cursor:pointer}.ba-list{display:flex;flex-direction:column;gap:3px;max-height:300px;overflow:auto}.ba-row{display:flex;align-items:center;gap:8px;width:100%;padding:10px 7px;border:0;border-radius:7px;background:none;color:var(--el-text-color-primary);text-align:left;font:inherit;font-size:12px;cursor:pointer}.ba-row:hover{background:var(--el-fill-color-light)}.ba-kind{flex:none;color:var(--el-text-color-secondary)}.ba-copy{display:flex;flex:1;min-width:0;flex-direction:column;gap:4px}.ba-title{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.ba-copy small,.ba-state{font-size:10px;color:var(--el-text-color-secondary)}.ba-state{flex:none}.ba-state.active{color:var(--el-color-primary)}.ba-error,.ba-empty{margin:8px 0 12px;font-size:12px;line-height:1.7;color:var(--el-text-color-secondary)}.ba-error{color:var(--el-color-warning)}.ba-error button{font:inherit;border:0;background:none;color:inherit;text-decoration:underline;cursor:pointer}.ba-center{width:100%;margin-top:9px;padding:9px;border:1px solid var(--el-border-color-lighter);border-radius:7px;background:var(--el-bg-color);color:var(--el-color-primary);font:inherit;font-size:12px;cursor:pointer}button:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:2px}
</style>
