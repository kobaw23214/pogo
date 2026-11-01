<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import { ElMessageBox } from 'element-plus';
import { agentWorkflowsApi, type AgentWorkflowCapabilities, type AgentWorkflowCodePolicy, type AgentWorkflowCodeSpec, type AgentWorkflowCondition, type AgentWorkflowDAG, type AgentWorkflowDefinition, type AgentWorkflowEdge, type AgentWorkflowInputType, type AgentWorkflowLayout, type AgentWorkflowPosition, type AgentWorkflowResourceCatalog, type AgentWorkflowRun, type AgentWorkflowStep, type AgentWorkflowToolApprovalMode, type AgentWorkflowViewport } from '@/api/agentWorkflows';
import { cloneWorkflowDefinition, mergeWorkflowEdits, newestWorkflowDefinition, sameWorkflowContent, uniqueWorkflowOutput, workflowChanges, workflowEditOperations, workflowEditorIssues, workflowInsertionIndex, workflowLayoutIsCurrent, workflowNodeResourceRows, workflowReloadTarget, workflowToolApprovalMode, workflowVariables, type WorkflowChange, type WorkflowResourceField } from '@/agent/agentWorkflowEditor';
import { AGENT_WORKFLOW_MAX_STEPS } from '@/agent/agentWorkflowLimits';
import type { AgentWorkflowNodeActivity } from '@/agent/agentWorkflowActivity';
import { validAgentWorkflowId } from '@/agent/agentWorkflowNavigation';
import { agentWorkflowStepDisplayStatus, agentWorkflowStepElapsedMs } from '@/agent/agentWorkflowGraph';
import { insertWorkflowGraphGroup, insertWorkflowGraphStep, removeWorkflowGraphStep, upgradeWorkflowGraph, workflowInsertionEdge } from '@/agent/agentWorkflowDag';
import { randomUUID } from '@/utils/uuid';
import MarkdownView from '../MarkdownView.vue';
import TaskContextRing from '../TaskContextRing.vue';
import type { TaskContextReading } from '@/agent/taskContextUsage';
import AgentWorkflowCanvas from './AgentWorkflowCanvas.vue';
import AgentWorkflowCodeConfig from './AgentWorkflowCodeConfig.vue';
import AgentWorkflowCodeResult from './AgentWorkflowCodeResult.vue';
import AgentWorkflowRepeatConfig from './AgentWorkflowRepeatConfig.vue';
import AgentWorkflowRepeatResult from './AgentWorkflowRepeatResult.vue';
import { defaultWorkflowRepeat, workflowNodeCount } from '@/agent/agentWorkflowRepeat';
import { defaultWorkflowCode, workflowNodeTestDefaults, workflowNodeTestInputs } from '@/agent/agentWorkflowCode';
import { workflowInputDisplay } from '@/agent/agentWorkflowInputs';

const props = defineProps<{ definition: AgentWorkflowDefinition; run?: AgentWorkflowRun | null; version?: AgentWorkflowDefinition | null; capabilities: AgentWorkflowCapabilities; agentId: string; sessionId: string | null; visible: boolean; activities?: Record<string, AgentWorkflowNodeActivity>; resourceCatalog?: AgentWorkflowResourceCatalog; resourcesLoading?: boolean; resourcesError?: string; conversationOpen?: boolean; controlsOpen?: boolean; processOpen?: boolean; topInset?: number; leftInset?: number; bottomInset?: number; contextReading?: TaskContextReading | null }>();
const emit = defineEmits<{ saved: [AgentWorkflowDefinition]; intent: [string]; dirty: [boolean]; artifact: [string]; select: [string]; process: [boolean, string?]; refreshResources: []; testNode: [{ stepId:string; inputs:Record<string,unknown>; expectedRevision:number; expectedHash:string }] }>();
const { t } = useI18n();
const tr = (key: string, args?: Record<string, string | number>) => t(`agentWorkflowCanvas.${key}`, args || {});
const base = ref(cloneWorkflowDefinition(props.definition));
const draft = ref(cloneWorkflowDefinition(props.definition));
const selectedId = ref('');
type DetailTab = 'process' | 'io' | 'configuration';
const detailTab = ref<DetailTab>('process'), detailTabList = ref<HTMLElement | null>(null);
const detailTabs: DetailTab[] = ['process', 'io', 'configuration'];
const inspectorOpen = ref(false), frame = ref<HTMLElement | null>(null);
const addMenu = ref<{ handleClose(): void } | null>(null);
const frameSize = ref({ width: 1024, height: 768 });
let frameObserver: ResizeObserver | undefined;
const compactInspector = computed(() => frameSize.value.width <= 760);
const inspectorVisible = computed(() => inspectorOpen.value && !props.conversationOpen && !(compactInspector.value && props.processOpen));
const baseTopInset = computed(() => Math.max(56, props.topInset ?? 88));
const baseBottomInset = computed(() => Math.max(56, props.bottomInset || 0));
const inspectorHeight = computed(() => {
  const process = executionDetails.value && detailTab.value === 'process';
  const available = Math.max(100, frameSize.value.height - baseBottomInset.value - baseTopInset.value - 24);
  return Math.min(process ? 560 : 400, Math.max(process ? 240 : 120, frameSize.value.height * (process ? .68 : .42)), available);
});
const canvasInsets = computed(() => ({ top: baseTopInset.value, left: Math.max(16, props.leftInset ?? 72), right: inspectorVisible.value && !compactInspector.value ? 408 : 16, bottom: baseBottomInset.value + (inspectorVisible.value && compactInspector.value ? inspectorHeight.value + 16 : 0) }));
const saving = ref(false), error = ref(''), notice = ref(''), comparisonOpen = ref(false), authenticationInvalid = ref(false);
const recentUpdate = ref<{ revision: number; count: number; targetId: string } | null>(null), updatedIds = ref<string[]>([]);
let updateTimer: ReturnType<typeof setTimeout> | undefined;
const pendingRemote = ref<AgentWorkflowDefinition | null>(null);
const history = ref<AgentWorkflowDefinition[]>([cloneWorkflowDefinition(props.definition)]), historyIndex = ref(0);
const positions = ref<Record<string, AgentWorkflowPosition>>({}), viewport = ref<AgentWorkflowViewport>();
const layoutRevision = ref(0), layoutDirty = ref(false), layoutSaving = ref(false), layoutError = ref('');
const lastLayout = ref<AgentWorkflowLayout | null>(null), incomingLayout = ref<AgentWorkflowLayout | null>(null);
const instruction = ref<HTMLTextAreaElement | null>(null);
const canvas = ref<{ focus(id: string): void | Promise<void>; arrange(): void | Promise<void> } | null>(null);
const variable = ref('');
const formEpoch = ref(0);
const codeFormValid = ref(true), codeTestOpen = ref(false), codeTestValue = ref(''), codeTestError = ref('');
const codeTestSnapshot = ref<{ stepId:string; revision:number; hash:string; spec:AgentWorkflowCodeSpec } | null>(null);
const requestIds = new Map<string, string>();
const controllers = new Set<AbortController>();
let disposed = false, epoch = 0, layoutGeneration = 0, layoutReadTicket = 0, layoutTimer: ReturnType<typeof setTimeout> | undefined;
let historyKey = '', historyAt = 0;
const scopeKey = computed(() => JSON.stringify([props.agentId, props.definition.id, props.run?.id || '', props.version?.id || '', props.version?.agent_id || '', props.version?.revision || 0, props.version?.definition_hash || '']));
const validScope = computed(() => !authenticationInvalid.value && validAgentWorkflowId(props.agentId) && validAgentWorkflowId(props.definition.id) && (!props.definition.agent_id || props.definition.agent_id === props.agentId) && (!props.run || props.run.agent_id === props.agentId && props.run.workflow_id === props.definition.id && props.run.definition.id === props.definition.id) && (!props.version || props.version.id === props.definition.id && props.version.agent_id === props.agentId));
const isHistory = computed(() => !!props.run || !!props.version);
const editable = computed(() => validScope.value && !isHistory.value && props.capabilities.edit === true && !props.definition.archived && !props.definition.deleted);
const canEdit = computed(() => editable.value && !saving.value);
const dirty = computed(() => !isHistory.value && !sameWorkflowContent(base.value, draft.value));
const hasUnsavedForm = computed(() => dirty.value || !codeFormValid.value);
const displayed = computed(() => props.run?.definition || props.version || draft.value);
const step = computed(() => displayed.value.steps.find(item => item.id === selectedId.value));
const stepIndex = computed(() => displayed.value.steps.findIndex(item => item.id === selectedId.value));
const stepRun = computed(() => props.run?.steps?.find(item => item.step_id === selectedId.value));
const canAddControlGroup = computed(() => canEdit.value && codeFormValid.value && !!draft.value.graph && workflowNodeCount(draft.value) + 4 <= AGENT_WORKFLOW_MAX_STEPS);
const canDuplicate = computed(() => canEdit.value && codeFormValid.value && !!step.value && ['agent','code','iteration','loop','confirm'].includes(step.value.kind) && workflowNodeCount(draft.value) + workflowNodeCount({steps:[step.value]}) <= AGENT_WORKFLOW_MAX_STEPS);
const finalStructuredResults = computed(() => {
  const value = props.run; if (!value) return [];
  const ids = value.node_test ? [value.node_test.step_id] : value.definition.graph ? value.definition.graph.edges.filter(edge => edge.target === '__output').map(edge => edge.source) : [value.definition.steps[value.definition.steps.length - 1]?.id];
  return [...new Set(ids)].flatMap(id => {
    const result = value.steps?.find(item => item.step_id === id);
    return result?.output ? [{ id, title: value.definition.steps.find(item => item.id === id)?.title || id, value: result.output }] : [];
  });
});
const executionDetails = computed(() => !!props.run && step.value?.kind === 'agent');
const showContextRing = computed(() => !!props.run && (step.value?.kind === 'agent' || step.value?.kind === 'iteration' && step.value.iteration?.body.kind === 'agent'));
const processVisible = computed(() => props.visible && inspectorVisible.value && executionDetails.value && detailTab.value === 'process');
const detailPanelId = computed(() => `aw-node-detail-${props.run?.id || props.definition.id}-${selectedId.value}`);
const stepDisplayStatus = computed(() => agentWorkflowStepDisplayStatus(props.run, selectedId.value) || 'pending');
const waiting = computed(() => !!props.run && !['completed', 'failed', 'cancelled', 'interrupted'].includes(props.run.status) && (props.run.definition.graph ? stepRun.value?.status === 'waiting_confirmation' : props.run.status === 'waiting_confirmation' && props.run.definition.steps[props.run.current_step || 0]?.id === selectedId.value));
const basicStep = computed(() => !!step.value && ['agent', 'confirm'].includes(step.value.kind));
const conditionOperators = ['equals', 'not_equals', 'contains', 'not_contains', 'is_empty', 'not_empty', 'gt', 'gte', 'lt', 'lte'] as const;
const variables = computed(() => step.value ? workflowVariables(displayed.value, step.value.id) : []);
const resourceFields: WorkflowResourceField[] = ['skills', 'shortcuts'];
const resourceVersion = (value: string) => /^[a-f0-9]{64}$/i.test(value) ? value.slice(0, 10) : value;
const runInputDisplay = (key: string | number, value: string) => workflowInputDisplay(props.run?.definition.inputs || [], String(key), value, t('agentWorkflow.inputYes'), t('agentWorkflow.inputNo'));
const nodeResourceSnapshot = computed(() => selectedId.value ? props.run?.resources?.nodes[selectedId.value] : undefined);
const nodeResourceGroups = computed(() => step.value ? resourceFields.map(field => ({ field, rows: workflowNodeResourceRows(step.value!, field, props.resourceCatalog, nodeResourceSnapshot.value, isHistory.value) })) : []);
const nodeResourceCount = computed(() => (step.value?.skills?.length || 0) + (step.value?.shortcuts?.length || 0));
const issues = computed(() => workflowEditorIssues(draft.value));
const localChanges = computed(() => workflowChanges(base.value, draft.value));
const remoteChanges = computed(() => pendingRemote.value ? workflowChanges(base.value, pendingRemote.value) : []);
const mergeResult = computed(() => pendingRemote.value ? mergeWorkflowEdits(base.value, draft.value, pendingRemote.value) : null);
const duration = computed(() => {
  const elapsed = agentWorkflowStepElapsedMs(props.run, selectedId.value);
  return elapsed === undefined ? '' : tr('seconds', { n: (elapsed / 1000).toFixed(1) });
});
const scopeCurrent = (key: string, mine: number) => !disposed && scopeKey.value === key && epoch === mine && validScope.value;
function beginRequest() { const controller = new AbortController(); controllers.add(controller); return controller; }
function keyFor(kind: string, body: unknown) {
  const key = `${kind}:${JSON.stringify(body)}`;
  if (!requestIds.has(key)) requestIds.set(key, randomUUID());
  return { key, id: requestIds.get(key)! };
}
function cleanPositions(value: Record<string, AgentWorkflowPosition>, definition = base.value) {
  const known = new Set(['__input', '__output', ...definition.steps.map(item => item.id)]);
  return Object.fromEntries(Object.entries(value).filter(([id, p]) => known.has(id) && Number.isFinite(p.x) && Number.isFinite(p.y)).map(([id, p]) => [id, { x: p.x, y: p.y }]));
}
function resetHistory(value: AgentWorkflowDefinition) {
  history.value = [cloneWorkflowDefinition(value)]; historyIndex.value = 0; historyKey = ''; historyAt = 0;
}
function setBase(value: AgentWorkflowDefinition) {
  codeFormValid.value = true; formEpoch.value++;
  base.value = cloneWorkflowDefinition(value); draft.value = cloneWorkflowDefinition(value); pendingRemote.value = null; resetHistory(value);
  if (selectedId.value && !['__input', '__output'].includes(selectedId.value) && !value.steps.some(item => item.id === selectedId.value)) { selectedId.value = ''; inspectorOpen.value = false; }
}
function clearRecentUpdate() { clearTimeout(updateTimer); recentUpdate.value = null; updatedIds.value = []; }
function showRemoteUpdate(previous: AgentWorkflowDefinition, value: AgentWorkflowDefinition) {
  const changes = workflowChanges(previous, value);
  if (!changes.length) return;
  const changed = new Set(changes.flatMap(change => change.stepId ? [change.stepId] : []));
  updatedIds.value = value.steps.filter(step => changed.has(step.id)).map(step => step.id);
  if (changes.some(change => ['name', 'description', 'inputs'].includes(change.field))) updatedIds.value.push('__input');
  recentUpdate.value = { revision: value.revision, count: changed.size, targetId: updatedIds.value[0] || '__input' };
  clearTimeout(updateTimer); updateTimer = setTimeout(clearRecentUpdate, 10000);
}
function focusRecentUpdate() { if (recentUpdate.value) focusStep(recentUpdate.value.targetId); }
function receiveDefinition(value: AgentWorkflowDefinition) {
  if (!validScope.value || isHistory.value || value.id !== base.value.id || value.revision < base.value.revision || value.revision < (pendingRemote.value?.revision || 0)) return;
  if (value.revision === base.value.revision && value.definition_hash === base.value.definition_hash) {
    base.value.archived = value.archived; draft.value.archived = value.archived;
    base.value.enabled_version = value.enabled_version; draft.value.enabled_version = value.enabled_version;
    return;
  }
  if (codeFormValid.value && (!dirty.value || sameWorkflowContent(value, draft.value))) {
    const previous = base.value;
    setBase(value); showRemoteUpdate(previous, value);
    if (layoutDirty.value) scheduleLayoutSave();
  } else if (sameWorkflowContent(value, base.value)) {
    base.value = cloneWorkflowDefinition(value);
  } else pendingRemote.value = cloneWorkflowDefinition(value);
}
function mutate(change: (value: AgentWorkflowDefinition) => void, key = '') {
  if (!canEdit.value) return;
  const next = cloneWorkflowDefinition(draft.value); change(next);
  if (sameWorkflowContent(next, draft.value)) return;
  const now = Date.now();
  history.value = history.value.slice(0, historyIndex.value + 1);
  if (key && key === historyKey && now - historyAt < 800 && historyIndex.value > 0) history.value[historyIndex.value] = cloneWorkflowDefinition(next);
  else { history.value.push(cloneWorkflowDefinition(next)); historyIndex.value++; }
  if (history.value.length > 60) { history.value.shift(); historyIndex.value--; }
  historyKey = key; historyAt = now; draft.value = next; error.value = ''; notice.value = '';
}
function undo() { if (canEdit.value && historyIndex.value > 0) { draft.value = cloneWorkflowDefinition(history.value[--historyIndex.value]!); historyKey = ''; } }
function redo() { if (canEdit.value && historyIndex.value + 1 < history.value.length) { draft.value = cloneWorkflowDefinition(history.value[++historyIndex.value]!); historyKey = ''; } }
function select(id: string) {
  if (id !== selectedId.value && !codeFormValid.value && !isHistory.value) { notice.value = tr('finishFormBeforeSwitch'); return; }
  if (id && !['__input', '__output'].includes(id) && !displayed.value.steps.some(item => item.id === id)) return;
  codeTestOpen.value = false; selectedId.value = id; detailTab.value = 'process'; inspectorOpen.value = !!id; variable.value = ''; addMenu.value?.handleClose(); emit('select', id);
}
function closeInspector() { inspectorOpen.value = false; }
function selectDetailTab(value: DetailTab) { detailTab.value = value; }
function detailTabKey(event: KeyboardEvent) {
  const current = detailTabs.indexOf(detailTab.value);
  const next = event.key === 'ArrowRight' ? (current + 1) % detailTabs.length : event.key === 'ArrowLeft' ? (current + detailTabs.length - 1) % detailTabs.length : event.key === 'Home' ? 0 : event.key === 'End' ? detailTabs.length - 1 : -1;
  if (next < 0) return;
  event.preventDefault(); selectDetailTab(detailTabs[next]!);
  detailTabList.value?.querySelectorAll<HTMLButtonElement>('[role=tab]')[next]?.focus();
}
function focusStep(id: string) {
  if (['__input', '__output'].includes(id) || displayed.value.steps.some(item => item.id === id)) {
    select(id); void nextTick(() => { void canvas.value?.focus(id); });
  }
}
function addNodeAction(kind: string) {
  if (!codeFormValid.value) { notice.value=tr('finishFormBeforeSwitch'); return; }
  if (!canEdit.value) return;
  if (kind === 'agent' || kind === 'confirm' || kind === 'code' || kind === 'iteration' || kind === 'loop') add(kind);
  else if (kind === 'condition' || kind === 'parallel') addGroup(kind);
  else if (kind === 'upgrade') upgradeGraph();
}
function state() { return { dirty: hasUnsavedForm.value, saving: saving.value, canSave: canEdit.value && dirty.value && !pendingRemote.value && !layoutSaving.value && issues.value.length === 0 && codeFormValid.value }; }
function add(kind: 'agent' | 'confirm' | 'code' | 'iteration' | 'loop', afterId: string | null = selectedId.value, edge?: AgentWorkflowEdge) {
  if (!canEdit.value) return;
  if (workflowNodeCount(draft.value) >= AGENT_WORKFLOW_MAX_STEPS) { error.value = tr('maxSteps'); return; }
  const id = `step_${randomUUID().replace(/-/g, '').slice(0, 24)}`;
  const index = workflowInsertionIndex(draft.value, afterId);
  const next: AgentWorkflowStep = { id, kind, title: tr(kind), ...((kind === 'iteration' || kind === 'loop') ? defaultWorkflowRepeat(kind) : kind === 'code' ? { code: defaultWorkflowCode() } : kind === 'agent' ? { task: '' } : { prompt: '' }) };
  if (workflowNodeCount(draft.value) + workflowNodeCount({steps:[next]}) > AGENT_WORKFLOW_MAX_STEPS) { error.value = tr('maxSteps'); return; }
  if (draft.value.graph && !workflowInsertionEdge(draft.value, afterId, edge)) { error.value = tr('chooseBranch'); return; }
  mutate(value => { if (value.graph) insertWorkflowGraphStep(value, next, afterId, edge); else value.steps.splice(index, 0, next); }); select(id);
}
function upgradeGraph() { mutate(value => { value.graph = upgradeWorkflowGraph(value); }); notice.value = tr('upgraded'); }
function addGroup(kind: 'condition' | 'parallel') {
  if (!canAddControlGroup.value) return;
  if (!workflowInsertionEdge(draft.value, selectedId.value)) { error.value = tr('chooseBranch'); return; }
  const newId = () => `step_${randomUUID().replace(/-/g, '').slice(0, 24)}`;
  const source = draft.value.inputs?.[0]?.key ? `inputs.${draft.value.inputs[0].key}` : '';
  const control: AgentWorkflowStep = { id: newId(), kind, title: tr(kind), ...(kind === 'condition' ? { condition: { source, operator: 'equals', value: '' } as AgentWorkflowCondition } : {}) };
  const left: AgentWorkflowStep = { id: newId(), kind: 'agent', title: tr(kind === 'condition' ? 'trueTask' : 'parallelTaskA'), task: '' };
  const right: AgentWorkflowStep = { id: newId(), kind: 'agent', title: tr(kind === 'condition' ? 'falseTask' : 'parallelTaskB'), task: '' };
  const join: AgentWorkflowStep = { id: newId(), kind: 'join', title: tr('join') };
  mutate(value => { insertWorkflowGraphGroup(value, [control, left, right, join], selectedId.value); }); select(control.id);
  // Existing coordinates belong to the user. New group nodes receive free
  // positions without rearranging the rest of the canvas.
}
function graphChanged(graph: AgentWorkflowDAG) { mutate(value => { value.graph = graph; }); }
function setCode(code: AgentWorkflowCodeSpec) {
  const id = selectedId.value;
  mutate(next => { const node = next.steps.find(item => item.id === id); if (node?.kind === 'code') node.code = code; }, `${id}:code`);
}
function setCodePolicy(policy: AgentWorkflowCodePolicy | undefined) { const id=selectedId.value; mutate(next=>{const node=next.steps.find(item=>item.id===id);if(node?.kind==='code'){if(policy)node.policy=policy;else delete node.policy;}},`${id}:policy`); }
function setRepeat(value: AgentWorkflowStep) { const id=selectedId.value; mutate(next=>{const index=next.steps.findIndex(item=>item.id===id);if(index>=0)next.steps[index]=JSON.parse(JSON.stringify({...value,id}));},`${id}:repeat`); }
function openCodeTest() {
  if (!canEdit.value || dirty.value || !codeFormValid.value || issues.value.length || props.capabilities.run !== true || step.value?.kind !== 'code' || !step.value.code) return;
  codeTestSnapshot.value = { stepId:step.value.id, revision:base.value.revision, hash:base.value.definition_hash, spec:JSON.parse(JSON.stringify(step.value.code)) };
  codeTestValue.value = JSON.stringify(workflowNodeTestDefaults(step.value.code),null,2); codeTestError.value = ''; codeTestOpen.value = true;
}
function submitCodeTest() {
  const test = codeTestSnapshot.value;
  if (!test || !canEdit.value || dirty.value || props.capabilities.run !== true || test.revision !== base.value.revision || test.hash !== base.value.definition_hash) { codeTestError.value = tr('codeTestStale'); return; }
  const inputs = workflowNodeTestInputs(test.spec,codeTestValue.value);
  if (!inputs || new TextEncoder().encode(codeTestValue.value).length > 262144) { codeTestError.value = tr('codeTestInvalid'); return; }
  codeTestOpen.value = false; emit('testNode',{ stepId:test.stepId,inputs,expectedRevision:test.revision,expectedHash:test.hash });
}
function setCondition(field: keyof AgentWorkflowCondition, value: string) {
  const id = selectedId.value;
  mutate(next => { const item = next.steps.find(node => node.id === id); if (item?.kind === 'condition') { item.condition = { source: '', operator: 'equals', ...item.condition, [field]: value } as AgentWorkflowCondition; if (field === 'operator' && ['is_empty', 'not_empty'].includes(value)) item.condition.value = ''; } }, `${id}:condition:${field}`);
}
function duplicate(id: string) {
  if (!codeFormValid.value) { notice.value=tr('finishFormBeforeSwitch'); return; }
  if (!canEdit.value) return;
  if (workflowNodeCount(draft.value) >= AGENT_WORKFLOW_MAX_STEPS) { error.value = tr('maxSteps'); return; }
  const index = draft.value.steps.findIndex(item => item.id === id); if (index < 0) return;
  const current = draft.value.steps[index]!, copy: AgentWorkflowStep = { ...JSON.parse(JSON.stringify(current)), id: `step_${randomUUID().replace(/-/g, '').slice(0, 24)}`, title: `${current.title || current.id} (copy)` };
  if (!['agent', 'code', 'iteration', 'loop', 'confirm'].includes(current.kind)) { error.value = tr('controlCannotCopy'); return; }
  if (workflowNodeCount(draft.value)+workflowNodeCount({steps:[current]})>AGENT_WORKFLOW_MAX_STEPS) { error.value=tr('maxSteps'); return; }
  if (draft.value.graph && !workflowInsertionEdge(draft.value, id)) { error.value = tr('chooseBranch'); return; }
  if (copy.output_file) copy.output_file = uniqueWorkflowOutput(copy.output_file, draft.value.steps);
  mutate(value => { if (value.graph) insertWorkflowGraphStep(value, copy, id); else value.steps.splice(index + 1, 0, copy); }); select(copy.id);
}
function remove(id: string) {
  if (!codeFormValid.value) { notice.value=tr('finishFormBeforeSwitch'); return; }
  if (!canEdit.value) return;
  if (draft.value.steps.length < 2) { error.value = tr('cannotRemoveLast'); return; }
  const index = draft.value.steps.findIndex(item => item.id === id); if (index < 0) return;
  if (draft.value.graph && !removeWorkflowGraphStep(cloneWorkflowDefinition(draft.value), id)) { error.value = tr('cannotRemoveGraphNode'); return; }
  mutate(value => { if (value.graph) removeWorkflowGraphStep(value, id); else value.steps.splice(index, 1); });
  if (draft.value.graph) notice.value = tr('graphRemovalReview');
  if (selectedId.value === id) select(draft.value.steps[Math.min(index, draft.value.steps.length - 1)]?.id || '__input');
}
function reorder(change: { stepId: string; index: number }) {
  if (draft.value.graph) return;
  const current = draft.value.steps.findIndex(item => item.id === change.stepId);
  if (current < 0 || change.index < 0 || change.index >= draft.value.steps.length || current === change.index) return;
  mutate(value => value.steps.splice(change.index, 0, value.steps.splice(current, 1)[0]!));
}
function setMeta(field: 'name' | 'description', value: string) { mutate(next => { next[field] = value; }, `meta:${field}`); }
function setToolApprovalMode(value: AgentWorkflowToolApprovalMode) {
  if (!['standard', 'strict', 'auto'].includes(value)) return;
  mutate(next => { next.tool_approval_mode = value; }, 'meta:tool_approval_mode');
}
function setStep(field: 'title' | 'task' | 'prompt' | 'output_file', value: string) {
  const id = selectedId.value;
  mutate(next => { const item = next.steps.find(s => s.id === id); if (item) item[field] = value; }, `${id}:${field}`);
}
function setKind(kind: 'agent' | 'confirm') {
  const id = selectedId.value;
  mutate(next => { const item = next.steps.find(s => s.id === id); if (!item || item.kind === kind || !['agent', 'confirm'].includes(item.kind)) return; item.kind = kind; item.task = ''; item.prompt = ''; item.output_file = ''; delete item.condition; delete item.skills; delete item.shortcuts; });
}
function setInput(index: number, field: 'key' | 'label' | 'required' | 'default', value: string | boolean) { mutate(next => { const item = next.inputs?.[index]; if (item) { if (field === 'default' && value === '') delete item.default; else Object.assign(item, { [field]: value }); } }, `input:${index}:${field}`); }
function setInputType(index: number, type: AgentWorkflowInputType) {
  if (!['text', 'boolean', 'select', 'number', 'integer', 'object', 'array'].includes(type)) return;
  mutate(next => {
    const item = next.inputs?.[index]; if (!item || (item.type || 'text') === type) return;
    item.type = type;
    if (['number','integer','object','array'].includes(type)) delete item.default;
    if (type === 'select') item.options = [{ value: 'option_1', label: '' }, { value: 'option_2', label: '' }];
    else if (type === 'boolean') item.options = [{ value: 'yes', label: tr('booleanYesLabel') }, { value: 'no', label: tr('booleanNoLabel') }];
    else delete item.options;
    if (item.default && (type === 'boolean' && !['yes', 'no'].includes(item.default) || type === 'select' && !item.options?.some(option => option.value === item.default))) delete item.default;
  });
}
function setInputOption(index: number, at: number, field: 'value' | 'label', value: string) { mutate(next => { const input = next.inputs?.[index], option = input?.options?.[at]; if (option && (input?.type !== 'boolean' || field === 'label')) option[field] = value; }, `input:${index}:option:${at}:${field}`); }
function addInputOption(index: number) { mutate(next => { const input = next.inputs?.[index]; if (input?.type !== 'select' || (input.options?.length || 0) >= 64) return; const values = new Set(input.options?.map(item => item.value)); let n = 1; while (values.has(`option_${n}`)) n++; input.options = [...(input.options || []), { value: `option_${n}`, label: '' }]; }); }
function removeInputOption(index: number, at: number) { mutate(next => { const input = next.inputs?.[index]; if (input?.type === 'select') input.options?.splice(at, 1); }); }
function toggleNodeResource(field: WorkflowResourceField, id: string, checked: boolean) {
  const node = step.value; if (!node || node.kind !== 'agent') return;
  const row = nodeResourceGroups.value.find(group => group.field === field)?.rows.find(item => item.id === id);
  if (checked && (!row?.available || props.resourcesLoading || props.resourcesError || (node[field]?.length || 0) >= 32)) return;
  mutate(next => { const item = next.steps.find(value => value.id === node.id); if (item?.kind !== 'agent') return; item[field] = checked ? [...new Set([...(item[field] || []), id])] : (item[field] || []).filter(value => value !== id); });
}
function addInput() {
  if ((draft.value.inputs?.length || 0) >= 32) return;
  const keys = new Set((draft.value.inputs || []).map(input => input.key)); let n = 1; while (keys.has(`input_${n}`)) n++;
  mutate(next => { next.inputs = [...(next.inputs || []), { key: `input_${n}`, label: '', required: false }]; });
}
function removeInput(index: number) { mutate(next => next.inputs?.splice(index, 1)); }
function insertVariable() {
  if (!variable.value || !step.value || !variables.value.some(item => item.value === variable.value)) return;
  const field = step.value.kind === 'agent' ? 'task' : 'prompt', text = step.value[field] || '', element = instruction.value;
  const start = element?.selectionStart ?? text.length, end = element?.selectionEnd ?? start;
  setStep(field, text.slice(0, start) + variable.value + text.slice(end));
  const cursor = start + variable.value.length; variable.value = '';
  void nextTick(() => { instruction.value?.focus(); instruction.value?.setSelectionRange(cursor, cursor); });
}
function reference() {
  if (!step.value || !canEdit.value || hasUnsavedForm.value) return;
  emit('intent', tr('referencePrompt', { workflowId: base.value.id, workflowName: base.value.name, revision: base.value.revision, stepId: step.value.id, stepTitle: step.value.title || step.value.id }));
}
function changeLabel(change: WorkflowChange) { return `${tr(`changes.${change.kind}`)} · ${change.title ? `${change.title} / ` : ''}${tr(`fields.${change.field}`)}`; }
function mergeIncoming() {
  if (!pendingRemote.value || !mergeResult.value || !canEdit.value) return;
  if (mergeResult.value.conflicts.length) { error.value = tr('mergeConflict'); comparisonOpen.value = true; return; }
  const remote = pendingRemote.value, merged = mergeResult.value.definition;
  base.value = cloneWorkflowDefinition(remote); draft.value = cloneWorkflowDefinition(merged); pendingRemote.value = null;
  resetHistory(remote); if (!sameWorkflowContent(remote, merged)) { history.value.push(cloneWorkflowDefinition(merged)); historyIndex.value = 1; }
  error.value = ''; notice.value = tr('merged');
}
async function reloadLatest() {
  if (!pendingRemote.value || saving.value) return;
  const key = scopeKey.value, mine = epoch;
  if (dirty.value) {
    try { await ElMessageBox.confirm(tr('reloadQuestion'), tr('diffTitle'), { confirmButtonText: tr('reloadConfirm'), cancelButtonText: tr('continueEditing'), type: 'warning' }); } catch { return; }
  }
  if (!scopeCurrent(key, mine) || saving.value) return;
  const latest = workflowReloadTarget(base.value, pendingRemote.value, props.definition);
  comparisonOpen.value = false;
  if (!latest) return;
  setBase(latest); error.value = '';
  positions.value = cleanPositions(positions.value); if (layoutDirty.value) scheduleLayoutSave();
}
async function refreshDefinition() {
  if (!validScope.value || isHistory.value) return;
  const key = scopeKey.value, mine = epoch, aid = props.agentId, wid = base.value.id, controller = beginRequest();
  try {
    const response = await agentWorkflowsApi.call(aid, 'read', { action: 'get', workflow_id: wid }, controller.signal);
    if (scopeCurrent(key, mine) && response.workflow?.id === wid && (!response.workflow.agent_id || response.workflow.agent_id === aid)) receiveDefinition(response.workflow);
  } finally { controllers.delete(controller); }
}
async function save() {
  if (!canEdit.value || !dirty.value || pendingRemote.value || layoutSaving.value || issues.value.length || !codeFormValid.value) return;
  const operations = workflowEditOperations(base.value, draft.value); if (!operations.length) return;
  const key = scopeKey.value, mine = epoch, aid = props.agentId, wid = base.value.id, controller = beginRequest();
  const body = { action: 'patch', workflow_id: wid, expected_revision: base.value.revision, operations, ...(props.sessionId ? { source_session_id: props.sessionId } : {}) };
  const request = keyFor('edit', body); saving.value = true; error.value = ''; notice.value = '';
  try {
    const response = await agentWorkflowsApi.call(aid, 'edit', { ...body, request_id: request.id }, controller.signal);
    if (!scopeCurrent(key, mine)) return;
    if (!response.ok || !response.workflow || response.workflow.id !== wid || response.workflow.agent_id && response.workflow.agent_id !== aid) throw new Error(t('agentWorkflow.scopeChanged'));
    requestIds.delete(request.key);
    const value = newestWorkflowDefinition(response.workflow, [base.value, pendingRemote.value, props.definition]);
    setBase(value); emit('saved', value); notice.value = tr('saved'); positions.value = cleanPositions(positions.value, value);
  } catch (issue) {
    if (!scopeCurrent(key, mine) || (issue as Error)?.name === 'AbortError') return;
    error.value = (issue as Error)?.message || t('agentWorkflow.failed');
    if ((issue as { status?: number })?.status === 409) { try { await refreshDefinition(); } catch { /* retain the draft and the original conflict */ } }
  } finally {
    controllers.delete(controller);
    if (scopeCurrent(key, mine)) { saving.value = false; if (layoutDirty.value && !dirty.value) scheduleLayoutSave(); }
  }
}
async function loadLayout(retainLocal = false) {
  if (!validScope.value || isHistory.value || !props.visible) return;
  const key = scopeKey.value, mine = epoch, aid = props.agentId, wid = base.value.id, controller = beginRequest(), generation = layoutGeneration, ticket = ++layoutReadTicket;
  try {
    const response = await agentWorkflowsApi.getLayout(aid, wid, controller.signal);
    if (!scopeCurrent(key, mine) || ticket !== layoutReadTicket || !response.layout || !currentLayoutResponse(response.layout)) return;
    if (retainLocal || layoutDirty.value || generation !== layoutGeneration) {
      if (!retainLocal && response.layout.layout_revision === layoutRevision.value && response.layout.definition_revision === base.value.revision) {
        lastLayout.value = response.layout; layoutError.value = ''; scheduleLayoutSave();
      } else incomingLayout.value = response.layout;
      return;
    }
    lastLayout.value = response.layout; layoutRevision.value = response.layout.layout_revision;
    positions.value = cleanPositions(response.layout.positions); viewport.value = response.layout.viewport;
    incomingLayout.value = null; layoutError.value = '';
  } catch (issue) {
    if (scopeCurrent(key, mine) && ticket === layoutReadTicket && (issue as Error)?.name !== 'AbortError') layoutError.value = (issue as Error)?.message || t('agentWorkflow.unavailable');
  } finally { controllers.delete(controller); }
}
function layoutRevisionFloor() { return Math.max(layoutRevision.value, lastLayout.value?.layout_revision || 0, incomingLayout.value?.layout_revision || 0); }
function currentLayoutResponse(value: AgentWorkflowLayout) { return workflowLayoutIsCurrent(value, base.value, layoutRevisionFloor(), Math.max(lastLayout.value?.definition_revision || 0, incomingLayout.value?.definition_revision || 0)); }
function positionsChanged(value: Record<string, AgentWorkflowPosition>) {
  if (!canEdit.value) return;
  positions.value = cleanPositions(value, draft.value); layoutDirty.value = true; layoutGeneration++; scheduleLayoutSave();
}
function viewportChanged(value: AgentWorkflowViewport) {
  if (!canEdit.value) return;
  viewport.value = { ...value }; layoutDirty.value = true; layoutGeneration++; scheduleLayoutSave();
}
function scheduleLayoutSave() {
  clearTimeout(layoutTimer);
  if (editable.value && !dirty.value && !saving.value && !incomingLayout.value && props.visible) layoutTimer = setTimeout(() => void saveLayout(), 650);
}
async function saveLayout() {
  if (!editable.value || !layoutDirty.value || dirty.value || saving.value || layoutSaving.value || incomingLayout.value || !props.visible) return;
  const key = scopeKey.value, mine = epoch, aid = props.agentId, wid = base.value.id, controller = beginRequest(), generation = layoutGeneration;
  const body = { expected_layout_revision: layoutRevision.value, expected_definition_revision: base.value.revision, positions: cleanPositions(positions.value), ...(viewport.value ? { viewport: { ...viewport.value } } : {}) };
  const request = keyFor('layout', body); ++layoutReadTicket; layoutSaving.value = true; layoutError.value = '';
  try {
    const response = await agentWorkflowsApi.saveLayout(aid, wid, { ...body, request_id: request.id }, controller.signal);
    if (!scopeCurrent(key, mine)) return;
    if (!response.ok || !response.layout || response.layout.workflow_id !== wid) throw new Error(t('agentWorkflow.scopeChanged'));
    requestIds.delete(request.key);
    if (!currentLayoutResponse(response.layout)) {
      if (!incomingLayout.value) await loadLayout(true);
      return;
    }
    ++layoutReadTicket; layoutRevision.value = response.layout.layout_revision; lastLayout.value = response.layout;
    if (generation === layoutGeneration) layoutDirty.value = false;
  } catch (issue) {
    if (!scopeCurrent(key, mine) || (issue as Error)?.name === 'AbortError') return;
    layoutError.value = (issue as Error)?.message || t('agentWorkflow.failed');
    if ((issue as { status?: number })?.status === 409) { await loadLayout(true); try { await refreshDefinition(); } catch { /* preserve local layout */ } }
  } finally {
    controllers.delete(controller);
    if (scopeCurrent(key, mine)) { layoutSaving.value = false; if (layoutDirty.value && !layoutError.value) scheduleLayoutSave(); }
  }
}
function acceptRemoteLayout() {
  if (!validScope.value || isHistory.value || !incomingLayout.value || layoutSaving.value) return;
  const value = incomingLayout.value; lastLayout.value = value; layoutRevision.value = value.layout_revision;
  positions.value = cleanPositions(value.positions); viewport.value = value.viewport; layoutDirty.value = false; incomingLayout.value = null; layoutError.value = ''; layoutGeneration++;
}
function keepLocalLayout() {
  if (!incomingLayout.value || !canEdit.value || dirty.value || incomingLayout.value.definition_revision !== base.value.revision) return;
  layoutRevision.value = incomingLayout.value.layout_revision; lastLayout.value = incomingLayout.value; incomingLayout.value = null; layoutError.value = ''; void saveLayout();
}
async function prepareClose(): Promise<boolean> {
  if (saving.value || layoutSaving.value) { notice.value = tr('busy'); return false; }
  if (layoutDirty.value && !dirty.value && !incomingLayout.value) await saveLayout();
  if (!hasUnsavedForm.value && !layoutDirty.value) return true;
  const key = scopeKey.value, mine = epoch;
  try { await ElMessageBox.confirm(tr('closeQuestion'), tr('unsaved'), { confirmButtonText: tr('discard'), cancelButtonText: tr('continueEditing'), type: 'warning' }); } catch { return false; }
  if (!scopeCurrent(key, mine)) return false;
  setBase(pendingRemote.value || props.definition); positions.value = cleanPositions((incomingLayout.value || lastLayout.value)?.positions || {}); viewport.value = (incomingLayout.value || lastLayout.value)?.viewport;
  layoutDirty.value = false; incomingLayout.value = null; layoutError.value = ''; clearTimeout(layoutTimer); return true;
}
function initialize() {
  ++epoch; ++layoutReadTicket; for (const controller of controllers) controller.abort(); controllers.clear(); clearTimeout(layoutTimer);
  authenticationInvalid.value = false; saving.value = false; layoutSaving.value = false; layoutDirty.value = false; layoutGeneration = 0; layoutRevision.value = 0;
  error.value = ''; notice.value = ''; clearRecentUpdate(); layoutError.value = ''; comparisonOpen.value = false; positions.value = {}; viewport.value = undefined; lastLayout.value = null; incomingLayout.value = null; requestIds.clear();
  setBase(props.definition); selectedId.value = props.run?.definition.graph ? props.run.steps?.find(item => item.status === 'waiting_confirmation')?.step_id || '' : props.run?.status === 'waiting_confirmation' ? props.run.definition.steps[props.run.current_step || 0]?.id || '' : ''; detailTab.value = 'process'; inspectorOpen.value = !!selectedId.value; addMenu.value?.handleClose();
  emit('select', selectedId.value);
  if (props.visible && !isHistory.value) void loadLayout();
}
function authenticationChanged() {
  ++epoch; for (const controller of controllers) controller.abort(); controllers.clear(); clearTimeout(layoutTimer); clearRecentUpdate(); authenticationInvalid.value = true; emit('dirty', false);
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && !event.defaultPrevented && !comparisonOpen.value && inspectorVisible.value) { event.preventDefault(); event.stopPropagation(); closeInspector(); return; }
  if (!(event.metaKey || event.ctrlKey) || (event.target as HTMLElement)?.closest('input,textarea,[contenteditable="true"]')) return;
  if (event.key.toLowerCase() === 'z') { event.preventDefault(); event.shiftKey ? redo() : undo(); }
}
watch(scopeKey, initialize, { immediate: true, flush: 'sync' });
watch(() => props.definition, value => receiveDefinition(value), { deep: true });
watch(() => props.visible, visible => { if (visible) { if (layoutDirty.value) scheduleLayoutSave(); else void loadLayout(); } else { addMenu.value?.handleClose(); ++layoutReadTicket; clearTimeout(layoutTimer); } });
watch(() => base.value.revision, () => { if (props.visible && !layoutDirty.value && !layoutSaving.value) void loadLayout(); });
watch([dirty, saving, codeFormValid], () => emit('dirty', hasUnsavedForm.value || saving.value), { immediate: true, flush: 'sync' });
watch([processVisible, selectedId], ([open, id]) => emit('process', open, id || undefined), { immediate: true });
watch(dirty, changed => { if (!changed && layoutDirty.value) scheduleLayoutSave(); });
watch(() => props.run?.steps?.filter(item => item.status === 'waiting_confirmation').map(item => item.step_id).join(','), () => {
  if (!props.run?.definition.graph) return;
  const first = props.run.steps?.find(item => item.status === 'waiting_confirmation');
  if (first && (selectedId.value === '__input' || !step.value)) focusStep(first.step_id);
});
watch(() => props.run?.status, status => { if (status === 'waiting_confirmation' && !props.run?.definition.graph) focusStep(props.run?.definition.steps[props.run.current_step || 0]?.id || ''); });
onMounted(() => {
  window.addEventListener('pogo:auth-changed', authenticationChanged);
  if (frame.value && typeof ResizeObserver !== 'undefined') {
    const update = () => { if (frame.value?.clientWidth && frame.value.clientHeight) frameSize.value = { width: frame.value.clientWidth, height: frame.value.clientHeight }; };
    update(); frameObserver = new ResizeObserver(update); frameObserver.observe(frame.value);
  }
});
onBeforeUnmount(() => { emit('process', false); frameObserver?.disconnect(); disposed = true; ++epoch; clearTimeout(layoutTimer); clearRecentUpdate(); for (const controller of controllers) controller.abort(); window.removeEventListener('pogo:auth-changed', authenticationChanged); });
defineExpose({ prepareClose, focusStep, closeInspector, save, state });
</script>

<template>
  <section v-if="validScope" ref="frame" class="aw-editor" :style="{ '--aw-top-inset': `${baseTopInset}px` }" data-testid="agent-workflow-editor" :data-workflow-id="definition.id" @keydown="keydown">
    <AgentWorkflowCanvas ref="canvas" :definition="displayed" :run="run" :positions="isHistory ? {} : positions" :viewport="isHistory ? undefined : viewport" :selected-id="selectedId" :updated-ids="updatedIds" :activities="activities" :editable="canEdit" :visible="visible" :insets="canvasInsets" @select="select" @insert="(afterId, edge) => add('agent', afterId, edge)" @duplicate="duplicate" @remove="remove" @reorder="reorder" @graph="graphChanged" @positions="positionsChanged" @viewport="viewportChanged" >
      <template #feedback>
        <details v-if="!isHistory && dirty && issues.length" class="aw-validation" open @wheel.stop @pointerdown.stop><summary>{{ tr('localValidation') }} · {{ issues.length }}</summary><ul><li v-for="(issue, index) in issues" :key="index"><button v-if="issue.stepId" type="button" @click="select(issue.stepId)">{{ draft.steps.find(item => item.id === issue.stepId)?.title || issue.stepId }}</button><span>{{ tr(`issues.${issue.code}`) }}</span></li></ul></details>
        <div v-if="!isHistory" class="aw-layout-status" @wheel.stop @pointerdown.stop><span role="status">{{ tr(layoutSaving ? 'layoutSaving' : dirty && layoutDirty ? 'layoutAfterDraft' : layoutDirty ? 'layoutUnsaved' : layoutError ? 'layoutUnavailable' : lastLayout ? 'layoutSaved' : 'layoutLoading') }}</span><button v-if="layoutError && !incomingLayout" type="button" :disabled="!canEdit || dirty || layoutSaving" @click="layoutDirty ? saveLayout() : loadLayout()">{{ tr('layoutRetry') }}</button><span v-if="layoutError && !incomingLayout" class="aw-error">{{ layoutError }}</span></div>
      </template>
    </AgentWorkflowCanvas>
    <div v-if="!isHistory || $slots['workflow-controls']" class="aw-floating-tools" :aria-label="tr('editingTools')" @wheel.stop @pointerdown.stop>
      <template v-if="!isHistory">
      <el-dropdown ref="addMenu" trigger="click" placement="right-start" :disabled="!canEdit" @command="addNodeAction">
        <button type="button" class="aw-add-button" :disabled="!canEdit || workflowNodeCount(draft) >= AGENT_WORKFLOW_MAX_STEPS" :aria-label="tr('addNode')" :title="tr('addNode')" data-testid="workflow-add-node">＋</button>
        <template #dropdown><el-dropdown-menu>
          <el-dropdown-item command="agent" :disabled="!canEdit || workflowNodeCount(draft) >= AGENT_WORKFLOW_MAX_STEPS" data-testid="workflow-add-agent">{{ tr('addAgent') }}</el-dropdown-item>
          <el-dropdown-item command="code" :disabled="!canEdit || workflowNodeCount(draft) >= AGENT_WORKFLOW_MAX_STEPS" data-testid="workflow-add-code">{{ tr('addCode') }}</el-dropdown-item>
          <el-dropdown-item command="iteration" :disabled="!canEdit || workflowNodeCount(draft)+2>AGENT_WORKFLOW_MAX_STEPS" data-testid="workflow-add-iteration">{{ tr('addIteration') }}</el-dropdown-item>
          <el-dropdown-item command="loop" :disabled="!canEdit || workflowNodeCount(draft)+2>AGENT_WORKFLOW_MAX_STEPS" data-testid="workflow-add-loop">{{ tr('addLoop') }}</el-dropdown-item>
          <el-dropdown-item command="confirm" :disabled="!canEdit || workflowNodeCount(draft) >= AGENT_WORKFLOW_MAX_STEPS" data-testid="workflow-add-confirm">{{ tr('addConfirm') }}</el-dropdown-item>
          <el-dropdown-item v-if="!draft.graph" command="upgrade" :disabled="!canEdit" divided data-testid="workflow-upgrade-graph">{{ tr('upgradeGraph') }}</el-dropdown-item>
          <template v-else><el-dropdown-item command="condition" :disabled="!canAddControlGroup" divided data-testid="workflow-add-condition">{{ tr('condition') }}</el-dropdown-item><el-dropdown-item command="parallel" :disabled="!canAddControlGroup" data-testid="workflow-add-parallel">{{ tr('parallel') }}</el-dropdown-item></template>
        </el-dropdown-menu></template>
      </el-dropdown>
      <span class="aw-tool-divider" />
      <button type="button" :disabled="!canEdit || historyIndex === 0" :aria-label="tr('undo')" :title="tr('undo')" @click="undo">↶</button>
      <button type="button" :disabled="!canEdit || historyIndex + 1 >= history.length" :aria-label="tr('redo')" :title="tr('redo')" @click="redo">↷</button>
      </template>
      <template v-if="$slots['workflow-controls']"><span v-if="!isHistory" class="aw-tool-divider" /><slot name="workflow-controls" /></template>
    </div>
    <div class="aw-messages" @wheel.stop @pointerdown.stop>
      <div v-if="recentUpdate && !isHistory" class="aw-sync-update aw-message-card" role="status" data-testid="workflow-live-update"><span>{{ tr(recentUpdate.count ? 'remoteNodesUpdated' : 'remoteSettingsUpdated', { revision: recentUpdate.revision, count: recentUpdate.count }) }}</span><button type="button" @click="focusRecentUpdate">{{ tr('locateUpdate') }}</button><button type="button" :aria-label="tr('dismissUpdate')" @click="clearRecentUpdate">×</button></div>
      <p v-if="error" class="aw-error aw-message-card" role="alert">{{ error }}</p>
      <p v-if="notice" class="aw-note aw-message-card" role="status">{{ notice }}</p>
      <div v-if="pendingRemote" class="aw-conflict" role="alert" data-testid="workflow-draft-conflict"><p>{{ tr('incoming') }}</p><div class="aw-actions"><button type="button" @click="comparisonOpen = true">{{ tr('compare') }}</button><button type="button" :disabled="!canEdit" @click="mergeIncoming">{{ tr('merge') }}</button><button type="button" :disabled="saving" @click="reloadLatest">{{ tr('reload') }}</button></div></div>
      <div v-if="incomingLayout" class="aw-conflict" role="alert"><p>{{ tr('layoutConflict') }}</p><div class="aw-actions"><button type="button" @click="acceptRemoteLayout">{{ tr('useRemoteLayout') }}</button><button type="button" :disabled="!canEdit || dirty || incomingLayout.definition_revision !== base.revision" @click="keepLocalLayout">{{ tr('useLocalLayout') }}</button></div></div>
      <span v-if="isHistory && !controlsOpen && !inspectorVisible" class="aw-history-badge" data-testid="workflow-history-readonly">{{ run ? tr('runReadOnly') : tr('versionReadOnly', { revision: version!.revision }) }}</span><p v-else-if="!isHistory && !editable && !controlsOpen" class="aw-note aw-message-card aw-readonly">{{ tr(definition.archived ? 'archived' : 'readOnly') }}</p>
    </div>
      <aside v-show="inspectorVisible" class="aw-inspector" :class="{ compact: compactInspector, 'showing-process': executionDetails && detailTab === 'process' }" :style="{ bottom: `${baseBottomInset + 12}px`, ...(compactInspector ? { height: `${inspectorHeight}px` } : {}) }" :aria-label="tr('properties')" data-testid="workflow-node-inspector" @wheel.stop @pointerdown.stop>
        <div class="aw-inspector-actions"><TaskContextRing v-if="showContextRing" :reading="contextReading || null" /><button type="button" class="aw-inspector-close" :aria-label="tr('closeInspector')" :title="tr('closeInspector')" @click="closeInspector">×</button></div>
        <template v-if="selectedId === '__input'">
          <header><small>{{ tr('workflowProperties') }}</small><h3>{{ tr('inputs') }}</h3></header>
          <label>{{ tr('flowName') }}<input :value="displayed.name" :disabled="!canEdit" data-testid="workflow-name-input" @input="setMeta('name', ($event.target as HTMLInputElement).value)" /></label>
          <label>{{ tr('flowDescription') }}<textarea :value="displayed.description || ''" :disabled="!canEdit" rows="3" @input="setMeta('description', ($event.target as HTMLTextAreaElement).value)" /></label>
          <section class="aw-tool-approval" data-testid="workflow-tool-approval-settings">
            <label>{{ tr('toolApprovalTitle') }}<select :value="workflowToolApprovalMode(displayed)" :disabled="!canEdit" :aria-label="tr('toolApprovalTitle')" data-testid="workflow-tool-approval-mode" @change="setToolApprovalMode(($event.target as HTMLSelectElement).value as AgentWorkflowToolApprovalMode)"><option v-for="mode in ['strict', 'standard', 'auto']" :key="mode" :value="mode">{{ t(`policyPicker.modes.${mode}.label`) }}</option></select></label>
            <p class="aw-hint">{{ tr(`toolApprovalDescriptions.${workflowToolApprovalMode(displayed)}`) }}</p>
            <p class="aw-hint">{{ tr('toolApprovalInheritance') }}</p>
            <p class="aw-hint">{{ tr(isHistory ? 'toolApprovalFrozen' : 'toolApprovalDraftHint') }}</p>
          </section>
          <p class="aw-hint">{{ tr('inputBoundary') }}</p>
          <div v-for="(input, index) in displayed.inputs || []" :key="index" class="aw-input-card">
            <label>{{ tr('inputKey') }}<input :value="input.key" :disabled="!canEdit" :aria-label="`${tr('inputKey')} ${index + 1}`" @input="setInput(index, 'key', ($event.target as HTMLInputElement).value)" /></label>
            <label>{{ tr('inputLabel') }}<input :value="input.label || ''" :disabled="!canEdit" @input="setInput(index, 'label', ($event.target as HTMLInputElement).value)" /></label>
            <label>{{ tr('inputType') }}<select :value="input.type || 'text'" :disabled="!canEdit" :aria-label="`${tr('inputType')} ${index + 1}`" data-testid="workflow-input-type" @change="setInputType(index, ($event.target as HTMLSelectElement).value as AgentWorkflowInputType)"><option v-for="type in ['text', 'boolean', 'select', 'number', 'integer', 'object', 'array']" :key="type" :value="type">{{ tr(`inputTypes.${type}`) }}</option></select></label>
            <p v-if="input.type === 'boolean'" class="aw-hint">{{ tr('inputBooleanHint') }}</p>
            <section v-if="input.type === 'select' || input.type === 'boolean' && input.options?.length" class="aw-input-options" :aria-label="tr('inputOptions')" data-testid="workflow-input-options"><strong>{{ tr('inputOptions') }}</strong><div v-for="(option, at) in input.options || []" :key="at" class="aw-option-row"><label>{{ tr('inputOptionValue') }}<input :value="option.value" :disabled="!canEdit || input.type === 'boolean'" :aria-label="`${tr('inputOptionValue')} ${index + 1}.${at + 1}`" @input="setInputOption(index, at, 'value', ($event.target as HTMLInputElement).value)" /></label><label>{{ tr('inputOptionLabel') }}<input :value="option.label || ''" :disabled="!canEdit" :aria-label="`${tr('inputOptionLabel')} ${index + 1}.${at + 1}`" @input="setInputOption(index, at, 'label', ($event.target as HTMLInputElement).value)" /></label><button v-if="input.type === 'select'" type="button" :disabled="!canEdit" :aria-label="`${tr('removeInputOption')} ${at + 1}`" @click="removeInputOption(index, at)">×</button></div><button v-if="!isHistory && input.type === 'select'" type="button" :disabled="!canEdit || (input.options?.length || 0) >= 64" @click="addInputOption(index)">{{ tr('addInputOption') }}</button></section>
            <label>{{ tr('inputDefault') }}<select v-if="input.type === 'boolean' || input.type === 'select'" :value="input.default || ''" :disabled="!canEdit" :aria-label="`${tr('inputDefault')} ${index + 1}`" data-testid="workflow-input-default" @change="setInput(index, 'default', ($event.target as HTMLSelectElement).value)"><option value="">{{ tr('noInputDefault') }}</option><template v-if="input.type === 'boolean'"><option value="yes">{{ input.options?.find(option => option.value === 'yes')?.label || tr('inputYes') }}</option><option value="no">{{ input.options?.find(option => option.value === 'no')?.label || tr('inputNo') }}</option></template><template v-else><option v-for="option in input.options || []" :key="option.value" :value="option.value">{{ option.label || option.value }}</option></template></select><input v-else :value="input.default || ''" :disabled="!canEdit" :aria-label="`${tr('inputDefault')} ${index + 1}`" data-testid="workflow-input-default" @input="setInput(index, 'default', ($event.target as HTMLInputElement).value)" /></label>
            <div class="aw-actions"><label class="aw-check"><input type="checkbox" :checked="input.required" :disabled="!canEdit" @change="setInput(index, 'required', ($event.target as HTMLInputElement).checked)" />{{ tr('inputRequired') }}</label><button type="button" :disabled="!canEdit" @click="removeInput(index)">{{ tr('removeInput') }}</button></div>
          </div>
          <button v-if="!isHistory" type="button" :disabled="!canEdit || (draft.inputs?.length || 0) >= 32" @click="addInput">＋ {{ tr('addInput') }}</button>
          <section v-if="run?.inputs" class="aw-result"><h4>{{ tr('runInputs') }}</h4><dl><template v-for="(value, key) in run.inputs" :key="key"><dt :title="String(key)">{{ runInputDisplay(key,value).label }}</dt><dd :title="value">{{ runInputDisplay(key,value).value }}</dd></template></dl></section>
        </template>
        <template v-else-if="selectedId === '__output'"><header><small>{{ tr('workflowProperties') }}</small><h3>{{ t('agentWorkflow.output') }}</h3></header><p class="aw-hint">{{ tr('outputBoundary') }}</p><section v-for="result in finalStructuredResults" :key="result.id" class="aw-final-output" data-testid="workflow-final-structured-output"><strong>{{ result.title }}</strong><pre>{{ JSON.stringify(result.value,null,2) }}</pre></section><div v-for="node in displayed.steps.filter(item => item.output_file)" :key="node.id" class="aw-input-card"><strong>{{ node.title || node.id }}</strong><code>{{ node.output_file }}</code><button type="button" @click="select(node.id)">{{ tr('properties') }}</button><button v-if="run?.steps?.find(item => item.step_id === node.id)?.artifact" type="button" @click="emit('artifact', node.id)">{{ tr('artifact') }}</button></div></template>
        <template v-else-if="step">
          <header><small>{{ tr(executionDetails ? 'executionDetails' : 'properties') }} · {{ stepIndex + 1 }}</small><h3>{{ step.title || step.id }}</h3><code v-if="!executionDetails || detailTab === 'configuration'" :title="tr('identity')">{{ step.id }}</code></header>
          <div v-if="executionDetails" ref="detailTabList" class="aw-detail-tabs" role="tablist" :aria-label="tr('nodeDetails')" @keydown="detailTabKey">
            <button v-for="item in detailTabs" :id="`${detailPanelId}-${item}`" :key="item" type="button" role="tab" :aria-selected="detailTab === item" :tabindex="detailTab === item ? 0 : -1" :aria-controls="detailPanelId" :data-testid="`workflow-node-tab-${item}`" @click="selectDetailTab(item)">{{ tr(`detailTabs.${item}`) }}</button>
          </div>
          <section v-if="executionDetails && detailTab === 'process'" :id="detailPanelId" class="aw-node-process" role="tabpanel" :aria-labelledby="`${detailPanelId}-process`" data-testid="workflow-node-process"><slot name="node-process" :step="step" :step-run="stepRun"><p class="aw-hint">{{ tr('processPending') }}</p></slot></section>
          <section v-else-if="executionDetails && detailTab === 'io'" :id="detailPanelId" class="aw-node-io" role="tabpanel" :aria-labelledby="`${detailPanelId}-io`" data-testid="workflow-node-io">
            <section><h4>{{ tr('runInputs') }}</h4><dl v-if="run?.inputs && Object.keys(run.inputs).length"><template v-for="(value, key) in run.inputs" :key="key"><dt :title="String(key)">{{ runInputDisplay(key,value).label }}</dt><dd :title="value">{{ runInputDisplay(key,value).value }}</dd></template></dl><p v-else class="aw-hint">{{ tr('noRunInputs') }}</p></section>
            <section class="aw-frozen-resources" data-testid="workflow-run-resources"><h4>{{ tr('runResources') }}</h4><p class="aw-hint">{{ tr(nodeResourceSnapshot ? 'runResourcesHint' : 'noRunResourceSnapshot') }}</p><template v-for="group in nodeResourceGroups" :key="group.field"><div v-if="group.rows.length" class="aw-frozen-resource-group"><strong>{{ tr(`nodeResourceGroups.${group.field}`) }}</strong><article v-for="item in group.rows" :key="item.id" class="aw-resource-record" :class="{ unavailable: item.missing }"><span>{{ item.title }}</span><code v-if="item.version" :title="item.version">{{ resourceVersion(item.version) }}</code><small v-if="item.missing">{{ tr('resourceSnapshotMissing') }}</small></article></div></template><div v-if="nodeResourceSnapshot?.files?.length" class="aw-frozen-resource-group"><strong>{{ tr('runResourceFiles') }}</strong><article v-for="file in nodeResourceSnapshot.files" :key="file.path" class="aw-resource-record"><span>{{ file.name }}</span><small>{{ file.path }} · {{ file.size }} B</small></article></div><p v-if="nodeResourceSnapshot && !nodeResourceGroups.some(group => group.rows.length) && !nodeResourceSnapshot.files?.length" class="aw-hint">{{ tr('noExplicitResources') }}</p></section>
            <section><h4>{{ tr('configuredTask') }}</h4><p class="aw-frozen-task">{{ step.task }}</p></section>
            <section class="aw-result"><h4>{{ tr('runResult') }}</h4><strong>{{ t(`agentWorkflow.status.${stepDisplayStatus}`) }}</strong><small v-if="duration">{{ tr('duration') }} · {{ duration }}</small><MarkdownView v-if="stepRun?.summary" :source="stepRun.summary" passive /><p v-else class="aw-hint">{{ tr('resultPending') }}</p><div v-if="stepRun?.feedback" class="aw-feedback"><strong>{{ tr('feedback') }}</strong><p>{{ stepRun.feedback }}</p></div><code v-if="stepRun?.artifact || step.output_file" class="aw-io-file">{{ stepRun?.artifact || step.output_file }}</code><button v-if="stepRun?.artifact" type="button" @click="emit('artifact', step.id)">{{ tr('artifact') }}</button></section>
          </section>
          <template v-else>
          <label>{{ tr('name') }}<input :value="step.title || ''" :disabled="!canEdit" data-testid="workflow-node-title" @input="setStep('title', ($event.target as HTMLInputElement).value)" /></label>
          <label v-if="basicStep">{{ tr('kind') }}<select :value="step.kind" :disabled="!canEdit" @change="setKind(($event.target as HTMLSelectElement).value as 'agent' | 'confirm')"><option value="agent">{{ tr('agent') }}</option><option value="confirm">{{ tr('confirm') }}</option></select></label><p v-else class="aw-control-kind">{{ tr(step.kind) }}</p>
          <p class="aw-hint">{{ tr(step.kind + 'Hint') }}</p>
          <p v-if="step.kind === 'agent' || step.kind === 'iteration' && step.iteration?.body.kind === 'agent'" class="aw-hint aw-approval-inherited" data-testid="workflow-node-inherited-approval">{{ tr('toolApprovalInheritedMode', { mode: t(`policyPicker.modes.${workflowToolApprovalMode(displayed)}.label`) }) }}</p>
          <label v-if="basicStep">{{ tr(step.kind === 'agent' ? 'task' : 'prompt') }}<textarea ref="instruction" :value="step.kind === 'agent' ? step.task || '' : step.prompt || ''" :disabled="!canEdit" rows="7" data-testid="workflow-node-instruction" @input="setStep(step.kind === 'agent' ? 'task' : 'prompt', ($event.target as HTMLTextAreaElement).value)" /></label>
          <AgentWorkflowCodeResult v-if="run && step.kind === 'code'" :record="stepRun" :status="stepDisplayStatus" />
          <AgentWorkflowCodeConfig v-if="step.kind === 'code' && step.code" :key="`${formEpoch}:${step.id}`" :model-value="step.code" :policy="step.policy" :disabled="!canEdit" :variables="variables" @update:model-value="setCode" @update:policy="setCodePolicy" @validity="codeFormValid = $event" />
          <AgentWorkflowRepeatResult v-if="run && ['iteration','loop'].includes(step.kind)" :key="`${run.id}:${step.id}`" :step="step" :record="stepRun" :run="run" :visible="visible && inspectorVisible" />
          <AgentWorkflowRepeatConfig v-if="['iteration','loop'].includes(step.kind)" :key="`${formEpoch}:${step.id}`" :model-value="step" :disabled="!canEdit" :variables="variables" :catalog="resourceCatalog" :snapshot="nodeResourceSnapshot" :historical="isHistory" :resources-loading="resourcesLoading" :resources-error="resourcesError" @update:model-value="setRepeat" @validity="codeFormValid=$event" @refresh-resources="emit('refreshResources')" />
          <template v-if="step.kind === 'condition'">
            <label>{{ tr('conditionSource') }}<select :value="step.condition?.source || ''" :disabled="!canEdit" data-testid="workflow-condition-source" @change="setCondition('source', ($event.target as HTMLSelectElement).value)"><option value="">{{ tr('conditionChooseSource') }}</option><option v-for="item in variables" :key="item.value" :value="item.value.replace(/^\{\{\s*|\s*\}\}$/g, '')">{{ item.label }}</option></select></label>
            <label>{{ tr('conditionOperator') }}<select :value="step.condition?.operator || 'equals'" :disabled="!canEdit" data-testid="workflow-condition-operator" @change="setCondition('operator', ($event.target as HTMLSelectElement).value)"><option v-for="operator in conditionOperators" :key="operator" :value="operator">{{ tr('operators.' + operator) }}</option></select></label>
            <label v-if="!['is_empty', 'not_empty'].includes(step.condition?.operator || '')">{{ tr('conditionValue') }}<input :value="step.condition?.value || ''" :disabled="!canEdit" data-testid="workflow-condition-value" @input="setCondition('value', ($event.target as HTMLInputElement).value)" /></label>
            <p v-if="stepRun?.branch" class="aw-note">{{ tr('selectedBranch', { branch: tr(stepRun.branch === 'true' ? 'trueBranch' : 'falseBranch') }) }}</p>
          </template>
          <div v-if="canEdit && basicStep" class="aw-variables"><label>{{ tr('variableSource') }}<select v-model="variable" :aria-label="tr('chooseVariable')"><option value="">{{ tr('chooseVariable') }}</option><option v-for="item in variables" :key="item.value" :value="item.value">{{ item.label }} — {{ item.value }}</option></select></label><button type="button" :disabled="!variable" @click="insertVariable">{{ tr('insertVariable') }}</button><small v-if="!variables.length">{{ tr('noVariables') }}</small></div>
          <label v-if="step.kind === 'agent'">{{ tr('output') }}<input :value="step.output_file || ''" :disabled="!canEdit" placeholder="report.md" data-testid="workflow-node-output" @input="setStep('output_file', ($event.target as HTMLInputElement).value)" /><small>{{ tr('outputHint') }}</small></label>
          <section v-if="step.kind === 'agent'" class="aw-resource-config" data-testid="workflow-node-resources"><header class="aw-resource-heading"><strong>{{ tr('nodeResources') }}</strong><button v-if="!isHistory" type="button" :disabled="resourcesLoading" @click="emit('refreshResources')">{{ tr(resourcesLoading ? 'resourcesLoading' : 'refreshResources') }}</button></header><p class="aw-hint">{{ tr('inheritAgentResources') }}</p><details :open="nodeResourceCount > 0 || isHistory"><summary>{{ tr('explicitNodeResources', { n: nodeResourceCount }) }}</summary><p class="aw-hint">{{ tr('nodeResourcesPermissionHint') }}</p><p v-if="!isHistory && resourcesError" class="aw-error" role="alert">{{ resourcesError }}</p><p v-else-if="!isHistory && resourcesLoading" class="aw-hint" role="status">{{ tr('resourcesLoading') }}</p><p v-else-if="!isHistory && !resourceCatalog" class="aw-hint">{{ tr('resourcesUnavailable') }}</p><p v-if="isHistory" class="aw-hint">{{ tr(run ? 'runResourcesHint' : 'versionResourceHint') }}</p><section v-for="group in nodeResourceGroups" :key="group.field" class="aw-resource-group" :data-testid="`workflow-node-resource-${group.field}`"><h4>{{ tr(`nodeResourceGroups.${group.field}`) }}</h4><div v-if="group.rows.length" class="aw-resource-choices"><label v-for="item in group.rows" :key="item.id" class="aw-resource-choice" :class="{ unavailable: !item.available && !item.pending, selected: item.selected }"><input type="checkbox" :checked="item.selected" :disabled="!canEdit || !item.selected && (!item.available || resourcesLoading || !!resourcesError || (step[group.field]?.length || 0) >= 32)" :aria-label="item.title" @change="toggleNodeResource(group.field, item.id, ($event.target as HTMLInputElement).checked)" /><span><strong>{{ item.title }}</strong><code v-if="item.version" :title="item.version">{{ resourceVersion(item.version) }}</code><small v-if="item.description">{{ item.description }}</small><small v-if="item.pending">{{ tr(resourcesLoading ? 'resourcesLoading' : 'resourcesUnavailable') }}</small><small v-else-if="!item.available" class="aw-resource-warning">{{ item.reason || tr(isHistory ? 'resourceSnapshotMissing' : item.missing ? 'resourceMissing' : 'resourceUnavailable') }}</small></span></label></div><p v-else class="aw-hint">{{ tr(isHistory ? 'noExplicitResources' : 'noAvailableResources') }}</p></section></details></section>
          <p v-if="displayed.graph && !basicStep && !['code','iteration','loop'].includes(step.kind) && !isHistory" class="aw-hint">{{ tr('controlRemovalHint') }}</p>
          <div v-if="!isHistory" class="aw-actions aw-node-actions"><template v-if="!displayed.graph"><button type="button" :disabled="!canEdit || stepIndex <= 0" @click="reorder({ stepId: step.id, index: stepIndex - 1 })">↑ {{ tr('moveUp') }}</button><button type="button" :disabled="!canEdit || stepIndex >= displayed.steps.length - 1" @click="reorder({ stepId: step.id, index: stepIndex + 1 })">↓ {{ tr('moveDown') }}</button></template><button type="button" :disabled="!canDuplicate" @click="duplicate(step.id)">{{ tr('duplicate') }}</button><button type="button" class="aw-danger" :disabled="!canEdit || draft.steps.length < 2" @click="remove(step.id)">{{ tr('remove') }}</button><button v-if="step.kind === 'code'" type="button" :disabled="!canEdit || dirty || !codeFormValid || issues.length > 0 || capabilities.run !== true" :title="dirty ? tr('codeTestSaveFirst') : ''" data-testid="workflow-test-code" @click="openCodeTest">{{ tr('codeTestNode') }}</button><button type="button" :disabled="!canEdit || hasUnsavedForm" :title="hasUnsavedForm ? tr('saveBeforeReference') : ''" @click="reference">{{ tr('reference') }}</button></div>
          <section v-if="run && !executionDetails" class="aw-result"><h4>{{ tr('runResult') }}</h4><strong>{{ t(`agentWorkflow.status.${stepDisplayStatus}`) }}</strong><small v-if="duration">{{ tr('duration') }} · {{ duration }}</small><MarkdownView v-if="stepRun?.summary" :source="stepRun.summary" passive /><p v-else class="aw-hint">{{ tr('resultPending') }}</p><div v-if="stepRun?.feedback" class="aw-feedback"><strong>{{ tr('feedback') }}</strong><p>{{ stepRun.feedback }}</p></div><button v-if="stepRun?.artifact" type="button" @click="emit('artifact', step.id)">{{ tr('artifact') }}</button><slot v-if="waiting" name="confirmation" :step="step" :step-run="stepRun" /></section>
          </template>
        </template>
        <p v-else class="aw-hint">{{ tr('chooseNode') }}</p>
      </aside>
    <el-dialog v-model="codeTestOpen" :title="tr('codeTestNode')" width="min(560px, 94vw)" :close-on-click-modal="false" append-to-body><p class="aw-hint">{{ tr('codeTestHint') }}</p><label class="aw-code-test-label">{{ tr('codeInputs') }}<textarea v-model="codeTestValue" rows="10" spellcheck="false" data-testid="workflow-code-test-inputs" /></label><p v-if="codeTestError" class="aw-error" role="alert">{{ codeTestError }}</p><template #footer><button type="button" @click="codeTestOpen = false">{{ tr('codeTestCancel') }}</button><button type="button" class="aw-primary" data-testid="workflow-code-test-start" @click="submitCodeTest">{{ tr('codeTestStart') }}</button></template></el-dialog>
    <el-dialog v-model="comparisonOpen" :title="tr('diffTitle')" width="min(920px, 94vw)" append-to-body :close-on-click-modal="false"><div class="aw-comparison"><section v-for="column in [{ title: tr('yours'), rows: localChanges }, { title: tr('theirs'), rows: remoteChanges }]" :key="column.title"><h3>{{ column.title }}</h3><p v-if="!column.rows.length">{{ tr('noChanges') }}</p><article v-for="(change, index) in column.rows" :key="index"><strong>{{ changeLabel(change) }}</strong><pre v-if="change.before" class="aw-before">{{ change.before }}</pre><pre v-if="change.after">{{ change.after }}</pre></article></section></div><template #footer><button type="button" :disabled="!canEdit || !pendingRemote" @click="mergeIncoming">{{ tr('merge') }}</button><button type="button" :disabled="!pendingRemote || saving" @click="reloadLatest">{{ tr('reload') }}</button><button type="button" @click="comparisonOpen = false">{{ tr('continueEditing') }}</button></template></el-dialog>
  </section>
  <p v-else class="aw-error" role="alert">{{ t('agentWorkflow.scopeChanged') }}</p>
</template>

<style scoped lang="scss">
.aw-editor { position: relative; width: 100%; height: 100%; min-width: 0; min-height: 0; overflow: hidden; color: var(--el-text-color-primary); background: var(--el-bg-color); }
.aw-editor > :deep(.aw-graph) { position: absolute; inset: 0; width: 100%; height: 100%; min-height: 0; border: 0; border-radius: 0; }
.aw-actions { display: flex; align-items: center; gap: 7px; flex-wrap: wrap; }
.aw-code-test-label { display: flex; flex-direction: column; gap: 8px; margin-top: 14px; font-size: 12px; }.aw-code-test-label textarea { width: 100%; box-sizing: border-box; padding: 10px; border: 1px solid var(--el-border-color); border-radius: 8px; background: var(--el-bg-color); color: var(--el-text-color-primary); font: 12px/1.65 ui-monospace,SFMono-Regular,Consolas,monospace; resize: vertical; }
.aw-editor button, .aw-comparison + button { padding: 6px 9px; font: inherit; font-size: 12px; border: 1px solid var(--el-border-color); border-radius: 7px; color: var(--el-text-color-primary); background: var(--el-bg-color); cursor: pointer; }
.aw-editor button:hover:not(:disabled) { border-color: var(--el-color-primary); color: var(--el-color-primary); } .aw-editor button:disabled { opacity: .48; cursor: not-allowed; }
.aw-editor button.aw-primary { background: var(--el-color-primary); border-color: var(--el-color-primary); color: var(--el-color-white, #fff); } .aw-editor button.aw-danger { color: var(--el-color-danger); }
.aw-inspector { position: absolute; z-index: 16; right: 16px; top: var(--aw-top-inset, 88px); width: 380px; max-width: calc(100% - 96px); padding: 16px; display: flex; flex-direction: column; gap: 13px; box-sizing: border-box; min-width: 0; min-height: 0; overflow: auto; overscroll-behavior: contain; border: 1px solid var(--el-border-color); border-radius: 12px; background: var(--el-bg-color-overlay); box-shadow: 0 8px 28px rgb(0 0 0 / 16%); }
.aw-inspector header small { font-size: 11px; color: var(--el-text-color-secondary); } .aw-inspector h3 { font-size: 15px; margin: 5px 0; overflow-wrap: anywhere; } .aw-inspector header code { font-size: 10px; color: var(--el-text-color-secondary); overflow-wrap: anywhere; }
.aw-inspector.showing-process { overflow: hidden; gap: 10px; }.aw-inspector.showing-process > header,.aw-detail-tabs { flex: none; }
.aw-detail-tabs { display: grid; grid-template-columns: repeat(3,minmax(0,1fr)); gap: 3px; padding: 3px; border-radius: 9px; background: var(--el-fill-color-light); }.aw-detail-tabs button { padding: 5px 7px; min-height: 30px; border: 0; border-radius: 6px; background: transparent; color: var(--el-text-color-secondary); font-size: 11px; }.aw-detail-tabs button[aria-selected=true] { background: var(--el-bg-color); color: var(--el-text-color-primary); font-weight: 600; box-shadow: 0 1px 4px #0001; }.aw-detail-tabs button:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 1px; }
.aw-node-process { display: flex; flex-direction: column; flex: 1; min-height: 0; min-width: 0; overflow: hidden; }.aw-node-io { display: flex; flex-direction: column; gap: 16px; min-width: 0; }.aw-node-io h4 { margin: 0 0 7px; font-size: 12px; font-weight: 600; }.aw-node-io dl { margin: 0; font-size: 11px; }.aw-node-io dt { font-weight: 600; color: var(--el-text-color-secondary); }.aw-node-io dd { margin: 4px 0 10px; white-space: pre-wrap; overflow-wrap: anywhere; }.aw-frozen-task { white-space: pre-wrap; overflow-wrap: anywhere; margin: 0; font-size: 12px; line-height: 1.7; color: var(--el-text-color-regular); }.aw-io-file { font-size: 11px; overflow-wrap: anywhere; color: var(--el-text-color-secondary); }
.aw-inspector label { display: flex; flex-direction: column; gap: 5px; font-size: 12px; font-weight: 600; } .aw-inspector input:not([type=checkbox]), .aw-inspector textarea, .aw-inspector select { box-sizing: border-box; width: 100%; min-width: 0; padding: 8px; font: inherit; font-size: 12px; font-weight: 400; border: 1px solid var(--el-border-color); border-radius: 7px; color: var(--el-text-color-primary); background: var(--el-bg-color); }
.aw-inspector textarea { resize: vertical; line-height: 1.6; } .aw-inspector input:focus, .aw-inspector textarea:focus, .aw-inspector select:focus { outline: 2px solid var(--el-color-primary-light-7); outline-offset: 1px; } .aw-inspector :disabled { cursor: default; opacity: .8; }
.aw-inspector .aw-check { flex-direction: row; align-items: center; font-weight: 400; } .aw-inspector small, .aw-hint { font-size: 11px; line-height: 1.6; color: var(--el-text-color-secondary); margin: 0; }
.aw-input-card { display: flex; flex-direction: column; gap: 8px; padding: 10px; border: 1px solid var(--el-border-color-lighter); border-radius: 8px; background: var(--el-bg-color); } .aw-input-card strong { font-size: 12px; } .aw-input-card code { font-size: 11px; overflow-wrap: anywhere; }
.aw-input-options { display: flex; flex-direction: column; gap: 8px; padding: 9px; border-radius: 7px; background: var(--el-fill-color-light); }.aw-input-options > button { align-self: flex-start; }.aw-option-row { display: grid; grid-template-columns: minmax(0,1fr) minmax(0,1fr) 25px; gap: 6px; align-items: end; }.aw-option-row > button { height: 32px; padding: 0; font-size: 16px; }
.aw-resource-config { display: flex; flex-direction: column; gap: 8px; padding-top: 12px; border-top: 1px solid var(--el-border-color-lighter); }.aw-resource-heading { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding-right: 0 !important; }.aw-resource-heading > strong { font-size: 12px; }.aw-resource-heading button { font-size: 11px; padding: 4px 7px; }.aw-resource-config details { min-width: 0; }.aw-resource-config summary { cursor: pointer; font-size: 12px; color: var(--el-text-color-regular); padding: 3px 0; }.aw-resource-config details > .aw-hint { margin-top: 7px; }.aw-resource-group { margin-top: 12px; }.aw-resource-group h4,.aw-frozen-resource-group > strong { margin: 0 0 7px; font-size: 11px; font-weight: 600; color: var(--el-text-color-secondary); }.aw-resource-choices { max-height: 220px; overflow: auto; display: flex; flex-direction: column; gap: 5px; overscroll-behavior: contain; }.aw-inspector label.aw-resource-choice { flex-direction: row; align-items: flex-start; gap: 8px; padding: 8px; border: 1px solid var(--el-border-color-lighter); border-radius: 8px; background: var(--el-bg-color); font-weight: 400; cursor: pointer; }.aw-resource-choice > input { flex: none; margin: 3px 0 0; }.aw-resource-choice > span { display: flex; flex-direction: column; gap: 3px; min-width: 0; }.aw-resource-choice strong { font-size: 12px; font-weight: 500; overflow-wrap: anywhere; }.aw-resource-choice code,.aw-resource-record code { font-size: 10px; color: var(--el-text-color-secondary); text-overflow: ellipsis; white-space: nowrap; overflow: hidden; max-width: 100%; }.aw-resource-choice.selected { border-color: var(--el-color-primary-light-5) !important; background: var(--el-color-primary-light-9) !important; }.aw-resource-choice.unavailable { border-style: dashed; }.aw-resource-warning { color: var(--el-color-warning) !important; }.aw-frozen-resources { display: flex; flex-direction: column; gap: 8px; }.aw-frozen-resources h4 { margin-bottom: 0; }.aw-frozen-resource-group { display: flex; flex-direction: column; gap: 5px; }.aw-resource-record { display: flex; flex-direction: column; gap: 3px; padding: 8px 10px; border: 1px solid var(--el-border-color-lighter); border-radius: 7px; background: var(--el-bg-color); font-size: 12px; min-width: 0; }.aw-resource-record > span { font-weight: 500; overflow-wrap: anywhere; }.aw-resource-record > small { overflow-wrap: anywhere; }.aw-resource-record.unavailable { border-style: dashed; color: var(--el-color-warning); }
.aw-final-output{font-size:12px;min-width:0}.aw-final-output pre{font:11px/1.65 ui-monospace,monospace;white-space:pre-wrap;overflow-wrap:anywhere;max-height:400px;overflow:auto;padding:10px;border-radius:7px;background:var(--el-fill-color-light)}
.aw-node-actions { border-top: 1px solid var(--el-border-color); padding-top: 12px; } .aw-variables { display: flex; flex-direction: column; gap: 6px; } .aw-variables button { align-self: flex-start; }
.aw-note, .aw-error, .aw-conflict { font-size: 12px; line-height: 1.6; margin: 5px 0; overflow-wrap: anywhere; } .aw-note { color: var(--el-text-color-secondary); } .aw-error { color: var(--el-color-danger); } .aw-conflict { padding: 10px 12px; background: var(--el-color-warning-light-9); border: 1px solid var(--el-color-warning-light-5); border-radius: 9px; } .aw-conflict p { margin: 0 0 7px; }
.aw-layout-status { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; padding: 7px 10px; font-size: 10px; color: var(--el-text-color-secondary); border-top: 1px solid var(--el-border-color-lighter); }
.aw-result { display: flex; flex-direction: column; gap: 9px; padding-top: 10px; border-top: 1px solid var(--el-border-color); font-size: 12px; overflow-wrap: anywhere; } .aw-result h4 { margin: 0; } .aw-result dl { margin: 0; } .aw-result dt { font-weight: 600; } .aw-result dd { margin: 4px 0 12px; white-space: pre-wrap; } .aw-feedback { padding: 9px; border-radius: 7px; background: var(--el-fill-color-light); } .aw-feedback p { white-space: pre-wrap; margin: 5px 0 0; }
.aw-validation { padding: 9px 12px; margin-top: 8px; font-size: 12px; color: var(--el-color-danger); border: 1px solid var(--el-color-danger-light-7); border-radius: 8px; } .aw-validation ul { margin: 8px 0 0; padding-left: 18px; } .aw-validation li { margin: 5px 0; } .aw-validation button { margin-right: 6px; } .aw-footer { padding-top: 8px; }
.aw-comparison { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; } .aw-comparison section { min-width: 0; } .aw-comparison h3 { margin: 0 0 10px; font-size: 14px; } .aw-comparison article { border: 1px solid var(--el-border-color); border-radius: 8px; padding: 10px; margin-bottom: 10px; font-size: 12px; } .aw-comparison pre { white-space: pre-wrap; overflow-wrap: anywhere; font: inherit; background: var(--el-fill-color-light); padding: 7px; border-radius: 4px; } .aw-comparison .aw-before { color: var(--el-text-color-secondary); text-decoration: line-through; }
@media (max-width: 600px) { .aw-comparison { grid-template-columns: 1fr; } }

.aw-floating-tools { position: absolute; z-index: 26; top: calc(var(--aw-top-inset, 88px) + 6px); left: 16px; display: flex; flex-direction: column; align-items: center; gap: 5px; padding: 5px; border: 1px solid var(--el-border-color); border-radius: 10px; background: var(--el-bg-color-overlay); box-shadow: 0 4px 16px rgb(0 0 0 / 12%); }
.aw-floating-tools > button, .aw-floating-tools .aw-add-button { display: grid; place-items: center; width: 32px; height: 32px; padding: 0; border-color: transparent; font-size: 18px; }
.aw-floating-tools .aw-add-button { color: var(--el-color-primary); }
.aw-tool-divider { width: 24px; height: 1px; background: var(--el-border-color-lighter); margin: 2px 0; }
.aw-messages { position: absolute; z-index: 20; top: calc(var(--aw-top-inset, 88px) - 2px); left: 76px; right: 16px; max-width: 540px; max-height: calc(100% - 172px); overflow: auto; pointer-events: none; display: flex; flex-direction: column; gap: 8px; }
.aw-messages > * { pointer-events: auto; }
.aw-message-card { padding: 8px 11px; border: 1px solid var(--el-border-color); border-radius: 8px; background: var(--el-bg-color-overlay); box-shadow: 0 3px 12px rgb(0 0 0 / 10%); }
.aw-messages .aw-readonly { font-size: 11px; }.aw-history-badge{align-self:flex-start;padding:4px 8px;border:1px solid var(--el-border-color-lighter);border-radius:6px;background:var(--el-bg-color-overlay);font-size:10px;color:var(--el-text-color-secondary)}
.aw-sync-update { display: flex; align-items: center; gap: 8px; border-color: var(--el-color-success-light-5); color: var(--el-color-success); }
.aw-sync-update span { flex: 1; } .aw-sync-update button { flex: none; }
.aw-inspector.compact { top: auto; left: 76px; width: auto; max-width: none; }
.aw-inspector-actions{position:sticky;top:0;z-index:2;display:flex;align-items:center;align-self:flex-end;gap:6px;min-height:28px;margin-bottom:-28px;background:var(--el-bg-color-overlay)}.aw-inspector .aw-inspector-close{width:26px;height:26px;padding:0;flex:none;font-size:18px}.aw-tool-approval{display:flex;flex-direction:column;gap:7px;padding:11px;border:1px solid var(--el-border-color-lighter);border-radius:8px;background:var(--el-fill-color-extra-light)}
.aw-inspector header { padding-right: 70px; }
.aw-layout-status{pointer-events:auto;align-self:flex-start;max-width:100%;box-sizing:border-box;border:1px solid var(--el-border-color-lighter);border-radius:7px;background:var(--el-bg-color-overlay)}
.aw-validation{pointer-events:auto;width:100%;max-height:170px;box-sizing:border-box;overflow:auto;margin:0;background:var(--el-bg-color-overlay);box-shadow:0 4px 16px rgb(0 0 0 / 12%)}
.aw-validation summary { cursor: pointer; }
</style>
