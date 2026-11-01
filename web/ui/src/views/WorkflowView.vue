<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import { ArrowLeft, CircleCheck, Clock, Histogram, Loading, VideoPlay, Warning } from "@element-plus/icons-vue";
import { Background } from "@vue-flow/background";
import { Controls } from "@vue-flow/controls";
import { MiniMap } from "@vue-flow/minimap";
import { MarkerType, VueFlow, useVueFlow } from "@vue-flow/core";
import WorkflowPalette, { AGENT_DRAG_MIME, HUMAN_DRAG_MIME } from "@/components/workflow/WorkflowPalette.vue";
import AgentFlowNode from "@/components/workflow/AgentFlowNode.vue";
import WorkflowNodeDrawer from "@/components/workflow/WorkflowNodeDrawer.vue";
import WorkflowHumanDrawer from "@/components/workflow/WorkflowHumanDrawer.vue";
import WorkflowSwimlane from "@/components/workflow/WorkflowSwimlane.vue";
import WorkflowParticipantsDrawer from "@/components/workflow/WorkflowParticipantsDrawer.vue";
import WorkflowAccessDrawer from "@/components/workflow/WorkflowAccessDrawer.vue";
import WorkflowPublicationDrawer from "@/components/workflow/WorkflowPublicationDrawer.vue";
import WorkflowTrialDialog from "@/components/workflow/WorkflowTrialDialog.vue";
import { workflowInputKeys } from "@/agent/workflowAuthoringReceipt";
import ConfirmDialog from "@/components/ConfirmDialog.vue";
import { navigateStudio, platformApi } from "@/api/platform";
import type { BuiltinToolInfo, PlatformAgent, PlatformModel } from "@/agent/platformTypes";
import type { DepWhen, ScheduleSummary, Workflow, WorkflowInput, WorkflowNode, WorkflowNodeState, WorkflowVersion } from "@/agent/workflowTypes";
import {
  addDependency,
  buildFlowNodes,
  candidateEdges,
  connectRefusal,
  layoutPositions,
  nextNodeId,
  normalizeAfter,
  removeDependency,
  removeNode,
  runLive,
  runDisplayStatus,
  toWorkflowInput,
  updateDependency,
  validateWorkflow,
  workflowDirty,
  workflowEdges,
  workflowRunCan,
  workflowRunNodes,
  workflowHistoryDefinition,
} from "@/agent/workflowPresentation";
import { useWorkflowRun } from "@/agent/useWorkflowRun";
import { prepareWorkflowRun, saveWorkflowDraft } from "@/agent/workflowEditorSave";
import { workflowHistoryLocation } from "@/agent/workflowHistoryLocation";
import { cloneWorkflowApprovalPolicy, newWorkflowHumanNode } from "@/agent/workflowApprovalPolicy";
import { browserTimeZone, localDateTimeInput, dateTimeInputToISO } from "@/agent/scheduleDateTime";
import { registerStudioNavigationGuard } from "@/agent/studioNavigation";
import { AVATAR_COLORS } from "@/components/AgentAvatar.vue";

/**
 * The workflow editor, full-screen: palette left, the Vue Flow canvas in the
 * middle, drawers right. The wire model is nodes + after[] deps with when
 * conditions; the canvas's nodes/edges are derived from it and every edit
 * routes back through the pure helpers, so what you see is exactly what the
 * 500ms-debounced PATCH saves (revision optimistic lock). Runs start from the
 * published version — the badge, the publish dialog and the versions drawer
 * own that lifecycle.
 */
const props = defineProps<{
  workflowId: string;
  agents: PlatformAgent[];
  models: PlatformModel[];
  canManage: boolean;
}>();

const { t, d } = useI18n();

/* ------------------------------ load & draft ------------------------------ */

const workflow = ref<Workflow | null>(null);
const canEditWorkflow = computed(() => workflow.value?.capabilities?.can_edit ?? props.canManage);
const canPublishWorkflow = computed(() => workflow.value?.capabilities?.can_publish ?? props.canManage);
const canInvokeWorkflow = computed(() => workflow.value?.capabilities?.can_invoke ?? props.canManage);
const canScheduleWorkflow = computed(() => workflow.value?.capabilities?.can_schedule ?? props.canManage);
const canManageAccess = computed(() => workflow.value?.capabilities?.can_manage_access ?? props.canManage);
const accessOpen = ref(false);
const publicationOpen = ref(false);
const trialOpen = ref(false);
const trialRevision = ref(0);
const trialInputKeys = ref<string[]>([]);
const loading = ref(true);
const loadError = ref("");
const notFound = ref(false);
const historyOnly = ref(false);

const draft = reactive<{ name: string; description: string; nodes: WorkflowNode[] }>({
  name: "",
  description: "",
  nodes: [],
});
const savedInput = ref<WorkflowInput | null>(null);
/** Draft vs last save (leave gate); the publish badge reads workflow.dirty. */
const unsaved = computed(() => savedInput.value !== null && workflowDirty(savedInput.value, toWorkflowInput(draft)));

function hydrate(wf: Workflow) {
  historyOnly.value = false;
  workflow.value = wf;
  draft.name = wf.name;
  draft.description = wf.description;
  draft.nodes = wf.nodes.map((n) => ({
    ...n,
    // The wire's dual dep form becomes the editor's uniform object form here;
    // toWorkflowInput serializes it back (success deps as plain strings).
    after: normalizeAfter(n.after as unknown as (string | { node: string; when?: DepWhen })[] | undefined),
    tools: n.tools ? [...n.tools] : undefined,
    approval_policy: n.approval_policy ? cloneWorkflowApprovalPolicy(n.approval_policy) : undefined,
    expect: n.expect ? { ...n.expect } : undefined,
    fields: n.fields ? n.fields.map((f) => ({ ...f })) : undefined,
    actions: n.actions ? [...n.actions] : undefined,
    orchestrate: n.orchestrate ? { ...n.orchestrate, candidates: n.orchestrate.candidates.map((c) => ({ ...c })) } : undefined,
  }));
  savedInput.value = toWorkflowInput(draft);
  positionOverrides.value = {};
}

async function load(silent = false) {
  if (!silent) loading.value = true;
  loadError.value = "";
  notFound.value = false;
  try {
    const { workflows } = await platformApi.workflows.list();
    const wf = workflows.find((w) => w.id === props.workflowId && !w.deleted);
    if (!wf) {
      notFound.value = true;
      return;
    }
    hydrate(wf);
    if (canEditWorkflow.value) void loadCapabilities();
    await nextTick();
    syncFlow();
  } catch (e) {
    loadError.value = (e as Error).message || t("workflow.editor.loadFailed");
  } finally {
    loading.value = false;
  }
}

async function refreshPermissions() {
  try {
    const { workflows } = await platformApi.workflows.list();
    const current = workflows.find(item => item.id === props.workflowId);
    if (workflow.value) workflow.value = { ...workflow.value, capabilities: current?.capabilities ?? { can_edit: false, can_publish: false, can_invoke: false } };
  } catch (error) {
    ElMessage.error((error as Error).message);
  }
}

/* ------------------------------ save channel ------------------------------ */

type SaveState = "saved" | "dirty" | "saving" | "error" | "conflict";
const saveState = ref<SaveState>("saved");
const saveError = ref("");
let saveTimer: number | undefined;
let saveInFlight = false;
let saveAgain = false;

function scheduleSave() {
  if (!canEditWorkflow.value || !unsaved.value) return;
  if (saveState.value !== "saving") saveState.value = "dirty";
  window.clearTimeout(saveTimer);
  saveTimer = window.setTimeout(() => void saveNow(), 500);
}

async function saveNow(): Promise<boolean> {
  const current = workflow.value;
  if (!current || !canEditWorkflow.value) return false;
  if (saveInFlight) {
    saveAgain = true;
    return false;
  }
  if (!unsaved.value && saveState.value === "saved") return true;
  saveInFlight = true;
  saveState.value = "saving";
  let saved = false;
  try {
    const result = await saveWorkflowDraft(current, draft, platformApi.workflows.update);
    workflow.value = result.workflow;
    savedInput.value = result.savedInput;
    saveState.value = unsaved.value ? "dirty" : "saved";
    saveError.value = "";
    saved = true;
    return true;
  } catch (e) {
    saveState.value = (e as { status?: number }).status === 409 ? "conflict" : "error";
    saveError.value = (e as Error).message;
    return false;
  } finally {
    saveInFlight = false;
    if (saveAgain || (saved && unsaved.value)) {
      saveAgain = false;
      scheduleSave();
    }
  }
}

const confirmBox = ref<InstanceType<typeof ConfirmDialog> | null>(null);

async function refreshAfterConflict() {
  const ok = await confirmBox.value?.ask({
    title: t("workflow.editor.save.conflictTitle"),
    body: t("workflow.editor.save.conflictHint"),
    confirmText: t("workflow.editor.save.refresh"),
    cancelText: t("workflow.list.cancel"),
  });
  if (!ok) return;
  saveState.value = "saved";
  void load();
}

watch(draft, () => scheduleSave(), { deep: true });

/* ------------------------------ validation ------------------------------ */

const validation = computed(() => validateWorkflow(draft, props.agents));
const formIssueText = computed(() => validation.value.form.map((code) => t(`workflow.errors.${code}`)).join("；"));

/* ------------------------------ canvas ------------------------------ */

const { onConnect, onNodesChange, onEdgesChange, onNodeClick, onEdgeClick, onPaneClick, onNodeDragStop, onNodesInitialized, screenToFlowCoordinate, fitView } = useVueFlow();

// VueFlow's component/prop generics are deep enough to blow vue-tsc's
// instantiation budget in a file this size; keeping the two v-model arrays
// structurally untyped short-circuits that check. The real contract is
// AgentFlowNodeData, enforced where the nodes are built (buildFlowNodes).
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const flowNodes = ref<any[]>([]);
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const flowEdges = ref<any[]>([]);
/** Session-only drag/drop positions; anything without an override falls back
    to the layered layout. */
const positionOverrides = ref<Record<string, { x: number; y: number }>>({});

const WHEN_COLOR: Record<DepWhen, string> = {
  success: "var(--pg-wf-edge-success)",
  failed: "var(--pg-wf-edge-failed)",
  rejected: "var(--pg-wf-edge-rejected)",
  always: "var(--pg-wf-edge-always)",
};

const { run, history: runHistory, historyLoading, historyError, starting, stopping, retrying, error: runError, accessError: runAccessError, start, stop, retry, loadHistory, loadLatest, loadRun, refresh } = useWorkflowRun(computed(() => props.workflowId));
const live = computed(() => runLive(run.value));
const viewMode = ref<"draft" | "run">("draft");
const canEditCanvas = computed(() => canEditWorkflow.value && viewMode.value === "draft");
const displayedRun = computed(() => viewMode.value === "run" ? run.value : null);
const displayedNodes = computed(() => displayedRun.value ? workflowRunNodes(displayedRun.value) : draft.nodes);
const displayedName = computed({
  get: () => displayedRun.value?.definition.name ?? draft.name,
  set: (name: string) => { if (canEditCanvas.value) draft.name = name; },
});
const participantsOpen = ref(false);

function selectRunView(id: string | null) {
  viewMode.value = id ? "run" : "draft";
  window.history.replaceState(null, "", workflowHistoryLocation(location.href, id));
}

async function selectHistory(id: string) {
  drawerOpen.value = false;
  approvalOpen.value = false;
  if (!id) {
    selectRunView(null);
    return;
  }
  if (!(await loadRun(id))) {
    ElMessage.error(runError.value || t("workflow.editor.loadFailed"));
    return;
  }
  selectRunView(id);
}

async function retryRun() {
  if (await retry()) {
    if (run.value) selectRunView(run.value.id);
  } else if (runError.value) ElMessage.error(runError.value);
}

const nodeStates = computed<Record<string, WorkflowNodeState | "">>(() => {
  const r = displayedRun.value;
  const out: Record<string, WorkflowNodeState | ""> = {};
  for (const n of displayedNodes.value) out[n.id] = r?.nodes[n.id]?.status ?? "";
  return out;
});

/** Canvas selection is owned here (not by the store): the ring and the
    drawer follow the same id, so a rebuild never loses the highlight. */
const selectedId = ref("");
const drawerOpen = ref(false);
const selectedNode = computed(() => displayedNodes.value.find((n) => n.id === selectedId.value) ?? null);

function syncFlow() {
  const layout = layoutPositions(displayedNodes.value);
  const positions: Record<string, { x: number; y: number }> = { ...layout };
  for (const fn of flowNodes.value) positions[fn.id] = fn.position;
  if (!displayedRun.value) Object.assign(positions, positionOverrides.value);
  flowNodes.value = buildFlowNodes({
    nodes: displayedNodes.value,
    agentOf: (id) => {
      const agent = props.agents.find((a) => a.id === id);
      return agent ? { name: agent.name, color: agent.color } : undefined;
    },
    states: nodeStates.value,
    readOnly: !canEditCanvas.value,
    issues: displayedRun.value ? {} : validation.value.nodes,
    positions,
    activeId: drawerOpen.value || approvalOpen.value ? selectedId.value : "",
    fallbackColor: AVATAR_COLORS[0],
    run: displayedRun.value,
  });
  flowEdges.value = [
    ...workflowEdges(displayedNodes.value).map((e) => ({
      ...e,
      class: `wf-edge-${e.when}`,
      label: e.when === "success" ? "" : t(`workflow.edge.when.${e.when}`),
      markerEnd: { type: MarkerType.ArrowClosed, color: WHEN_COLOR[e.when] },
    })),
    ...candidateEdges(displayedNodes.value).map((e) => ({
      ...e,
      class: "wf-edge-candidate",
      selectable: false,
      deletable: false,
      focusable: false,
    })),
  ];
}

watch([displayedNodes, nodeStates, validation, () => props.agents, selectedId, drawerOpen, displayedRun, canEditCanvas], () => syncFlow(), { deep: true });
watch([viewMode, () => displayedRun.value?.id], () => {
  flowNodes.value = [];
  edgeMenu.value = null;
  syncFlow();
  void nextTick(() => fitView({ padding: 0.2, maxZoom: 1 }));
});
watch([notFound, run], () => {
  if (!notFound.value || !run.value || run.value.workflow_id !== props.workflowId) return;
  hydrate(workflowHistoryDefinition(run.value));
  historyOnly.value = true;
  viewMode.value = "run";
  notFound.value = false;
});

// The workflow arrives async, long after the canvas mounts: the first fit has
// to wait until the fetched nodes are actually measured, or it frames nothing.
let fittedOnce = false;
onNodesInitialized(() => {
  if (fittedOnce) return;
  fittedOnce = true;
  void nextTick(() => fitView({ padding: 0.2, maxZoom: 1 }));
});

onConnect((connection) => {
  if (!canEditCanvas.value || !connection.source || !connection.target) return;
  const refusal = connectRefusal(draft.nodes, connection.source, connection.target);
  if (refusal) {
    ElMessage.warning(t(`workflow.refuse.${refusal}`));
    return;
  }
  const next = addDependency(draft.nodes, connection.source, connection.target);
  if (next) draft.nodes = next;
});

onNodesChange((changes) => {
  if (!canEditCanvas.value) return;
  for (const change of changes) {
    if (change.type !== "remove") continue;
    draft.nodes = removeNode(draft.nodes, change.id);
    if (selectedId.value === change.id) drawerOpen.value = false;
  }
});

onEdgesChange((changes) => {
  if (!canEditCanvas.value) return;
  for (const change of changes) {
    if (change.type !== "remove" || change.id.startsWith("cand:")) continue;
    const [source, target] = change.id.split("->");
    if (source && target) draft.nodes = removeDependency(draft.nodes, source, target);
  }
});

/* ------------------------------ edge menu ------------------------------ */

const edgeMenu = ref<{ source: string; target: string; when: DepWhen; x: number; y: number } | null>(null);

onEdgeClick(({ event, edge }) => {
  if (!canEditCanvas.value) return;
  if (edge.id.startsWith("cand:")) return;
  const [source, target] = edge.id.split("->");
  const dep = draft.nodes.find((n) => n.id === target)?.after?.find((x) => x.node === source);
  if (!source || !target || !dep) return;
  edgeMenu.value = {
    source,
    target,
    when: dep.when,
    x: Math.min((event as MouseEvent).clientX ?? 0, window.innerWidth - 240),
    y: Math.min((event as MouseEvent).clientY ?? 0, window.innerHeight - 180),
  };
});

function setEdgeWhen(when: DepWhen) {
  if (!canEditCanvas.value) return;
  const menu = edgeMenu.value;
  if (!menu) return;
  draft.nodes = updateDependency(draft.nodes, menu.source, menu.target, when);
  menu.when = when;
}

function removeEdge() {
  if (!canEditCanvas.value) return;
  const menu = edgeMenu.value;
  if (!menu) return;
  draft.nodes = removeDependency(draft.nodes, menu.source, menu.target);
  edgeMenu.value = null;
}

/* ------------------------------ node & approval ------------------------------ */

const approvalOpen = ref(false);

function openNode(id: string) {
  selectedId.value = id;
  // A node parked on a person opens the approval drawer instead of the form.
  if (displayedRun.value?.nodes[id]?.status === "waiting_human" && displayedRun.value?.nodes[id]?.human) {
    drawerOpen.value = false;
    approvalOpen.value = true;
  } else {
    approvalOpen.value = false;
    drawerOpen.value = true;
  }
}

onNodeClick(({ node }) => openNode(node.id));
onPaneClick(() => {
  drawerOpen.value = false;
  edgeMenu.value = null;
});
onNodeDragStop(({ node }) => {
  positionOverrides.value = { ...positionOverrides.value, [node.id]: { ...node.position } };
});

function addNode(agent: PlatformAgent, position?: { x: number; y: number }) {
  if (!canEditCanvas.value || !workflow.value) return;
  const id = nextNodeId(draft.nodes);
  if (position) positionOverrides.value = { ...positionOverrides.value, [id]: position };
  draft.nodes = [...draft.nodes, { id, agent: agent.id, task: "" }];
  selectedId.value = id;
  drawerOpen.value = true;
}

function addHuman(position?: { x: number; y: number }) {
  if (!canEditCanvas.value || !workflow.value) return;
  const id = nextNodeId(draft.nodes);
  if (position) positionOverrides.value = { ...positionOverrides.value, [id]: position };
  draft.nodes = [...draft.nodes, newWorkflowHumanNode(id)];
  selectedId.value = id;
  drawerOpen.value = true;
}

function onDrop(event: DragEvent) {
  const agentId = event.dataTransfer?.getData(AGENT_DRAG_MIME);
  const isHuman = event.dataTransfer?.getData(HUMAN_DRAG_MIME) === "1";
  const agent = props.agents.find((a) => a.id === agentId);
  if (!agent && !isHuman) return;
  event.preventDefault();
  const point = screenToFlowCoordinate({ x: event.clientX, y: event.clientY });
  if (isHuman) addHuman(point);
  else addNode(agent!, point);
}

function patchNode(id: string, patch: Partial<WorkflowNode>) {
  if (!canEditCanvas.value) return;
  draft.nodes = draft.nodes.map((n) => (n.id === id ? { ...n, ...patch } : n));
}

function removeSelected(id: string) {
  if (!canEditCanvas.value) return;
  draft.nodes = removeNode(draft.nodes, id);
}

/* ------------------------------ tools catalog ------------------------------ */

const builtinTools = ref<BuiltinToolInfo[]>([]);

async function loadCapabilities() {
  if (!canEditWorkflow.value) return;
  try {
    const catalog = await platformApi.workflows.capabilities(props.workflowId);
    builtinTools.value = catalog.builtin_tools ?? [];
  } catch {
    builtinTools.value = [];
  }
}

/* ------------------------------ publish & versions ------------------------------ */

const publishBadge = computed<"unpublished" | "dirty" | "published">(() => {
  if (!workflow.value?.published) return "unpublished";
  return workflow.value.dirty ? "dirty" : "published";
});

const publishOpen = ref(false);
const publishName = ref("");
const publishDesc = ref("");
const publishBusy = ref(false);
const publishError = ref("");
let publishThenRun = false;

function openPublish(thenRun: boolean) {
  if (!canPublishWorkflow.value) return;
  publishThenRun = thenRun;
  publishName.value = "";
  publishDesc.value = "";
  publishError.value = "";
  publishOpen.value = true;
}

/** Publishing snapshots the server-side draft, so the save channel flushes
    first; the pointer + dirty refresh comes from a reload (lossless — the
    draft was just persisted). */
async function confirmPublish() {
  if (publishBusy.value) return;
  window.clearTimeout(saveTimer);
  publishBusy.value = true;
  publishError.value = "";
  try {
    if (!(await flushSave())) {
      publishError.value = saveError.value;
      return;
    }
    if (!workflow.value?.definition_hash) throw new Error(t("workflowAuthoring.hashMissing"));
    await platformApi.workflows.publish(props.workflowId, { name: publishName.value.trim(), desc: publishDesc.value.trim(), expected_revision: workflow.value.revision, expected_hash: workflow.value.definition_hash });
    publishOpen.value = false;
    await load(true);
    if (publishThenRun) await startRun();
  } catch (e) {
    publishError.value = (e as { status?: number }).status === 409
      ? t("workflowAuthoring.publishChanged")
      : t("workflow.publish.failed", { msg: (e as Error).message });
  } finally {
    publishBusy.value = false;
  }
}

async function openTrial() {
  if (!canEditWorkflow.value || !canInvokeWorkflow.value || live.value) return;
  if (!(await flushSave()) || !workflow.value) return;
  trialRevision.value = workflow.value.revision;
  trialInputKeys.value = workflowInputKeys(draft.nodes);
  trialOpen.value = true;
}

async function trialStarted(id: string) {
  trialOpen.value = false;
  await selectHistory(id);
}

const versionsOpen = ref(false);
const versions = ref<WorkflowVersion[]>([]);
const versionsLoading = ref(false);
const rollbackBusy = ref("");

async function openVersions() {
  versionsOpen.value = true;
  versionsLoading.value = true;
  try {
    versions.value = (await platformApi.workflows.versions(props.workflowId)).versions;
  } catch {
    versions.value = [];
  } finally {
    versionsLoading.value = false;
  }
}

async function rollbackTo(v: WorkflowVersion) {
  if (!canPublishWorkflow.value || rollbackBusy.value || v.current) return;
  const ok = await confirmBox.value?.ask({
    title: t("workflow.publish.rollbackTitle"),
    body: t("workflow.publish.rollbackConfirm", { id: v.version_id.slice(0, 8) }),
    confirmText: t("workflow.publish.rollbackDo"),
    cancelText: t("workflow.list.cancel"),
    danger: true,
  });
  if (!ok) return;
  rollbackBusy.value = v.version_id;
  try {
    await platformApi.workflows.rollback(props.workflowId, v.version_id);
    await load(true);
    await openVersions();
  } catch (e) {
    ElMessage.error(t("workflow.publish.rollbackFailed", { msg: (e as Error).message }));
  } finally {
    rollbackBusy.value = "";
  }
}

/* ------------------------------ schedule ------------------------------ */

const scheduleOpen = ref(false);
const scheduleBusy = ref(false);
const scheduleError = ref("");
const scheduleForm = reactive<{ kind: "cron" | "every" | "at"; cron: string; everySec: number; at: string; timezone: string; enabled: boolean }>({
  kind: "cron",
  cron: "",
  everySec: 3600,
  at: "",
  timezone: browserTimeZone(),
  enabled: true,
});

function openSchedule() {
  const s = workflow.value?.schedule;
  scheduleForm.kind = (s?.kind as "cron" | "every" | "at") || "cron";
  scheduleForm.cron = s?.cron ?? "";
  scheduleForm.everySec = s?.every_sec ?? 3600;
  scheduleForm.timezone = s ? s.timezone || "" : browserTimeZone();
  try { scheduleForm.at = s?.at ? localDateTimeInput(s.at, scheduleForm.timezone || s.effective_timezone || browserTimeZone()) : ""; }
  catch (error) {
    scheduleForm.at = "";
    scheduleError.value = error instanceof Error ? error.message : String(error);
    scheduleOpen.value = true;
    return;
  }
  scheduleForm.enabled = s?.enabled ?? true;
  scheduleError.value = "";
  scheduleOpen.value = true;
}

async function saveSchedule() {
  if (scheduleBusy.value) return;
  scheduleBusy.value = true;
  scheduleError.value = "";
  try {
    const previous = workflow.value?.schedule;
    const zone = scheduleForm.timezone || previous?.effective_timezone || browserTimeZone();
    const unchangedAt = previous?.at && scheduleForm.timezone === (previous.timezone || "") && scheduleForm.at === localDateTimeInput(previous.at, zone);
    const summary = await platformApi.workflows.schedule.put(props.workflowId, {
      kind: scheduleForm.kind,
      cron: scheduleForm.kind === "cron" ? scheduleForm.cron.trim() : undefined,
      every_sec: scheduleForm.kind === "every" ? scheduleForm.everySec : undefined,
      at: scheduleForm.kind === "at" && scheduleForm.at ? (unchangedAt ? previous!.at : dateTimeInputToISO(scheduleForm.at, zone)) : undefined,
      timezone: scheduleForm.timezone,
      enabled: scheduleForm.enabled,
    });
    if (workflow.value) workflow.value = { ...workflow.value, schedule: summary };
    scheduleOpen.value = false;
  } catch (e) {
    scheduleError.value = t("workflow.schedule.failed", { msg: (e as Error).message });
  } finally {
    scheduleBusy.value = false;
  }
}

async function removeSchedule() {
  if (scheduleBusy.value) return;
  scheduleBusy.value = true;
  try {
    await platformApi.workflows.schedule.remove(props.workflowId);
    if (workflow.value) workflow.value = { ...workflow.value, schedule: undefined };
    scheduleOpen.value = false;
  } catch (e) {
    scheduleError.value = t("workflow.schedule.failed", { msg: (e as Error).message });
  } finally {
    scheduleBusy.value = false;
  }
}

function scheduleText(s: ScheduleSummary): string {
  if (s.kind === "cron") return s.cron ?? "";
  if (s.kind === "every") return `${Math.round((s.every_sec ?? 0) / 60)}min`;
  return s.at ?? "";
}

/* ------------------------------ swimlane ------------------------------ */

const swimlaneOpen = ref(false);

watch(runAccessError, (failure) => {
  if (!failure) return;
  drawerOpen.value = false;
  approvalOpen.value = false;
  participantsOpen.value = false;
  swimlaneOpen.value = false;
  selectedId.value = "";
  viewMode.value = "draft";
  if (historyOnly.value) {
    workflow.value = null;
    draft.name = "";
    draft.description = "";
    draft.nodes = [];
    savedInput.value = null;
    historyOnly.value = false;
  }
});

/* ------------------------------ run ------------------------------ */

const needsPublish = computed(() => !workflow.value?.published || (canPublishWorkflow.value && (workflow.value.dirty || unsaved.value)));
const canRun = computed(() => canInvokeWorkflow.value && (needsPublish.value ? canPublishWorkflow.value && validation.value.ok : !!workflow.value?.published) && !starting.value && !live.value);

/** The run snapshots the server's published definition; between the button
    and the start the draft must be saved through (and published, when the
    pointer is missing or stale — the dialog handles that path). */
async function flushSave(): Promise<boolean> {
  if (!canEditWorkflow.value) return true;
  for (;;) {
    window.clearTimeout(saveTimer);
    while (saveInFlight) await new Promise((resolve) => window.setTimeout(resolve, 60));
    if (!unsaved.value) return saveState.value !== "error" && saveState.value !== "conflict";
    if (!(await saveNow())) return false;
  }
}

async function startRun() {
  const ok = await start();
  if (ok && run.value) selectRunView(run.value.id);
  if (!ok && runError.value) ElMessage.error(t("workflow.run.startFailed", { msg: runError.value }));
}

async function runWorkflow() {
  if (!canRun.value || run.value?.persistence_error) return;
  const next = await prepareWorkflowRun(flushSave, () => needsPublish.value);
  if (next === "blocked") return;
  if (next === "publish") {
    openPublish(true);
    return;
  }
  await startRun();
}

async function stopWorkflow() {
  await stop();
  if (runError.value) ElMessage.error(t("workflow.run.stopFailed", { msg: runError.value }));
}

/* ------------------------------ leave gate ------------------------------ */

// The native window.confirm cannot be styled and offers no "save first" path,
// so the guard parks the navigation on a dialog promise instead; both callers
// (navigateStudio and the popstate handler) await it.
const leaveOpen = ref(false);
const leaveBusy = ref(false);
const leaveError = ref("");
let leaveResolve: ((ok: boolean) => void) | null = null;
let leavePromise: Promise<boolean> | null = null;

function confirmLeave(): boolean | Promise<boolean> {
  if (!unsaved.value && !saveInFlight) return true;
  if (leavePromise) return leavePromise;
  leaveError.value = "";
  leaveOpen.value = true;
  leavePromise = new Promise<boolean>((resolve) => {
    leaveResolve = resolve;
  });
  return leavePromise;
}

function settleLeave(ok: boolean) {
  leaveOpen.value = false;
  leaveBusy.value = false;
  leavePromise = null;
  const resolve = leaveResolve;
  leaveResolve = null;
  resolve?.(ok);
}

async function saveAndLeave() {
  if (leaveBusy.value) return;
  leaveBusy.value = true;
  leaveError.value = "";
  window.clearTimeout(saveTimer);
  if (await flushSave()) {
    settleLeave(true);
    return;
  }
  leaveBusy.value = false;
  leaveError.value = t("workflow.editor.leave.failed", { msg: saveError.value });
}
const unregisterGuard = registerStudioNavigationGuard(confirmLeave);
function preventDiscard(event: BeforeUnloadEvent) {
  if (unsaved.value) {
    event.preventDefault();
    event.returnValue = "";
  }
}

onMounted(() => {
  window.addEventListener("beforeunload", preventDiscard);
  void load();
  const selectedRun = new URLSearchParams(location.search).get("run");
  if (selectedRun) {
    void loadRun(selectedRun).then(ok => {
      if (ok) { viewMode.value = "run"; swimlaneOpen.value = true; }
      else ElMessage.error(runError.value || t("workflow.editor.loadFailed"));
    });
  } else void loadLatest();
  void loadCapabilities();
});
onBeforeUnmount(() => {
  unregisterGuard();
  // Unmount means the navigation went through; never leave the promise parked.
  settleLeave(true);
  window.clearTimeout(saveTimer);
  window.removeEventListener("beforeunload", preventDiscard);
});
</script>

<template>
  <div class="wf-editor">
    <div v-if="loading" class="wf-status">
      <el-icon class="is-loading" aria-hidden="true"><Loading /></el-icon>
    </div>
    <div v-else-if="notFound" class="wf-status">
      <p>{{ t("workflow.editor.notFound") }}</p>
      <button type="button" class="wf-btn ghost" @click="navigateStudio('/manage/workflows')">
        {{ t("workflow.list.backToList") }}
      </button>
    </div>
    <div v-else-if="loadError" class="wf-status">
      <p>{{ loadError }}</p>
      <button type="button" class="wf-btn ghost" @click="load()">{{ t("workflow.editor.save.refresh") }}</button>
    </div>

    <template v-else>
      <header class="wf-head">
        <button
          type="button"
          class="wf-iconbtn tip tip-down"
          :data-tip="t('workflow.editor.back')"
          :aria-label="t('workflow.editor.back')"
          @click="navigateStudio('/manage/workflows')"
        >
          <el-icon aria-hidden="true"><ArrowLeft /></el-icon>
        </button>

        <input
          v-model="displayedName"
          class="wf-name"
          :placeholder="t('workflow.editor.namePlaceholder')"
          :maxlength="60"
          :readonly="!canEditCanvas"
          :aria-label="t('workflow.editor.namePlaceholder')"
        />

        <span v-if="canEditWorkflow" class="wf-save" :class="saveState" :title="saveError">
          <el-icon v-if="saveState === 'saving'" class="is-loading" aria-hidden="true"><Loading /></el-icon>
          <el-icon v-else-if="saveState === 'saved'" aria-hidden="true"><CircleCheck /></el-icon>
          <el-icon v-else aria-hidden="true"><Warning /></el-icon>
          {{ t(`workflow.editor.save.${saveState === "conflict" ? "conflict" : saveState}`) }}
        </span>
        <button v-if="saveState === 'conflict'" type="button" class="wf-btn ghost sm" @click="refreshAfterConflict">
          {{ t("workflow.editor.save.refresh") }}
        </button>

        <button
          type="button"
          class="wf-publish-badge"
          :class="publishBadge"
          :disabled="!canPublishWorkflow"
          :title="workflow?.published ? `${workflow.published.version_id.slice(0, 12)} · ${t('workflow.publish.at', { time: d(new Date(workflow.published.published_at), 'short') })}` : undefined"
          @click="openVersions"
        >
          {{ publishBadge === "unpublished" ? t("workflow.publish.badgeUnpublished") : publishBadge === "dirty" ? t("workflow.publish.badgeDirty") : t("workflow.publish.badgePublished") }}
        </button>

        <span v-if="canEditCanvas && !validation.ok" class="wf-invalid" :title="formIssueText" role="status">
          <el-icon aria-hidden="true"><Warning /></el-icon>
          {{ formIssueText }}
        </span>

        <span v-if="run" class="wf-run-chip" :class="runDisplayStatus(run)" :title="t('workflow.run.startedAt', { time: d(new Date(run.created_at), 'short') })">
          {{ t(`workflow.run.${runDisplayStatus(run)}`) }}
        </span>

        <div class="wf-head-actions">
          <button
            type="button"
            class="wf-iconbtn tip tip-down"
            :data-tip="t('workflow.editor.swimlane')"
            :aria-label="t('workflow.editor.swimlane')"
            :disabled="!run"
            @click="swimlaneOpen = true"
          >
            <el-icon aria-hidden="true"><Histogram /></el-icon>
          </button>
          <button
            v-if="canScheduleWorkflow"
            type="button"
            class="wf-iconbtn tip tip-down"
            :class="{ on: !!workflow?.schedule?.enabled }"
            :data-tip="workflow?.schedule ? `${scheduleText(workflow.schedule)}${workflow.schedule.next_run ? ' · ' + t('workflow.schedule.nextRun', { time: d(new Date(workflow.schedule.next_run), 'short') }) : ''}` : t('workflow.schedule.title')"
            :aria-label="t('workflow.schedule.title')"
            @click="openSchedule"
          >
            <el-icon aria-hidden="true"><Clock /></el-icon>
          </button>
          <button v-if="canEditWorkflow && canInvokeWorkflow && !live" type="button" class="wf-btn ghost" @click="openTrial">{{ t('workflowAuthoring.trial') }}</button>
          <button
            v-if="!live"
            type="button"
            class="wf-btn solid"
            :disabled="!canRun || !!run?.persistence_error"
            :title="!validation.ok ? t('workflow.editor.runNeedsSave') : undefined"
            @click="runWorkflow"
          >
            <el-icon v-if="starting" class="is-loading" aria-hidden="true"><Loading /></el-icon>
            <el-icon v-else aria-hidden="true"><VideoPlay /></el-icon>
            {{ starting ? t("workflow.editor.starting") : needsPublish ? t("workflow.editor.publishRun") : t("workflow.editor.run") }}
          </button>
          <button v-else-if="workflowRunCan(run, 'can_cancel')" type="button" class="wf-btn danger" :disabled="stopping" @click="stopWorkflow">
            {{ t("workflow.editor.stop") }}
          </button>
        </div>
      </header>

      <div class="wf-run-toolbar">
        <label for="workflow-run-history">{{ t("workflow.history.title") }}</label>
        <el-select
          id="workflow-run-history"
          :model-value="viewMode === 'run' ? run?.id ?? '' : ''"
          class="wf-history-select"
          :loading="historyLoading"
          :placeholder="canEditWorkflow ? t('workflow.history.draft') : t('workflow.history.definition')"
          @visible-change="(open: boolean) => { if (open) void loadHistory(); }"
          @update:model-value="selectHistory(String($event))"
        >
          <el-option v-if="!historyOnly" value="" :label="canEditWorkflow ? t('workflow.history.draft') : t('workflow.history.definition')" />
          <el-option v-for="item in runHistory" :key="item.id" :value="item.id" :label="`${item.test_run ? t('workflowAuthoring.testLabel') + ' · ' : ''}${d(new Date(item.created_at), 'short')} · ${t(`workflow.run.${runDisplayStatus(item)}`)} · ${item.id.slice(-8)}`" />
        </el-select>
        <span v-if="viewMode === 'run'" class="wf-history-hint">{{ t("workflow.history.snapshot") }}</span>
        <span v-if="viewMode === 'run' && run?.test_run" class="wf-history-hint">{{ t('workflowAuthoring.testLabel') }} · {{ t('workflowAuthoring.revision', { n: run.draft_revision }) }} · {{ t('workflowAuthoring.existing') }}</span>
        <span v-if="historyOnly" class="wf-history-hint">{{ t("workflow.history.archived") }}</span>
        <button v-if="canManageAccess" type="button" class="wf-btn ghost sm" @click="accessOpen = true">{{ t("workflow.access.title") }}</button>
        <button v-if="workflow?.capabilities?.can_manage_publications" type="button" class="wf-btn ghost sm" @click="publicationOpen = true">{{ t("workflowEmbed.manageTitle") }}</button>
        <button v-if="run && viewMode === 'draft'" type="button" class="wf-btn ghost sm" @click="selectHistory(run.id)">{{ t("workflow.history.viewRun") }}</button>
        <button v-if="run && workflowRunCan(run, 'can_retry', false)" type="button" class="wf-btn ghost sm" :disabled="retrying" @click="retryRun">{{ retrying ? t("workflow.history.retrying") : t("workflow.history.retry") }}</button>
        <button v-if="run && workflowRunCan(run, 'can_manage_participants', false)" type="button" class="wf-btn ghost sm" @click="participantsOpen = true">{{ t("workflow.participants.title") }}</button>
        <span v-if="historyError" class="wf-history-error" role="alert">{{ historyError }}</span>
      </div>

      <p v-if="viewMode === 'run' && run?.origin?.publication_id" class="wf-history-origin">
        {{ t("workflowEmbed.publicationSource", { id: run.origin.publication_id }) }}
        <template v-if="run.origin.business_id"> · {{ t("workflowEmbed.businessId", { id: run.origin.business_id }) }}</template>
        <template v-if="run.origin.version_id"> · {{ t("workflowEmbed.version", { version: run.origin.version_id }) }}</template>
      </p>
      <p v-if="runAccessError" class="wf-persistence-error" role="alert">
        {{ t(runAccessError === 401 ? "workflow.history.loginRequired" : "workflow.history.accessUnavailable") }}
      </p>
      <p v-if="run?.persistence_error" class="wf-persistence-error" role="alert">
        {{ t("workflow.run.persistenceHint") }}
      </p>

      <div class="wf-body">
        <WorkflowPalette v-if="canEditCanvas" :agents="agents" @add="addNode" @add-human="addHuman()" />

        <div class="wf-canvas" @drop="onDrop" @dragover.prevent @dragenter.prevent>
          <VueFlow
            v-model:nodes="flowNodes"
            v-model:edges="flowEdges"
            :delete-key-code="canEditCanvas ? ['Backspace', 'Delete'] : null"
            :nodes-draggable="canEditCanvas"
            :nodes-connectable="canEditCanvas"
            :min-zoom="0.2"
            :max-zoom="1.6"
            class="wf-flow"
          >
            <Background variant="dots" :gap="20" :size="1.5" pattern-color="var(--pg-wf-dot)" />
            <Controls :show-interactive="false" />
            <MiniMap pannable zoomable node-color="var(--pg-wf-dot)" node-stroke-color="var(--pg-wf-edge)" mask-color="var(--pg-wf-minimap-mask)" />
            <template #node-agent="nodeProps">
              <AgentFlowNode v-bind="nodeProps" />
            </template>
          </VueFlow>
          <p v-if="!displayedNodes.length" class="wf-canvas-hint">{{ t(canEditCanvas ? "workflow.editor.canvasHint" : "workflow.history.emptyGraph") }}</p>

          <div v-if="edgeMenu" class="wf-edge-backdrop" @click="edgeMenu = null" />
          <div
            v-if="edgeMenu"
            class="wf-edge-menu"
            :style="{ left: `${edgeMenu.x}px`, top: `${edgeMenu.y}px` }"
            role="dialog"
            :aria-label="t('workflow.edge.title')"
          >
            <strong>{{ t("workflow.edge.title") }}</strong>
            <p>{{ t("workflow.edge.hint") }}</p>
            <el-select :model-value="edgeMenu.when" size="small" :disabled="!canEditCanvas" @update:model-value="setEdgeWhen($event as DepWhen)">
              <el-option v-for="w in ['success', 'failed', 'rejected', 'always']" :key="w" :value="w" :label="t(`workflow.edge.when.${w}`)" />
            </el-select>
            <button v-if="canEditCanvas" type="button" class="wf-edge-remove" @click="removeEdge">{{ t("workflow.edge.remove") }}</button>
          </div>
        </div>
      </div>

      <WorkflowTrialDialog v-if="trialOpen" :workflow-id="workflowId" :revision="trialRevision" :input-keys="trialInputKeys" @close="trialOpen = false" @started="trialStarted" />
      <WorkflowNodeDrawer
        :open="drawerOpen && !!selectedNode"
        :node="selectedNode"
        :nodes="displayedNodes"
        :agents="agents"
        :models="models"
        :tools-catalog="builtinTools"
        :run="displayedRun"
        :workflow-id="workflowId"
        :can-manage="canEditCanvas"
        @update:open="drawerOpen = $event"
        @patch="patchNode"
        @remove="removeSelected"
      />

      <WorkflowHumanDrawer
        :open="approvalOpen"
        :node-id="selectedId"
        :run="run"
        :workflow-id="workflowId"
        @update:open="approvalOpen = $event"
        @resumed="approvalOpen = false; void refresh()"
      />

      <el-drawer :model-value="swimlaneOpen" :title="t('workflow.swimlane.title')" direction="btt" size="46%" @update:model-value="swimlaneOpen = $event">
        <WorkflowSwimlane :run="run" @select="(id) => { viewMode = 'run'; swimlaneOpen = false; openNode(id); }" />
      </el-drawer>

      <WorkflowParticipantsDrawer :open="participantsOpen" :workflow-id="workflowId" :run="run" @update:open="participantsOpen = $event" @changed="void refresh()" />
      <WorkflowAccessDrawer :open="accessOpen" :workflow-id="workflowId" @update:open="accessOpen = $event" @changed="void refreshPermissions()" />
      <WorkflowPublicationDrawer :open="publicationOpen" :workflow-id="workflowId" :name="workflow?.name || ''" :published-version="workflow?.published?.version_id" @update:open="publicationOpen = $event" />

      <el-drawer :model-value="versionsOpen" :title="t('workflow.publish.versionsTitle')" direction="rtl" size="380px" @update:model-value="versionsOpen = $event">
        <div class="wf-versions">
          <p v-if="versionsLoading" class="wf-versions-empty">{{ t("workflow.drawer.result.artifactLoading") }}</p>
          <p v-else-if="!versions.length" class="wf-versions-empty">{{ t("workflow.publish.versionsEmpty") }}</p>
          <div v-for="v in versions" :key="v.version_id" class="wf-version" :class="{ current: v.current }">
            <div class="wf-version-head">
              <code>{{ v.version_id.slice(0, 8) }}</code>
              <span v-if="v.current" class="wf-version-current">{{ t("workflow.publish.current") }}</span>
              <button
                v-else-if="canPublishWorkflow"
                type="button"
                class="wf-version-rollback"
                :disabled="rollbackBusy === v.version_id"
                @click="rollbackTo(v)"
              >
                {{ t("workflow.publish.rollback") }}
              </button>
            </div>
            <strong v-if="v.name">{{ v.name }}</strong>
            <p v-if="v.desc" class="wf-version-desc">{{ v.desc }}</p>
            <span class="wf-version-at">{{ t("workflow.publish.at", { time: d(new Date(v.published_at), "short") }) }}</span>
          </div>
        </div>
      </el-drawer>

      <el-dialog
        :model-value="publishOpen"
        :title="t('workflow.publish.dialogTitle')"
        width="min(440px, 94vw)"
        :close-on-click-modal="false"
        @update:model-value="publishOpen = $event"
      >
        <div class="wf-dialog-form">
          <p class="wf-dialog-hint">{{ t("workflow.publish.dialogHint") }}</p>
          <el-input v-model="publishName" :placeholder="t('workflow.publish.name')" :maxlength="60" />
          <el-input v-model="publishDesc" type="textarea" :rows="3" :maxlength="240" :placeholder="t('workflow.publish.desc')" />
          <p v-if="publishError" class="wf-dialog-error" role="alert">{{ publishError }}</p>
        </div>
        <template #footer>
          <button type="button" class="wf-btn ghost" :disabled="publishBusy" @click="publishOpen = false">{{ t("workflow.list.cancel") }}</button>
          <button type="button" class="wf-btn solid" :disabled="publishBusy" @click="confirmPublish">
            {{ publishBusy ? t("workflow.publish.busy") : publishThenRun ? t("workflow.publish.confirmRun") : t("workflow.publish.confirm") }}
          </button>
        </template>
      </el-dialog>

      <el-dialog
        :model-value="scheduleOpen"
        :title="t('workflow.schedule.title')"
        width="min(460px, 94vw)"
        :close-on-click-modal="false"
        @update:model-value="scheduleOpen = $event"
      >
        <div class="wf-dialog-form">
          <p class="wf-dialog-hint">{{ t("workflow.schedule.hint") }}</p>
          <template v-if="workflow?.schedule">
            <p v-if="workflow.schedule.next_run" class="wf-dialog-meta">{{ t("workflow.schedule.nextRun", { time: d(new Date(workflow.schedule.next_run), "short") }) }}</p>
            <p v-if="workflow.schedule.last_run_status" class="wf-dialog-meta">{{ t("workflow.schedule.lastStatus", { status: workflow.schedule.last_run_status }) }}</p>
          </template>
          <p v-else class="wf-dialog-meta">{{ t("workflow.schedule.none") }}</p>
          <div class="wf-field">
            <label>{{ t("workflow.schedule.timezone") }}</label>
            <el-input v-model="scheduleForm.timezone" :placeholder="workflow?.schedule?.effective_timezone_label || 'Asia/Shanghai'" />
            <p class="wf-dialog-meta">{{ t("workflow.schedule.timezoneHint") }}</p>
          </div>
          <div class="wf-field">
            <label>{{ t("workflow.schedule.kind") }}</label>
            <el-radio-group v-model="scheduleForm.kind">
              <el-radio-button value="cron">{{ t("workflow.schedule.kindCron") }}</el-radio-button>
              <el-radio-button value="every">{{ t("workflow.schedule.kindEvery") }}</el-radio-button>
              <el-radio-button value="at">{{ t("workflow.schedule.kindAt") }}</el-radio-button>
            </el-radio-group>
          </div>
          <el-input v-if="scheduleForm.kind === 'cron'" v-model="scheduleForm.cron" :placeholder="t('workflow.schedule.cronPlaceholder')" />
          <el-input-number v-else-if="scheduleForm.kind === 'every'" v-model="scheduleForm.everySec" :min="60" :placeholder="t('workflow.schedule.everyPlaceholder')" style="width: 100%" />
          <el-date-picker
            v-else
            v-model="scheduleForm.at"
            type="datetime"
            :placeholder="t('workflow.schedule.atPlaceholder')"
            value-format="YYYY-MM-DDTHH:mm"
            style="width: 100%"
          />
          <el-checkbox v-model="scheduleForm.enabled">{{ t("workflow.schedule.enabled") }}</el-checkbox>
          <p v-if="scheduleError" class="wf-dialog-error" role="alert">{{ scheduleError }}</p>
        </div>
        <template #footer>
          <button v-if="workflow?.schedule" type="button" class="wf-btn ghost" :disabled="scheduleBusy" @click="removeSchedule">{{ t("workflow.schedule.remove") }}</button>
          <button type="button" class="wf-btn ghost" :disabled="scheduleBusy" @click="scheduleOpen = false">{{ t("workflow.list.cancel") }}</button>
          <button type="button" class="wf-btn solid" :disabled="scheduleBusy" @click="saveSchedule">{{ t("workflow.schedule.save") }}</button>
        </template>
      </el-dialog>

      <ConfirmDialog ref="confirmBox" />

      <el-dialog
        :model-value="leaveOpen"
        :title="t('workflow.editor.leave.title')"
        width="min(420px, 94vw)"
        :close-on-click-modal="false"
        @update:model-value="(v: boolean) => { if (!v) settleLeave(false); }"
      >
        <p class="wf-dialog-hint">{{ t("workflow.editor.leave.body") }}</p>
        <p v-if="leaveError" class="wf-dialog-error" role="alert">{{ leaveError }}</p>
        <template #footer>
          <button type="button" class="wf-btn ghost" :disabled="leaveBusy" @click="settleLeave(false)">
            {{ t("workflow.editor.leave.stay") }}
          </button>
          <button type="button" class="wf-btn ghost discard" :disabled="leaveBusy" @click="settleLeave(true)">
            {{ t("workflow.editor.leave.discard") }}
          </button>
          <button type="button" class="wf-btn solid" :disabled="leaveBusy" @click="saveAndLeave">
            {{ leaveBusy ? t("workflow.editor.save.saving") : t("workflow.editor.leave.saveLeave") }}
          </button>
        </template>
      </el-dialog>
    </template>
  </div>
</template>

<style scoped>
.wf-history-origin { margin: 0; padding: 8px 18px; font-size: 12px; color: var(--el-text-color-secondary); overflow-wrap: anywhere; border-bottom: 1px solid var(--el-border-color-lighter); }
.wf-editor {
  height: 100%;
  display: flex;
  flex-direction: column;
  background: var(--el-bg-color-page);
  color: var(--el-text-color-primary);
}
.wf-status {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 14px;
  color: var(--el-text-color-secondary);
  font-size: 14px;
}

/* top bar */
.wf-head {
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 16px;
  background: var(--el-bg-color);
  border-bottom: 1px solid var(--el-border-color-lighter);
  /* Stack above the Vue Flow canvas (z-index 0): a disabled icon button has
     opacity < 1, which traps its tooltip pseudo-element inside the button's
     own stacking context — without this the canvas paints over the tooltip. */
  position: relative;
  z-index: 10;
}
.wf-name {
  flex: 0 1 320px;
  min-width: 0;
  border: 1px solid transparent;
  border-radius: 8px;
  background: transparent;
  font: inherit;
  font-size: 15px;
  font-weight: 650;
  color: var(--el-text-color-primary);
  padding: 6px 10px;
  transition: border-color var(--pg-transition), background var(--pg-transition);
}
.wf-name:hover:not(:read-only) {
  border-color: var(--el-border-color-light);
}
.wf-name:focus {
  outline: none;
  border-color: var(--el-color-primary);
  background: var(--el-bg-color);
  box-shadow: var(--pg-ring);
}
.wf-name::placeholder {
  color: var(--el-text-color-placeholder);
  font-weight: 500;
}

.wf-save {
  flex: none;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.wf-save.saved {
  color: var(--el-color-success);
}
.wf-save.error {
  color: var(--el-color-danger);
}
.wf-save.conflict {
  color: var(--el-color-warning);
}

.wf-publish-badge {
  flex: none;
  border: 0;
  font: inherit;
  font-size: 11.5px;
  font-weight: 600;
  border-radius: 999px;
  padding: 3px 11px;
  cursor: pointer;
  background: var(--el-fill-color-light);
  color: var(--el-text-color-secondary);
  transition: background var(--pg-transition);
}
.wf-publish-badge.dirty {
  background: var(--el-color-warning-light-9);
  color: var(--el-color-warning);
}
.wf-publish-badge.published {
  background: var(--el-color-success-light-9);
  color: var(--el-color-success);
}

.wf-invalid {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
  font-size: 12px;
  color: var(--el-color-danger);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.wf-run-chip {
  flex: none;
  font-size: 11.5px;
  font-weight: 600;
  border-radius: 999px;
  padding: 3px 11px;
  background: var(--el-fill-color-light);
  color: var(--el-text-color-secondary);
}
.wf-run-chip.working, .wf-run-chip.running, .wf-run-chip.queued {
  background: color-mix(in srgb, var(--pg-wf-state-running) 14%, transparent);
  color: var(--pg-wf-state-running);
}
.wf-run-chip.completed {
  background: color-mix(in srgb, var(--pg-wf-state-success) 14%, transparent);
  color: var(--pg-wf-state-success);
}
.wf-run-chip.failed, .wf-run-chip.persistence_error {
  background: color-mix(in srgb, var(--pg-wf-state-failed) 12%, transparent);
  color: var(--pg-wf-state-failed);
}
.wf-run-chip.waiting_human { background: var(--el-color-warning-light-9); color: var(--el-color-warning-dark-2); }
.wf-run-chip.interrupted { background: var(--el-fill-color-light); color: var(--el-text-color-secondary); }
.wf-run-toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding: 8px 18px; border-bottom: 1px solid var(--el-border-color); font-size: 12px; }
.wf-history-select { width: 330px; max-width: 100%; }
.wf-history-hint { color: var(--el-text-color-secondary); }
.wf-history-error { color: var(--el-color-danger); overflow-wrap: anywhere; }

.wf-persistence-error {
  margin: 0;
  padding: 10px 18px;
  color: var(--el-color-danger);
  background: var(--el-color-danger-light-9);
  overflow-wrap: anywhere;
}

.wf-head-actions {
  margin-left: auto;
  flex: none;
  display: flex;
  align-items: center;
  gap: 8px;
}
.wf-iconbtn.on {
  color: var(--el-color-primary);
  background: var(--pg-accent-wash);
}

/* buttons (the studio's own, restated here so the full-screen page does not
   depend on ManageView's scoped styles). */
.wf-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border-radius: 8px;
  padding: 8px 15px;
  font: inherit;
  font-size: 13px;
  font-weight: 550;
  cursor: pointer;
  border: 1px solid transparent;
  transition: background var(--pg-transition), border-color var(--pg-transition), color var(--pg-transition);
}
.wf-btn.sm {
  padding: 5px 11px;
  font-size: 12px;
}
.wf-btn.solid {
  background: var(--pg-solid);
  border-color: var(--pg-solid);
  color: var(--pg-on-solid);
}
.wf-btn.solid:hover:not(:disabled) {
  background: var(--pg-solid-hover);
  border-color: var(--pg-solid-hover);
}
.wf-btn.ghost {
  background: transparent;
  border-color: var(--el-border-color);
  color: var(--el-text-color-regular);
}
.wf-btn.ghost:hover:not(:disabled) {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}
.wf-btn.danger {
  background: var(--el-color-danger);
  border-color: var(--el-color-danger);
  color: var(--el-color-white, #fff);
}
.wf-btn.danger:hover:not(:disabled) {
  background: var(--el-color-danger-dark-2);
  border-color: var(--el-color-danger-dark-2);
}
.wf-btn.discard:not(:disabled) {
  color: var(--el-color-danger);
  border-color: var(--el-color-danger-light-5);
}
.wf-btn.discard:hover:not(:disabled) {
  background: var(--el-color-danger-light-9);
  border-color: var(--el-color-danger-light-7);
  color: var(--el-color-danger);
}
.wf-btn:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.wf-iconbtn {
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 34px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  transition: background var(--pg-transition), color var(--pg-transition);
}
.wf-iconbtn:hover:not(:disabled) {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}
.wf-iconbtn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

/* body */
.wf-body {
  flex: 1;
  min-height: 0;
  display: flex;
}
.wf-canvas {
  position: relative;
  flex: 1;
  min-width: 0;
  background: var(--pg-wf-canvas-bg);
}
.wf-flow {
  height: 100%;
  width: 100%;
}
.wf-canvas-hint {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  margin: 0;
  padding: 0 40px;
  text-align: center;
  font-size: 13px;
  line-height: 1.8;
  color: var(--el-text-color-placeholder);
  pointer-events: none;
}

/* edge menu */
.wf-edge-backdrop {
  position: fixed;
  inset: 0;
  z-index: 80;
}
.wf-edge-menu {
  position: fixed;
  z-index: 81;
  width: 224px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  background: var(--el-bg-color-overlay);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  box-shadow: var(--el-box-shadow);
  padding: 12px;
}
.wf-edge-menu strong {
  font-size: 12.5px;
  color: var(--el-text-color-primary);
}
.wf-edge-menu p {
  margin: 0;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--el-text-color-placeholder);
}
.wf-edge-remove {
  align-self: flex-start;
  border: 1px solid var(--el-color-danger-light-5);
  background: transparent;
  color: var(--el-color-danger);
  border-radius: 7px;
  padding: 5px 11px;
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: background var(--pg-transition);
}
.wf-edge-remove:hover {
  background: var(--el-color-danger-light-9);
}

/* versions drawer */
.wf-versions {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 2px 2px 16px;
}
.wf-versions-empty {
  padding: 30px 6px;
  text-align: center;
  font-size: 12.5px;
  color: var(--el-text-color-secondary);
}
.wf-version {
  display: flex;
  flex-direction: column;
  gap: 4px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  padding: 11px 13px;
}
.wf-version.current {
  border-color: var(--pg-accent-line);
  background: var(--pg-accent-wash);
}
.wf-version-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.wf-version-head code {
  font-family: var(--pg-mono);
  font-size: 12px;
  color: var(--el-text-color-primary);
}
.wf-version-current {
  font-size: 10.5px;
  font-weight: 600;
  border-radius: 999px;
  padding: 1px 8px;
  background: var(--el-color-success-light-9);
  color: var(--el-color-success);
}
.wf-version-rollback {
  margin-left: auto;
  border: 1px solid var(--el-border-color);
  background: transparent;
  color: var(--el-text-color-regular);
  border-radius: 7px;
  padding: 4px 10px;
  font: inherit;
  font-size: 11.5px;
  cursor: pointer;
  transition: background var(--pg-transition);
}
.wf-version-rollback:hover:not(:disabled) {
  background: var(--el-fill-color-light);
}
.wf-version-desc {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--el-text-color-regular);
}
.wf-version strong {
  font-size: 12.5px;
  color: var(--el-text-color-primary);
}
.wf-version-at {
  font-size: 11px;
  color: var(--el-text-color-placeholder);
}

/* dialogs */
.wf-dialog-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.wf-dialog-hint {
  margin: 0;
  font-size: 12px;
  line-height: 1.7;
  color: var(--el-text-color-secondary);
}
.wf-dialog-meta {
  margin: 0;
  font-size: 12px;
  color: var(--el-text-color-regular);
}
.wf-dialog-error {
  margin: 0;
  font-size: 12px;
  color: var(--el-color-danger);
  line-height: 1.6;
}
.wf-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.wf-field label {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--el-text-color-regular);
}

@media (max-width: 860px) {
  .wf-invalid {
    display: none;
  }
}
</style>
