import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { effectScope, nextTick, reactive, type Ref } from 'vue';
import type { AgentWorkflowDAG, AgentWorkflowDefinition, AgentWorkflowRun, AgentWorkflowViewport } from '@/api/agentWorkflows';
import type { AgentWorkflowNodeActivity } from '@/agent/agentWorkflowActivity';
import { defaultWorkflowRepeat, workflowNodeCount } from '@/agent/agentWorkflowRepeat';
import { workflowCanvasVisibleRect } from '@/agent/agentWorkflowGraph';

const host = vi.hoisted(() => ({
  mounted: [] as (() => void)[], cleanup: [] as (() => void)[], setViewport: vi.fn(),
  viewport: { x: -210, y: 80, zoom: 0.8 },
  connect: undefined as ((connection: { source: string; target: string; sourceHandle?: string }) => void) | undefined,
  edgeClick: undefined as ((event: { edge: { source: string; target: string; data?: { branch?: 'true' | 'false' } } }) => void) | undefined,
  nodes: {} as Record<string, { position: { x: number; y: number }; dimensions: { width: number; height: number } }>,
}));
vi.mock('vue', async original => ({ ...await original<typeof import('vue')>(), onMounted: (fn: () => void) => host.mounted.push(fn), onBeforeUnmount: (fn: () => void) => host.cleanup.push(fn), useId: () => 'workspace-test', useSSRContext: () => ({ modules: new Set() }) }));
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: { value: 'zh-CN' } }) }));
vi.mock('@vue-flow/core', () => ({
  BaseEdge: {}, EdgeLabelRenderer: {}, VueFlow: {}, MarkerType: { ArrowClosed: 'arrowclosed' }, Position: { Left: 'left', Right: 'right' }, getSmoothStepPath: () => [],
  useVueFlow: () => ({ onConnect(fn: typeof host.connect) { host.connect = fn; }, onEdgeClick(fn: typeof host.edgeClick) { host.edgeClick = fn; }, onNodeClick() {}, onPaneClick() {}, onNodeDragStart() {}, onNodeDragStop() {}, onNodesInitialized() {}, onMoveEnd() {}, findNode: (id: string) => host.nodes[id], setViewport: host.setViewport, getViewport: () => host.viewport }),
}));
vi.mock('@vue-flow/background', () => ({ Background: {} }));
vi.mock('@vue-flow/minimap', () => ({ MiniMap: {} }));
vi.mock('./AgentWorkflowCanvasNode.vue', () => ({ default: {} }));
import AgentWorkflowCanvas from './AgentWorkflowCanvas.vue';

interface Controller {
  canAdd:Ref<boolean>;
  frame: Ref<{ clientWidth: number; clientHeight: number } | null>;
  flowNodes: Ref<{ id: string; position: { x: number; y: number }; data: { updated?: boolean; activity?: AgentWorkflowNodeActivity }; draggable: boolean }[]>;
  initialView(): Promise<void>;
  focusNode(id: string): Promise<void>;
  changeView(action: 'fit' | 'in' | 'out'): Promise<void>;
  arrange(): Promise<void>;
  insertAfter(id: string): void;
  selectedConnection: Ref<{ source: string; target: string } | undefined>;
  removeConnection(): void;
}
const scopes: ReturnType<typeof effectScope>[] = [];
const definition = (): AgentWorkflowDefinition => ({ id: 'flow-a', agent_id: 'agent-a', name: 'Research', revision: 1, definition_hash: 'a'.repeat(64), steps: [{ id: 'facts', kind: 'agent', task: 'Read facts', output_file: 'facts.md' }] });
const flush = async () => { for (let i = 0; i < 5; i++) await Promise.resolve(); await nextTick(); };
function mount(run: AgentWorkflowRun | null = null) {
  const props = reactive({ definition: definition(), run, visible: true, positions: {}, viewport: { ...host.viewport }, editable: !run, selectedId: 'facts', updatedIds: [] as string[], activities: {} as Record<string, AgentWorkflowNodeActivity>, insets: { top: 88, left: 72, right: 16, bottom: 64 } });
  const scope = effectScope(), emit = vi.fn(); scopes.push(scope);
  const c = scope.run(() => (AgentWorkflowCanvas as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  c.frame.value = { clientWidth: 1000, clientHeight: 700 };
  for (const mounted of host.mounted.splice(0)) mounted();
  return { props, c, emit };
}
function transformed(id: string) {
  const node = host.nodes[id], view = host.viewport;
  return { x: node.position.x * view.zoom + view.x, y: node.position.y * view.zoom + view.y, width: node.dimensions.width * view.zoom, height: node.dimensions.height * view.zoom };
}
beforeEach(() => {
  vi.useFakeTimers(); host.viewport = { x: -210, y: 80, zoom: 0.8 };
  host.nodes = Object.fromEntries(['__input', 'facts', '__output'].map((id, i) => [id, { position: { x: 48 + i * 310, y: 92 }, dimensions: { width: 248, height: 168 } }]));
  host.setViewport.mockReset().mockImplementation(async (value: AgentWorkflowViewport) => { host.viewport = { ...value }; });
});
afterEach(() => {
  for (const cleanup of host.cleanup.splice(0)) cleanup();
  for (const scope of scopes.splice(0)) scope.stop();
  vi.clearAllTimers(); vi.useRealTimers();
});

describe('workflow canvas background workspace', () => {
  it('adds a new route through a port gesture and removes only an explicitly selected connection', async () => {
    const { c, props, emit } = mount();
    props.definition = { ...definition(), steps: [...definition().steps, { id: 'report', kind: 'agent', task: 'Summarize' }] }; await flush();
    const positions = c.flowNodes.value.map(node => ({ id: node.id, position: { ...node.position } }));
    emit.mockClear();
    host.connect!({ source: '__input', target: 'report', sourceHandle: 'out' });
    const graph = emit.mock.calls.find(([name]) => name === 'graph')![1] as AgentWorkflowDAG;
    expect(graph.edges).toEqual([{ source: '__input', target: 'facts' }, { source: 'facts', target: 'report' }, { source: 'report', target: '__output' }, { source: '__input', target: 'report' }]);
    expect(emit.mock.calls.some(([name]) => name === 'reorder')).toBe(false);
    props.definition = { ...props.definition, graph }; await flush();
    expect(c.flowNodes.value.map(node => ({ id: node.id, position: node.position }))).toEqual(positions);
    emit.mockClear(); host.edgeClick!({ edge: { source: '__input', target: 'report' } });
    expect(c.selectedConnection.value).toEqual({ source: '__input', target: 'report' });
    c.removeConnection();
    const removed = emit.mock.calls.find(([name]) => name === 'graph')![1] as AgentWorkflowDAG;
    expect(removed.edges).toEqual(graph.edges.filter(edge => !(edge.source === '__input' && edge.target === 'report')));
    expect(c.selectedConnection.value).toBeUndefined();
  });

  it('does not add or delete a connection in a frozen run snapshot', async () => {
    const d = definition(), { c, emit } = mount({ id: 'run-a', workflow_id: d.id, agent_id: 'agent-a', status: 'completed', definition: d });
    emit.mockClear();
    host.connect!({ source: '__input', target: '__output' }); host.edgeClick!({ edge: { source: '__input', target: 'facts' } }); c.removeConnection();
    expect(c.selectedConnection.value).toBeUndefined(); expect(emit).not.toHaveBeenCalled();
  });

  it('removes insertion affordances when repeat bodies bring the canvas to 200 nodes',async()=>{
    const{c,props,emit}=mount();props.definition={...definition(),steps:[{id:'batch',kind:'iteration',...defaultWorkflowRepeat('iteration')},...Array.from({length:198},(_,i)=>({id:`n${i}`,kind:'confirm' as const,prompt:'Confirm'}))]};await flush();
    expect(workflowNodeCount(props.definition)).toBe(200);expect(props.definition.steps).toHaveLength(199);expect(c.canAdd.value).toBe(false);expect(c.flowNodes.value.every(node=>!(node.data as {canInsert?:boolean}).canInsert)).toBe(true);
    emit.mockClear();c.insertAfter('batch');expect(emit).not.toHaveBeenCalled();
    props.definition={...props.definition,steps:props.definition.steps.slice(0,-1)};await flush();expect(c.canAdd.value).toBe(true);
  });

  it('updates live activity without refitting, rearranging or changing the selected node', async () => {
    const run: AgentWorkflowRun = { id: 'run-a', workflow_id: 'flow-a', agent_id: 'agent-a', status: 'running', definition: definition(), steps: [{ step_id: 'facts', status: 'running', session_id: 'node-session' }] };
    const { c, props, emit } = mount(run); await c.initialView(); await flush();
    const positions = c.flowNodes.value.map(node => ({ id: node.id, position: { ...node.position } })), viewport = { ...host.viewport };
    host.setViewport.mockClear(); emit.mockClear();
    props.activities = { facts: { step_id: 'facts', session_id: 'node-session', status: 'running', phase: 'thinking', observed: true, turn: 2, updated_at: 2000 } }; await flush();
    props.activities.facts = { ...props.activities.facts!, phase: 'tool', tool_name: 'read', tool_count: 1, updated_at: 2500 }; await flush();
    expect(c.flowNodes.value.find(node => node.id === 'facts')?.data.activity).toMatchObject({ phase: 'tool', tool_name: 'read', turn: 2, tool_count: 1 });
    expect(c.flowNodes.value.map(node => ({ id: node.id, position: node.position }))).toEqual(positions);
    expect(props.selectedId).toBe('facts'); expect(host.setViewport).not.toHaveBeenCalled(); expect(host.viewport).toEqual(viewport); expect(emit).not.toHaveBeenCalled();
  });
  it('updates remote node content and highlights it without moving existing nodes or the viewport', async () => {
    const { c, props, emit } = mount(); await c.initialView(); await flush();
    const positions = Object.fromEntries(c.flowNodes.value.map(node => [node.id, { ...node.position }])), viewport = { ...host.viewport };
    host.setViewport.mockClear(); emit.mockClear();
    props.definition = { ...definition(), revision: 2, definition_hash: 'b'.repeat(64), steps: [
      { id: 'new', kind: 'agent', task: 'New task inserted by conversation' }, { ...definition().steps[0]!, title: 'Updated facts' },
    ] };
    props.updatedIds = ['new', 'facts']; await flush();
    for (const [id, position] of Object.entries(positions)) expect(c.flowNodes.value.find(node => node.id === id)?.position).toEqual(position);
    expect(c.flowNodes.value.find(node => node.id === 'new')?.data.updated).toBe(true);
    expect(c.flowNodes.value.find(node => node.id === 'facts')?.data.updated).toBe(true);
    expect(host.setViewport).not.toHaveBeenCalled(); expect(host.viewport).toEqual(viewport); expect(emit).not.toHaveBeenCalled();
  });

  it('keeps the same nodes, selection and viewport when workspace panels open and close', async () => {
    const { c, props, emit } = mount(); await c.initialView(); await flush();
    const before = JSON.stringify(props.definition), nodes = c.flowNodes.value, viewport = { ...host.viewport };
    host.setViewport.mockClear(); emit.mockClear();
    props.insets.right = 400; props.insets.bottom = 260; await flush();
    props.insets.right = 16; props.insets.bottom = 64; await flush();
    expect(host.viewport).toEqual(viewport); expect(host.setViewport).not.toHaveBeenCalled(); expect(emit).not.toHaveBeenCalled();
    expect(c.flowNodes.value).toBe(nodes); expect(props.selectedId).toBe('facts'); expect(JSON.stringify(props.definition)).toBe(before);
  });

  it('focuses a selected node in the visible area above the execution drawer and beside settings', async () => {
    const { c, props } = mount(); props.insets.right = 360; props.insets.bottom = 220;
    await c.focusNode('facts');
    const area = workflowCanvasVisibleRect(1000, 700, props.insets), node = transformed('facts');
    expect(node.x).toBeGreaterThanOrEqual(area.x); expect(node.y).toBeGreaterThanOrEqual(area.y);
    expect(node.x + node.width).toBeLessThanOrEqual(area.x + area.width);
    expect(node.y + node.height).toBeLessThanOrEqual(area.y + area.height);
    expect(node.x + node.width / 2).toBeCloseTo(area.x + area.width / 2);
  });

  it('fits the complete frozen graph into the unobscured area without enabling edits or saving history layout', async () => {
    const d = definition(); const { c, props, emit } = mount({ id: 'run-a', workflow_id: d.id, agent_id: 'agent-a', status: 'completed', definition: d });
    props.insets.right = 360; props.insets.bottom = 220;
    await c.changeView('fit');
    const area = workflowCanvasVisibleRect(1000, 700, props.insets);
    for (const id of Object.keys(host.nodes)) {
      const node = transformed(id);
      expect(node.x).toBeGreaterThanOrEqual(area.x); expect(node.y).toBeGreaterThanOrEqual(area.y);
      expect(node.x + node.width).toBeLessThanOrEqual(area.x + area.width);
      expect(node.y + node.height).toBeLessThanOrEqual(area.y + area.height);
    }
    await c.arrange(); c.insertAfter('facts');
    expect(emit).not.toHaveBeenCalled();
    expect(c.flowNodes.value.every(node => !(node as { draggable: boolean }).draggable)).toBe(true);
  });

  it('zooms around the unobscured area instead of the center behind an open settings panel', async () => {
    const { c, props } = mount(); props.insets.right = 360;
    const area = workflowCanvasVisibleRect(1000, 700, props.insets), center = { x: area.x + area.width / 2, y: area.y + area.height / 2 };
    const world = { x: (center.x - host.viewport.x) / host.viewport.zoom, y: (center.y - host.viewport.y) / host.viewport.zoom };
    await c.changeView('in');
    expect(world.x * host.viewport.zoom + host.viewport.x).toBeCloseTo(center.x);
    expect(world.y * host.viewport.zoom + host.viewport.y).toBeCloseTo(center.y);
  });
});
