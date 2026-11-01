<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, onMounted, provide, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessageBox } from "element-plus";
import { agentWorkflowsApi, type AgentWorkflowCapabilities, type AgentWorkflowDefinition, type AgentWorkflowDiagnostic, type AgentWorkflowResponse, type AgentWorkflowRun, type AgentWorkflowVersionSummary } from "@/api/agentWorkflows";
import { validAgentWorkflowId, type AgentWorkflowLocation } from "@/agent/agentWorkflowNavigation";
import { agentWorkflowUpdatesKey } from "@/agent/agentWorkflowUpdates";
import { useAgentWorkflowActivity } from "@/agent/useAgentWorkflowActivity";
import { agentWorkflowRunStatusKey } from "@/agent/useAgentWorkflowRunStatus";
import { workflowNodeActivities } from "@/agent/agentWorkflowActivity";
import { useAgentWorkflowResources } from "@/agent/useAgentWorkflowResources";
import { workflowInputDefaults, workflowSelectedFiles } from "@/agent/agentWorkflowInputs";
import { cloneWorkflowDefinition, workflowToolApprovalMode } from "@/agent/agentWorkflowEditor";
import { workspaceFileNavigationKey } from "@/agent/workspaceFileNavigation";
import { randomUUID } from "@/utils/uuid";
import MarkdownView from "./MarkdownView.vue";
import TaskSessionResult from "./TaskSessionResult.vue";
import AgentWorkflowEditor from "./agent-workflow/AgentWorkflowEditor.vue";
import AgentWorkflowControlCard from "./agent-workflow/AgentWorkflowControlCard.vue";
import TaskContextRing from "./TaskContextRing.vue";
import type { TaskContextReading, TaskContextScope } from "@/agent/taskContextUsage";
import AgentWorkflowRunInputs from "./agent-workflow/AgentWorkflowRunInputs.vue";
import AgentWorkflowRunBadge from "./agent-workflow/AgentWorkflowRunBadge.vue";

const props = defineProps<{ agentId?: string; sessionId: string | null; visible: boolean; conversationOpen?: boolean; immersive?: boolean; selection?: AgentWorkflowLocation & { sequence: number }; toolRevision?: number }>();
const emit = defineEmits<{ intent: [string]; close: []; conversation: [boolean]; workspace: [boolean]; immersive: [boolean] }>();
const { t, locale } = useI18n();
const liveUpdate = inject(agentWorkflowUpdatesKey, ref(null));
const runOverview = inject(agentWorkflowRunStatusKey, null);
const statusCopy = computed(() => locale.value.startsWith('zh') ? {
  viewActive: '查看运行', activeRuns: '当前运行', activeCount: '个进行中的运行', completed: '已完成', nodes: '个节点',
  waiting: '等待本人确认', syncing: '运行状态正在重新同步，上次状态仅供参考', preparing: '准备执行', loading: '正在同步运行状态…', denied: '暂时无法读取运行状态',
} : {
  viewActive: 'View run', activeRuns: 'Active runs', activeCount: 'active runs', completed: 'Completed', nodes: 'nodes',
  waiting: 'Waiting for your confirmation', syncing: 'Reconnecting run status. Showing the last known state.', preparing: 'Preparing execution', loading: 'Syncing run status…', denied: 'Run status is currently unavailable',
});
provide(workspaceFileNavigationKey, { scope: () => null, open: () => {} });
const workflows = ref<AgentWorkflowDefinition[]>([]), workflow = ref<AgentWorkflowDefinition | null>(null);
const showDeleted = ref(false), deletingFlowId = ref("");
const visibleWorkflows = computed(() => workflows.value.filter(item => showDeleted.value ? item.deleted : !item.deleted));
const runs = ref<AgentWorkflowRun[]>([]), run = ref<AgentWorkflowRun | null>(null);
const nodeContextReadings = ref<Record<string, TaskContextReading>>({});
const capabilities = ref<AgentWorkflowCapabilities>({}), diagnostics = ref<AgentWorkflowDiagnostic[]>([]);
const workflowId = ref(""), runId = ref(""), error = ref(""), notice = ref(""), loading = ref(false), busy = ref(false), showProcess = ref(false), controlOpen = ref(false), pendingOpen = ref(false);
const feedback = ref(""), inputOpen = ref(false), inputMode = ref<"trial" | "start">("trial"), inputs = ref<Record<string, string>>({}), files = ref("");
const inputLoading = ref(false), preparedStart = ref<{ definition: AgentWorkflowDefinition; versionId: string; sessionId: string; scope: string; epoch: number; navigation: number } | null>(null);
const preparedTrial = ref<{ definition: AgentWorkflowDefinition; sessionId: string; scope: string; epoch: number; navigation: number } | null>(null);
const inputFields = computed(() => inputMode.value === "start" ? preparedStart.value?.definition.inputs || [] : preparedTrial.value?.definition.inputs || []);
const runInputNotice = computed(() => containsAgent(inputMode.value === "start" ? preparedStart.value?.definition : preparedTrial.value?.definition) ? "agentWorkflow.realRun" : "agentWorkflow.codeRun");
const runInputApprovalMode = computed(() => {
  const definition = inputMode.value === "start" ? preparedStart.value?.definition : preparedTrial.value?.definition;
  return definition ? workflowToolApprovalMode(definition) : null;
});
let inputAbort: AbortController | undefined, inputTicket = 0;
const selectedStepId = ref(""), confirmationFeedback = ref<Record<string, string>>({});
const nodeProcessOpen = ref(false), nodeProcessStepId = ref(""), activityAuthEpoch = ref(0);
const artifactOpen = ref(false), artifactText = ref(""), artifactPath = ref("");
const editor = ref<{ prepareClose(): Promise<boolean>; focusStep(id: string): void; closeInspector?(): void; save?(): Promise<void>; state?(): { dirty: boolean; saving: boolean; canSave: boolean } } | null>(null), editorDirty = ref(false);
const editorStatus = computed(() => editor.value?.state?.() || { dirty: editorDirty.value, saving: false, canSave: false });
const controlTab = ref<"overview" | "versions" | "runs">("overview"), controlLauncher = ref<HTMLButtonElement | null>(null);
const versions = ref<AgentWorkflowVersionSummary[]>([]), enabledVersion = ref<AgentWorkflowVersionSummary | null>(null);
const versionsLoading = ref(false), versionsError = ref(""), hasMoreVersions = ref(false), nextBeforeRevision = ref<number>();
const previewDefinition = ref<AgentWorkflowDefinition | null>(null), previewVersion = ref<AgentWorkflowVersionSummary | null>(null), previewLoading = ref(false);
const viewingSnapshot = computed(() => !!runId.value || !!previewDefinition.value);
const canRestore = computed(() => !!previewVersion.value && canEdit.value && !busy.value && !previewLoading.value && !versionsLoading.value && previewVersion.value.revision !== workflow.value?.revision);
const editorKey = computed(() => `${workflowId.value}:${runId.value || (previewVersion.value ? `version-${previewVersion.value.revision}-${previewVersion.value.version_id}` : "draft")}`);
let versionsAbort: AbortController | undefined, previewAbort: AbortController | undefined, versionsTicket = 0, previewTicket = 0;
const viewportHeight = ref(typeof window === "undefined" ? 800 : window.innerHeight || 800);
const viewportWidth = ref(typeof window === "undefined" ? 1280 : window.innerWidth || 1280);
const sheetFrame = ref<HTMLElement | null>(null), floatingHeader = ref<HTMLElement | null>(null), topInset = ref(88);
let frameObserver: ResizeObserver | undefined;
const processHeight = computed(() => Math.min(420, Math.max(190, Math.round(viewportHeight.value * 0.38))));
const bottomInset = computed(() => showProcess.value && run.value ? processHeight.value + (viewportWidth.value <= 900 ? 116 : 76) : run.value && viewportWidth.value <= 720 ? 108 : 64);
function resizeWorkspace() {
  viewportHeight.value = sheetFrame.value?.clientHeight || window.innerHeight || 800;
  viewportWidth.value = sheetFrame.value?.clientWidth || window.innerWidth || 1280;
  if (workflowId.value && floatingHeader.value) topInset.value = floatingHeader.value.offsetTop + floatingHeader.value.offsetHeight + 16;
}
function toggleConversation() {
  controlOpen.value = false;
  if (!props.conversationOpen) showProcess.value = false;
  emit("conversation", !props.immersive || !props.conversationOpen);
}
function toggleProcess() {
  controlOpen.value = false;
  showProcess.value = !showProcess.value;
  if (showProcess.value) {
    editor.value?.closeInspector?.(); nodeProcessOpen.value = false; nodeProcessStepId.value = "";
    if (!processSessionId.value) selectedStepId.value = (processSteps.value.find(step => step.status === "running") || processSteps.value[processSteps.value.length - 1])?.id || "";
    emit("conversation", false);
  }
}
function workflowSettings() { controlOpen.value = false; emit("conversation", false); editor.value?.focusStep("__input"); }
function workspaceEscape(event: KeyboardEvent) {
  if (event.defaultPrevented) return;
  if (controlOpen.value) { closeControl(); event.preventDefault(); event.stopPropagation(); }
  else if (pendingOpen.value) { pendingOpen.value = false; event.preventDefault(); event.stopPropagation(); }
  else if (showProcess.value) { showProcess.value = false; event.preventDefault(); event.stopPropagation(); }
}
const createOpen = ref(false), createName = ref(""), createTask = ref(""), createOutput = ref(""), createStepId = ref("");
const operationKeys = new Map<string, string>();
let disposed = false, epoch = 0, authEpoch = 0, request = 0, navigation = 0, selectionSeen = -1;
let abort: AbortController | undefined, timer: ReturnType<typeof setTimeout> | undefined;
const scope = () => JSON.stringify([props.agentId, authEpoch]);
const current = (key: string, mine: number) => !disposed && props.visible && scope() === key && epoch === mine;
const bound = () => validAgentWorkflowId(props.agentId);
const canEdit = computed(() => capabilities.value.edit === true && !workflow.value?.archived && !workflow.value?.deleted);
const canRun = computed(() => capabilities.value.run === true && !workflow.value?.archived && !workflow.value?.deleted);
const active = computed(() => !!run.value && ["queued", "running", "waiting_confirmation"].includes(run.value.status));
const canRetry = computed(() => canRun.value && !!run.value && ["failed", "cancelled", "interrupted"].includes(run.value.status));
const flowActivity = (id: string) => runOverview?.byWorkflow.value[id];
const currentFlowActivity = computed(() => flowActivity(workflowId.value));
function hasActiveRuns(id: string) {
  return !!flowActivity(id)?.active || id === workflowId.value && runs.value.some(item => ["queued", "running", "waiting_confirmation"].includes(item.status));
}
function canDelete(flow: AgentWorkflowDefinition) {
  return capabilities.value.edit === true && !flow.deleted && !busy.value && !deletingFlowId.value && !hasActiveRuns(flow.id);
}
async function setDeletedView(value: boolean) {
  if (busy.value || deletingFlowId.value) return;
  showDeleted.value = value; error.value = ""; notice.value = "";
  await refresh();
}
function overviewStatus(value: { waiting: number; running: number; queued: number; cancelling: number; preferred?: { status: string; cancel_requested: boolean } }) {
  if (value.preferred) return value.preferred.cancel_requested ? 'cancelling' : value.preferred.status;
  return value.waiting ? 'waiting_confirmation' : value.running ? 'running' : value.queued ? 'queued' : 'cancelling';
}
const selectedRunStatus = computed(() => active.value && run.value?.cancel_requested ? 'cancelling' : run.value?.status || 'pending');
const currentRunNodes = computed(() => {
  const value = run.value;
  if (!value || !active.value || value.cancel_requested) return [];
  return value.definition.steps.filter(step => value.steps?.some(result => result.step_id === step.id && ['running', 'waiting_confirmation', 'waiting'].includes(result.status)));
});
const currentRunSummary = computed(() => {
  const value = run.value;
  if (!value) return '';
  const names = currentRunNodes.value.map(step => step.title || step.id);
  if (names.length) return names.join(' · ');
  if (active.value) return value.cancel_requested ? t('agentWorkflow.cancelling') : statusCopy.value.preparing;
  const completed = value.steps?.filter(step => step.status === 'completed').length || 0;
  return `${statusCopy.value.completed} ${completed}/${value.definition.steps.length} ${statusCopy.value.nodes}`;
});
function openActiveRun(id = workflowId.value) {
  const target = flowActivity(id)?.preferred;
  if (!target) return;
  void chooseWorkflow(id, target.id);
}
function focusCurrentRun() {
  const step = currentRunNodes.value.find(item => item.kind === 'confirm') || currentRunNodes.value[0];
  if (step) focusStep(step.id);
  else openControl('runs');
}
function containsAgent(value?: AgentWorkflowDefinition | null) {
  return value?.steps.some(step => step.kind === 'agent' || step.kind === 'iteration' && step.iteration?.body.kind === 'agent' || step.kind === 'loop' && step.loop?.body.kind === 'agent') === true;
}
type WorkflowAction = { command: string; label: string; disabled: boolean; divided?: boolean; hint?: string };
const workflowActions = computed<WorkflowAction[]>(() => {
  const flow = workflow.value;
  if (!flow) return [];
  if (flow.deleted) return [{ command: "settings", label: t("agentWorkflow.settings"), disabled: false }];
  const saved = !editorDirty.value && !busy.value && !previewLoading.value && !inputLoading.value;
  const editableDraft = saved && !viewingSnapshot.value;
  const actions: WorkflowAction[] = [
    { command: "settings", label: t("agentWorkflow.settings"), disabled: false },
    { command: "edit", label: t("agentWorkflow.edit"), disabled: !editableDraft || !canEdit.value },
    { command: "validate", label: t("agentWorkflow.validate"), disabled: !editableDraft },
    { command: "trial", label: t("agentWorkflow.trial"), disabled: !editableDraft || !canRun.value || !props.sessionId },
    { command: "enable", label: t("agentWorkflow.enableDraft"), disabled: !editableDraft || capabilities.value.enable !== true || !!flow.archived || flow.enabled_version === flow.definition_hash },
  ];
  if (flow.enabled_version) actions.push({ command: "start", label: t("agentWorkflow.start"), disabled: !saved || !canRun.value || !props.sessionId });
  if (!viewingSnapshot.value && flow.enabled_version) actions.push({ command: "disable", label: t("agentWorkflow.disable"), disabled: busy.value || capabilities.value.enable !== true, divided: true });
  if (!viewingSnapshot.value && !flow.archived) actions.push({ command: "archive", label: t("agentWorkflow.archive"), disabled: busy.value || !canEdit.value, divided: !flow.enabled_version });
  if (!viewingSnapshot.value) actions.push({ command: "delete", label: t("agentWorkflow.delete"), disabled: !canDelete(flow), divided: true, hint: hasActiveRuns(flow.id) ? t("agentWorkflow.deleteActiveHint") : undefined });
  return actions;
});
async function workflowAction(command: string) {
  if (!workflowActions.value.some(action => action.command === command && !action.disabled)) return;
  switch (command) {
    case "settings": workflowSettings(); break;
    case "edit": editInConversation(); break;
    case "validate": await validate(); break;
    case "trial": await prepareRun("trial"); break;
    case "enable": await enable(true); break;
    case "start": await prepareRun("start"); break;
    case "disable": await enable(false); break;
    case "archive": await archive(); break;
    case "delete": await deleteWorkflow(); break;
  }
}
const waitingSteps = computed(() => {
  const value = run.value;
  if (!value || !active.value || value.cancel_requested) return [];
  if (value.definition.graph) return value.definition.steps.filter(step => step.kind === "confirm" && value.steps?.some(result => result.step_id === step.id && result.status === "waiting_confirmation"));
  const step = value.status === "waiting_confirmation" ? value.definition.steps[value.current_step ?? 0] : undefined;
  return step ? [step] : [];
});
const waitingStep = computed(() => waitingSteps.value[0]);
const hasNodeSessions = computed(() => !!run.value && (!!run.value.definition.graph || !!run.value.resources || run.value.definition.steps.some(step => ['code', 'iteration', 'loop'].includes(step.kind))));
const resourceCatalog = useAgentWorkflowResources(() => ({ agentId: props.agentId, sessionId: props.sessionId, visible: props.visible, authEpoch: activityAuthEpoch.value, revision: props.toolRevision }));
const processSteps = computed(() => (hasNodeSessions.value ? run.value!.definition.steps : []).flatMap(step => {
  const result = run.value?.steps?.find(item => item.step_id === step.id);
  const body = step.kind === 'iteration' ? step.iteration?.body : step.kind === 'loop' ? step.loop?.body : undefined;
  if (body?.kind === 'agent') return (result?.repeat?.items || []).flatMap(item => item.session_id ? [{
    id: `${step.id}:${item.index}`, parentStepId: step.id,
    title: `${step.title || step.id} · ${t('agentWorkflowCanvas.repeatItem', { n: item.index + 1 })}`,
    sessionId: item.session_id, activeRunId: item.active_run_id,
    status: item.status === 'running' && (['failed', 'cancelled', 'interrupted'].includes(result!.status) || ['failed', 'cancelled', 'interrupted'].includes(run.value!.status)) ? 'interrupted' : item.status,
  }] : []);
  return result?.session_id ? [{ id: step.id, parentStepId: step.id, title: step.title || step.id, sessionId: result.session_id, activeRunId: result.active_run_id, status: result.status }] : [];
}));
const processStep = computed(() => {
  const value = run.value;
  if (!value || !hasNodeSessions.value) return undefined;
  const selected = processSteps.value.find(step => step.id === selectedStepId.value);
  if (selected) return selected;
  if (value.definition.steps.some(step => step.id === selectedStepId.value)) return processSteps.value.find(step => step.parentStepId === selectedStepId.value && step.status === 'running') || processSteps.value.find(step => step.parentStepId === selectedStepId.value);
  return processSteps.value.find(step => step.status === "running") || processSteps.value[0];
});
const processSessionId = computed(() => hasNodeSessions.value ? processStep.value?.sessionId || "" : run.value?.execution_session_id || "");
const processActiveRunId = computed(() => {
  if (processStep.value) return processStep.value.activeRunId;
  const value = run.value;
  const selected = value?.definition.steps.find(step => step.id === selectedStepId.value && step.kind === 'agent');
  const stepId = selected?.id || value?.steps?.find(step => step.status === 'running')?.step_id || value?.steps?.slice().reverse().find(step => step.active_run_id)?.step_id;
  return value?.steps?.find(step => step.step_id === stepId)?.active_run_id;
});
function contextTarget(stepId: string) {
  const value = run.value;
  if (!value) return undefined;
  const item = processSteps.value.find(step => step.id === stepId);
  if (item) return { stepId: item.id, sessionId: item.sessionId, activeRunId: item.activeRunId };
  const node = value.definition.steps.find(step => step.id === stepId && step.kind === 'agent');
  const result = value.steps?.find(step => step.step_id === stepId);
  if (!node || !value.execution_session_id || hasNodeSessions.value) return undefined;
  return { stepId, sessionId: value.execution_session_id, activeRunId: result?.active_run_id };
}
function contextKey(target: { stepId: string; sessionId: string; activeRunId?: string }) {
  return JSON.stringify([props.agentId, run.value?.id, target.stepId, target.sessionId, target.activeRunId || '']);
}
function setNodeContext(stepId: string, reading: TaskContextReading | null, source?: TaskContextScope) {
  const target = contextTarget(stepId);
  if (!target) return;
  if (!reading && !source) return;
  if (source && (source.agentId !== props.agentId || source.workflowRunId !== run.value?.id || source.sessionId !== target.sessionId || (source.activeRunId || '') !== (target.activeRunId || ''))) return;
  const key = contextKey(target);
  if (!reading && source?.unmounted) {
    const previous = nodeContextReadings.value[key];
    if (previous) nodeContextReadings.value = { ...nodeContextReadings.value, [key]: { ...previous, stale: true } };
    return;
  }
  if (reading && (reading.sessionId !== target.sessionId || (reading.activeRunId || '') !== (target.activeRunId || ''))) return;
  if (reading) nodeContextReadings.value = { ...nodeContextReadings.value, [key]: reading };
  else { const next = { ...nodeContextReadings.value }; delete next[key]; nodeContextReadings.value = next; }
}
const observedContextReading = computed(() => {
  const selected = selectedStepId.value;
  const target = contextTarget(selected) || (processStep.value?.parentStepId === selected ? contextTarget(processStep.value.id) : undefined);
  return target ? nodeContextReadings.value[contextKey(target)] || null : null;
});
const processContextReading = computed(() => {
  const target = contextTarget(processStep.value?.id || selectedStepId.value);
  return target ? nodeContextReadings.value[contextKey(target)] || null : null;
});
watch(() => run.value?.id, () => { nodeContextReadings.value = {}; }, { flush: 'sync' });
const activityStream = useAgentWorkflowActivity(() => ({ agentId: props.agentId, workflowId: workflowId.value, runId: run.value?.id || "", visible: props.visible && !!run.value, authEpoch: activityAuthEpoch.value }));
const nodeActivities = computed(() => workflowNodeActivities(activityStream.snapshot.value, run.value, activityStream.connected.value));
function selectStep(id: string) {
  selectedStepId.value = id;
  if (id) {
    controlOpen.value = false; emit("conversation", false);
    if (run.value?.definition.steps.some(step => step.id === id && step.kind === 'agent')) showProcess.value = false;
  }
}
function nodeProcessChanged(open: boolean, id?: string) {
  nodeProcessOpen.value = open; nodeProcessStepId.value = open ? id || "" : "";
  if (open) showProcess.value = false;
}
function nodeExecutionSession(stepId: string) {
  return hasNodeSessions.value ? run.value?.steps?.find(step => step.step_id === stepId)?.session_id || "" : run.value?.execution_session_id || "";
}
function closeNodeProcess() { editor.value?.closeInspector?.(); nodeProcessOpen.value = false; nodeProcessStepId.value = ""; }
function chooseProcessStep(id: string) {
  if (!processSteps.value.some(step => step.id === id)) return;
  editor.value?.closeInspector?.(); nodeProcessOpen.value = false; nodeProcessStepId.value = "";
  selectedStepId.value = id; showProcess.value = true;
}
function focusStep(id: string) { selectStep(id); editor.value?.focusStep(id); }
function focusConfirmation(id: string) { pendingOpen.value = false; showProcess.value = false; focusStep(id); }
function feedbackFor(id: string) { return run.value?.definition.graph ? confirmationFeedback.value[id] || "" : feedback.value; }
function setFeedback(id: string, value: string) { if (run.value?.definition.graph) confirmationFeedback.value[id] = value; else feedback.value = value; }
const date = (value?: number) => value ? new Intl.DateTimeFormat(locale.value, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(new Date(value)) : "";
const state = (value?: string) => t(`agentWorkflow.status.${value || "pending"}`);
const stepResult = (id: string) => run.value?.steps?.find(step => step.step_id === id);
function setCapabilities(value?: AgentWorkflowCapabilities) { if (value) capabilities.value = value; }
function checkedWorkflow(value?: AgentWorkflowDefinition) {
  if (!value || value.id !== workflowId.value || value.agent_id && value.agent_id !== props.agentId) throw new Error(t("agentWorkflow.scopeChanged"));
  return value;
}
function checkedRun(value?: AgentWorkflowRun) {
  if (!value || value.id !== runId.value || value.workflow_id !== workflowId.value || value.agent_id !== props.agentId) throw new Error(t("agentWorkflow.scopeChanged"));
  return value;
}
function validVersion(value: AgentWorkflowVersionSummary): boolean {
  return !!value && /^[a-f0-9]{64}$/.test(value.version_id) && Number.isSafeInteger(value.revision) && value.revision > 0;
}
function closeControl(restoreFocus = true) {
  controlOpen.value = false;
  if (previewLoading.value) { ++previewTicket; previewAbort?.abort(); previewLoading.value = false; }
  if (restoreFocus) void nextTick(() => controlLauncher.value?.focus({ preventScroll: true }));
}
function openControl(tab: "overview" | "versions" | "runs" = "overview", toggle = false) {
  if (toggle && controlOpen.value && controlTab.value === tab) { closeControl(); return; }
  controlTab.value = tab; controlOpen.value = true; showProcess.value = false;
  editor.value?.closeInspector?.(); emit("conversation", false);
}
function setControlTab(tab: "overview" | "versions" | "runs") {
  controlTab.value = tab;
  if (tab !== "runs" && !versions.value.length && !versionsLoading.value) void loadVersions();
}
function clearVersionView() {
  ++previewTicket; previewAbort?.abort(); previewLoading.value = false;
  previewDefinition.value = null; previewVersion.value = null;
}
function resetVersions() {
  ++versionsTicket; versionsAbort?.abort(); versionsLoading.value = false; versionsError.value = "";
  versions.value = []; enabledVersion.value = null; hasMoreVersions.value = false; nextBeforeRevision.value = undefined;
  clearVersionView();
}
async function loadVersions(append = false) {
  if (!workflowId.value || !props.visible || !bound()) return;
  if (append && (!hasMoreVersions.value || !nextBeforeRevision.value || versionsLoading.value)) return;
  versionsAbort?.abort(); const controller = new AbortController(); versionsAbort = controller;
  const key = scope(), mine = epoch, wid = workflowId.value, ticket = ++versionsTicket;
  versionsLoading.value = true; versionsError.value = "";
  try {
    const result = await agentWorkflowsApi.call(props.agentId!, "read", { action: "versions", workflow_id: wid, ...(append ? { before_revision: nextBeforeRevision.value } : {}) }, controller.signal);
    if (!current(key, mine) || wid !== workflowId.value || ticket !== versionsTicket) return;
    if (result.ok === false || !Array.isArray(result.versions)) throw new Error(t("agentWorkflow.versionsUnavailable"));
    const incoming = result.versions.filter(validVersion);
    const rows = append ? [...versions.value, ...incoming] : incoming;
    versions.value = [...new Map(rows.map(row => [`${row.revision}:${row.version_id}`, row])).values()].sort((a, b) => b.revision - a.revision);
    enabledVersion.value = result.enabled_version && validVersion(result.enabled_version) ? result.enabled_version : null;
    hasMoreVersions.value = result.has_more === true;
    nextBeforeRevision.value = result.next_before_revision;
    setCapabilities(result.capabilities);
  } catch (issue) {
    if (current(key, mine) && ticket === versionsTicket && (issue as Error)?.name !== "AbortError") versionsError.value = (issue as Error)?.message || t("agentWorkflow.versionsUnavailable");
  } finally { if (current(key, mine) && ticket === versionsTicket) versionsLoading.value = false; }
}
async function previewSavedVersion(target: AgentWorkflowVersionSummary) {
  if (!validVersion(target) || !workflow.value || !props.visible || previewLoading.value || busy.value) return;
  if (target.revision === workflow.value.revision && target.version_id === workflow.value.definition_hash) { await chooseRun(""); return; }
  clearRunInput();
  previewAbort?.abort(); const controller = new AbortController(); previewAbort = controller;
  const key = scope(), mine = epoch, wid = workflowId.value, ticket = ++previewTicket, intent = ++navigation;
  previewLoading.value = true; versionsError.value = "";
  try {
    const result = await agentWorkflowsApi.call(props.agentId!, "read", { action: "version", workflow_id: wid, version_id: target.version_id, target_revision: target.revision }, controller.signal);
    if (!current(key, mine) || wid !== workflowId.value || ticket !== previewTicket || intent !== navigation) return;
    const value = checkedWorkflow(result.workflow);
    if (value.revision !== target.revision || value.definition_hash !== target.version_id || result.version?.version_id !== target.version_id || result.version.revision !== target.revision) throw new Error(t("agentWorkflow.versionsUnavailable"));
    // Confirm against the editor after the read, so edits typed while loading
    // cannot disappear behind an earlier discard confirmation.
    if (!await leaveEditor() || !current(key, mine) || wid !== workflowId.value || ticket !== previewTicket) return;
    ++request; abort?.abort(); runId.value = ""; run.value = null; previewDefinition.value = value; previewVersion.value = result.version;
    editorDirty.value = false; showProcess.value = false; controlTab.value = "versions"; controlOpen.value = true; emit("conversation", false);
    void refresh();
  } catch (issue) {
    if (current(key, mine) && ticket === previewTicket && (issue as Error)?.name !== "AbortError") versionsError.value = (issue as Error)?.message || t("agentWorkflow.versionsUnavailable");
  } finally { if (current(key, mine) && ticket === previewTicket) previewLoading.value = false; }
}
async function restoreSavedVersion() {
  const target = previewVersion.value, flow = workflow.value;
  if (!target || !flow || !canRestore.value) return;
  const key = scope(), mine = epoch, wid = flow.id, revision = flow.revision;
  try { await ElMessageBox.confirm(t("agentWorkflow.restoreQuestion", { n: target.revision }), t("agentWorkflow.restoreAsDraft"), { confirmButtonText: t("agentWorkflow.restoreConfirm"), cancelButtonText: t("agentWorkflow.cancel"), type: "warning" }); }
  catch { return; }
  if (!current(key, mine) || workflowId.value !== wid || previewVersion.value?.version_id !== target.version_id || previewVersion.value.revision !== target.revision) return;
  if (workflow.value?.revision !== revision) { versionsError.value = t("agentWorkflow.restoreChanged"); return; }
  const result = await call("edit", { action: "restore", workflow_id: wid, expected_revision: revision, version_id: target.version_id, target_revision: target.revision });
  if (!result?.workflow) return;
  clearVersionView(); runId.value = ""; run.value = null; editorDirty.value = false;
  savedDefinition(checkedWorkflow(result.workflow)); controlTab.value = "overview"; controlOpen.value = true;
  notice.value = t("agentWorkflow.restoredDraft", { n: result.workflow.revision });
}
async function refresh() {
  clearTimeout(timer); abort?.abort();
  if (!props.visible || !bound() || disposed) return;
  const key = scope(), mine = epoch, ticket = ++request, aid = props.agentId!;
  const controller = new AbortController(); abort = controller; loading.value = true;
  try {
    const listed = showDeleted.value
      ? await agentWorkflowsApi.call(aid, "read", { action: "list", include_deleted: true }, controller.signal)
      : await agentWorkflowsApi.list(aid, controller.signal);
    if (!current(key, mine) || ticket !== request) return;
    workflows.value = (listed.workflows || []).filter(item => !item.agent_id || item.agent_id === aid); setCapabilities(listed.capabilities);
    if (workflowId.value) {
      const wid = workflowId.value;
      const [detail, history] = await Promise.all([
        agentWorkflowsApi.call(aid, "read", { action: "get", workflow_id: wid }, controller.signal),
        agentWorkflowsApi.call(aid, "read", { action: "runs", workflow_id: wid }, controller.signal),
      ]);
      if (!current(key, mine) || ticket !== request || workflowId.value !== wid) return;
      workflow.value = checkedWorkflow(detail.workflow); setCapabilities(detail.capabilities);
      runs.value = (history.runs || []).filter(item => item.workflow_id === wid && item.agent_id === aid).sort((a, b) => (b.created_at || 0) - (a.created_at || 0));
      if (runId.value) {
        const rid = runId.value;
        const result = await agentWorkflowsApi.call(aid, "read", { action: "run", workflow_id: wid, run_id: rid }, controller.signal);
        if (!current(key, mine) || ticket !== request || workflowId.value !== wid || runId.value !== rid) return;
        run.value = checkedRun(result.run); setCapabilities(result.capabilities);
      }
    }
    error.value = "";
  } catch (issue) {
    if (current(key, mine) && ticket === request && (issue as Error)?.name !== "AbortError") error.value = (issue as Error)?.message || t("agentWorkflow.unavailable");
  } finally {
    if (current(key, mine) && ticket === request) { loading.value = false; timer = setTimeout(() => void refresh(), 2000); }
  }
}
async function leaveEditor() {
  clearRunInput();
  const intent = ++navigation;
  const accepted = await editor.value?.prepareClose() ?? true;
  return accepted && intent === navigation;
}
async function chooseWorkflow(id: string, selectedRun?: string) {
  if (!validAgentWorkflowId(id) || selectedRun && !validAgentWorkflowId(selectedRun)) return;
  if (workflowId.value === id && runId.value === (selectedRun || "") && !previewDefinition.value) { void refresh(); return; }
  if (!await leaveEditor()) return;
  resetVersions(); controlOpen.value = false; controlTab.value = "overview";
  ++request; abort?.abort(); workflowId.value = id; runId.value = selectedRun || ""; workflow.value = null; run.value = null; runs.value = []; diagnostics.value = []; feedback.value = ""; error.value = ""; notice.value = ""; showProcess.value = false; controlOpen.value = false;
  editorDirty.value = false;
  void refresh();
}
async function chooseRun(id: string) {
  if (busy.value || previewLoading.value) return;
  if (id && !validAgentWorkflowId(id)) return;
  if (id === runId.value && !previewDefinition.value || !await leaveEditor()) return;
  clearVersionView();
  ++request; abort?.abort(); runId.value = id; run.value = null; showProcess.value = false; feedback.value = ""; void refresh();
  editorDirty.value = false;
}
async function back() {
  if (!await leaveEditor()) return;
  resetVersions();
  ++request; abort?.abort(); workflowId.value = ""; runId.value = ""; workflow.value = null; run.value = null; runs.value = []; diagnostics.value = []; error.value = ""; notice.value = ""; showProcess.value = false; controlOpen.value = false; editorDirty.value = false; emit("conversation", false); void refresh();
}
function savedDefinition(value: AgentWorkflowDefinition) {
  if (value.id !== workflowId.value || value.agent_id && value.agent_id !== props.agentId) return;
  ++request; abort?.abort(); workflow.value = value; editorDirty.value = false; diagnostics.value = [];
  workflows.value = workflows.value.map(item => item.id === value.id ? value : item);
  void refresh();
}
function editorIntent(value: string) {
  if (editorDirty.value) { notice.value = t("agentWorkflow.saveBeforeAction"); return; }
  showProcess.value = false; controlOpen.value = false; emit("conversation", true); emit("intent", value);
}
async function testCodeNode(value: { stepId: string; inputs: Record<string, unknown>; expectedRevision: number; expectedHash: string }) {
  const flow = workflow.value;
  if (!flow || !props.sessionId || !canRun.value || viewingSnapshot.value || editorDirty.value || busy.value || !flow.steps.some(step => step.id === value.stepId && step.kind === 'code')) return;
  const key = scope(), mine = epoch, intent = navigation, sessionId = props.sessionId;
  const result = await call('run', { action: 'test_node', workflow_id: flow.id, step_id: value.stepId, expected_revision: value.expectedRevision, expected_hash: value.expectedHash, node_inputs: value.inputs });
  if (!current(key, mine) || intent !== navigation || flow.id !== workflowId.value || sessionId !== props.sessionId) return;
  if (result?.run && validAgentWorkflowId(result.run.id)) { chooseRun(result.run.id); notice.value = t('agentWorkflow.waiting'); }
}
async function call(group: "read" | "edit" | "enable" | "run" | "confirm", body: Record<string, unknown>): Promise<AgentWorkflowResponse | null> {
  if (busy.value || !props.visible || !bound()) return null;
  const key = scope(), mine = epoch, aid = props.agentId!, wid = workflowId.value;
  const requestBody = { ...body, ...(props.sessionId ? { source_session_id: props.sessionId } : {}) };
  const intent = JSON.stringify([aid, group, requestBody]);
  let requestId = operationKeys.get(intent);
  if (!requestId) { requestId = randomUUID(); operationKeys.set(intent, requestId); }
  busy.value = true; error.value = ""; notice.value = "";
  try {
    const response = await agentWorkflowsApi.call(aid, group, { ...requestBody, ...(group === "read" ? {} : { request_id: requestId }) });
    if (!current(key, mine) || workflowId.value !== wid) return null;
    if (response.ok === false) throw new Error(t("agentWorkflow.failed"));
    operationKeys.delete(intent); setCapabilities(response.capabilities);
    if (group !== 'read') runOverview?.refresh();
    return response;
  } catch (issue) {
    if (current(key, mine)) error.value = (issue as Error)?.message || t("agentWorkflow.failed");
    return null;
  } finally { if (scope() === key && epoch === mine) busy.value = false; }
}
function editInConversation() {
  if (!workflow.value || !canEdit.value) return;
  editorIntent(t("agentWorkflow.editPrompt", { id: workflow.value.id, name: workflow.value.name, revision: workflow.value.revision }));
}
async function validate() {
  if (!workflow.value) return;
  const result = await call("read", { action: "validate", workflow_id: workflow.value.id, expected_revision: workflow.value.revision });
  if (result) { diagnostics.value = result.diagnostics || []; notice.value = t(result.valid ? "agentWorkflow.valid" : "agentWorkflow.invalid"); }
}
async function enable(enabled: boolean) {
  if (!workflow.value) return;
  const result = await call("enable", { action: enabled ? "enable" : "disable", workflow_id: workflow.value.id, expected_revision: workflow.value.revision, expected_hash: workflow.value.definition_hash });
  if (result) { workflow.value = checkedWorkflow(result.workflow); void refresh(); }
}
async function archive() {
  if (!workflow.value) return;
  if (!await leaveEditor()) return;
  const key = scope(), mine = epoch;
  try { await ElMessageBox.confirm(t("agentWorkflow.archiveQuestion"), t("agentWorkflow.archive"), { confirmButtonText: t("agentWorkflow.archiveConfirm"), cancelButtonText: t("agentWorkflow.cancel"), type: "warning" }); }
  catch { return; }
  if (!current(key, mine)) return;
  const result = await call("edit", { action: "archive", workflow_id: workflow.value.id, expected_revision: workflow.value.revision });
  if (result) { workflow.value = checkedWorkflow(result.workflow); void refresh(); }
}
async function deleteWorkflow(target: AgentWorkflowDefinition | null = workflow.value) {
  if (!target || !canDelete(target)) return;
  const key = scope(), mine = epoch, location = workflowId.value, id = target.id, revision = target.revision;
  deletingFlowId.value = id;
  try {
    if (id === location && !await leaveEditor()) return;
    try { await ElMessageBox.confirm(t("agentWorkflow.deleteQuestion", { name: target.name }), t("agentWorkflow.delete"), { confirmButtonText: t("agentWorkflow.deleteConfirm"), cancelButtonText: t("agentWorkflow.cancel"), type: "warning" }); }
    catch { return; }
    if (!current(key, mine) || workflowId.value !== location) return;
    if (hasActiveRuns(id)) { error.value = t("agentWorkflow.deleteActiveHint"); return; }
    const result = await call("edit", { action: "delete", workflow_id: id, expected_revision: revision });
    if (!result || !current(key, mine) || workflowId.value !== location) return;
    if (result.workflow?.id !== id || !result.workflow.deleted) { error.value = t("agentWorkflow.failed"); return; }
    workflows.value = workflows.value.filter(item => item.id !== id);
    if (location === id) {
      clearRunInput(); resetVersions(); ++request; abort?.abort();
      workflowId.value = ""; runId.value = ""; workflow.value = null; run.value = null; runs.value = [];
      diagnostics.value = []; showProcess.value = false; controlOpen.value = false; editorDirty.value = false; emit("conversation", false);
    }
    notice.value = t("agentWorkflow.deletedNotice", { name: target.name });
    await refresh();
  } finally { if (scope() === key && epoch === mine) deletingFlowId.value = ""; }
}
function clearRunInput() {
  ++inputTicket; inputAbort?.abort(); inputLoading.value = false; inputOpen.value = false; preparedStart.value = null; preparedTrial.value = null; inputs.value = {}; files.value = "";
}
async function prepareRun(mode: "trial" | "start") {
  const flow = workflow.value;
  if (!flow || !canRun.value || !props.visible || !props.sessionId || inputLoading.value) return;
  if (editorDirty.value) { notice.value = t("agentWorkflow.saveBeforeAction"); return; }
  clearRunInput();
  controlOpen.value = false;
  inputMode.value = mode;
  if (mode === "trial") {
    const definition = cloneWorkflowDefinition(flow);
    preparedTrial.value = { definition, sessionId: props.sessionId, scope: scope(), epoch, navigation };
    inputs.value = workflowInputDefaults(definition.inputs || []); inputOpen.value = true; void resourceCatalog.refresh(); return;
  }
  if (!flow.enabled_version) return;
  const key = scope(), mine = epoch, intent = navigation, wid = flow.id, sessionId = props.sessionId, ticket = ++inputTicket;
  const controller = new AbortController(); inputAbort = controller; inputLoading.value = true; error.value = "";
  const valid = () => current(key, mine) && ticket === inputTicket && intent === navigation && wid === workflowId.value && sessionId === props.sessionId && canRun.value;
  try {
    // The active snapshot can have a different input schema from the draft.
    const listed = await agentWorkflowsApi.call(props.agentId!, "read", { action: "versions", workflow_id: wid }, controller.signal);
    if (!valid()) return;
    const enabled = listed.enabled_version;
    if (listed.ok === false || !enabled || !validVersion(enabled) || enabled.version_id !== flow.enabled_version) throw new Error(t("agentWorkflow.versionsUnavailable"));
    const result = await agentWorkflowsApi.call(props.agentId!, "read", { action: "version", workflow_id: wid, version_id: enabled.version_id, target_revision: enabled.revision }, controller.signal);
    if (!valid()) return;
    const definition = checkedWorkflow(result.workflow);
    if (result.ok === false || definition.definition_hash !== enabled.version_id || definition.revision !== enabled.revision || result.version?.version_id !== enabled.version_id || result.version.revision !== enabled.revision || !result.version.is_enabled) throw new Error(t("agentWorkflow.versionsUnavailable"));
    if (editorDirty.value) { notice.value = t("agentWorkflow.saveBeforeAction"); return; }
    preparedStart.value = { definition, versionId: enabled.version_id, sessionId, scope: key, epoch: mine, navigation: intent };
    inputs.value = workflowInputDefaults(definition.inputs || []); inputOpen.value = true; void resourceCatalog.refresh();
  } catch (issue) {
    if (valid() && (issue as Error)?.name !== "AbortError") error.value = (issue as Error)?.message || t("agentWorkflow.versionsUnavailable");
  } finally { if (ticket === inputTicket) inputLoading.value = false; }
}
async function createWorkflow() {
  if (!createName.value.trim() || !createTask.value.trim() || capabilities.value.edit !== true) return;
  if (!createStepId.value) createStepId.value = "step_" + randomUUID().replace(/-/g, "").slice(0, 12);
  const result = await call("edit", { action: "create", name: createName.value.trim(), inputs: [], steps: [{ id: createStepId.value, kind: "agent", title: t("agentWorkflow.firstStepTitle"), task: createTask.value.trim(), ...(createOutput.value.trim() ? { output_file: createOutput.value.trim() } : {}) }] });
  if (result?.workflow) { showDeleted.value = false; createOpen.value = false; createName.value = ""; createTask.value = ""; createOutput.value = ""; createStepId.value = ""; await chooseWorkflow(result.workflow.id); }
}
async function start() {
  const flow = workflow.value;
  if (!flow || !props.sessionId || !canRun.value || !inputOpen.value) return;
  const prepared = preparedStart.value, key = scope(), mine = epoch, intent = navigation, sessionId = props.sessionId;
  if (inputMode.value === "start" && (!prepared || prepared.definition.id !== flow.id || prepared.scope !== key || prepared.epoch !== mine || prepared.navigation !== intent || prepared.sessionId !== sessionId)) return;
  const trial = preparedTrial.value;
  if (inputMode.value === "trial" && (!trial || trial.definition.id !== flow.id || trial.scope !== key || trial.epoch !== mine || trial.navigation !== intent || trial.sessionId !== sessionId)) return;
  const selectedFiles = workflowSelectedFiles(files.value);
  if (selectedFiles.length > 16) { error.value = t('agentWorkflow.inputFilesTooMany'); return; }
  const result = await call("run", { action: inputMode.value, workflow_id: flow.id,
    ...(inputMode.value === "trial" ? { expected_revision: trial!.definition.revision, expected_hash: trial!.definition.definition_hash } : { version_id: prepared!.versionId }),
    inputs: { ...inputs.value }, files: selectedFiles });
  if (!current(key, mine) || intent !== navigation || flow.id !== workflowId.value || sessionId !== props.sessionId) return;
  if (result?.run && validAgentWorkflowId(result.run.id)) { inputOpen.value = false; chooseRun(result.run.id); notice.value = t("agentWorkflow.waiting"); }
}
async function confirm(stepId = waitingStep.value?.id || "") {
  if (!run.value || capabilities.value.confirm !== true || !validAgentWorkflowId(stepId) || !waitingSteps.value.some(step => step.id === stepId)) return;
  const rid = run.value.id;
  const result = await call("confirm", { workflow_id: workflowId.value, run_id: rid, step_id: stepId, feedback: feedbackFor(stepId) });
  if (result && runId.value === rid) {
    feedback.value = ""; delete confirmationFeedback.value[stepId];
    if (result.run?.id === rid) run.value = checkedRun(result.run);
    void refresh();
  }
}
async function control(action: "cancel" | "retry") {
  if (!run.value || action === "retry" && !canRetry.value) return;
  const rid = run.value.id, key = scope(), mine = epoch;
  try { await ElMessageBox.confirm(t(action === "cancel" ? "agentWorkflow.cancelQuestion" : "agentWorkflow.retryQuestion"), t(action === "cancel" ? "agentWorkflow.cancelRun" : "agentWorkflow.retry"), { confirmButtonText: t(action === "cancel" ? "agentWorkflow.cancelConfirm" : "agentWorkflow.retryConfirm"), cancelButtonText: t("agentWorkflow.cancel"), type: "warning" }); }
  catch { return; }
  if (!current(key, mine) || runId.value !== rid) return;
  const result = await call("run", { action, workflow_id: workflowId.value, run_id: rid });
  if (result?.run) { if (action === "retry") chooseRun(result.run.id); else void refresh(); }
}
async function artifact(stepId: string) {
  if (!run.value || !stepId) return;
  const result = await call("read", { action: "artifact", workflow_id: workflowId.value, run_id: run.value.id, step_id: stepId });
  if (result) { artifactPath.value = result.path || stepId; artifactText.value = result.text || ""; artifactOpen.value = true; }
}
function reset() {
  nodeContextReadings.value = {};
  showDeleted.value = false; deletingFlowId.value = "";
  nodeProcessOpen.value = false; nodeProcessStepId.value = "";
  clearRunInput();
  resetVersions(); controlTab.value = "overview";
  controlOpen.value = false; pendingOpen.value = false; emit("conversation", false);
  selectedStepId.value = ""; confirmationFeedback.value = {};
  ++epoch; ++request; ++navigation; abort?.abort(); clearTimeout(timer); workflows.value = []; workflow.value = null; runs.value = []; run.value = null; workflowId.value = ""; runId.value = ""; capabilities.value = {}; diagnostics.value = []; error.value = ""; notice.value = ""; loading.value = false; busy.value = false; feedback.value = ""; inputOpen.value = false; inputs.value = {}; files.value = ""; artifactOpen.value = false; artifactText.value = ""; artifactPath.value = ""; showProcess.value = false; editorDirty.value = false; createOpen.value = false; createName.value = ""; createTask.value = ""; createOutput.value = ""; createStepId.value = ""; selectionSeen = -1; operationKeys.clear();
}
function authenticationChanged() { ++authEpoch; ++activityAuthEpoch.value; reset(); if (props.visible) void refresh(); }
function requestedSelection() {
  if (!props.visible || !props.selection || props.selection.sequence === selectionSeen) return false;
  selectionSeen = props.selection.sequence; void chooseWorkflow(props.selection.workflowId, props.selection.runId); return true;
}
let liveSelectionSeen = "";
function receiveLiveUpdate() {
  const update = liveUpdate.value;
  if (!props.visible || !update || update.agentId !== props.agentId || update.sessionId !== props.sessionId) return false;
  const identity = `${update.sessionId}:${update.seq}`;
  if (identity === liveSelectionSeen) return false;
  liveSelectionSeen = identity;
  // Stay on an existing draft or frozen Run. A create/patch receipt selects a
  // workflow only while the panel is showing its list.
  if (!workflowId.value && ["create", "patch"].includes(update.action)) void chooseWorkflow(update.workflowId);
  else void refresh();
  return true;
}
watch(() => props.agentId, () => { reset(); if (props.visible && bound() && !requestedSelection()) void refresh(); }, { immediate: true, flush: "sync" });
watch(() => props.visible, visible => { if (visible) { if (!requestedSelection() && !receiveLiveUpdate()) void refresh(); } else { clearRunInput(); ++request; abort?.abort(); clearTimeout(timer); loading.value = false; } });
watch(liveUpdate, receiveLiveUpdate, { flush: "post" });
watch([workflowId, () => props.immersive, editorDirty], () => void nextTick(resizeWorkspace));
watch(() => props.selection?.sequence, requestedSelection);
watch(workflowId, id => emit("workspace", !!id), { immediate: true });
watch(() => props.toolRevision, () => { if (props.visible) void refresh(); });
watch(() => props.sessionId, clearRunInput, { flush: "sync" });
watch(() => activityStream.connection.value, connection => { if (connection === 'denied') { closeNodeProcess(); showProcess.value = false; } }, { flush: 'sync' });
watch(canRun, allowed => { if (!allowed) clearRunInput(); }, { flush: "sync" });
watch(() => props.conversationOpen, value => { if (value) { showProcess.value = false; controlOpen.value = false; } });
watch([() => workflow.value?.revision, () => workflow.value?.enabled_version, controlOpen], () => { if (controlOpen.value && workflowId.value) void loadVersions(); });
watch(() => waitingSteps.value.length, count => { if (!count) pendingOpen.value = false; });
watch(() => run.value?.status, status => { if (status === "waiting_confirmation") showProcess.value = false; if (status && notice.value === t("agentWorkflow.waiting")) notice.value = ""; });
watch(() => `${run.value?.id || ""}:${waitingStep.value?.id || ""}`, () => { feedback.value = ""; });
watch([workflowId, runId], () => { clearRunInput(); pendingOpen.value = false; selectedStepId.value = ""; confirmationFeedback.value = {}; }, { flush: "sync" });
watch(() => run.value?.id, id => {
  if (!id || !run.value) return;
  if (run.value.node_test) {
    showProcess.value = false; emit('conversation', false);
    const stepId = run.value.node_test.step_id;
    void nextTick(() => focusStep(stepId));
  } else if (["queued", "running"].includes(run.value.status) && containsAgent(run.value.definition)) { showProcess.value = true; emit("conversation", false); }
});
onMounted(() => {
  window.addEventListener("pogo:auth-changed", authenticationChanged); window.addEventListener("resize", resizeWorkspace);
  if (typeof ResizeObserver !== "undefined") {
    frameObserver = new ResizeObserver(resizeWorkspace);
    if (sheetFrame.value) frameObserver.observe(sheetFrame.value);
    if (floatingHeader.value) frameObserver.observe(floatingHeader.value);
  }
  resizeWorkspace(); receiveLiveUpdate();
});
onBeforeUnmount(() => { disposed = true; reset(); frameObserver?.disconnect(); window.removeEventListener("pogo:auth-changed", authenticationChanged); window.removeEventListener("resize", resizeWorkspace); });
</script>

<template>
  <section ref="sheetFrame" class="agent-workflow-sheet" :class="{ 'af-workspace': workflowId }" :style="{ '--af-process-height': processHeight + 'px', '--af-top-inset': topInset + 'px' }" data-testid="agent-workflow-sheet" :data-agent-id="agentId" @keydown.esc="workspaceEscape">
    <header ref="floatingHeader" class="af-head" :class="{ 'af-head-floating': workflowId }">
      <button v-if="workflowId" type="button" class="af-back" :aria-label="t('agentWorkflow.back')" :title="t('agentWorkflow.back')" @click="back">←</button>
      <div class="af-heading"><small v-if="!workflowId">{{ t('agentWorkflow.subtitle') }}</small><button v-if="workflow" type="button" class="af-name" :title="t('agentWorkflow.settings')" @click="workflowSettings">{{ run?.definition.name || previewDefinition?.name || workflow.name }}</button><h2 v-else>{{ t('agentWorkflow.title') }}</h2>
        <button v-if="run" type="button" class="af-run-heading-meta" :title="currentRunSummary" data-testid="workflow-header-run-status" @click="focusCurrentRun"><AgentWorkflowRunBadge :status="selectedRunStatus" :stale="active && !activityStream.connected.value" /><span>{{ currentRunSummary }}</span></button>
      </div>
      <div v-if="workflow" class="af-header-actions">
        <button v-if="currentFlowActivity && currentFlowActivity.preferred.id !== run?.id" type="button" class="af-live-run-link" :title="runOverview?.stale.value ? statusCopy.syncing : statusCopy.viewActive" data-testid="workflow-open-active-run" @click="openActiveRun()"><AgentWorkflowRunBadge :status="overviewStatus(currentFlowActivity)" :count="currentFlowActivity.active" :stale="runOverview?.stale.value" /><span>{{ statusCopy.viewActive }} ↗</span></button>
        <button type="button" class="af-mode-button" :class="{ 'is-history': viewingSnapshot }" :aria-label="t('agentWorkflow.controlTitle')" @click="openControl(previewVersion ? 'versions' : runId ? 'runs' : 'overview')">{{ t(workflow.deleted && !viewingSnapshot ? 'agentWorkflow.deletedDefinition' : previewVersion ? 'agentWorkflow.versionPreviewLabel' : runId ? 'agentWorkflow.runSnapshotLabel' : 'agentWorkflow.draftVersionLabel', { n: previewVersion?.revision || run?.definition.revision || workflow.revision }) }}</button>
        <button v-if="viewingSnapshot" type="button" class="af-return-draft" :disabled="busy || previewLoading" @click="chooseRun('')">{{ t(workflow.deleted ? 'agentWorkflow.deletedDefinition' : 'agentWorkflow.backToDraft') }}</button>
        <span v-if="!viewingSnapshot && !workflow.deleted" class="af-save-state" role="status">{{ t(editorStatus.saving ? 'agentWorkflowCanvas.saving' : editorDirty ? 'agentWorkflowCanvas.unsaved' : 'agentWorkflowCanvas.saved') }}</span>
        <button v-if="!viewingSnapshot && !workflow.deleted && editorDirty" type="button" class="af-primary" data-testid="workflow-save-draft" :disabled="busy || !editorStatus.canSave" @click="editor?.save?.()">{{ t('agentWorkflowCanvas.save') }}</button>
        <button type="button" class="af-chat-button" :aria-pressed="!!conversationOpen" @click="toggleConversation">{{ t('agentWorkflow.conversation') }}</button>
        <button type="button" class="af-immersive-button" :aria-pressed="!!immersive" data-testid="workflow-immersive-mode" @click="emit('immersive', !immersive)">{{ t(immersive ? 'agentWorkflow.exitImmersive' : 'agentWorkflow.enterImmersive') }}</button>
      </div>
      <button type="button" :disabled="loading" :aria-label="t('agentWorkflow.refresh')" :title="t('agentWorkflow.refresh')" @click="refresh">↻</button>
      <button type="button" :aria-label="t('agentWorkflow.close')" :title="t('agentWorkflow.close')" @click="emit('close')">×</button>
    </header>
    <div v-if="!workflowId" class="af-list-body">
      <p v-if="error" class="af-error" role="alert">{{ error }}</p><p v-if="notice" class="af-note" role="status">{{ notice }}</p>
      <p v-if="capabilities.reason || capabilities.missing_tools?.length" class="af-capability">{{ capabilities.reason || t('agentWorkflow.disabled') }}</p>
      <div class="af-toolbar"><button v-if="!showDeleted" type="button" class="af-primary" :disabled="busy || capabilities.edit !== true" @click="createOpen = true">＋ {{ t('agentWorkflow.createCanvas') }}</button><button type="button" class="af-deleted-toggle" :aria-pressed="showDeleted" :disabled="busy || !!deletingFlowId" data-testid="workflow-deleted-toggle" @click="setDeletedView(!showDeleted)">{{ t(showDeleted ? 'agentWorkflow.backToWorkflows' : 'agentWorkflow.viewDeleted') }}</button></div>
      <p v-if="showDeleted" class="af-note" data-testid="workflow-deleted-hint">{{ t('agentWorkflow.deletedHint') }}</p>
      <p v-if="runOverview?.stale.value" class="af-note af-status-sync" role="status">{{ runOverview.connection.value === 'denied' ? statusCopy.denied : statusCopy.syncing }}</p><p v-else-if="runOverview?.connection.value === 'loading'" class="af-note" role="status">{{ statusCopy.loading }}</p>
      <p v-if="!loading && !visibleWorkflows.length" class="af-empty">{{ t(showDeleted ? 'agentWorkflow.noDeleted' : 'agentWorkflow.empty') }}</p>
      <div class="af-list"><article v-for="flow in visibleWorkflows" :key="flow.id" class="af-flow" :class="{ 'has-active-run': flowActivity(flow.id), 'needs-confirmation': flowActivity(flow.id)?.waiting, 'is-deleted': flow.deleted }">
        <button type="button" class="af-flow-open" @click="chooseWorkflow(flow.id)"><strong>{{ flow.name }}</strong><small>{{ flow.description }}</small></button>
        <div class="af-flow-meta"><span class="af-badge">{{ t(flow.deleted ? 'agentWorkflow.deleted' : flow.archived ? 'agentWorkflow.archived' : flow.enabled_version ? 'agentWorkflow.enabled' : 'agentWorkflow.draft') }}</span><small>{{ date(flow.updated_at) }}</small></div>
        <button v-if="!flow.deleted" type="button" class="af-flow-delete" :disabled="!canDelete(flow)" :aria-label="t('agentWorkflow.deleteNamed', { name: flow.name })" :title="hasActiveRuns(flow.id) ? t('agentWorkflow.deleteActiveHint') : t('agentWorkflow.deleteNamed', { name: flow.name })" data-testid="workflow-list-delete" @click.stop="deleteWorkflow(flow)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M9 3h6l1 4H8l1-4ZM6 7l1 14h10l1-14M10 11v6M14 11v6" /></svg><span>{{ t(deletingFlowId === flow.id ? 'agentWorkflow.deleting' : 'agentWorkflow.deleteShort') }}</span></button>
        <button v-if="flowActivity(flow.id)" type="button" class="af-flow-live" data-testid="workflow-list-active-run" :title="runOverview?.stale.value ? statusCopy.syncing : statusCopy.viewActive" @click="openActiveRun(flow.id)"><AgentWorkflowRunBadge :status="overviewStatus(flowActivity(flow.id)!)" :count="flowActivity(flow.id)!.active" :stale="runOverview?.stale.value" /><span>{{ statusCopy.viewActive }} ↗</span></button>
      </article></div>
    </div>
    <template v-else>
      <div v-if="workflow" class="af-canvas-host">
        <AgentWorkflowEditor v-if="!runId || run" :key="editorKey" ref="editor" :definition="workflow" :run="run" :activities="nodeActivities" :context-reading="observedContextReading" :resource-catalog="resourceCatalog.catalog.value || undefined" :resources-loading="resourceCatalog.loading.value" :resources-error="resourceCatalog.error.value ? t('agentWorkflowCanvas.resourcesUnavailable') : ''" :version="previewDefinition" :capabilities="capabilities" :agent-id="agentId!" :session-id="sessionId" :visible="visible" :conversation-open="conversationOpen" :controls-open="controlOpen" :process-open="showProcess" :top-inset="topInset" :left-inset="controlOpen && viewportWidth > 760 ? 392 : 72" :bottom-inset="bottomInset" @saved="savedDefinition" @dirty="editorDirty = $event" @intent="editorIntent" @artifact="artifact" @select="selectStep" @process="nodeProcessChanged" @refresh-resources="resourceCatalog.refresh" @test-node="testCodeNode">
          <template #workflow-controls>
            <button ref="controlLauncher" type="button" class="af-control-launcher" :class="{ active: controlOpen, changed: editorDirty || workflow.enabled_version && workflow.enabled_version !== workflow.definition_hash }" :aria-label="t('agentWorkflow.controlTitle')" :title="t('agentWorkflow.controlTitle')" :aria-expanded="controlOpen" data-testid="workflow-control-launcher" @click="openControl(controlTab, true)">
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m12 3 9 5-9 5-9-5 9-5ZM3 12l9 5 9-5M3 16l9 5 9-5" /></svg><small>{{ t(workflow.deleted && !viewingSnapshot ? 'agentWorkflow.deleted' : 'agentWorkflow.versionShort', { n: previewVersion?.revision || run?.definition.revision || workflow.revision }) }}</small>
            </button>
          </template>
          <template #node-process="{ step: observedStep, stepRun: observedResult }">
            <div class="af-node-execution" data-testid="workflow-node-execution" :data-step-id="observedStep.id">
              <nav v-if="processSteps.length > 1" class="af-parallel-switch" :aria-label="t('agentWorkflow.nodeProcessSwitch')">
                <button v-for="item in processSteps" :key="item.id" type="button" :aria-pressed="observedStep.id === item.id" @click="focusStep(item.id)">{{ item.title }}<span>{{ state(item.status) }}</span></button>
              </nav>
              <TaskSessionResult v-if="nodeExecutionSession(observedStep.id) && (hasNodeSessions || observedResult?.active_run_id)" :key="`${agentId}:${run!.id}:${observedStep.id}:${nodeExecutionSession(observedStep.id)}`" compact :session-id="nodeExecutionSession(observedStep.id)" :active-run-id="observedResult?.active_run_id" :agent-id="agentId!" :workflow-id="workflowId" :workflow-run-id="run!.id" :visible="visible && nodeProcessOpen && nodeProcessStepId === observedStep.id" :title="observedStep.title || observedStep.id" hide-context-ring @context="(usage, source) => setNodeContext(observedStep.id, usage, source)" @close="closeNodeProcess" />
              <p v-else class="af-note af-node-preparing">{{ t(nodeExecutionSession(observedStep.id) ? 'agentWorkflow.legacyNodeProcessMissing' : 'agentWorkflow.nodePreparing') }}</p>
            </div>
          </template>
          <template #confirmation="{ step: confirmationStep }">
            <section v-if="waitingSteps.some(item => item.id === confirmationStep.id)" class="af-confirm" data-testid="agent-workflow-confirmation" :data-step-id="confirmationStep.id">
              <h3>{{ confirmationStep.title || t('agentWorkflow.confirmation') }}</h3>
              <MarkdownView v-if="stepResult(confirmationStep.id)?.summary" :source="stepResult(confirmationStep.id)!.summary!" passive /><p v-else>{{ confirmationStep.prompt }}</p>
              <p class="af-note">{{ t('agentWorkflow.confirmationHint') }}</p><label :for="`agent-workflow-feedback-${confirmationStep.id}`">{{ t('agentWorkflow.feedback') }}</label>
              <textarea :id="`agent-workflow-feedback-${confirmationStep.id}`" :value="feedbackFor(confirmationStep.id)" @input="setFeedback(confirmationStep.id, ($event.target as HTMLTextAreaElement).value)" rows="3" maxlength="16000" :disabled="busy || capabilities.confirm !== true" />
              <div class="af-toolbar"><button type="button" class="af-primary" data-testid="agent-workflow-continue" :disabled="busy || capabilities.confirm !== true" @click="confirm(confirmationStep.id)">{{ t('agentWorkflow.continue') }}</button><button type="button" :disabled="busy || capabilities.run !== true" @click="control('cancel')">{{ t('agentWorkflow.stopHere') }}</button></div>
            </section>
          </template>
        </AgentWorkflowEditor>
        <p v-else class="af-loading" role="status">{{ t('agentWorkflow.loadingRun') }}</p>
      </div>
      <p v-else class="af-loading" role="status">{{ t('agentWorkflow.loadingRun') }}</p>
      <div v-if="!controlOpen && (workflow?.deleted || error || notice || capabilities.reason || capabilities.missing_tools?.length || diagnostics.length)" class="af-notices">
        <p v-if="workflow?.deleted" class="af-note" role="status">{{ t('agentWorkflow.deletedHint') }}</p>
        <p v-if="error" class="af-error" role="alert">{{ error }}</p><p v-if="notice" class="af-note" role="status">{{ notice }}</p>
        <p v-if="capabilities.reason || capabilities.missing_tools?.length" class="af-capability">{{ capabilities.reason || t('agentWorkflow.disabled') }}</p>
        <ul v-if="diagnostics.length" class="af-diagnostics"><li v-for="(item,index) in diagnostics" :key="index"><strong>{{ item.message }}</strong><small v-if="item.field_path">{{ item.field_path }}</small><p v-if="item.fix_hint">{{ item.fix_hint }}</p></li></ul>
      </div>
      <div v-if="workflow && controlOpen" class="af-control-panel" :style="{ '--af-card-bottom': run ? '116px' : '26px' }" @wheel.stop @pointerdown.stop>
        <AgentWorkflowControlCard :workflow="workflow" :run="run" :preview="previewVersion" :preview-definition="previewDefinition" :enabled-version="enabledVersion" :versions="versions" :versions-loading="versionsLoading" :versions-error="versionsError" :has-more-versions="hasMoreVersions" :runs="runs" :tab="controlTab" :dirty="editorDirty" :saving="editorStatus.saving" :busy="busy || previewLoading" :actions="workflowActions" :can-restore="canRestore" :operation-error="error || capabilities.reason || (capabilities.missing_tools?.length ? t('agentWorkflow.disabled') : '')" :operation-notice="notice" :diagnostics="diagnostics" @close="closeControl()" @tab="setControlTab" @action="workflowAction" @version="previewSavedVersion" @restore="restoreSavedVersion" @run="chooseRun" @draft="chooseRun('')" @load-more="loadVersions(true)" @retry-versions="loadVersions()" />
      </div>
      <div v-if="run" class="af-run-status" :style="{ bottom:(showProcess ? processHeight + 32 : 18)+'px' }" data-testid="agent-workflow-run-status">
        <AgentWorkflowRunBadge :status="selectedRunStatus" /><span v-if="active" class="af-activity-link" :class="{ stale: !activityStream.connected.value }" role="status">{{ t(activityStream.connected.value ? 'agentWorkflow.activityLive' : activityStream.connection.value === 'connecting' ? 'agentWorkflow.activityConnecting' : activityStream.connection.value === 'denied' ? 'agentWorkflow.activityUnavailable' : 'agentWorkflow.activityReconnecting') }}</span><button v-if="activityStream.connection.value === 'denied'" type="button" @click="activityStream.refresh">{{ t('agentWorkflow.retryActivity') }}</button><span v-if="run.trial" class="af-badge">{{ t(run.node_test ? 'agentWorkflow.nodeTestLabel' : 'agentWorkflow.testLabel') }}</span>
        <button v-if="waitingSteps.length" type="button" class="af-confirm-link" :aria-expanded="pendingOpen" @click="pendingOpen = !pendingOpen">{{ t('agentWorkflow.pendingCount',{n:waitingSteps.length}) }} ⌄</button>
        <div v-if="pendingOpen && waitingSteps.length" class="af-pending-panel" :aria-label="t('agentWorkflow.pendingNodes')"><button v-for="pending in waitingSteps" :key="pending.id" type="button" @click="focusConfirmation(pending.id)">{{ pending.title || pending.id }}</button></div>
        <button type="button" :aria-expanded="showProcess" @click="toggleProcess">{{ t('agentWorkflow.processTitle') }}</button>
        <button v-if="active" type="button" :disabled="busy || capabilities.run !== true || run.cancel_requested" @click="control('cancel')">{{ t('agentWorkflow.cancelRun') }}</button><button v-else-if="canRetry" type="button" :disabled="busy || !canRun" @click="control('retry')">{{ t('agentWorkflow.retry') }}</button>
        <span v-if="run.cancel_requested" class="af-note">{{ t('agentWorkflow.cancelling') }}</span><span v-if="run.status === 'interrupted'" class="af-note">{{ t('agentWorkflow.interruptedHint') }}</span><span v-if="run.error" class="af-error">{{ run.error }}</span>
      </div>
      <section v-if="run && showProcess" class="af-process-drawer" :aria-label="t('agentWorkflow.processTitle')" data-testid="agent-workflow-process-drawer" @wheel.stop>
        <header><strong>{{ t('agentWorkflow.processTitle') }}</strong><small>{{ run.id.slice(-8) }}</small><TaskContextRing :reading="processContextReading" /><button type="button" :aria-label="t('agentWorkflow.processClose')" @click="showProcess = false">×</button></header>
        <div v-if="processSteps.length" class="af-node-process" data-testid="agent-workflow-node-process"><div class="af-toolbar"><button v-for="item in processSteps" :key="item.id" type="button" :class="{ 'af-primary':processStep?.id === item.id }" :aria-pressed="processStep?.id === item.id" @click="chooseProcessStep(item.id)">{{ item.title }} · {{ state(item.status) }}</button></div></div>
        <TaskSessionResult v-if="processSessionId" :key="`${agentId}:${run.id}:${processSessionId}:${processActiveRunId || ''}`" compact :active-run-id="processActiveRunId" :session-id="processSessionId" :agent-id="agentId!" :workflow-id="workflowId" :workflow-run-id="run.id" :visible="visible && showProcess" :title="processStep ? processStep.title + ' · ' + state(processStep.status) : state(run.status)" hide-context-ring @context="(usage, source) => setNodeContext(processStep?.id || selectedStepId, usage, source)" @close="showProcess = false" />
        <p v-else class="af-note">{{ t('agentWorkflow.chooseAgentProcess') }}</p>
      </section>
    </template>
    <el-dialog v-model="createOpen" :title="t('agentWorkflow.createCanvas')" width="min(560px, 92vw)" append-to-body :close-on-click-modal="false">
      <form class="af-form" @submit.prevent="createWorkflow">
        <label><span>{{ t('agentWorkflow.flowName') }}</span><input v-model="createName" required maxlength="160" :placeholder="t('agentWorkflow.flowNamePlaceholder')" /></label>
        <label><span>{{ t('agentWorkflow.firstTask') }}</span><textarea v-model="createTask" required maxlength="32000" rows="4" :placeholder="t('agentWorkflow.firstTaskPlaceholder')" /></label>
        <label><span>{{ t('agentWorkflow.firstOutput') }}</span><input v-model="createOutput" maxlength="1024" placeholder="notes.md" /></label>
        <p class="af-note">{{ t('agentWorkflow.createCanvasHint') }}</p><p v-if="error" class="af-error" role="alert">{{ error }}</p>
        <button type="submit" class="af-primary" :disabled="busy || !createName.trim() || !createTask.trim() || capabilities.edit !== true">{{ t('agentWorkflow.createAndEdit') }}</button>
      </form>
    </el-dialog>
    <el-dialog v-model="inputOpen" :title="t('agentWorkflow.inputTitle')" width="min(520px, 92vw)" append-to-body :close-on-click-modal="false"><p v-if="inputMode === 'start' && preparedStart" class="af-note">{{ t('agentWorkflow.start') }} · {{ t('agentWorkflow.versionShort', { n: preparedStart.definition.revision }) }}</p><p class="af-note"><template v-if="inputMode === 'trial' && preparedTrial">{{ t('agentWorkflow.trial') }} · {{ t('agentWorkflow.versionShort',{ n:preparedTrial.definition.revision }) }}<br /></template>{{ t(runInputNotice) }}</p><p v-if="runInputApprovalMode" class="af-note" data-testid="workflow-run-input-approval">{{ t(inputMode === 'start' ? 'agentWorkflowCanvas.toolApprovalStartInput' : 'agentWorkflowCanvas.toolApprovalTrialInput', { mode: t(`policyPicker.modes.${runInputApprovalMode}.label`) }) }}</p><form class="af-form" @submit.prevent="start"><div class="af-run-fields"><AgentWorkflowRunInputs v-model="inputs" v-model:files="files" :fields="inputFields" :catalog="resourceCatalog.catalog.value" :loading="resourceCatalog.loading.value" :error="resourceCatalog.error.value" :disabled="busy" @refresh="resourceCatalog.refresh" /></div><p v-if="!sessionId" class="af-error">{{ t('agentWorkflow.noSession') }}</p><p v-if="error" class="af-error" role="alert">{{ error }}</p><button type="submit" class="af-primary" :disabled="busy || !sessionId">{{ t('agentWorkflow.runNow') }}</button></form></el-dialog>
    <el-dialog v-model="artifactOpen" :title="artifactPath || t('agentWorkflow.artifactTitle')" width="min(820px, 94vw)" append-to-body><MarkdownView :source="artifactText" passive /></el-dialog>
  </section>
</template>


<style scoped>
.agent-workflow-sheet{height:100%;min-height:0;min-width:0;position:relative;display:flex;flex-direction:column;overflow:hidden;container-type:inline-size;color:var(--el-text-color-primary);background:var(--el-bg-color);font-size:13px}
.af-workspace{background:var(--el-fill-color-extra-light)}
.af-head{display:flex;flex:none;align-items:center;gap:7px;padding:15px 16px 12px;min-width:0;background:var(--el-bg-color)}
.af-head-floating{position:absolute;top:16px;left:16px;right:16px;z-index:20;flex-wrap:wrap;padding:9px 10px;border:1px solid var(--el-border-color-light);border-radius:12px;box-shadow:0 4px 20px rgb(0 0 0 / 10%)}
.af-header-actions{display:flex;align-items:center;gap:7px;flex-wrap:wrap;min-width:0}
.agent-workflow-sheet .af-run-heading-meta{display:flex;align-items:center;gap:7px;width:100%;max-width:100%;padding:1px 0 0;border:0;background:none;text-align:left}.af-run-heading-meta>span:last-child{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:10px;color:var(--el-text-color-secondary)}.af-run-heading-meta:deep(.aw-run-badge){flex:none;font-size:10px;padding:2px 5px}.agent-workflow-sheet .af-live-run-link{display:inline-flex;align-items:center;gap:6px;padding:3px 6px 3px 3px;border-color:var(--el-color-primary-light-7);background:var(--el-fill-color-extra-light)}.af-live-run-link>span:last-child{font-size:10px;color:var(--el-color-primary)}
.af-heading{flex:1;min-width:80px}.af-heading small,.af-save-state,.af-note,.af-empty,.af-flow small{color:var(--el-text-color-secondary);font-size:11px;line-height:1.6}.af-heading h2{font-size:16px;margin:3px 0;overflow-wrap:anywhere}
.agent-workflow-sheet button,.af-form button{font:inherit;font-size:12px;border:1px solid var(--el-border-color);background:var(--el-bg-color);color:var(--el-text-color-regular);padding:6px 9px;border-radius:7px;cursor:pointer;white-space:nowrap}.agent-workflow-sheet button:disabled,.af-form button:disabled{opacity:.45;cursor:default}.agent-workflow-sheet button:hover:not(:disabled){border-color:var(--el-color-primary);color:var(--el-color-primary)}
.agent-workflow-sheet .af-name{border:0;background:none;padding:4px 0;font-weight:600;font-size:14px;display:block;max-width:100%;overflow:hidden;text-overflow:ellipsis;text-align:left}.agent-workflow-sheet .af-back{border:0;padding-inline:5px}.agent-workflow-sheet .af-primary,.af-form .af-primary{color:var(--el-color-white);background:var(--el-color-primary);border-color:var(--el-color-primary)}
.af-mode-button{font-size:11px!important;background:var(--el-fill-color-light)!important}.af-mode-button.is-history{color:var(--el-color-info)!important;border-color:var(--el-color-info-light-5)!important}.af-chat-button[aria-pressed="true"]{border-color:var(--el-color-primary);color:var(--el-color-primary)}.af-save-state{white-space:nowrap}
.agent-workflow-sheet .af-control-launcher{position:relative;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:2px;width:32px;min-height:42px;padding:4px 2px;border:1px solid transparent;background:transparent;border-radius:7px;color:var(--el-text-color-secondary)}
.af-control-launcher svg{width:18px;height:18px;fill:none;stroke:currentColor;stroke-width:1.5;stroke-linecap:round;stroke-linejoin:round}.af-control-launcher small{font-size:9px;line-height:1.2;font-weight:600}.agent-workflow-sheet .af-control-launcher.active{color:var(--el-color-primary);background:var(--el-color-primary-light-9);border-color:var(--el-color-primary-light-5)}.af-control-launcher.changed:after{content:'';position:absolute;right:2px;top:3px;width:5px;height:5px;border-radius:50%;background:var(--el-color-warning);border:2px solid var(--el-bg-color)}
.af-control-panel{position:absolute;z-index:32;top:calc(var(--af-top-inset) + 6px);left:76px;width:300px;max-width:calc(100% - 92px);height:min(620px,calc(100% - var(--af-top-inset) - var(--af-card-bottom,26px)));display:flex;align-items:flex-start;min-height:0;pointer-events:none}.af-control-panel:deep(.aw-control-card){max-width:100%;max-height:100%;pointer-events:auto}
.af-list-body{min-height:0;overflow:auto;padding:0 16px 20px}.af-toolbar{display:flex;align-items:center;gap:7px;flex-wrap:wrap;margin:10px 0}.af-list{display:flex;flex-direction:column;gap:10px;padding:4px 0 16px}.agent-workflow-sheet .af-flow{display:flex;flex-wrap:wrap;align-items:center;gap:8px;text-align:left;padding:14px;white-space:normal}.af-flow>span:first-child{flex:1;min-width:130px}.af-flow strong,.af-flow small{display:block}.af-badge{font-size:10px;color:var(--el-text-color-secondary);background:var(--el-fill-color-light);padding:3px 6px;border-radius:5px;white-space:nowrap}
.af-canvas-host{position:absolute;inset:0;min-height:0;min-width:0}.af-loading{position:absolute;inset:40% 16px auto;text-align:center;color:var(--el-text-color-secondary);font-size:12px}
.agent-workflow-sheet .af-flow{border:1px solid var(--el-border-color-light);border-radius:11px;background:var(--el-bg-color);padding:12px;transition:border-color 120ms,box-shadow 120ms}.agent-workflow-sheet .af-flow.has-active-run{border-left:3px solid var(--el-color-primary);padding-left:10px}.agent-workflow-sheet .af-flow.needs-confirmation{border-left-color:var(--el-color-warning)}.agent-workflow-sheet .af-flow-open{display:block;flex:1;min-width:130px;max-width:100%;border:0;background:none;padding:2px;text-align:left;white-space:normal}.af-flow-open strong{font-size:13px;font-weight:600}.af-flow-open small{display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;margin-top:3px}.af-flow-meta{display:flex;flex-direction:column;align-items:flex-end;gap:5px}.agent-workflow-sheet .af-flow-live{display:flex;align-items:center;justify-content:space-between;gap:10px;flex-basis:100%;padding:9px 2px 0;border:0;border-top:1px solid var(--el-border-color-extra-light);border-radius:0;background:none;text-align:left}.af-flow-live>span:last-child{font-size:11px;color:var(--el-color-primary)}.af-status-sync{color:var(--el-color-warning)}
.af-notices{position:absolute;z-index:24;top:var(--af-top-inset,87px);left:88px;max-width:min(520px,calc(100% - 104px));max-height:28%;overflow:auto;box-sizing:border-box;background:var(--el-bg-color);border:1px solid var(--el-border-color-light);border-radius:10px;padding:0 12px;box-shadow:0 4px 18px rgb(0 0 0 / 9%)}
.agent-workflow-sheet .af-deleted-toggle{margin-left:auto;background:transparent;color:var(--el-text-color-secondary);border-color:transparent}.agent-workflow-sheet .af-deleted-toggle[aria-pressed="true"]{color:var(--el-color-primary);background:var(--el-color-primary-light-9)}.agent-workflow-sheet .af-flow-delete{display:inline-flex;align-items:center;gap:5px;color:var(--el-text-color-secondary);border-color:transparent;background:transparent;padding:6px 7px}.af-flow-delete svg{width:14px;height:14px;fill:none;stroke:currentColor;stroke-width:1.6;stroke-linecap:round;stroke-linejoin:round}.agent-workflow-sheet .af-flow-delete:hover:not(:disabled){color:var(--el-color-danger);border-color:var(--el-color-danger-light-7);background:var(--el-color-danger-light-9)}.agent-workflow-sheet .af-flow.is-deleted{border-style:dashed}.agent-workflow-sheet .af-flow-delete:focus-visible{outline:2px solid var(--el-color-danger);outline-offset:2px}
.af-error{color:var(--el-color-danger);font-size:12px;line-height:1.7;white-space:pre-wrap;overflow-wrap:anywhere}.af-capability{color:var(--el-color-warning);font-size:12px;line-height:1.7}.af-diagnostics{padding-left:18px;font-size:12px;line-height:1.7}.af-diagnostics small{display:block;color:var(--el-text-color-secondary)}
.af-node-execution{display:flex;flex-direction:column;min-height:0;flex:1;height:100%;overflow:hidden}.af-node-execution>:deep(.task-session-result){flex:1;min-height:0;height:auto}.af-node-preparing{padding:12px}.af-parallel-switch{display:flex;gap:5px;overflow-x:auto;flex:none;padding:0 0 9px;margin-bottom:4px;scrollbar-width:thin}.af-parallel-switch button{display:flex;gap:6px;font-size:10px;padding:5px 7px}.af-parallel-switch button[aria-pressed="true"]{color:var(--el-color-primary);border-color:var(--el-color-primary-light-5);background:var(--el-color-primary-light-9)}.af-parallel-switch span{color:var(--el-text-color-secondary);font-size:9px}.af-activity-link{font-size:10px;color:var(--el-color-success)}.af-activity-link.stale{color:var(--el-color-warning)}
.af-process-drawer>header{display:flex;align-items:center;gap:8px;min-width:0}.af-process-drawer>header>strong{flex:1;font-size:12px}
.af-run-status{position:absolute;z-index:18;left:76px;display:flex;align-items:center;gap:7px;flex-wrap:wrap;max-width:calc(100% - 360px);max-height:none;overflow:visible;box-sizing:border-box;padding:8px 10px;border:1px solid var(--el-border-color-light);border-radius:10px;background:var(--el-bg-color);box-shadow:0 3px 15px rgb(0 0 0 / 9%);font-size:11px}.af-run-status .af-waiting{color:var(--el-color-warning)}.af-pending-panel{position:absolute;left:0;bottom:calc(100% + 8px);display:flex;flex-direction:column;gap:5px;min-width:180px;max-height:240px;overflow:auto;padding:8px;border:1px solid var(--el-border-color);border-radius:10px;background:var(--el-bg-color);box-shadow:0 5px 22px #0003}.af-pending-panel button{text-align:left}.af-confirm-link{border-color:var(--el-color-warning-light-5)!important;color:var(--el-color-warning)!important}
.af-process-drawer{position:absolute;z-index:22;bottom:16px;left:16px;right:16px;height:var(--af-process-height);max-height:55%;display:flex;flex-direction:column;overflow:hidden;min-height:0;border:1px solid var(--el-border-color-light);border-radius:12px;background:var(--el-bg-color);box-shadow:0 8px 26px rgb(0 0 0 / 15%)}.af-process-drawer>header{padding:8px 12px;flex:none;border-bottom:1px solid var(--el-border-color-lighter)}.af-process-drawer>header>small{font-size:10px;color:var(--el-text-color-secondary)}.af-node-process{flex:none;overflow:auto;max-height:66px;padding:0 10px}.af-node-process .af-toolbar{margin:6px 0;gap:5px}.af-node-process button{font-size:10px;padding:4px 6px}.af-process-drawer>:deep(.task-session-result){flex:1;min-height:0;height:auto;max-height:none;overflow:auto;padding:0 10px;margin:0;border:0;border-radius:0}.af-process-drawer>.af-note{padding:12px}
.af-run-fields{max-height:min(60vh,580px);overflow:auto;overscroll-behavior:contain;padding:2px 4px 2px 1px;scrollbar-width:thin}
.af-confirm{margin:14px 0 6px;padding:12px;border:1px solid var(--el-color-primary-light-5);background:var(--el-color-primary-light-9);border-radius:9px}.af-confirm h3{font-size:13px;margin:0 0 10px}.af-confirm label{display:block;font-size:12px;margin:8px 0}.af-confirm textarea,.af-form input,.af-form textarea{box-sizing:border-box;width:100%;border:1px solid var(--el-border-color);border-radius:6px;background:var(--el-bg-color);color:var(--el-text-color-primary);padding:8px;font:inherit;resize:vertical}.af-form{display:flex;flex-direction:column;gap:15px}.af-form label{display:flex;flex-direction:column;gap:7px}.af-form small{font-size:11px;color:var(--el-text-color-secondary);line-height:1.7}.af-form button{align-self:flex-end}
@media(max-width:900px){.af-head-floating{top:10px;left:10px;right:10px;gap:5px}.af-head-floating .af-heading{min-width:100px}.af-save-state{font-size:10px}.af-run-status{left:16px;right:16px;max-width:none}.af-notices{left:76px;max-width:calc(100% - 92px)}}
@media(max-width:600px){.af-head-floating .af-heading{flex-basis:calc(100% - 70px);min-width:0}.af-head-floating .af-name{font-size:13px}.af-head-floating{padding:7px}.af-head-floating button{padding:5px 7px;font-size:11px}.af-notices{top:116px}.af-run-status{font-size:10px;gap:5px}.af-run-status button{font-size:10px}.af-process-drawer{left:8px;right:8px;bottom:8px}}
@container(max-width:640px){.af-head-floating{top:10px;left:10px;right:10px;gap:6px;padding:7px}.af-head-floating .af-heading{min-width:0;flex:1;flex-basis:0}.af-head-floating .af-name{font-size:13px}.af-header-actions{flex-basis:100%;order:10;gap:5px}.af-save-state{display:none}.af-head-floating button{padding:5px 7px;font-size:11px}.af-run-status{left:16px;right:16px;max-width:none;font-size:10px}.af-run-status button{font-size:10px}.af-notices{top:var(--af-top-inset)}.af-notices{left:76px;max-width:calc(100% - 92px)}}
@media(prefers-reduced-motion:reduce){.agent-workflow-sheet .af-flow{transition:none}}
</style>
