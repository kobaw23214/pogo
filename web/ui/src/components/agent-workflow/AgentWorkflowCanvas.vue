<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from "vue";
import { BaseEdge, EdgeLabelRenderer, MarkerType, Position, VueFlow, getSmoothStepPath, useVueFlow, type Connection } from "@vue-flow/core";
import { Background } from "@vue-flow/background";
import { MiniMap } from "@vue-flow/minimap";
import { useI18n } from "vue-i18n";
import type { AgentWorkflowDAG, AgentWorkflowDefinition, AgentWorkflowEdge, AgentWorkflowRun } from "@/api/agentWorkflows";
import type { AgentWorkflowNodeActivity } from '@/agent/agentWorkflowActivity';
import { connectWorkflowGraph, disconnectWorkflowGraphEdge, outgoingWorkflowEdges, workflowEdges, workflowEdgeHandle, workflowEdgeKey } from '@/agent/agentWorkflowDag';
import { AGENT_WORKFLOW_INPUT, agentWorkflowAfterId, agentWorkflowNodeIds, buildAgentWorkflowGraph, layoutAgentWorkflow, mergeAgentWorkflowPositions, validWorkflowPosition, validWorkflowViewport, workflowCanvasBoundsViewport, workflowCanvasVisibleRect, type AgentWorkflowCanvasInsets, type AgentWorkflowCanvasRect, type AgentWorkflowPositions, type AgentWorkflowViewport } from "@/agent/agentWorkflowGraph";
import { workflowNodeCount } from '@/agent/agentWorkflowRepeat';
import { AGENT_WORKFLOW_MAX_STEPS } from '@/agent/agentWorkflowLimits';
import AgentWorkflowCanvasNode from "./AgentWorkflowCanvasNode.vue";
import "@vue-flow/core/dist/style.css";
import "@vue-flow/core/dist/theme-default.css";
import "@vue-flow/minimap/dist/style.css";

const props = withDefaults(defineProps<{
  definition: AgentWorkflowDefinition;
  run?: AgentWorkflowRun | null;
  positions?: AgentWorkflowPositions;
  viewport?: AgentWorkflowViewport;
  selectedId?: string;
  editable?: boolean;
  visible?: boolean;
  insets?: AgentWorkflowCanvasInsets;
  updatedIds?: string[];
  activities?: Record<string, AgentWorkflowNodeActivity>;
}>(), { run: null, positions: () => ({}), selectedId: "", editable: false, visible: true });
const emit = defineEmits<{
  select: [id: string]; insert: [afterId: string | null, edge?: AgentWorkflowEdge]; duplicate: [id: string]; remove: [id: string]; graph: [AgentWorkflowDAG];
  reorder: [move: { stepId: string; index: number }]; positions: [positions: AgentWorkflowPositions]; viewport: [viewport: AgentWorkflowViewport];
}>();
const { t, locale } = useI18n();
const canvasId = "agent-workflow-canvas-" + useId();
const { onConnect, onNodeClick, onEdgeClick, onPaneClick, onNodeDragStart, onNodeDragStop, onNodesInitialized, onMoveEnd, findNode, setViewport, getViewport } = useVueFlow(canvasId);
const displayed = computed(() => props.run?.definition ?? props.definition);
const canEdit = computed(() => props.editable && !props.run && !props.definition.archived && !props.definition.deleted);
const canAdd = computed(() => canEdit.value && workflowNodeCount(displayed.value) < AGENT_WORKFLOW_MAX_STEPS);
const frame = ref<HTMLElement | null>(null);
const frameSize = ref({ width: 1, height: 1 }), viewZoom = ref(1);
const safeInsets = computed(() => ({ top: 88, left: 72, right: 16, bottom: 64, ...props.insets }));
const visibleArea = computed(() => workflowCanvasVisibleRect(frameSize.value.width, frameSize.value.height, safeInsets.value));
const viewControlsStyle = computed(() => ({
  "--aw-view-top": Math.max(12, visibleArea.value.y + 6) + "px",
  "--aw-view-right": Math.max(12, frameSize.value.width - visibleArea.value.x - visibleArea.value.width + 12) + "px",
  "--aw-view-bottom": Math.max(12, frameSize.value.height - visibleArea.value.y - visibleArea.value.height + 12) + "px",
}));
const showMiniMap = ref(false), notice = ref("");
const selectedConnectionKey = ref('');
const selectedConnection = computed(() => canEdit.value ? workflowEdges(displayed.value).find(edge => workflowEdgeKey(edge) === selectedConnectionKey.value) : undefined);
// Vue Flow augments these nodes with measurement/interaction state. The pure
// builder enforces our payload type without recursive VueFlow generic expansion.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const flowNodes = ref<any[]>([]);
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const flowEdges = ref<any[]>([]);
let localPositions: AgentWorkflowPositions = {};
let dragging = false, initialViewportApplied = false, userViewport = false, applyingViewport = false, pendingViewport = false, disposed = false;
let observer: ResizeObserver | undefined, resizeTimer: ReturnType<typeof setTimeout> | undefined;
const viewKey = computed(() => [props.definition.agent_id || "", props.definition.id, props.run?.id || "draft"].join(":"));

function color(status: string) {
  if (status === "completed") return "var(--el-color-success)";
  if (status === "running") return "var(--el-color-primary)";
  if (status === "waiting_confirmation" || status === "waiting") return "var(--el-color-warning)";
  if (status === "failed" || status === "interrupted") return "var(--el-color-danger)";
  return "var(--el-border-color-darker)";
}
function syncGraph() {
  if (dragging || disposed) return;
  // A conversation may add or reorder nodes while this canvas is open. Keep
  // the rendered coordinates of existing nodes; only new nodes need placement.
  const retained = props.run ? {} : Object.fromEntries(flowNodes.value.map(node => [node.id, node.position]));
  const graph = buildAgentWorkflowGraph({ definition: props.definition, run: props.run, positions: { ...retained, ...props.positions, ...localPositions }, selectedId: props.selectedId, updatedIds: props.updatedIds, activities: props.activities, editable: canEdit.value });
  flowNodes.value = graph.nodes;
  flowEdges.value = graph.edges.map(edge => ({ ...edge, markerEnd: { type: MarkerType.ArrowClosed, color: color(edge.data.status) }, style: { stroke: color(edge.data.status), strokeWidth: 1.8, ...(edge.data.status === 'skipped' ? { strokeDasharray: '5 4', opacity: .55 } : {}) } }));
}
function rendered() { return !disposed && props.visible && !!frame.value?.clientWidth && !!frame.value?.clientHeight; }
function nodeBounds(id: string) {
  const node = findNode(id);
  if (!node) return undefined;
  return { x: node.position.x, y: node.position.y, width: node.dimensions.width || 248, height: node.dimensions.height || 168 };
}
function measureFrame() {
  if (frame.value) frameSize.value = { width: frame.value.clientWidth, height: frame.value.clientHeight };
}
function currentVisibleArea() {
  const element = frame.value;
  return workflowCanvasVisibleRect(element?.clientWidth || 1, element?.clientHeight || 1, safeInsets.value);
}
function readableViewport(id: string, center = false): AgentWorkflowViewport | undefined {
  const bounds = nodeBounds(id);
  if (!bounds || !frame.value) return undefined;
  return workflowCanvasBoundsViewport(bounds, currentVisibleArea(), { padding: 24, maxZoom: 0.85, alignStart: !center });
}
function graphBounds(): AgentWorkflowCanvasRect | undefined {
  const bounds = flowNodes.value.map(node => nodeBounds(node.id)).filter((value): value is AgentWorkflowCanvasRect => !!value);
  if (!bounds.length) return undefined;
  const x = Math.min(...bounds.map(value => value.x)), y = Math.min(...bounds.map(value => value.y));
  return { x, y, width: Math.max(...bounds.map(value => value.x + value.width)) - x, height: Math.max(...bounds.map(value => value.y + value.height)) - y };
}
function syncZoom() { viewZoom.value = getViewport().zoom; }
async function focusNode(id: string, force = true) {
  await nextTick();
  if (!rendered() || !id) return;
  const bounds = nodeBounds(id), area = currentVisibleArea(), current = getViewport();
  if (!bounds) return;
  const left = bounds.x * current.zoom + current.x, top = bounds.y * current.zoom + current.y;
  const visible = left >= area.x + 12 && top >= area.y + 12 && left + bounds.width * current.zoom <= area.x + area.width - 12 && top + bounds.height * current.zoom <= area.y + area.height - 12;
  if (!force && visible && current.zoom >= 0.65) return;
  const next = readableViewport(id, true);
  if (!next) return;
  userViewport = true; applyingViewport = true;
  try { await setViewport(next, { duration: 150 }); initialViewportApplied = true; }
  finally { applyingViewport = false; syncZoom(); }
  emitViewport();
}
async function initialView() {
  await nextTick();
  if (!rendered() || userViewport || applyingViewport || initialViewportApplied) return;
  applyingViewport = true;
  try {
    if (!props.run && validWorkflowViewport(props.viewport)) {
      if (!initialViewportApplied) await setViewport({ ...props.viewport });
    } else {
      const selected = props.selectedId && props.selectedId !== AGENT_WORKFLOW_INPUT ? props.selectedId : AGENT_WORKFLOW_INPUT;
      const next = readableViewport(selected, selected !== AGENT_WORKFLOW_INPUT);
      if (!next) return;
      await setViewport(next);
    }
    initialViewportApplied = true;
  } finally { applyingViewport = false; syncZoom(); }
}
function scheduleInitialView() {
  clearTimeout(resizeTimer);
  if (userViewport) return;
  resizeTimer = setTimeout(() => void initialView(), 80);
}
async function applyPropViewport() {
  if (props.run || !validWorkflowViewport(props.viewport)) return;
  if (!rendered()) { pendingViewport = true; return; }
  pendingViewport = false;
  const next = { ...props.viewport }, current = getViewport();
  // Parent echoes our own gestures and can also explicitly accept a remote
  // layout. Apply the latter even after panning, without saving it back again.
  if (Math.abs(next.x - current.x) < 0.01 && Math.abs(next.y - current.y) < 0.01 && Math.abs(next.zoom - current.zoom) < 0.0001) return;
  applyingViewport = true;
  try { await setViewport(next); initialViewportApplied = true; }
  finally { applyingViewport = false; syncZoom(); }
}
function emitViewport() {
  if (!canEdit.value || !rendered()) return;
  const value = getViewport();
  if (validWorkflowViewport(value)) emit("viewport", { x: value.x, y: value.y, zoom: value.zoom });
}
async function changeView(action: "fit" | "in" | "out") {
  if (!rendered()) return;
  userViewport = true; applyingViewport = true;
  try {
    const area = currentVisibleArea();
    if (action === "fit") {
      const bounds = graphBounds();
      if (bounds) await setViewport(workflowCanvasBoundsViewport(bounds, area), { duration: 150 });
    } else {
      const current = getViewport(), zoom = Math.max(0.1, Math.min(2, current.zoom * (action === "in" ? 1.2 : 1 / 1.2)));
      const x = area.x + area.width / 2, y = area.y + area.height / 2;
      await setViewport({ x: x - (x - current.x) / current.zoom * zoom, y: y - (y - current.y) / current.zoom * zoom, zoom }, { duration: 120 });
    }
  } finally { applyingViewport = false; syncZoom(); }
  emitViewport();
}
async function arrange() {
  if (!canEdit.value) return;
  localPositions = layoutAgentWorkflow(displayed.value);
  syncGraph(); emit("positions", { ...localPositions });
  await nextTick(); await changeView("fit");
}
function insertAfter(id: string) {
  if (!canAdd.value) return;
  if (displayed.value.graph && outgoingWorkflowEdges(displayed.value, id).length > 1) { notice.value = t('agentWorkflowGraph.chooseBranch'); return; }
  const after = agentWorkflowAfterId(displayed.value, id);
  if (after !== undefined) emit("insert", after);
}
function insertOnEdge(source: string, target: string, sourceHandle?: string | null) {
  if (!canAdd.value) return;
  const edge = workflowEdges(displayed.value).find(item => item.source === source && item.target === target && workflowEdgeHandle(displayed.value, item) === (sourceHandle || 'out'));
  if (edge) emit('insert', source === AGENT_WORKFLOW_INPUT ? null : source, displayed.value.graph ? { ...edge } : undefined);
}
function validConnection(connection: Connection) {
  // Vue Flow also checks supplied edges. Keep frozen history edges valid while
  // granting mutation only to an editable draft.
  const existing = workflowEdges(displayed.value).some(edge => edge.source === connection.source && edge.target === connection.target && workflowEdgeHandle(displayed.value, edge) === (connection.sourceHandle || 'out'));
  return existing || canEdit.value && !!connectWorkflowGraph(displayed.value, connection.source, connection.target, connection.sourceHandle);
}
function chainPath(edge: { sourceX: number; sourceY: number; targetX: number; targetY: number; sourcePosition?: Position; targetPosition?: Position }) {
  return getSmoothStepPath({ sourceX: edge.sourceX, sourceY: edge.sourceY, targetX: edge.targetX, targetY: edge.targetY, sourcePosition: edge.sourcePosition ?? Position.Right, targetPosition: edge.targetPosition ?? Position.Left, borderRadius: 12, offset: 24 });
}
function edgeLabel(source: string) { return source === AGENT_WORKFLOW_INPUT ? t("agentWorkflowGraph.input") : source === '__output' ? t('agentWorkflowGraph.output') : displayed.value.steps.find(step => step.id === source)?.title || source; }
function selectConnection(source: string, target: string, branch?: 'true' | 'false') {
  if (!canEdit.value) return;
  selectedConnectionKey.value = workflowEdgeKey({ source, target, ...(branch ? { branch } : {}) }); notice.value = ''; emit('select', '');
  frame.value?.parentElement?.focus({ preventScroll: true });
}
function removeConnection() {
  const edge = selectedConnection.value;
  if (!edge || !canEdit.value) return;
  const graph = disconnectWorkflowGraphEdge(displayed.value, edge);
  if (graph) { emit('graph', graph); selectedConnectionKey.value = ''; notice.value = t('agentWorkflowGraph.connectionRemoved'); }
}
function connectionKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') { if (selectedConnectionKey.value) { selectedConnectionKey.value = ''; event.preventDefault(); event.stopPropagation(); } return; }
  const target = event.target as HTMLElement | null;
  if (!selectedConnection.value || !canEdit.value || !['Delete', 'Backspace'].includes(event.key) || target?.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target?.tagName || '')) return;
  event.preventDefault(); event.stopPropagation(); removeConnection();
}

onNodeClick(({ node }) => { selectedConnectionKey.value = ''; notice.value = ""; emit("select", node.id); });
onEdgeClick(({ edge }) => { selectConnection(edge.source, edge.target, edge.data?.branch); });
onPaneClick(() => { selectedConnectionKey.value = ''; notice.value = ""; emit("select", ""); });
onConnect(connection => {
  if (!canEdit.value) return;
  const graph = connectWorkflowGraph(displayed.value, connection.source, connection.target, connection.sourceHandle);
  if (graph) { emit('graph', graph); selectedConnectionKey.value = ''; notice.value = t('agentWorkflowGraph.connectionAdded'); }
  else notice.value = t('agentWorkflowGraph.invalidConnection');
});
onNodeDragStart(() => { dragging = true; userViewport = true; });
onNodeDragStop(() => {
  dragging = false;
  if (!canEdit.value) { syncGraph(); return; }
  const ids = new Set(agentWorkflowNodeIds(displayed.value));
  const positions: AgentWorkflowPositions = {};
  for (const node of flowNodes.value) if (ids.has(node.id) && validWorkflowPosition(node.position)) positions[node.id] = { x: node.position.x, y: node.position.y };
  localPositions = mergeAgentWorkflowPositions(displayed.value, positions);
  emit("positions", { ...localPositions });
  syncGraph();
});
onNodesInitialized(() => { if (!initialViewportApplied) scheduleInitialView(); });
onMoveEnd(({ event }) => {
  if (!rendered()) return;
  syncZoom();
  if (applyingViewport) return;
  const source = event && "sourceEvent" in event ? event.sourceEvent : event;
  if (!source) return;
  userViewport = true; emitViewport();
});

watch(viewKey, () => {
  localPositions = {}; flowNodes.value = []; selectedConnectionKey.value = ''; dragging = false; initialViewportApplied = false; userViewport = false; pendingViewport = false; notice.value = "";
  syncGraph(); scheduleInitialView();
});
watch([displayed, () => props.run?.steps, () => props.run?.status, () => props.selectedId, () => props.updatedIds, () => props.activities, canEdit, locale], syncGraph, { deep: true, immediate: true });
watch(() => props.positions, () => {
  if (dragging) return;
  localPositions = {}; syncGraph();
  if (!userViewport && !validWorkflowViewport(props.viewport)) { initialViewportApplied = false; scheduleInitialView(); }
}, { deep: true });
watch(() => props.viewport, async () => { await nextTick(); await applyPropViewport(); }, { deep: true });
watch(() => props.selectedId, id => { if (initialViewportApplied && id) void focusNode(id, false); });
watch(() => props.visible, async visible => { if (!visible) return; syncGraph(); await nextTick(); if (pendingViewport) await applyPropViewport(); scheduleInitialView(); });
onMounted(() => {
  measureFrame();
  if (frame.value && typeof ResizeObserver !== "undefined") {
    observer = new ResizeObserver(() => {
      measureFrame();
      if (!userViewport && (props.run || !validWorkflowViewport(props.viewport))) initialViewportApplied = false;
      scheduleInitialView();
    });
    observer.observe(frame.value);
  }
  scheduleInitialView();
});
onBeforeUnmount(() => {
  disposed = true; clearTimeout(resizeTimer); observer?.disconnect();
});
defineExpose({ fit: () => changeView("fit"), focus: (id: string) => focusNode(id), arrange });
</script>

<template>
  <section class="aw-graph" :style="viewControlsStyle" tabindex="0" :aria-label="t('agentWorkflowGraph.canvas')" data-testid="agent-workflow-canvas" @keydown="connectionKeydown">
    <div ref="frame" class="aw-graph-frame">
      <VueFlow :id="canvasId" v-model:nodes="flowNodes" v-model:edges="flowEdges" :delete-key-code="null" :nodes-draggable="canEdit" :nodes-connectable="canEdit" :edges-updatable="false" :is-valid-connection="validConnection" :min-zoom="0.1" :max-zoom="2" class="aw-flow">
        <Background :gap="22" :size="1.1" pattern-color="var(--el-border-color)" />
        <MiniMap v-if="showMiniMap" pannable zoomable node-color="var(--el-fill-color-dark)" node-stroke-color="var(--el-text-color-secondary)" mask-color="var(--el-fill-color-light)" />
        <template #node-procedure="node"><AgentWorkflowCanvasNode :data="node.data" @insert="insertAfter" @inspect="id => emit('select', id)" /></template>
        <template #edge-sequence="edge">
          <BaseEdge :id="edge.id" :path="chainPath(edge)[0]" :marker-end="edge.markerEnd" :style="selectedConnection && selectedConnection.source === edge.source && selectedConnection.target === edge.target && selectedConnection.branch === edge.data?.branch ? { ...edge.style, stroke: 'var(--el-color-primary)', strokeWidth: 3 } : edge.style" />
          <EdgeLabelRenderer v-if="canAdd || edge.data?.branch"><div class="aw-edge-label nodrag nopan" :style="{ transform: 'translate(-50%, -50%) translate(' + chainPath(edge)[1] + 'px, ' + chainPath(edge)[2] + 'px)' }"><span v-if="edge.data?.branch">{{ t('agentWorkflowGraph.branch_' + edge.data.branch) }}</span><button v-if="canAdd" type="button" class="aw-edge-insert" :aria-label="edge.data?.branch ? t('agentWorkflowGraph.insertBranch', { name: edgeLabel(edge.source), branch: t('agentWorkflowGraph.branch_' + edge.data.branch) }) : t('agentWorkflowGraph.insertAfter', { name: edgeLabel(edge.source) })" @click.stop="insertOnEdge(edge.source, edge.target, edge.sourceHandleId)">＋</button></div></EdgeLabelRenderer>
        </template>
      </VueFlow>
    </div>
    <div class="aw-view-controls" role="group" :aria-label="t('agentWorkflowGraph.viewControls')" @pointerdown.stop @wheel.stop>
      <button type="button" @click="changeView('fit')">{{ t('agentWorkflowGraph.fit') }}</button>
      <span class="aw-view-divider" aria-hidden="true" />
      <button type="button" class="icon" :aria-label="t('agentWorkflowGraph.zoomOut')" @click="changeView('out')">−</button>
      <span class="aw-view-zoom">{{ Math.round(viewZoom * 100) }}%</span>
      <button type="button" class="icon" :aria-label="t('agentWorkflowGraph.zoomIn')" @click="changeView('in')">＋</button>
      <span class="aw-view-divider" aria-hidden="true" />
      <button type="button" :aria-pressed="showMiniMap" @click="showMiniMap = !showMiniMap">{{ t('agentWorkflowGraph.miniMap') }}</button>
      <small class="aw-graph-count">{{ t('agentWorkflowGraph.stepCount', { count: workflowNodeCount(displayed) }) }}</small>
    </div>
    <div class="aw-canvas-feedback" data-testid="workflow-canvas-feedback" @pointerdown.stop @wheel.stop>
      <slot name="feedback" />
      <div v-if="selectedConnection" class="aw-connection-toolbar" role="group" :aria-label="t('agentWorkflowGraph.connectionActions')" data-testid="workflow-connection-actions" @pointerdown.stop @wheel.stop><span>{{ edgeLabel(selectedConnection.source) }} → {{ edgeLabel(selectedConnection.target) }}<small v-if="selectedConnection.branch"> · {{ t('agentWorkflowGraph.branch_' + selectedConnection.branch) }}</small></span><button type="button" class="aw-disconnect" @click="removeConnection">{{ t('agentWorkflowGraph.removeConnection') }}</button><button type="button" :aria-label="t('agentWorkflowGraph.closeConnectionActions')" @click="selectedConnectionKey = ''">×</button></div>
      <p v-if="notice" class="aw-graph-hint" role="status">{{ notice }}</p>
      <p v-else-if="canEdit && !selectedConnection" class="aw-graph-hint aw-connection-help">{{ t('agentWorkflowGraph.graphHint') }}</p>
    </div>
  </section>
</template>

<style scoped>
.aw-graph { position: absolute; inset: 0; min-width: 0; min-height: 0; overflow: hidden; background: var(--el-fill-color-extra-light); }
.aw-graph-frame { position: absolute; inset: 0; min-width: 0; min-height: 0; }
.aw-flow { width: 100%; height: 100%; min-height: 0; }
.aw-view-controls { position: absolute; z-index: 4; right: var(--aw-view-right); top: var(--aw-view-top); display: flex; align-items: center; gap: 4px; max-width: calc(100% - 24px); padding: 6px; border: 1px solid var(--el-border-color-light); border-radius: 10px; background: var(--el-bg-color); color: var(--el-text-color-primary); box-shadow: 0 5px 22px rgb(0 0 0 / 10%); pointer-events: auto; }
.aw-view-controls button { appearance: none; border: 0; border-radius: 6px; padding: 5px 8px; background: transparent; color: var(--el-text-color-regular); font: inherit; font-size: 11px; white-space: nowrap; cursor: pointer; }
.aw-view-controls button:hover:not(:disabled), .aw-view-controls button[aria-pressed="true"] { color: var(--el-color-primary); background: var(--el-color-primary-light-9); }
.aw-view-controls button:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
.aw-view-controls button:disabled { opacity: .42; cursor: not-allowed; }
.aw-view-controls button.icon { min-width: 25px; padding-inline: 4px; font-size: 15px; }
.aw-view-divider { width: 1px; height: 17px; background: var(--el-border-color-lighter); }
.aw-view-zoom { min-width: 36px; text-align: center; color: var(--el-text-color-secondary); font-size: 10px; font-variant-numeric: tabular-nums; }
.aw-graph-count { color: var(--el-text-color-secondary); font-size: 10px; margin-inline: 4px; white-space: nowrap; }
.aw-canvas-feedback{position:absolute;z-index:14;left:76px;bottom:var(--aw-view-bottom);display:flex;flex-direction:column;align-items:flex-start;gap:7px;max-width:min(540px,calc(100% - 100px));max-height:45%;overflow:auto;pointer-events:none}.aw-canvas-feedback>*{pointer-events:auto}.aw-graph-hint { box-sizing:border-box;max-width:100%; margin: 0; padding: 9px 12px; border: 1px solid var(--el-color-primary-light-5); border-radius: 9px; background: var(--el-bg-color); color: var(--el-color-primary); font-size: 11px; line-height: 1.5; box-shadow: 0 4px 18px rgb(0 0 0 / 8%); pointer-events: none; }
.aw-connection-help{border-color:var(--el-border-color-light);color:var(--el-text-color-secondary);font-size:10px;max-width:100%}.aw-connection-toolbar{box-sizing:border-box;display:flex;align-items:center;gap:9px;max-width:100%;padding:8px 10px;border:1px solid var(--el-border-color);border-radius:9px;background:var(--el-bg-color);box-shadow:var(--el-box-shadow-light);font-size:11px;color:var(--el-text-color-primary)}.aw-connection-toolbar>span{overflow:hidden;white-space:nowrap;text-overflow:ellipsis}.aw-connection-toolbar button{flex:none;border:1px solid var(--el-border-color);border-radius:5px;background:var(--el-bg-color);color:var(--el-text-color-regular);font:inherit;padding:4px 7px;cursor:pointer}.aw-connection-toolbar .aw-disconnect{color:var(--el-color-danger)}.aw-connection-toolbar button:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:2px}
.aw-edge-label { position: absolute; display: flex; align-items: center; gap: 4px; pointer-events: all; }
.aw-edge-label > span { border: 1px solid var(--el-border-color); border-radius: 5px; padding: 2px 5px; background: var(--el-bg-color); color: var(--el-text-color-secondary); font-size: 10px; white-space: nowrap; }
.aw-edge-insert { position: relative; display: flex; align-items: center; justify-content: center; width: 21px; height: 21px; border: 1px solid var(--el-border-color-darker); border-radius: 50%; background: var(--el-bg-color); color: var(--el-text-color-secondary); pointer-events: all; cursor: pointer; font: inherit; font-size: 14px; padding: 0; box-shadow: 0 1px 4px rgb(0 0 0 / 5%); }
.aw-edge-insert:hover, .aw-edge-insert:focus-visible { border-color: var(--el-color-primary); color: var(--el-color-primary); }
.aw-flow :deep(.vue-flow__minimap) { right: var(--aw-view-right); top: calc(var(--aw-view-top) + 52px); bottom: auto; width: 145px; height: 92px; border-radius: 7px; overflow: hidden; border: 1px solid var(--el-border-color); background: var(--el-bg-color); box-shadow: none; }
.aw-flow :deep(.vue-flow__node-procedure) { border-radius: 12px; }
.aw-flow :deep(.vue-flow__node-procedure:focus-visible) { outline: 2px solid var(--el-color-primary); outline-offset: 4px; }
@media (max-width: 700px) { .aw-graph-count { display: none; } .aw-view-controls { gap: 2px; padding: 5px; } .aw-view-controls button { padding: 5px 6px; } .aw-view-zoom { min-width: 30px; } }
</style>
