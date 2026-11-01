<script setup lang="ts">
import { computed, onBeforeUnmount, provide, ref, useId, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Refresh } from "@element-plus/icons-vue";
import { api } from "@/api/client";
import { taskCenterApi, type BackgroundTask, type BackgroundTaskRun } from "@/api/taskCenterApi";
import type { ScheduleJob, ScheduleRunRecord } from "@/api/types";
import { backgroundTaskState, isPendingTaskRun, taskCenterDate, taskRunState } from "@/agent/taskCenterPresentation";
import { inputPartsBlocks } from "@/agent/orderedInput";
import { workspaceFileNavigationKey } from "@/agent/workspaceFileNavigation";
import OrderedMessageContent from "./OrderedMessageContent.vue";
import TaskSessionResult from "./TaskSessionResult.vue";

const props = defineProps<{
  taskId: string; taskSource: "schedule"; taskSourceId: string; agentId: string;
  taskRunSession?: string; label?: string; visible: boolean;
}>();
const { t, locale } = useI18n();
const runSelectId = `task-run-${useId()}`;
// Definition attachments belong to this task, never the chat behind the dock.
provide(workspaceFileNavigationKey, { scope: () => null, open: () => {} });
const task = ref<BackgroundTask | null>(null), definition = ref<ScheduleJob | null>(null);
const runs = ref<BackgroundTaskRun[]>([]), legacyRuns = ref<ScheduleRunRecord[]>([]);
const loading = ref(false), error = ref(""), detailError = ref(""), resultError = ref(""), verifying = ref(false);
const selectedKey = ref(""), resultSession = ref(""), retainedSession = ref(""), overviewOnly = ref(false);
const lastRequestedSession = ref("");
let disposed = false, request = 0, selection = 0;
let abort: AbortController | undefined, resultAbort: AbortController | undefined, timer: ReturnType<typeof setTimeout> | undefined;
const validId = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_.-]{1,128}$/.test(value) && !value.includes("..");
const validSourceId = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$/.test(value);
const validContext = () => props.taskSource === "schedule" && validId(props.agentId) && validSourceId(props.taskSourceId) && props.taskId === `schedule:${props.taskSourceId}`;
const context = () => JSON.stringify([props.agentId, props.taskId, props.taskSource, props.taskSourceId]);
const current = (key: string, ticket: number) => !disposed && props.visible && key === context() && ticket === request;
const belongsRun = (run: BackgroundTaskRun) => run.source === "schedule" && run.agent_id === props.agentId &&
  run.source_id === props.taskSourceId && run.task_id === props.taskId && !run.workflow_id;
const date = (value?: number | string | null) => taskCenterDate(value, locale.value);
const title = computed(() => task.value?.title || definition.value?.title || props.label || props.taskId);
const blocks = computed(() => definition.value ? inputPartsBlocks(definition.value.parts?.length ? definition.value.parts : [{ type: "text", text: definition.value.prompt }]) : []);

interface RunEntry { key: string; sessionId?: string; at: number | string; state: string; summary?: string; error?: string; run?: BackgroundTaskRun }
const records = computed<RunEntry[]>(() => {
  const entries: RunEntry[] = runs.value.map(run => ({ key: run.id, sessionId: validId(run.session_id) ? run.session_id : undefined,
    at: run.created_at, state: t(`taskCenter.state_${taskRunState(run)}`), summary: run.summary, error: run.error, run }));
  const known = new Set(entries.flatMap(entry => entry.sessionId ? [entry.sessionId] : []));
  for (const [index, run] of legacyRuns.value.entries()) {
    if (run.session_id && known.has(run.session_id)) continue;
    entries.push({ key: `legacy:${run.ts}:${index}`, sessionId: validId(run.session_id) ? run.session_id : undefined, at: run.ts,
      state: run.end_reason === "waiting_human" ? t("taskCenter.state_waiting") : t(`scheduleDialog.state_${run.status}`), summary: run.summary, error: run.error });
    if (run.session_id) known.add(run.session_id);
  }
  entries.sort((a, b) => (new Date(b.at).getTime() || 0) - (new Date(a.at).getTime() || 0));
  if (retainedSession.value && !known.has(retainedSession.value)) entries.unshift({ key: `session:${retainedSession.value}`, sessionId: retainedSession.value,
    at: "", state: t("taskSessionResult.done") });
  return entries;
});
const selected = computed(() => records.value.find(record => record.key === selectedKey.value));

async function selectSession(sid: string) {
  if (!props.visible || !validContext() || !validId(sid)) return;
  if (resultSession.value === sid) return;
  resultAbort?.abort(); resultAbort = new AbortController();
  const key = context(), ticket = ++selection;
  resultSession.value = ""; resultError.value = ""; verifying.value = true;
  try {
    const response = await api.session(sid, resultAbort.signal);
    if (disposed || !props.visible || key !== context() || ticket !== selection) return;
    const identity = response.background_task;
    if (response.session_id !== sid || !identity || identity.source !== "schedule" || identity.agent_id !== props.agentId ||
        identity.task_id !== props.taskId || identity.source_id !== props.taskSourceId) {
      resultError.value = t("taskSessionResult.scopeChanged"); return;
    }
    retainedSession.value = sid; resultSession.value = sid;
    const record = records.value.find(item => item.sessionId === sid);
    selectedKey.value = record?.key || `session:${sid}`;
  } catch (issue) {
    if (!disposed && props.visible && key === context() && ticket === selection && (issue as Error)?.name !== "AbortError") resultError.value = t("taskSessionResult.unavailable");
  } finally {
    if (!disposed && key === context() && ticket === selection) verifying.value = false;
  }
}
function selectRecord(record: RunEntry | undefined) {
  if (!props.visible || !record) return;
  overviewOnly.value = false; selectedKey.value = record.key; resultError.value = "";
  if (record.sessionId) void selectSession(record.sessionId);
  else { ++selection; resultAbort?.abort(); resultSession.value = ""; verifying.value = false; }
}
function closeResult() {
  ++selection; resultAbort?.abort(); resultSession.value = ""; selectedKey.value = ""; overviewOnly.value = true; verifying.value = false;
}
async function refresh() {
  clearTimeout(timer); abort?.abort();
  if (disposed || !props.visible) return;
  if (!validContext()) { error.value = t("taskCenter.scopeChanged"); return; }
  const key = context(), ticket = ++request;
  abort = new AbortController(); loading.value = true;
  try {
    const response = await taskCenterApi.list(props.agentId, abort.signal);
    if (!current(key, ticket)) return;
    task.value = response.tasks.find(item => item.id === props.taskId && item.source === "schedule" && item.source_id === props.taskSourceId &&
      item.agent_id === props.agentId && item.executor === "agent" && !item.workflow_id) || null;
    runs.value = response.runs.filter(belongsRun).sort((a, b) => b.created_at - a.created_at);
    error.value = response.queue_error ? t("taskCenter.queueError") : ""; detailError.value = "";
    if (task.value) {
      const [job, history] = await Promise.allSettled([api.scheduleJob(props.taskSourceId), api.scheduleRuns(props.taskSourceId)]);
      if (!current(key, ticket)) return;
      if (job.status === "fulfilled" && job.value.job.id === props.taskSourceId && job.value.job.agent_id === props.agentId && !job.value.job.workflow_id) {
        definition.value = job.value.job;
        legacyRuns.value = history.status === "fulfilled" ? history.value.runs : [];
        if (history.status === "rejected") detailError.value = t("taskSessionResult.unavailable");
      } else { definition.value = null; legacyRuns.value = []; detailError.value = t("taskSessionResult.unavailable"); }
    } else { definition.value = null; legacyRuns.value = []; }
    if (overviewOnly.value) return;
    const existing = selected.value || records.value.find(item => item.sessionId === resultSession.value);
    const initial = validId(props.taskRunSession) && (!selectedKey.value || props.taskRunSession !== lastRequestedSession.value) ? props.taskRunSession : "";
    if (initial) { lastRequestedSession.value = initial; await selectSession(initial); }
    else if (existing) { selectedKey.value = existing.key; if (existing.sessionId && !resultSession.value) await selectSession(existing.sessionId); }
    else {
      const next = records.value.find(item => item.run && isPendingTaskRun(item.run)) || records.value[0];
      if (next) selectRecord(next);
      else if (!resultSession.value) detailError.value = task.value ? "" : t("taskSessionResult.unavailable");
    }
  } catch (issue) {
    if (current(key, ticket) && (issue as Error)?.name !== "AbortError") {
      error.value = t("taskSessionResult.unavailable");
      if (validId(props.taskRunSession) && (!selectedKey.value || props.taskRunSession !== lastRequestedSession.value)) { lastRequestedSession.value = props.taskRunSession; await selectSession(props.taskRunSession); }
    }
  } finally {
    if (current(key, ticket)) {
      loading.value = false;
      timer = setTimeout(() => { void refresh(); }, runs.value.some(isPendingTaskRun) ? 2500 : 12000);
    }
  }
}
watch(() => [props.agentId, props.taskId, props.taskSource, props.taskSourceId], () => {
  ++request; ++selection; abort?.abort(); resultAbort?.abort(); clearTimeout(timer);
  task.value = null; definition.value = null; runs.value = []; legacyRuns.value = []; selectedKey.value = "";
  resultSession.value = ""; retainedSession.value = ""; lastRequestedSession.value = ""; overviewOnly.value = false; error.value = ""; detailError.value = ""; resultError.value = ""; loading.value = false; verifying.value = false;
  if (props.visible) void refresh();
}, { flush: "sync" });
watch(() => props.taskRunSession, sid => {
  if (!validId(sid)) return;
  overviewOnly.value = false;
  if (props.visible) { lastRequestedSession.value = sid; void selectSession(sid); }
});
watch(() => props.visible, visible => {
  ++request; ++selection; abort?.abort(); resultAbort?.abort(); clearTimeout(timer); loading.value = false; verifying.value = false;
  if (visible) void refresh();
}, { immediate: true, flush: "sync" });
onBeforeUnmount(() => { disposed = true; ++request; ++selection; abort?.abort(); resultAbort?.abort(); clearTimeout(timer); });
</script>

<template>
  <section class="task-sheet" data-testid="task-sheet" :data-task-id="taskId">
    <header class="ts-head"><div><small>{{ t('taskCenter.title') }}</small><h2>{{ title }}</h2></div><button type="button" :disabled="loading" :aria-label="t('taskCenter.refresh')" @click="refresh"><el-icon :class="{ 'is-loading': loading }"><Refresh /></el-icon></button></header>
    <p v-if="error || detailError || resultError" class="ts-error" role="alert">{{ resultError || error || detailError }}</p>
    <details class="ts-definition" open>
      <summary>{{ t('taskCenter.instructions') }}<span v-if="task"> · {{ t(`taskCenter.taskState_${backgroundTaskState(task)}`) }}</span></summary>
      <OrderedMessageContent v-if="definition" :blocks="blocks" :parse-skill="false" />
      <p v-else>{{ task?.description || t('taskCenter.noTaskDescription') }}</p>
      <dl v-if="task"><div><dt>{{ t('taskCenter.nextRun') }}</dt><dd>{{ task.next_run ? date(task.next_run) : t('taskCenter.noNextRun') }}</dd></div><div v-if="task.expires_at"><dt>{{ t('taskCenter.expiresAt') }}</dt><dd>{{ date(task.expires_at) }}</dd></div></dl>
    </details>
    <div class="ts-history"><label :for="runSelectId">{{ t('taskCenter.history') }}</label><select :id="runSelectId" :value="selectedKey" :aria-label="t('taskCenter.history')" @change="selectRecord(records.find(item => item.key === ($event.target as HTMLSelectElement).value))"><option value="" disabled>{{ t(records.length ? 'taskCenter.choose' : 'taskCenter.noRelatedRuns') }}</option><option v-for="record in records" :key="record.key" :value="record.key">{{ date(record.at) }} · {{ record.state }}</option></select></div>
    <p v-if="verifying || loading && !records.length" class="ts-note">{{ t('taskSessionResult.loading') }}</p>
    <TaskSessionResult v-if="resultSession" :key="`${agentId}:${taskId}:${resultSession}`" :session-id="resultSession" :agent-id="agentId" :title="selected ? `${date(selected.at)} · ${selected.state}` : undefined" :visible="visible" @close="closeResult" />
    <div v-else-if="selected && !verifying" class="ts-run"><strong>{{ selected.state }}</strong><p v-if="selected.error" class="ts-error">{{ selected.error }}</p><p>{{ selected.summary || t(selected.run && isPendingTaskRun(selected.run) ? 'taskCenter.resultPending' : 'taskCenter.noSummary') }}</p></div>
    <p v-else-if="!loading && !verifying" class="ts-note">{{ t('taskCenter.noRelatedRuns') }}</p>
  </section>
</template>

<style scoped>
.task-sheet{display:flex;flex-direction:column;min-height:0;height:100%;min-width:0;padding:0 14px}.ts-head{display:flex;align-items:center;gap:10px;flex:none;padding:13px 0 10px}.ts-head>div{flex:1;min-width:0}.ts-head small{font-size:11px;color:var(--el-text-color-secondary)}.ts-head h2{font-size:15px;line-height:1.5;margin:3px 0 0;overflow-wrap:anywhere}.ts-head button{border:1px solid var(--el-border-color);background:var(--el-bg-color);color:var(--el-text-color-regular);padding:6px;border-radius:6px;cursor:pointer}.ts-definition{flex:none;max-height:32vh;overflow:auto;border-bottom:1px solid var(--el-border-color-lighter);padding:0 0 9px;font-size:12px;line-height:1.7}.ts-definition summary{cursor:pointer;color:var(--el-text-color-secondary);margin-bottom:6px}.ts-definition p{white-space:pre-wrap;overflow-wrap:anywhere}.ts-definition dl{margin:9px 0 0}.ts-definition dl>div{display:flex;gap:12px}.ts-definition dt{color:var(--el-text-color-secondary)}.ts-definition dd{margin:0}.ts-history{display:flex;align-items:center;gap:10px;flex:none;padding:10px 0;font-size:12px}.ts-history label{flex:none;color:var(--el-text-color-secondary)}.ts-history select{width:100%;min-width:0;border:1px solid var(--el-border-color);border-radius:6px;color:var(--el-text-color-regular);background:var(--el-bg-color);padding:6px;font:inherit}.ts-error{font-size:12px;line-height:1.7;color:var(--el-color-danger);margin:6px 0;white-space:pre-wrap;overflow-wrap:anywhere}.ts-note{font-size:12px;color:var(--el-text-color-secondary);line-height:1.7}.ts-run{overflow:auto;min-height:0;padding:12px 0;font-size:13px;line-height:1.8;white-space:pre-wrap;overflow-wrap:anywhere}.task-sheet>:deep(.task-session-result){flex:1;min-height:0}.task-sheet:deep(.tr-header){padding-top:8px}
</style>
