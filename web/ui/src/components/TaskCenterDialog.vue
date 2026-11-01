<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowLeft, ArrowRight, Clock, Close, Collection, Connection, EditPen, Loading, Plus, Refresh, VideoPlay } from "@element-plus/icons-vue";
import { api } from "@/api/client";
import { taskCenterApi, type BackgroundTask, type BackgroundTaskRun } from "@/api/taskCenterApi";
import type { ModelInfo, ScheduleJob, ScheduleRunRecord, ScheduleRunsInfo } from "@/api/types";
import { backgroundTaskState, isPendingTaskRun, latestTaskRun, matchesTaskQuery, runResultIdentity, taskCenterDate, taskCenterNeedsAttention, taskRunLane, taskRunNeedsAttention, taskRunState, type TaskCenterView, type TaskRunLane } from "@/agent/taskCenterPresentation";
import { describeSchedule, type ScheduleDraft } from "@/agent/schedulePresentation";
import { inputPartsBlocks } from "@/agent/orderedInput";
import OrderedMessageContent from "./OrderedMessageContent.vue";
import ScheduleEditorDialog, { type ScheduleAgentOption } from "./ScheduleEditorDialog.vue";
import ConfirmDialog from "./ConfirmDialog.vue";

const props = withDefaults(defineProps<{ modelValue: boolean; agentId?: string; agentName?: string; sessionId?: string | null; agents?: ScheduleAgentOption[]; models?: ModelInfo[]; initialDraft?: ScheduleDraft | null; initialResultSessionId?: string; isAdmin?: boolean; draftBusy?: boolean }>(), { agentId: "", agentName: "", sessionId: "", agents: () => [], models: () => [], initialDraft: null, initialResultSessionId: "", isAdmin: false, draftBusy: false });
const emit = defineEmits<{ "update:modelValue": [boolean]; attention: [boolean]; "open-task": [taskId: string, label: string, agentId: string, sessionId?: string]; "open-sentinel": [sentinelId: string, label: string, agentId: string, parentSessionId?: string]; "open-workflow-run": [runId: string, workflowId?: string]; "request-draft": []; saved: [job: ScheduleJob] }>();
const { t, locale } = useI18n();
const tasks = ref<BackgroundTask[]>([]), runs = ref<BackgroundTaskRun[]>([]), view = ref<TaskCenterView>("tasks"), query = ref(""), filter = ref("all");
const loading = ref(false), loadError = ref(""), queueError = ref(""), actionError = ref(""), actionId = ref("");
const selectedTaskId = ref(""), selectedRunId = ref(""), mobileDetail = ref(false);
const selectedSchedule = ref<ScheduleJob | null>(null), olderRuns = ref<ScheduleRunsInfo | null>(null), detailLoading = ref(false), detailError = ref("");
const editorOpen = ref(false), editing = ref<ScheduleJob | null>(null), editorDraft = ref<ScheduleDraft | null>(null), editorVersion = ref(0);
const editor = ref<InstanceType<typeof ScheduleEditorDialog> | null>(null), confirm = ref<InstanceType<typeof ConfirmDialog> | null>(null);
let disposed = false, loadSequence = 0, detailSequence = 0, editorSequence = 0, resultSequence = 0, acceptedDraftContext = "";
let pollTimer: ReturnType<typeof setTimeout> | undefined, loadAbort: AbortController | undefined;
const context = () => JSON.stringify([props.agentId, props.sessionId]);
const belongs = (item: { agent_id: string }) => !!props.agentId && item.agent_id === props.agentId;
const selectedTask = computed(() => tasks.value.find(task => task.id === selectedTaskId.value && belongs(task)) || null);
const selectedRun = computed(() => runs.value.find(run => run.id === selectedRunId.value && belongs(run)) || null);
const queuedRuns = computed(() => runs.value.filter(isPendingTaskRun));
const historyRuns = computed(() => runs.value.filter(run => !isPendingTaskRun(run)).sort((a, b) => b.created_at - a.created_at));
const attention = computed(() => taskCenterNeedsAttention(tasks.value, runs.value) || !!queueError.value);
const visibleTasks = computed(() => tasks.value.filter(task => matchesTaskQuery(task, query.value) && (filter.value === "all" || filter.value === "attention" && task.attention || filter.value === task.trigger)).sort((a, b) => Number(b.attention) - Number(a.attention) || a.title.localeCompare(b.title, locale.value)));
const visibleRuns = computed(() => (view.value === "queue" ? queuedRuns.value : historyRuns.value).filter(run => matchesTaskQuery(run, query.value) && (filter.value === "all" || filter.value === "attention" && taskRunNeedsAttention(run) || filter.value === (run.source === "sentinel" ? "condition" : "time"))));
const selectedTaskRuns = computed(() => runs.value.filter(run => run.task_id === selectedTask.value?.id).sort((a, b) => b.created_at - a.created_at));
const selectedInterruptedRun = computed(() => {
  const latest = selectedTask.value && latestTaskRun(selectedTask.value.id, runs.value);
  return latest?.status === "interrupted" ? latest : null;
});
const selectedBlocks = computed(() => selectedSchedule.value ? inputPartsBlocks(selectedSchedule.value.parts?.length ? selectedSchedule.value.parts : [{ type: "text", text: selectedSchedule.value.prompt }]) : []);
const lanes: TaskRunLane[] = ["running", "queued", "waiting"];
const laneRuns = (lane: TaskRunLane) => visibleRuns.value.filter(run => taskRunLane(run) === lane).sort((a, b) => a.created_at - b.created_at);
const date = (value: string | number | null | undefined) => taskCenterDate(value, locale.value), state = backgroundTaskState, runState = taskRunState;
const textError = (value: unknown) => value instanceof Error ? value.message : String(value);
const currentExecuting = (task: BackgroundTask) => runs.value.filter(run => run.task_id === task.id && run.status === "running" && !run.waiting_human).length;
const currentWaiting = (task: BackgroundTask) => runs.value.filter(run => run.task_id === task.id && run.waiting_human).length;
const editable = (task: BackgroundTask) => belongs(task) && task.source === "schedule" && task.executor === "agent" && task.can_manage && !task.running_count && !task.queued_count && !runs.value.some(run => run.task_id === task.id && isPendingTaskRun(run));
const canRunNow = (task: BackgroundTask) => editable(task) && latestTaskRun(task.id, runs.value)?.status !== "interrupted" && !["expired", "unavailable", "blocked"].includes(task.state);
const canToggle = (task: BackgroundTask) => task.can_manage && (task.enabled || task.state !== "expired");
function triggerText(run: BackgroundTaskRun) { return t(`taskCenter.${run.trigger === "manual" ? "sourceManual" : run.trigger === "retry" ? "sourceRetry" : run.source === "sentinel" ? "sourceSentinel" : "sourceSchedule"}`); }
function taskTrigger(task: BackgroundTask) {
  if (task.trigger === "condition") return t("taskCenter.condition");
  if (selectedSchedule.value?.id === task.source_id) { try { return describeSchedule(selectedSchedule.value, t); } catch { /* host's next-run time remains available */ } }
  return t("taskCenter.time");
}
function schedulePoll() {
  clearTimeout(pollTimer); if (disposed || !props.modelValue) return;
  pollTimer = setTimeout(async () => { await load(true); schedulePoll(); }, queuedRuns.value.length ? 2500 : 12000);
}
async function load(silent = false) {
  const ticket = ++loadSequence, origin = context(); loadAbort?.abort(); loadAbort = new AbortController();
  if (!props.agentId) { tasks.value = []; runs.value = []; loading.value = false; loadError.value = t("taskCenter.needsAgent"); return; }
  if (!silent) loading.value = true; loadError.value = "";
  try {
    const result = await taskCenterApi.list(props.agentId, loadAbort.signal);
    if (disposed || ticket !== loadSequence || origin !== context() || !props.modelValue) return;
    tasks.value = (result.tasks || []).filter(belongs); runs.value = (result.runs || []).filter(belongs); queueError.value = result.queue_error || "";
    if (!tasks.value.some(task => task.id === selectedTaskId.value)) {
      selectedTaskId.value = visibleTasks.value[0]?.id || ""; selectedSchedule.value = null; olderRuns.value = null;
      if (selectedTask.value && view.value === "tasks") void loadTaskDetail(selectedTask.value);
    }
    if (!runs.value.some(run => run.id === selectedRunId.value)) selectedRunId.value = visibleRuns.value[0]?.id || "";
    emit("attention", attention.value);
  } catch (error) { if (!disposed && ticket === loadSequence && origin === context() && props.modelValue && (error as Error)?.name !== "AbortError") loadError.value = textError(error); }
  finally { if (!disposed && ticket === loadSequence && origin === context()) loading.value = false; }
}
async function loadTaskDetail(task: BackgroundTask) {
  const ticket = ++detailSequence, origin = context(); selectedSchedule.value = null; olderRuns.value = null; detailError.value = "";
  if (!belongs(task) || task.source !== "schedule" || task.executor !== "agent") { detailLoading.value = false; return; }
  detailLoading.value = true;
  const current = () => !disposed && origin === context() && ticket === detailSequence && props.modelValue && selectedTaskId.value === task.id;
  try {
    const result = await api.scheduleJob(task.source_id); if (!current()) return;
    if (result.job.id !== task.source_id || result.job.agent_id !== props.agentId || result.job.workflow_id) { detailError.value = t("taskCenter.scopeChanged"); return; }
    selectedSchedule.value = result.job;
    try { const history = await api.scheduleRuns(task.source_id); if (current()) olderRuns.value = history; } catch (error) { if (current()) detailError.value = textError(error); }
  } catch (error) { if (current()) detailError.value = textError(error); }
  finally { if (current()) detailLoading.value = false; }
}
function switchView(next: TaskCenterView) {
  view.value = next; mobileDetail.value = false; actionError.value = "";
  if (next === "tasks" && selectedTask.value && !selectedSchedule.value) void loadTaskDetail(selectedTask.value);
  if (next !== "tasks" && !visibleRuns.value.some(run => run.id === selectedRunId.value)) selectedRunId.value = visibleRuns.value[0]?.id || "";
}
function selectTask(task: BackgroundTask) { if (belongs(task)) { selectedTaskId.value = task.id; mobileDetail.value = true; actionError.value = ""; void loadTaskDetail(task); } }
function selectRun(run: BackgroundTaskRun) { if (belongs(run)) { selectedRunId.value = run.id; mobileDetail.value = true; actionError.value = ""; } }
function showRelatedRun(run: BackgroundTaskRun) { switchView(isPendingTaskRun(run) ? "queue" : "history"); selectRun(run); }
function showRunTask(run: BackgroundTaskRun) { const task = tasks.value.find(item => item.id === run.task_id); if (task) { switchView("tasks"); selectTask(task); } }
async function confirmEditorLeave() { return !editorOpen.value || !editor.value || await editor.value.confirmLeave(); }
async function openCreate(draft?: ScheduleDraft | null) {
  const origin = context(), ticket = ++editorSequence;
  if (!props.agentId || draft?.agent_id && draft.agent_id !== props.agentId) { actionError.value = t("taskCenter.scopeChanged"); return; }
  if (!await confirmEditorLeave() || disposed || origin !== context() || ticket !== editorSequence || !props.modelValue) return;
  editing.value = null; editorDraft.value = draft ? JSON.parse(JSON.stringify(draft)) : null; editorVersion.value++; editorOpen.value = true;
}
async function openEdit(task: BackgroundTask) {
  if (!editable(task) || actionId.value) return; const origin = context(), ticket = ++editorSequence;
  if (!await confirmEditorLeave() || disposed || origin !== context() || ticket !== editorSequence || !props.modelValue) return;
  actionId.value = task.id; actionError.value = "";
  try {
    const result = await api.scheduleJob(task.source_id);
    if (disposed || origin !== context() || ticket !== editorSequence || !props.modelValue) return;
    if (result.job.id !== task.source_id || result.job.agent_id !== props.agentId || result.job.workflow_id) { actionError.value = t("taskCenter.unavailableEditor"); return; }
    editing.value = result.job; editorDraft.value = null; editorVersion.value++; editorOpen.value = true;
  } catch (error) { if (!disposed && origin === context()) actionError.value = textError(error); }
  finally { if (!disposed && origin === context()) actionId.value = ""; }
}
async function requestDraft() {
  const origin = context(), ticket = ++editorSequence;
  if (!await confirmEditorLeave() || disposed || origin !== context() || ticket !== editorSequence || !props.modelValue) return;
  acceptedDraftContext = origin; emit("request-draft");
}
async function saved(job: ScheduleJob) {
  if (disposed || !props.modelValue || job.agent_id !== props.agentId || job.workflow_id) return;
  emit("saved", job); view.value = "tasks"; selectedTaskId.value = `schedule:${job.id}`; mobileDetail.value = true;
  await load(true); if (selectedTask.value) await loadTaskDetail(selectedTask.value);
}
async function mutateTask(task: BackgroundTask, action: "pause" | "resume" | "run" | "remove") {
  if (disposed || !props.modelValue || !belongs(task) || !task.can_manage || actionId.value || action === "run" && !canRunNow(task) || action === "remove" && !editable(task) || action === "resume" && !canToggle(task)) return;
  const origin = context();
  if (action === "remove" && !await confirm.value?.ask({ title: t("taskCenter.removeTitle"), body: t("taskCenter.removeBody"), confirmText: t("taskCenter.deleteConfirm"), cancelText: t("taskCenter.cancel"), danger: true })) return;
  if (disposed || origin !== context() || !props.modelValue) return; actionId.value = task.id; actionError.value = "";
  try {
    if (action === "run") await api.scheduleRunNow(task.source_id); else if (action === "remove") await api.scheduleDeleteJob(task.source_id); else await taskCenterApi.setEnabled(task.source, task.source_id, action === "resume");
    if (disposed || origin !== context() || !props.modelValue) return;
    await load(true); if (action === "run") switchView("queue");
  } catch (error) { if (!disposed && origin === context()) actionError.value = textError(error); }
  finally { if (!disposed && origin === context()) actionId.value = ""; }
}
async function mutateRun(run: BackgroundTaskRun, action: "cancel" | "retry") {
  if (disposed || !props.modelValue || !belongs(run) || actionId.value || (action === "cancel" ? !run.can_cancel || run.cancel_requested : !run.can_retry)) return;
  const origin = context(), queued = run.status === "queued";
  const approved = await confirm.value?.ask({ title: t(`taskCenter.${action === "retry" ? "retryTitle" : queued ? "cancelQueuedTitle" : "cancelTitle"}`), body: t(`taskCenter.${action === "retry" ? "retryBody" : queued ? "cancelQueuedBody" : "cancelBody"}`), confirmText: t(`taskCenter.${action === "retry" ? "retryConfirm" : queued ? "cancelQueuedConfirm" : "cancelConfirm"}`), cancelText: t("taskCenter.cancel"), danger: action === "cancel" });
  if (!approved || disposed || origin !== context() || !props.modelValue) return; actionId.value = run.id; actionError.value = "";
  try { if (action === "cancel") await taskCenterApi.cancel(run.id, run.revision); else await taskCenterApi.retry(run.id, run.revision); if (!disposed && origin === context() && props.modelValue) { await load(true); if (action === "retry") switchView("queue"); } }
  catch (error) { if (!disposed && origin === context()) actionError.value = textError(error); }
  finally { if (!disposed && origin === context()) actionId.value = ""; }
}
function sentinelIdentity(item: BackgroundTask | BackgroundTaskRun): string | null {
  if (!belongs(item) || item.source !== "sentinel") return null;
  const id = item.source_id || item.sentinel_id || "";
  if (typeof id !== "string" || !/^[a-zA-Z0-9_-]{1,160}$/.test(id) || item.sentinel_id && item.sentinel_id !== id) return null;
  return id;
}
function canOpenRunResult(run: BackgroundTaskRun): boolean {
  return !disposed && props.modelValue && belongs(run) && (run.source === "sentinel" ? !!sentinelIdentity(run) : !!runResultIdentity(run));
}
function canOpenTaskResult(task: BackgroundTask): boolean {
  if (disposed || !props.modelValue || !belongs(task)) return false;
  return task.source === "sentinel" ? !!sentinelIdentity(task) : !!(task.result_url || selectedSchedule.value?.session_id || selectedTaskRuns.value.some(canOpenRunResult));
}
function openRunResult(run: BackgroundTaskRun) {
  if (disposed || !props.modelValue || !belongs(run)) return;
  if (run.source === "sentinel") {
    const id = sentinelIdentity(run); if (!id) return;
    const task = tasks.value.find(item => belongs(item) && item.source === "sentinel" && sentinelIdentity(item) === id);
    emit("open-sentinel", id, run.title, run.agent_id, run.parent_session_id || task?.parent_session_id);
    return;
  }
  const result = runResultIdentity(run); if (!result) return;
  if (result.type === "workflow") emit("open-workflow-run", result.id, result.workflowId); else void openSessionResult(result.id, run.title, result.agentId, run.task_id);
}
function openTaskResult(task: BackgroundTask) {
  if (disposed || !props.modelValue || !belongs(task)) return;
  if (task.source === "sentinel") {
    const id = sentinelIdentity(task); if (id) emit("open-sentinel", id, task.title, task.agent_id, task.parent_session_id);
    return;
  }
  const recent = selectedTaskRuns.value.find(canOpenRunResult); if (recent) { openRunResult(recent); return; }
  const result = runResultIdentity({ agent_id: task.agent_id, workflow_id: task.workflow_id, result_url: task.result_url, session_id: selectedSchedule.value?.id === task.source_id ? selectedSchedule.value.session_id : undefined });
  if (result?.type === "workflow") emit("open-workflow-run", result.id, result.workflowId); else if (result) void openSessionResult(result.id, task.title, result.agentId, task.id);
}
function openOlderResult(run: ScheduleRunRecord) { if (selectedTask.value && belongs(selectedTask.value) && run.session_id) void openSessionResult(run.session_id, selectedTask.value.title, props.agentId, selectedTask.value.id); }
async function openSessionResult(id: string, title: string, agentId: string, taskId?: string) {
  if (disposed || !props.modelValue || !props.agentId || agentId !== props.agentId || !/^[A-Za-z0-9_-]{1,160}$/.test(id)) return;
  const origin = context(), ticket = ++resultSequence;
  const validTask = (value: unknown): value is string => typeof value === "string" && /^schedule:[A-Za-z0-9][A-Za-z0-9._-]{0,39}$/.test(value);
  if (validTask(taskId)) { emit("open-task", taskId, title, agentId, id); return; }
  // A legacy result-only deep link carries no task id. Recover it from the
  // authenticated host response instead of inferring it from a title or URL.
  try {
    const result = await api.session(id);
    if (disposed || !props.modelValue || origin !== context() || ticket !== resultSequence) return;
    const identity = result.background_task;
    if (result.session_id !== id || !identity || identity.source !== "schedule" || identity.agent_id !== props.agentId ||
        !validTask(identity.task_id) || identity.task_id !== `schedule:${identity.source_id}`) { actionError.value = t("taskCenter.scopeChanged"); return; }
    emit("open-task", identity.task_id, title, agentId, id);
  } catch { if (!disposed && props.modelValue && origin === context() && ticket === resultSequence) actionError.value = t("taskSessionResult.unavailable"); }
}
async function close() { const origin = context(); if (!await confirmEditorLeave() || disposed || origin !== context()) return; editorOpen.value = false; emit("update:modelValue", false); }
watch(() => [props.modelValue, props.agentId, props.sessionId] as const, ([open]) => {
  loadSequence++; detailSequence++; editorSequence++; resultSequence++; clearTimeout(pollTimer); loadAbort?.abort();
  tasks.value = []; runs.value = []; selectedTaskId.value = ""; selectedRunId.value = ""; selectedSchedule.value = null; olderRuns.value = null;
  loading.value = false; detailLoading.value = false; loadError.value = ""; queueError.value = ""; actionError.value = ""; actionId.value = ""; mobileDetail.value = false;
  editorOpen.value = false; editing.value = null; editorDraft.value = null; acceptedDraftContext = "";
  if (open && props.initialResultSessionId) void openSessionResult(props.initialResultSessionId, "", props.agentId);
  if (open) void load().then(schedulePoll);
}, { immediate: true });
watch(() => props.initialResultSessionId, id => {
  if (!props.modelValue || disposed) return;
  if (id) void openSessionResult(id, "", props.agentId);
});
watch(() => props.initialDraft?.key, async key => { if (!key || !props.initialDraft || !props.modelValue) return; const draft = props.initialDraft; if (acceptedDraftContext === context()) { acceptedDraftContext = ""; editorOpen.value = false; } await openCreate(draft); }, { immediate: true });
onBeforeUnmount(() => { disposed = true; loadSequence++; detailSequence++; editorSequence++; resultSequence++; loadAbort?.abort(); clearTimeout(pollTimer); });
</script>

<template>
  <section v-show="modelValue" class="task-center-sheet" data-testid="task-center" :aria-label="t('taskCenter.title')">
    <header class="tc-header">
      <div class="tc-brand">
        <div><h1>{{ t('taskCenter.title') }}</h1><p>{{ agentName }}<span v-if="agentName"> · </span>{{ t('taskCenter.subtitle') }}</p></div>
      </div>
      <div class="tc-header-actions">
        <button type="button" class="tc-primary" :disabled="!agentId || draftBusy" data-testid="task-create" @click="openCreate()"><el-icon><Plus /></el-icon>{{ t('taskCenter.create') }}</button>
        <button type="button" class="tc-icon" :aria-label="t('taskCenter.close')" @click="close"><el-icon><Close /></el-icon></button>
      </div>
    </header>
    <nav class="tc-main-tabs" :aria-label="t('taskCenter.title')"><button v-for="tab in ['tasks','queue','history'] as const" :key="tab" type="button" :class="{ active: view === tab }" :aria-pressed="view === tab" :data-testid="`task-view-${tab}`" @click="switchView(tab)">{{ t(`taskCenter.${tab}`) }}<span>{{ tab === 'tasks' ? tasks.length : tab === 'queue' ? queuedRuns.length : historyRuns.length }}</span></button><span v-if="runs.some(run => run.waiting_human)" class="tc-needs-attention">{{ t('taskCenter.waitingCount', { n: runs.filter(run => run.waiting_human).length }) }}</span></nav>
    <div v-if="loadError || queueError" class="tc-top-error" role="alert"><strong v-if="queueError">{{ t('taskCenter.queueError') }}</strong> {{ loadError || queueError }} <button type="button" class="tc-link" @click="load()">{{ t('taskCenter.retryLoad') }}</button></div>
    <div class="tc-layout" :class="{ 'tc-mobile-detail': mobileDetail }">
      <aside class="tc-list-pane">
        <div class="tc-filter"><input v-model="query" type="search" :placeholder="t('taskCenter.search')" :aria-label="t('taskCenter.search')" data-testid="task-search" /><select v-model="filter" :aria-label="t('taskCenter.filter')"><option value="all">{{ t('taskCenter.all') }}</option><option value="time">{{ t('taskCenter.time') }}</option><option value="condition">{{ t('taskCenter.condition') }}</option><option value="attention">{{ t('taskCenter.attention') }}</option></select></div>
        <div class="tc-list-heading"><span>{{ t(view === 'tasks' ? 'taskCenter.taskCount' : 'taskCenter.runCount', { n: view === 'tasks' ? visibleTasks.length : visibleRuns.length }) }}</span><button type="button" class="tc-icon" :aria-label="t('taskCenter.refresh')" :disabled="loading" @click="load()"><el-icon :class="{ 'is-loading': loading }"><Refresh /></el-icon></button></div>
        <div v-if="loading && !tasks.length && !runs.length" class="tc-empty">{{ t('taskCenter.loading') }}</div>
        <div v-else-if="view === 'tasks' && !visibleTasks.length || view !== 'tasks' && !visibleRuns.length" class="tc-empty"><el-icon><Collection /></el-icon><strong>{{ t(query || filter !== 'all' ? 'taskCenter.noMatches' : view === 'tasks' ? 'taskCenter.taskEmpty' : view === 'queue' ? 'taskCenter.queueEmpty' : 'taskCenter.historyEmpty') }}</strong><p>{{ t(view === 'tasks' ? 'taskCenter.taskEmptyHint' : view === 'queue' ? 'taskCenter.queueEmptyHint' : 'taskCenter.historyEmptyHint') }}</p></div>
        <div v-if="view === 'tasks'" class="tc-list" data-testid="task-list"><button v-for="task in visibleTasks" :key="task.id" type="button" class="tc-task-card" :class="{ selected: selectedTaskId === task.id }" :data-task-id="task.id" @click="selectTask(task)"><div class="tc-card-top"><span class="tc-source-icon"><el-icon><Connection v-if="task.trigger === 'condition'" /><Clock v-else /></el-icon></span><strong>{{ task.title }}</strong><span v-if="task.attention" class="tc-attention-dot" :aria-label="t('taskCenter.attention')" /></div><p class="tc-card-description">{{ task.description || t('taskCenter.noTaskDescription') }}</p><div class="tc-card-meta"><span class="tc-status" :class="state(task)">{{ t(`taskCenter.taskState_${state(task)}`) }}</span><span v-if="currentExecuting(task)" class="tc-running-count"><el-icon class="is-loading"><Loading /></el-icon>{{ t('taskCenter.runningCount', { n: currentExecuting(task) }) }}</span><span v-else-if="currentWaiting(task)">{{ t('taskCenter.waitingCount', { n: currentWaiting(task) }) }}</span><span v-else-if="task.queued_count">{{ t('taskCenter.queuedCount', { n: task.queued_count }) }}</span><span v-else-if="task.pending_events">{{ t('taskCenter.eventCount', { n: task.pending_events }) }}</span><span v-else>{{ task.trigger === 'condition' ? t('taskCenter.idle') : task.next_run ? date(task.next_run) : '—' }}</span></div></button></div>
        <div v-else class="tc-list" data-testid="task-run-list"><template v-if="view === 'queue'"><section v-for="lane in lanes" :key="lane" class="tc-lane"><h2 v-if="laneRuns(lane).length">{{ t(`taskCenter.lane_${lane}`) }} <span>{{ laneRuns(lane).length }}</span></h2><button v-for="run in laneRuns(lane)" :key="run.id" type="button" class="tc-run-card" :class="{ selected: selectedRunId === run.id }" :data-run-id="run.id" @click="selectRun(run)"><div class="tc-run-top"><el-icon v-if="run.status === 'running' && !run.waiting_human" class="is-loading tc-running-count"><Loading /></el-icon><strong>{{ run.test_run ? t('workflowAuthoring.testLabel') + ' · ' + run.title : run.title }}</strong><span class="tc-status" :class="runState(run)">{{ t(`taskCenter.state_${runState(run)}`) }}</span></div><p>{{ triggerText(run) }} · {{ date(run.created_at) }}</p></button></section></template><template v-else><button v-for="run in visibleRuns" :key="run.id" type="button" class="tc-run-card" :class="{ selected: selectedRunId === run.id }" :data-run-id="run.id" @click="selectRun(run)"><div class="tc-run-top"><strong>{{ run.test_run ? t('workflowAuthoring.testLabel') + ' · ' + run.title : run.title }}</strong><span class="tc-status" :class="runState(run)">{{ t(`taskCenter.state_${runState(run)}`) }}</span></div><p class="tc-card-description">{{ run.summary || run.error || triggerText(run) }}</p><p>{{ date(run.finished_at || run.created_at) }}</p></button></template></div>
        <p v-if="view === 'tasks' && filter === 'condition'" class="tc-watch-hint">{{ t('taskCenter.monitorCreation') }}</p>
      </aside>
      <main class="tc-detail-pane">
        <p v-if="actionError" class="tc-error" role="alert">{{ actionError }}</p>
        <template v-if="view === 'tasks' && selectedTask">
          <header class="tc-detail-heading"><button type="button" class="tc-icon tc-back" :aria-label="t('taskCenter.back')" @click="mobileDetail = false"><el-icon><ArrowLeft /></el-icon><span>{{ t('taskCenter.back') }}</span></button><div><small>{{ t('taskCenter.taskDefinition') }}</small><h2>{{ selectedTask.title }}</h2></div><button v-if="selectedTask.source === 'schedule'" type="button" class="tc-secondary" :disabled="!editable(selectedTask) || !!actionId" @click="openEdit(selectedTask)"><el-icon><EditPen /></el-icon>{{ t('taskCenter.edit') }}</button></header>
          <div class="tc-overview"><span class="tc-status" :class="state(selectedTask)">{{ t(`taskCenter.taskState_${state(selectedTask)}`) }}</span><span>{{ taskTrigger(selectedTask) }}</span><span>·</span><span>{{ t(`taskCenter.executor_${selectedTask.executor}`) }}</span></div>
          <div class="tc-task-actions"><button v-if="selectedTask.source === 'schedule' && selectedTask.executor === 'agent'" type="button" class="tc-primary" :disabled="!!actionId || !canRunNow(selectedTask)" data-testid="task-run-now" @click="mutateTask(selectedTask, 'run')"><el-icon><VideoPlay /></el-icon>{{ t('taskCenter.runNow') }}</button><button type="button" class="tc-secondary" :disabled="!!actionId || !canToggle(selectedTask)" data-testid="task-toggle" @click="mutateTask(selectedTask, selectedTask.enabled ? 'pause' : 'resume')">{{ t(selectedTask.enabled ? 'taskCenter.pauseTask' : 'taskCenter.resumeTask') }}</button><button v-if="canOpenTaskResult(selectedTask)" type="button" class="tc-link" data-testid="task-open-result" @click="openTaskResult(selectedTask)">{{ t(selectedTask.source === 'sentinel' ? 'taskCenter.viewSentinelSession' : 'taskCenter.lastResult') }} →</button></div>
          <p v-if="selectedInterruptedRun" class="tc-footnote tc-review-note" data-testid="task-review-note">{{ t('taskCenter.reviewBeforeRun') }} <button type="button" class="tc-link" data-testid="task-review-last-run" @click="showRelatedRun(selectedInterruptedRun)">{{ t('taskCenter.reviewLastRun') }} →</button></p>
          <p class="tc-footnote">{{ t('taskCenter.pauseHint') }}</p>
          <div class="tc-detail-scroll"><p v-if="detailError" class="tc-error" role="alert">{{ detailError }} <button type="button" class="tc-link" @click="loadTaskDetail(selectedTask)">{{ t('taskCenter.retryLoad') }}</button></p><section class="tc-content-section"><h3>{{ t('taskCenter.instructions') }}</h3><div v-if="selectedSchedule" class="tc-instructions"><OrderedMessageContent :blocks="selectedBlocks" :parse-skill="false" /></div><p v-else class="tc-prose">{{ selectedTask.description || t('taskCenter.noTaskDescription') }}</p><p v-if="detailLoading" class="tc-footnote">{{ t('taskCenter.loading') }}</p></section><dl class="tc-facts"><div><dt>{{ t('taskCenter.definitionStatus') }}</dt><dd>{{ t(`taskCenter.taskState_${state(selectedTask)}`) }}</dd></div><div><dt>{{ t('taskCenter.nextRun') }}</dt><dd>{{ selectedTask.next_run ? date(selectedTask.next_run) : t('taskCenter.noNextRun') }}</dd></div><div v-if="selectedTask.expires_at" data-testid="task-expires-at"><dt>{{ t('taskCenter.expiresAt') }}</dt><dd>{{ date(selectedTask.expires_at) }}</dd></div><div v-if="selectedTask.pending_events"><dt>{{ t('taskCenter.queue') }}</dt><dd>{{ t('taskCenter.eventCount', { n: selectedTask.pending_events }) }}</dd></div></dl><section class="tc-content-section"><h3>{{ t('taskCenter.relatedRuns') }}</h3><p v-if="!selectedTaskRuns.length" class="tc-muted">{{ t('taskCenter.noRelatedRuns') }}</p><button v-for="run in selectedTaskRuns" :key="run.id" type="button" class="tc-related-run" @click="showRelatedRun(run)"><span class="tc-status" :class="runState(run)">{{ t(`taskCenter.state_${runState(run)}`) }}</span><time>{{ date(run.created_at) }}</time><span>{{ triggerText(run) }}</span><el-icon><ArrowRight /></el-icon></button></section>
            <details v-if="selectedTask.source === 'schedule' && olderRuns?.runs.length" class="tc-older"><summary>{{ t('taskCenter.legacyHistory') }}</summary><p class="tc-footnote">{{ t('taskCenter.olderRecordsHint') }}</p><article v-for="(run, index) in olderRuns.runs" :key="`${run.ts}-${index}`" class="tc-old-run"><div><time>{{ date(run.ts) }}</time><span>{{ t(`scheduleDialog.state_${run.end_reason === 'waiting_human' ? 'waiting' : run.status}`) }}</span><button v-if="run.session_id" type="button" class="tc-link" @click="openOlderResult(run)">{{ t('taskCenter.viewResult') }} →</button></div><p>{{ run.summary || run.error }}</p></article></details><details class="tc-technical"><summary>{{ t('taskCenter.details') }}</summary><p>{{ t('taskCenter.taskId') }}: {{ selectedTask.id }}</p></details><div v-if="selectedTask.source === 'schedule' && selectedTask.can_manage" class="tc-delete-row"><span v-if="!editable(selectedTask)" class="tc-footnote">{{ t('taskCenter.runBusyHint') }}</span><button type="button" class="tc-link tc-danger" :disabled="!!actionId || !editable(selectedTask)" @click="mutateTask(selectedTask, 'remove')">{{ t('taskCenter.remove') }}</button></div>
          </div>
        </template>
        <template v-else-if="view !== 'tasks' && selectedRun">
          <header class="tc-detail-heading"><button type="button" class="tc-icon tc-back" :aria-label="t('taskCenter.back')" @click="mobileDetail = false"><el-icon><ArrowLeft /></el-icon><span>{{ t('taskCenter.back') }}</span></button><div><small>{{ t('taskCenter.runDetails') }}</small><h2>{{ selectedRun.title }}</h2></div><span class="tc-status" :class="runState(selectedRun)">{{ t(`taskCenter.state_${runState(selectedRun)}`) }}</span></header><div class="tc-overview"><span>{{ triggerText(selectedRun) }}</span><span>·</span><span>{{ t('taskCenter.attempt', { n: selectedRun.attempt }) }}</span><button type="button" class="tc-link" @click="showRunTask(selectedRun)">{{ t('taskCenter.taskDefinition') }} →</button></div><div class="tc-task-actions"><button v-if="canOpenRunResult(selectedRun)" type="button" class="tc-primary" data-testid="run-open-result" @click="openRunResult(selectedRun)">{{ t(selectedRun.waiting_human ? 'taskCenter.handleApproval' : selectedRun.source === 'sentinel' ? 'taskCenter.viewSentinelSession' : isPendingTaskRun(selectedRun) ? 'taskCenter.viewProgress' : 'taskCenter.viewResult') }}<el-icon><ArrowRight /></el-icon></button><button v-if="selectedRun.can_cancel" type="button" class="tc-secondary" :disabled="!!actionId || selectedRun.cancel_requested" data-testid="task-cancel-run" @click="mutateRun(selectedRun, 'cancel')">{{ t(selectedRun.status === 'queued' ? 'taskCenter.cancelQueued' : 'taskCenter.cancelRun') }}</button><button v-if="selectedRun.can_retry" type="button" class="tc-secondary" :disabled="!!actionId" data-testid="task-retry-run" @click="mutateRun(selectedRun, 'retry')">{{ t('taskCenter.retryRun') }}</button></div><p v-if="selectedRun.waiting_human" class="tc-approval-note">{{ t('taskCenter.approvalHint') }}</p><p v-if="selectedRun.cancel_requested && selectedRun.status === 'running'" class="tc-footnote">{{ t('taskCenter.stoppingHint') }}</p><div class="tc-detail-scroll"><dl class="tc-facts"><div><dt>{{ t('taskCenter.thisRunStatus') }}</dt><dd>{{ t(`taskCenter.state_${runState(selectedRun)}`) }}</dd></div><div><dt>{{ t('taskCenter.createdAt') }}</dt><dd>{{ date(selectedRun.created_at) }}</dd></div><div v-if="selectedRun.started_at"><dt>{{ t('taskCenter.startedAt') }}</dt><dd>{{ date(selectedRun.started_at) }}</dd></div><div v-if="selectedRun.finished_at"><dt>{{ t('taskCenter.finishedAt') }}</dt><dd>{{ date(selectedRun.finished_at) }}</dd></div></dl><div v-if="selectedRun.error" class="tc-run-error"><strong>{{ t('taskCenter.historicalError') }}</strong><p>{{ selectedRun.error }}</p></div><p class="tc-prose tc-result-text">{{ selectedRun.summary || t(isPendingTaskRun(selectedRun) ? 'taskCenter.resultPending' : 'taskCenter.noSummary') }}</p><details class="tc-technical"><summary>{{ t('taskCenter.details') }}</summary><p>{{ t('taskCenter.runId') }}: {{ selectedRun.id }}</p><p v-if="selectedRun.end_reason">{{ selectedRun.end_reason }}</p></details></div>
        </template>
        <div v-else class="tc-empty tc-choose"><el-icon><Collection /></el-icon><h2>{{ t('taskCenter.choose') }}</h2><p>{{ t('taskCenter.chooseHint') }}</p></div>
      </main>
    </div>
  </section>
  <ScheduleEditorDialog :key="editorVersion" ref="editor" v-model="editorOpen" :item="editing" :draft="editorDraft" :agent-id="agentId" :agent-name="agentName" :session-id="sessionId" :agents="agents" :models="models" :draft-busy="draftBusy" @saved="saved" @request-draft="requestDraft" />
  <ConfirmDialog ref="confirm" />
</template>

<style scoped>
.task-center-sheet{display:flex;flex-direction:column;height:100%;min-width:0;min-height:0;overflow:hidden;background:var(--el-bg-color)}
button{font:inherit;cursor:pointer}button:disabled{opacity:.45;cursor:not-allowed}button:focus-visible,input:focus-visible,select:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:3px}.tc-header{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:14px 18px 12px;flex:none}.tc-brand{display:flex;align-items:center;min-width:0}.tc-brand h1{font-size:15px;font-weight:600;line-height:1.5;margin:0}.tc-brand p{font-size:11px;color:var(--el-text-color-secondary);margin:4px 0 0;line-height:1.6;overflow-wrap:anywhere}.tc-header-actions{display:flex;align-items:center;gap:6px;flex:none}.tc-primary,.tc-secondary{display:inline-flex;align-items:center;justify-content:center;gap:7px;min-height:34px;border-radius:9px;padding:8px 13px;font-size:12px;line-height:1.4;transition:background .15s,border-color .15s}.tc-primary{background:var(--el-color-primary);color:var(--el-color-white);border:1px solid var(--el-color-primary)}.tc-secondary{background:var(--el-bg-color);color:var(--el-text-color-regular);border:1px solid var(--el-border-color-light)}.tc-secondary:hover:not(:disabled){color:var(--el-color-primary);border-color:var(--el-color-primary-light-5)}.tc-icon{display:grid;place-items:center;width:30px;height:30px;padding:0;border:0;border-radius:7px;background:none;color:var(--el-text-color-secondary);font-size:17px;flex:none}.tc-icon:hover{background:var(--el-fill-color-light)}.tc-main-tabs{display:flex;align-items:center;gap:24px;padding:0 18px;border-bottom:1px solid var(--el-border-color-light);flex:none;overflow-x:auto}.tc-main-tabs>button{display:flex;align-items:center;gap:8px;flex:none;white-space:nowrap;padding:14px 0;border:0;border-bottom:2px solid transparent;background:none;color:var(--el-text-color-secondary);font-size:13px}.tc-main-tabs>button.active{color:var(--el-color-primary);border-bottom-color:var(--el-color-primary)}.tc-main-tabs>button>span{border-radius:6px;padding:1px 5px;background:var(--el-fill-color-light);font-size:10px;color:var(--el-text-color-placeholder);min-width:13px}.tc-needs-attention{margin-left:auto;font-size:11px;color:var(--el-color-warning)}.tc-layout{display:grid;grid-template-columns:clamp(220px,34%,280px) minmax(0,1fr);min-height:0;flex:1;color:var(--el-text-color-primary)}.tc-list-pane{display:flex;flex-direction:column;min-width:0;min-height:0;background:var(--el-fill-color-lighter);border-right:1px solid var(--el-border-color-light)}.tc-filter{display:flex;flex-wrap:wrap;gap:9px;padding:20px 18px 4px}.tc-filter input{flex:1;width:100%;min-width:120px}.tc-filter input,.tc-filter select{font:inherit;font-size:11px;background:var(--el-bg-color);color:var(--el-text-color-regular);border:1px solid var(--el-border-color-light);border-radius:8px;padding:9px 10px;box-sizing:border-box;min-height:35px}.tc-filter select{max-width:120px}.tc-watch-hint{padding:10px 18px;margin:0;border-top:1px solid var(--el-border-color-light);font-size:10px;line-height:1.8;color:var(--el-text-color-secondary)}.tc-list-heading{display:flex;align-items:center;justify-content:space-between;padding:4px 18px 10px;font-size:10px;color:var(--el-text-color-placeholder)}.tc-list{display:flex;flex-direction:column;gap:9px;flex:1;min-height:0;overflow:auto;padding:0 12px 22px;overscroll-behavior:contain}.tc-task-card,.tc-run-card{display:flex;flex-direction:column;flex:none;gap:9px;text-align:left;width:100%;min-width:0;padding:15px 14px;border:1px solid var(--el-border-color-extra-light);border-radius:11px;background:var(--el-bg-color);color:inherit;transition:background .15s,border-color .15s}.tc-task-card:hover,.tc-run-card:hover{border-color:var(--el-border-color)}.tc-task-card.selected,.tc-run-card.selected{border-color:var(--pg-accent-line,var(--el-color-primary-light-5));background:color-mix(in srgb,var(--el-color-primary) 5%,var(--el-bg-color))}.tc-card-top,.tc-run-top{display:flex;align-items:center;gap:8px;min-width:0}.tc-card-top strong,.tc-run-top strong{font-size:12px;line-height:1.65;font-weight:550;flex:1;min-width:0;overflow-wrap:anywhere}.tc-source-icon{color:var(--el-text-color-secondary);font-size:15px;flex:none}.tc-attention-dot{width:6px;height:6px;border-radius:50%;background:var(--el-color-warning);flex:none}.tc-card-description{display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;overflow-wrap:anywhere;font-size:11px;line-height:1.8;color:var(--el-text-color-secondary);margin:0}.tc-card-meta{display:flex;flex-wrap:wrap;align-items:center;gap:8px;font-size:10px;line-height:1.7;color:var(--el-text-color-placeholder)}.tc-status{display:inline-flex;align-items:center;gap:4px;white-space:nowrap;flex:none;padding:3px 7px;border-radius:6px;font-size:10px;font-weight:500;line-height:1.4;background:var(--el-fill-color-light);color:var(--el-text-color-secondary)}.tc-status.listening,.tc-status.completed{background:var(--el-color-success-light-9);color:var(--el-color-success)}.tc-status.running,.tc-status.scheduled{background:var(--el-color-primary-light-9);color:var(--el-color-primary)}.tc-status.waiting,.tc-status.queued,.tc-status.stopping{background:var(--el-color-warning-light-9);color:var(--el-color-warning)}.tc-status.failed,.tc-status.interrupted,.tc-status.error{background:var(--el-color-danger-light-9);color:var(--el-color-danger)}.tc-running-count{display:inline-flex;align-items:center;gap:5px;color:var(--el-color-primary)}.tc-lane{display:flex;flex-direction:column;gap:8px}.tc-lane h2{display:flex;align-items:center;gap:8px;margin:12px 4px 3px;font-size:11px;font-weight:500;color:var(--el-text-color-secondary)}.tc-lane h2 span{font-size:10px;color:var(--el-text-color-placeholder)}.tc-run-card p{font-size:10px;line-height:1.7;color:var(--el-text-color-secondary);margin:0}.tc-detail-pane{display:flex;flex-direction:column;min-width:0;min-height:0;padding:0 24px;background:var(--el-bg-color)}.tc-detail-heading{display:flex;align-items:center;gap:12px;padding:22px 0 12px;flex:none}.tc-detail-heading>div{flex:1;min-width:0}.tc-detail-heading small{display:block;color:var(--el-text-color-placeholder);font-size:10px;margin-bottom:7px}.tc-detail-heading h2{font-size:19px;line-height:1.5;margin:0;font-weight:550;overflow-wrap:anywhere}.tc-overview{display:flex;align-items:center;flex-wrap:wrap;gap:9px;font-size:11px;line-height:1.8;color:var(--el-text-color-secondary)}.tc-task-actions{display:flex;align-items:center;gap:9px;flex-wrap:wrap;margin-top:20px}.tc-footnote{font-size:10px;color:var(--el-text-color-placeholder);line-height:1.8;margin:10px 0 15px}.tc-detail-scroll{flex:1;min-height:0;overflow:auto;overscroll-behavior:contain;padding:6px 0 28px}.tc-content-section{padding:19px 0;border-bottom:1px solid var(--el-border-color-extra-light)}.tc-content-section h3{font-size:12px;font-weight:550;margin:0 0 13px;color:var(--el-text-color-secondary)}.tc-prose{font-size:13px;line-height:1.9;white-space:pre-wrap;overflow-wrap:anywhere;margin:0}.tc-instructions{font-size:13px;line-height:1.9}.tc-facts{display:flex;flex-direction:column;gap:14px;margin:23px 0;font-size:12px;line-height:1.8}.tc-facts>div{display:grid;grid-template-columns:115px minmax(0,1fr);gap:15px}.tc-facts dt{color:var(--el-text-color-placeholder)}.tc-facts dd{margin:0;overflow-wrap:anywhere}.tc-related-run{display:flex;align-items:center;flex-wrap:wrap;gap:10px;width:100%;padding:13px 0;text-align:left;border:0;border-bottom:1px solid var(--el-border-color-extra-light);background:none;color:var(--el-text-color-secondary);font-size:11px}.tc-related-run:hover{color:var(--el-color-primary)}.tc-related-run>.el-icon{margin-left:auto}.tc-muted{font-size:12px;line-height:1.8;color:var(--el-text-color-secondary)}.tc-older,.tc-technical{margin:23px 0;font-size:11px;line-height:1.9;color:var(--el-text-color-secondary)}.tc-older summary,.tc-technical summary{cursor:pointer}.tc-old-run{padding:14px 0;border-bottom:1px solid var(--el-border-color-extra-light)}.tc-old-run>div{display:flex;align-items:center;gap:12px;flex-wrap:wrap}.tc-old-run>div>button{margin-left:auto}.tc-old-run p{white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px;line-height:1.8}.tc-technical{color:var(--el-text-color-placeholder);font-size:10px;overflow-wrap:anywhere}.tc-delete-row{display:flex;align-items:center;justify-content:flex-end;gap:12px;padding-top:8px}.tc-link{border:0;padding:0;background:none;color:var(--el-color-primary);font:inherit;font-size:11px;line-height:1.6}.tc-danger{color:var(--el-color-danger)}.tc-empty{display:flex;flex-direction:column;align-items:center;justify-content:center;text-align:center;gap:13px;padding:45px 24px;color:var(--el-text-color-secondary);font-size:12px}.tc-empty>.el-icon{font-size:32px;color:var(--el-text-color-placeholder)}.tc-empty strong,.tc-empty h2{font-size:14px;font-weight:500;margin:0;color:var(--el-text-color-primary)}.tc-empty p{max-width:310px;font-size:11px;line-height:1.8;margin:0}.tc-choose{flex:1}.tc-error,.tc-top-error{font-size:12px;line-height:1.8;color:var(--el-color-danger);overflow-wrap:anywhere}.tc-top-error{padding:10px 18px;background:var(--el-color-danger-light-9);border-bottom:1px solid var(--el-border-color-light)}.tc-run-error{padding:16px 18px;border-radius:10px;background:var(--el-color-danger-light-9);font-size:12px;line-height:1.8;color:var(--el-color-danger);margin:22px 0}.tc-run-error p{white-space:pre-wrap;overflow-wrap:anywhere;margin:8px 0 0}.tc-result-text{margin:26px 0}.tc-approval-note{font-size:11px;line-height:1.8;color:var(--el-color-warning);background:var(--el-color-warning-light-9);border-radius:8px;padding:11px 14px;margin:12px 0}.tc-back{display:none}
.tc-list-pane>.tc-empty{flex:1}
/* The task center shares the dock with chat. Respond to that panel's width,
   rather than the full browser viewport. */
@container task-center (max-width:760px){
  .tc-main-tabs{gap:22px}
  .tc-main-tabs>button{font-size:12px}
  .tc-needs-attention{display:none}
  .tc-layout{grid-template-columns:minmax(0,1fr)}
  .tc-list-pane{border:0}
  .tc-detail-pane{display:none;padding:0 18px}
  .tc-mobile-detail>.tc-list-pane{display:none}
  .tc-mobile-detail>.tc-detail-pane{display:flex}
  .tc-back{display:inline-flex;width:auto;gap:5px;padding:0 7px;font-size:11px;white-space:nowrap}
  .tc-detail-heading{flex-wrap:wrap;gap:10px;padding-top:16px}
  .tc-detail-heading>.tc-back{flex-basis:100%;justify-content:flex-start;padding:0}
  .tc-detail-heading h2{font-size:18px}
  .tc-facts>div{grid-template-columns:95px minmax(0,1fr)}
}
@container task-center (max-width:520px){
  .tc-header{gap:10px;padding:12px 14px}
  .tc-brand h1{font-size:14px}
  .tc-brand p{display:none}
  .tc-header-actions>.tc-primary{min-height:32px;padding:6px 9px;font-size:11px}
  .tc-main-tabs{gap:20px;padding-inline:14px}
  .tc-main-tabs>button{gap:6px;padding:12px 0}
  .tc-filter{padding:16px 14px 4px}
  .tc-list-heading{padding-inline:14px}
  .tc-top-error{padding-inline:14px}
}

@container task-center (max-width:360px){
  .tc-header{flex-wrap:wrap}
  .tc-header-actions{margin-left:auto}
}
.tc-review-note{color:var(--el-color-warning);margin-bottom:0}.tc-review-note>.tc-link{margin-left:7px}
</style>
