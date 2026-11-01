import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createSSRApp, effectScope, h, nextTick, reactive, type Ref } from 'vue';
import { renderToString } from 'vue/server-renderer';
import { defaultWorkflowRepeat, workflowNodeCount } from '@/agent/agentWorkflowRepeat';
import { defaultWorkflowCode } from '@/agent/agentWorkflowCode';
import type { AgentWorkflowDefinition, AgentWorkflowInputType, AgentWorkflowResourceCatalog, AgentWorkflowRun } from '@/api/agentWorkflows';
import type { AgentWorkflowNodeActivity } from '@/agent/agentWorkflowActivity';
import { insertWorkflowGraphGroup, upgradeWorkflowGraph } from '@/agent/agentWorkflowDag';

const host = vi.hoisted(() => ({ call: vi.fn(), getLayout: vi.fn(), saveLayout: vi.fn(), confirm: vi.fn(), serial: 0, mounted: [] as (() => void)[], cleanup: [] as (() => void)[] }));
vi.mock('vue', async original => ({ ...await original<typeof import('vue')>(), onMounted: (fn: () => void) => host.mounted.push(fn), onBeforeUnmount: (fn: () => void) => host.cleanup.push(fn), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock('element-plus', () => ({ ElMessageBox: { confirm: host.confirm } }));
vi.mock('@/api/agentWorkflows', () => ({ agentWorkflowsApi: { call: host.call, getLayout: host.getLayout, saveLayout: host.saveLayout } }));
vi.mock('@/utils/uuid', () => ({ randomUUID: () => `${String(++host.serial).padStart(8, '0')}-0000-4000-8000-000000000000` }));
vi.mock('../MarkdownView.vue', () => ({ default: {} }));
vi.mock('../TaskSessionResult.vue', () => ({ default: {} }));
vi.mock('./AgentWorkflowCanvas.vue', () => ({ default: {} }));
import AgentWorkflowEditor from './AgentWorkflowEditor.vue';

const legacy = (): AgentWorkflowDefinition => ({ id: 'flow-a', agent_id: 'agent-a', name: 'Private research', revision: 1, definition_hash: 'a'.repeat(64), inputs: [{ key: 'amount' }], steps: [{ id: 'collect', kind: 'agent', task: 'Read source material', output_file: 'notes.md' }] });
interface Controller {
  canAddControlGroup:Ref<boolean>;canDuplicate:Ref<boolean>;duplicate(id:string):void;
  codeFormValid:Ref<boolean>;
  draft: Ref<AgentWorkflowDefinition>; dirty: Ref<boolean>; selectedId: Ref<string>; waiting: Ref<boolean>; issues: Ref<unknown[]>; canEdit: Ref<boolean>; validScope: Ref<boolean>; isHistory: Ref<boolean>;
  inspectorOpen: Ref<boolean>; inspectorVisible: Ref<boolean>; positions: Ref<Record<string, { x: number; y: number }>>; viewport: Ref<{ x: number; y: number; zoom: number } | undefined>;
  frameSize: Ref<{ width: number; height: number }>;
  inspectorHeight: Ref<number>;
  canvasInsets: Ref<{ top: number; right: number; bottom: number; left: number }>;
  pendingRemote: Ref<AgentWorkflowDefinition | null>; displayed: Ref<AgentWorkflowDefinition>;
  recentUpdate: Ref<{ revision: number; count: number; targetId: string } | null>; updatedIds: Ref<string[]>;
  detailTab: Ref<'process' | 'io' | 'configuration'>; executionDetails: Ref<boolean>; processVisible: Ref<boolean>;
  selectDetailTab(value: 'process' | 'io' | 'configuration'): void;
  setToolApprovalMode(value: 'standard' | 'strict' | 'auto'): void;
  setInput(index: number, field: string, value: string | boolean): void; setInputType(index: number, type: AgentWorkflowInputType): void; setInputOption(index: number, at: number, field: string, value: string): void; addInputOption(index: number): void; removeInputOption(index: number, at: number): void; toggleNodeResource(field: 'skills' | 'shortcuts', id: string, checked: boolean): void; setKind(kind: 'agent' | 'confirm'): void;
  nodeResourceGroups: Ref<{ field: 'skills' | 'shortcuts'; rows: { id: string; title: string; version?: string; selected: boolean; available: boolean }[] }[]>;
  select(id: string): void; add(kind: 'agent' | 'confirm' | 'iteration' | 'loop'): void; upgradeGraph(): void; addGroup(kind: 'condition' | 'parallel'): void; setCondition(field: string, value: string): void; setStep(field: string, value: string): void; save(): Promise<void>;
  openCodeTest(): void; submitCodeTest(): void; codeTestValue:Ref<string>; codeTestOpen:Ref<boolean>; codeTestError:Ref<string>; setCode(code: ReturnType<typeof defaultWorkflowCode>):void;
  undo():void; setRepeat(value:import('@/api/agentWorkflows').AgentWorkflowStep):void;
  closeInspector(): void; focusStep(id: string): void; keydown(event: KeyboardEvent): void; state(): { dirty: boolean; saving: boolean; canSave: boolean };
  positionsChanged(value: Record<string, { x: number; y: number }>): void; viewportChanged(value: { x: number; y: number; zoom: number }): void; prepareClose(): Promise<boolean>;
}
const scopes: ReturnType<typeof effectScope>[] = [];
const flush = async () => { for (let i = 0; i < 6; i++) await Promise.resolve(); await nextTick(); };
function mount(run: AgentWorkflowRun | null = null, version: AgentWorkflowDefinition | null = null) {
  const props = reactive({ definition: run?.definition || legacy(), run, version, capabilities: { read: true, edit: true, confirm: true }, agentId: 'agent-a', sessionId: 'source-a', visible: true, activities: {} as Record<string, AgentWorkflowNodeActivity>, resourceCatalog: undefined as AgentWorkflowResourceCatalog | undefined, resourcesLoading: false, resourcesError: '', conversationOpen: false, processOpen: false, topInset: 88, bottomInset: 60 });
  const scope = effectScope(), emit = vi.fn(); scopes.push(scope);
  const c = scope.run(() => (AgentWorkflowEditor as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  for (const mounted of host.mounted.splice(0)) mounted();
  return { c, props, emit };
}
beforeEach(() => {
  vi.useFakeTimers(); vi.stubGlobal('window', new EventTarget()); host.serial = 0;
  host.call.mockReset(); host.saveLayout.mockReset(); host.confirm.mockReset().mockRejectedValue(new Error('cancel'));
  host.getLayout.mockReset().mockResolvedValue({ ok: true, layout: { workflow_id: 'flow-a', layout_revision: 0, definition_revision: 1, positions: {} } });
});
afterEach(() => { for (const cleanup of host.cleanup.splice(0)) cleanup(); for (const scope of scopes.splice(0)) scope.stop(); vi.clearAllTimers(); vi.useRealTimers(); vi.unstubAllGlobals(); });

describe('graph editor integration', () => {
  it('saves a workflow approval setting as a new draft patch without enabling or changing a historical run', async () => {
    const { c } = mount(); await flush();
    c.setToolApprovalMode('standard'); expect(c.dirty.value).toBe(false); expect(c.draft.value.tool_approval_mode).toBeUndefined();
    c.setToolApprovalMode('auto'); expect(c.dirty.value).toBe(true); expect(c.draft.value.tool_approval_mode).toBe('auto');
    c.undo(); expect(c.draft.value.tool_approval_mode).toBeUndefined(); c.setToolApprovalMode('strict');
    host.call.mockImplementation(async () => ({ ok: true, workflow: { ...JSON.parse(JSON.stringify(c.draft.value)), revision: 2, definition_hash: 'b'.repeat(64) } }));
    await c.save(); expect(host.call.mock.calls[0]![1]).toBe('edit'); expect(host.call.mock.calls[0]![2].operations).toEqual([{ op: 'set_metadata', tool_approval_mode: 'strict' }]);
    const fixed: AgentWorkflowRun = { id: 'historic', workflow_id: 'flow-a', agent_id: 'agent-a', status: 'completed', definition: { ...legacy(), tool_approval_mode: 'auto' } };
    const historical = mount(fixed); await flush(); historical.c.setToolApprovalMode('standard');
    expect(historical.c.canEdit.value).toBe(false); expect(historical.c.displayed.value.tool_approval_mode).toBe('auto'); expect(historical.c.dirty.value).toBe(false);
  });

  it('counts hidden repeat bodies when adding four-node graph blocks at the 200-node boundary',async()=>{
    const{c,props}=mount();await flush();
    const batch={id:'batch',kind:'iteration' as const,...defaultWorkflowRepeat('iteration')};
    const nodes=Array.from({length:195},(_,i)=>({id:`n${i}`,kind:'confirm' as const,prompt:'Confirm'}));
    const value={...legacy(),revision:2,definition_hash:'b'.repeat(64),steps:[batch,...nodes]};value.graph=upgradeWorkflowGraph(value);props.definition=value;await flush();
    c.select('n194');expect(workflowNodeCount(c.draft.value)).toBe(197);expect(c.canAddControlGroup.value).toBe(false);
    c.addGroup('parallel');expect(workflowNodeCount(c.draft.value)).toBe(197);
    const smaller={...value,revision:3,definition_hash:'c'.repeat(64),steps:[batch,...nodes.slice(0,-1)]};smaller.graph=upgradeWorkflowGraph(smaller);props.definition=smaller;await flush();c.select('n193');
    expect(workflowNodeCount(c.draft.value)).toBe(196);expect(c.canAddControlGroup.value).toBe(true);c.addGroup('condition');expect(workflowNodeCount(c.draft.value)).toBe(200);
  });
  it('allows a one-node copy at 199 but blocks copying a repeat body that would add two nodes',async()=>{
    const{c,props}=mount();await flush();
    props.definition={...legacy(),revision:2,definition_hash:'b'.repeat(64),steps:[{id:'batch',kind:'iteration',...defaultWorkflowRepeat('iteration')},...Array.from({length:197},(_,i)=>({id:`n${i}`,kind:'confirm' as const,prompt:'Confirm'}))]};await flush();
    expect(workflowNodeCount(c.draft.value)).toBe(199);c.select('batch');expect(c.canDuplicate.value).toBe(false);c.duplicate('batch');expect(workflowNodeCount(c.draft.value)).toBe(199);
    c.select('n196');expect(c.canDuplicate.value).toBe(true);c.duplicate('n196');expect(workflowNodeCount(c.draft.value)).toBe(200);expect(c.canDuplicate.value).toBe(false);
  });

  it('treats incomplete form buffers as unsaved and does not lose them on selection or chat updates',async()=>{
    const{c,props,emit}=mount();await flush();c.select('collect');c.codeFormValid.value=false;await flush();
    expect(c.state().dirty).toBe(true);expect(c.state().canSave).toBe(false);expect(emit).toHaveBeenCalledWith('dirty',true);
    c.select('__input');expect(c.selectedId.value).toBe('collect');expect(await c.prepareClose()).toBe(false);
    props.definition={...legacy(),revision:2,definition_hash:'b'.repeat(64),name:'Changed from chat'};await flush();
    expect(c.pendingRemote.value?.name).toBe('Changed from chat');expect(c.draft.value.name).toBe('Private research');
    host.confirm.mockResolvedValue('confirm');expect(await c.prepareClose()).toBe(true);expect(c.codeFormValid.value).toBe(true);
  });

  it('adds repeat nodes as ordinary execution positions and saves body edits in the same version patch',async()=>{
    const{c}=mount();await flush();c.select('collect');c.add('iteration');
    const item=c.draft.value.steps[1]!;expect(item.kind).toBe('iteration');expect(item.iteration?.body.kind).toBe('code');expect(c.issues.value).toEqual([]);
    c.setRepeat({...item,iteration:{...item.iteration!,concurrency:4}});expect(c.state().canSave).toBe(true);
    host.call.mockImplementation(async()=>({ok:true,workflow:{...JSON.parse(JSON.stringify(c.draft.value)),revision:2,definition_hash:'b'.repeat(64)}}));
    await c.save();expect(host.call.mock.calls[0]![2].operations).toContainEqual(expect.objectContaining({op:'add_step',step:expect.objectContaining({kind:'iteration',iteration:expect.objectContaining({concurrency:4})})}));
  });
  it('keeps a newly added bounded loop inside one node and can undo it without graph cycles',async()=>{
    const{c}=mount();await flush();c.select('collect');c.add('loop');expect(c.draft.value.steps[1]!.loop?.max_iterations).toBe(10);expect(c.issues.value).toEqual([]);c.undo();expect(c.draft.value.steps.map(step=>step.id)).toEqual(['collect']);
  });

  it('tests a saved code node with typed values and its exact version, without invoking a full workflow run', async () => {
    const { c, props, emit } = mount();
    Object.assign(props.capabilities,{run:true});
    props.definition = {...legacy(), revision:2,definition_hash:'b'.repeat(64), steps:[{id:'calculate',kind:'code',code:{...defaultWorkflowCode(),inputs:{value:{type:'number',value:2}},outputs:{result:{type:'number'}}}}]}; await flush();
    c.select('calculate'); c.openCodeTest(); expect(c.codeTestOpen.value).toBe(true);
    c.codeTestValue.value='{"value":3}'; c.submitCodeTest();
    expect(emit).toHaveBeenCalledWith('testNode',{stepId:'calculate',inputs:{value:3},expectedRevision:2,expectedHash:'b'.repeat(64)});
    expect(host.call).not.toHaveBeenCalled();
    c.openCodeTest(); c.codeTestValue.value='{"value":"3"}'; c.submitCodeTest();
    expect(c.codeTestError.value).toContain('codeTestInvalid'); expect(c.codeTestOpen.value).toBe(true);
  });
  it('blocks code tests when a conversation changes the saved draft while the test form is open', async () => {
    const { c, props, emit } = mount(); Object.assign(props.capabilities,{run:true});
    props.definition={...legacy(),revision:2,definition_hash:'b'.repeat(64),steps:[{id:'calculate',kind:'code',code:defaultWorkflowCode()}]}; await flush();
    c.select('calculate'); c.openCodeTest();
    props.definition={...props.definition,revision:3,definition_hash:'c'.repeat(64),steps:[{id:'calculate',kind:'code',code:{...defaultWorkflowCode(),source:'def main(inputs):\n    return {"result":"changed"}'}}]};await flush();
    c.submitCodeTest(); expect(c.codeTestError.value).toContain('codeTestStale');expect(emit.mock.calls.filter(row=>row[0]==='testNode')).toEqual([]);
  });

  it('edits typed input choices and node resources in the same patch without changing legacy text inputs', async () => {
    const { c, props } = mount(); await flush();
    c.select('__input'); c.setInputType(0, 'boolean'); c.setInput(0, 'default', 'yes');
    expect(c.draft.value.inputs![0]).toMatchObject({ type: 'boolean', default: 'yes' });
    c.setInputType(0, 'select'); expect(c.draft.value.inputs![0]!.default).toBeUndefined();
    c.setInputOption(0, 0, 'value', 'report'); c.setInputOption(0, 0, 'label', '报告'); c.setInput(0, 'default', 'report');
    props.resourceCatalog = { skills: [{ id: 'builtin:reader', name: 'Reader', version: 'v1', available: true }, { id: 'builtin:blocked', name: 'Blocked', version: 'v1', available: false }], shortcuts: [{ id: 'shortcut:private', title: 'Reference', version: 'v2', available: true }], files: [] };
    c.select('collect'); c.toggleNodeResource('skills', 'builtin:reader', true); c.toggleNodeResource('skills', 'builtin:blocked', true); c.toggleNodeResource('shortcuts', 'shortcut:private', true);
    expect(c.draft.value.steps[0]).toMatchObject({ skills: ['builtin:reader'], shortcuts: ['shortcut:private'] });
    expect(c.issues.value).toEqual([]);
    host.call.mockImplementation(async () => ({ ok: true, workflow: { ...JSON.parse(JSON.stringify(c.draft.value)), revision: 2, definition_hash: 'b'.repeat(64) } }));
    await c.save();
    expect(host.call.mock.calls[0]![2]).toMatchObject({ operations: [expect.objectContaining({ op: 'set_metadata', inputs: [expect.objectContaining({ type: 'select', default: 'report', options: [expect.objectContaining({ value: 'report', label: '报告' }), expect.anything()] })] }), { op: 'update_step', step_id: 'collect', patch: { skills: ['builtin:reader'], shortcuts: ['shortcut:private'] } }] });
    expect(c.dirty.value).toBe(false);
  });

  it('retains unavailable selected resources across refresh and allows explicit removal without granting them again', async () => {
    const { c, props } = mount(); await flush();
    props.definition = { ...legacy(), revision: 2, definition_hash: 'b'.repeat(64), steps: [{ ...legacy().steps[0]!, skills: ['builtin:removed'] }] }; await flush();
    c.select('collect'); props.resourceCatalog = { skills: [], shortcuts: [], files: [] }; await flush();
    expect(c.nodeResourceGroups.value[0]!.rows).toEqual([expect.objectContaining({ id: 'builtin:removed', selected: true, available: false })]);
    expect(c.draft.value.steps[0]!.skills).toEqual(['builtin:removed']);
    c.toggleNodeResource('skills', 'builtin:removed', false); expect(c.draft.value.steps[0]!.skills).toEqual([]);
    c.toggleNodeResource('skills', 'builtin:removed', true); expect(c.draft.value.steps[0]!.skills).toEqual([]);
  });

  it('uses only the run resource snapshot for history and refuses input or binding edits', async () => {
    const definition = { ...legacy(), steps: [{ ...legacy().steps[0]!, skills: ['builtin:reader'] }] };
    const run: AgentWorkflowRun = { id: 'run-a', workflow_id: definition.id, agent_id: 'agent-a', status: 'completed', definition, resources: { version: 1, nodes: { collect: { skills: [{ id: 'builtin:reader', name: 'Frozen reader', version: 'frozen-version', path: 'resources/skills/reader', sha256: 'sha' }] } } } };
    const { c, props } = mount(run); await flush();
    props.resourceCatalog = { skills: [{ id: 'builtin:reader', name: 'New name', version: 'new-version', available: true }], shortcuts: [], files: [] }; c.select('collect');
    expect(c.nodeResourceGroups.value[0]!.rows[0]).toMatchObject({ title: 'Frozen reader', version: 'frozen-version' });
    c.toggleNodeResource('skills', 'builtin:reader', false); c.setInputType(0, 'boolean'); c.setInput(0, 'default', 'yes'); await c.save();
    expect(c.displayed.value).toEqual(definition); expect(c.dirty.value).toBe(false); expect(host.call).not.toHaveBeenCalled();
  });

  it('opens process details for a selected Agent without switching its tab when the run completes', async () => {
    const definition = legacy(), run: AgentWorkflowRun = { id: 'run-a', workflow_id: definition.id, agent_id: 'agent-a', status: 'running', definition, steps: [{ step_id: 'collect', status: 'running', session_id: 'node-session' }] };
    const { c, props, emit } = mount(run); await flush();
    c.select('collect'); await flush();
    expect(c.executionDetails.value).toBe(true); expect(c.detailTab.value).toBe('process'); expect(c.processVisible.value).toBe(true);
    expect(emit).toHaveBeenCalledWith('process', true, 'collect');
    c.frameSize.value = { width: 480, height: 720 }; await flush();
    const processHeight = c.inspectorHeight.value;
    expect(processHeight).toBeGreaterThan(400);
    c.selectDetailTab('io'); await flush();
    expect(c.inspectorHeight.value).toBeLessThan(processHeight);
    expect(c.processVisible.value).toBe(false); expect(emit).toHaveBeenCalledWith('process', false, 'collect');
    props.run!.status = 'completed'; props.run!.steps![0]!.status = 'completed'; await flush();
    expect(c.selectedId.value).toBe('collect'); expect(c.detailTab.value).toBe('io'); expect(c.inspectorVisible.value).toBe(true);
    c.selectDetailTab('configuration'); props.activities = { collect: { step_id: 'collect', status: 'completed', phase: 'completed', observed: true, turn: 4, tool_count: 2, updated_at: 5000 } }; await flush();
    expect(c.detailTab.value).toBe('configuration'); expect(c.canEdit.value).toBe(false);
    c.focusStep('collect'); await flush(); expect(c.detailTab.value).toBe('process');
    props.conversationOpen = true; await flush(); expect(c.processVisible.value).toBe(false);
    props.conversationOpen = false; await flush(); expect(c.processVisible.value).toBe(true);
    c.closeInspector(); await flush(); expect(c.processVisible.value).toBe(false); expect(c.selectedId.value).toBe('collect');
  });

  it('keeps saved-version previews out of runtime process subscriptions', async () => {
    const { c, emit } = mount(null, { ...legacy(), revision: 2 }); await flush(); c.select('collect'); await flush();
    expect(c.executionDetails.value).toBe(false); expect(c.processVisible.value).toBe(false);
    expect(emit.mock.calls.some(([event, visible]) => event === 'process' && visible === true)).toBe(false);
  });
  it('previews a saved version as read-only without loading or writing the draft layout', async () => {
    const version = { ...legacy(), revision: 3, definition_hash: 'c'.repeat(64), name: 'Frozen version', steps: [{ ...legacy().steps[0]!, title: '历史节点' }] };
    const { c, props } = mount(null, version); await flush();
    expect(c.validScope.value).toBe(true); expect(c.isHistory.value).toBe(true); expect(c.canEdit.value).toBe(false);
    c.select('collect'); c.setStep('title', '不应写入'); c.add('agent');
    c.positionsChanged({ collect: { x: 20, y: 40 } }); c.viewportChanged({ x: 5, y: 10, zoom: .8 });
    await c.save(); expect(await c.prepareClose()).toBe(true);
    props.definition = { ...legacy(), revision: 8, definition_hash: 'd'.repeat(64), name: 'New draft', steps: [...legacy().steps, { id: 'new', kind: 'agent', task: '草稿新增' }] };
    await flush(); vi.advanceTimersByTime(1000); await flush();
    expect(c.displayed.value).toEqual(version); expect(c.displayed.value.steps[0]!.title).toBe('历史节点'); expect(c.displayed.value.steps).toHaveLength(1);
    expect(c.state()).toEqual({ dirty: false, saving: false, canSave: false }); expect(c.waiting.value).toBe(false);
    expect(c.positions.value).toEqual({}); expect(c.viewport.value).toBeUndefined(); expect(c.pendingRemote.value).toBeNull(); expect(c.recentUpdate.value).toBeNull();
    expect(host.getLayout).not.toHaveBeenCalled(); expect(host.saveLayout).not.toHaveBeenCalled(); expect(host.call).not.toHaveBeenCalled();
  });

  it.each([
    { id: 'another-flow' }, { agent_id: 'another-agent' }, { agent_id: undefined },
  ])('rejects a version outside its workflow or Agent scope: %j', async patch => {
    const { c } = mount(null, { ...legacy(), ...patch }); await flush();
    expect(c.validScope.value).toBe(false); expect(c.canEdit.value).toBe(false);
    c.add('agent'); await c.save();
    expect(host.getLayout).not.toHaveBeenCalled(); expect(host.call).not.toHaveBeenCalled(); expect(host.saveLayout).not.toHaveBeenCalled();
  });

  it('cancels an old layout read when switching to a preview and ignores its late response', async () => {
    let resolve!: (value: unknown) => void;
    host.getLayout.mockImplementation(() => new Promise(done => { resolve = done; }));
    const { c, props } = mount(); await flush();
    const signal = host.getLayout.mock.calls[0]![2] as AbortSignal;
    props.version = { ...legacy(), revision: 2, definition_hash: 'b'.repeat(64) }; await flush();
    expect(signal.aborted).toBe(true);
    resolve({ ok: true, layout: { workflow_id: 'flow-a', layout_revision: 5, definition_revision: 1, positions: { collect: { x: 100, y: 200 } }, viewport: { x: 80, y: 20, zoom: .5 } } });
    await flush();
    expect(c.isHistory.value).toBe(true); expect(c.positions.value).toEqual({}); expect(c.viewport.value).toBeUndefined(); expect(host.getLayout).toHaveBeenCalledTimes(1);
    window.dispatchEvent(new Event('pogo:auth-changed')); await flush();
    expect(c.validScope.value).toBe(false); await c.save(); expect(host.call).not.toHaveBeenCalled(); expect(host.saveLayout).not.toHaveBeenCalled();
  });

  it('keeps the workflow controls available during version preview while edit tools stay hidden', async () => {
    const version = { ...legacy(), revision: 3, definition_hash: 'c'.repeat(64) };
    const app = createSSRApp({ render: () => h(AgentWorkflowEditor as Parameters<typeof h>[0], {
      definition: legacy(), version, capabilities: { edit: true }, agentId: 'agent-a', sessionId: 'source-a', visible: true,
    }, { 'workflow-controls': () => h('button', { 'data-testid': 'version-control-entry' }, '版本控制') }) });
    app.component('el-dialog', { render: () => null });
    const html = await renderToString(app);
    expect(html).toContain('version-control-entry'); expect(html).toContain('workflow-history-readonly'); expect(html).toContain('agentWorkflowCanvas.versionReadOnly');
    expect(html).not.toContain('workflow-add-node'); expect(html).not.toContain('agentWorkflowCanvas.undo'); expect(html).not.toContain('agentWorkflowCanvas.runResult');
    expect(host.getLayout).not.toHaveBeenCalled(); expect(host.call).not.toHaveBeenCalled(); expect(host.saveLayout).not.toHaveBeenCalled();
  });
  it('allows adding the 200th node but keeps the 201st out of the local draft', async () => {
    const { c, props } = mount(); await flush();
    props.definition = { ...legacy(), revision: 2, definition_hash: 'b'.repeat(64), steps: Array.from({ length: 199 }, (_, index) => ({ id: `step_${index}`, kind: 'agent', task: '整理资料' })) };
    await flush(); c.add('agent'); expect(c.draft.value.steps).toHaveLength(200);
    c.add('confirm'); expect(c.draft.value.steps).toHaveLength(200);
  });

  it('renders saved conversation updates immediately without changing selection, layout or viewport', async () => {
    const positions = { collect: { x: 120, y: 80 } }, viewport = { x: -50, y: 35, zoom: .7 };
    host.getLayout.mockResolvedValue({ ok: true, layout: { workflow_id: 'flow-a', layout_revision: 1, definition_revision: 1, positions, viewport } });
    const { c, props } = mount(); await flush(); c.select('collect');
    props.definition = { ...legacy(), revision: 2, definition_hash: 'b'.repeat(64), steps: [
      { ...legacy().steps[0]!, title: '对话改好的资料整理' }, { id: 'report', kind: 'agent', task: '生成报告' },
    ] };
    await flush();
    expect(c.draft.value.revision).toBe(2); expect(c.displayed.value.steps.map(step => step.id)).toEqual(['collect', 'report']);
    expect(c.displayed.value.steps[0]!.title).toBe('对话改好的资料整理');
    expect(c.recentUpdate.value).toEqual({ revision: 2, count: 2, targetId: 'collect' });
    expect(c.updatedIds.value).toEqual(['collect', 'report']);
    expect(c.selectedId.value).toBe('collect'); expect(c.inspectorVisible.value).toBe(true); expect(c.dirty.value).toBe(false);
    expect(c.positions.value).toEqual(positions); expect(c.viewport.value).toEqual(viewport);
    expect(host.saveLayout).not.toHaveBeenCalled(); expect(host.call).not.toHaveBeenCalled();
    vi.advanceTimersByTime(10000); await flush(); expect(c.recentUpdate.value).toBeNull(); expect(c.updatedIds.value).toEqual([]);
  });

  it('keeps unsaved canvas edits when a conversation changes the same workflow', async () => {
    const { c, props } = mount(); await flush(); c.select('collect'); c.setStep('title', '我的未保存标题');
    props.definition = { ...legacy(), revision: 2, definition_hash: 'b'.repeat(64), steps: [{ ...legacy().steps[0]!, task: 'Agent 已保存的新指令' }] };
    await flush();
    expect(c.draft.value.steps[0]!.title).toBe('我的未保存标题'); expect(c.draft.value.steps[0]!.task).toBe('Read source material');
    expect(c.pendingRemote.value?.revision).toBe(2); expect(c.pendingRemote.value?.steps[0]!.task).toBe('Agent 已保存的新指令');
    expect(c.dirty.value).toBe(true); expect(c.recentUpdate.value).toBeNull(); expect(c.updatedIds.value).toEqual([]);
  });

  it('never applies a new draft revision or update highlight to a frozen historical run', async () => {
    const definition = legacy(), run: AgentWorkflowRun = { id: 'run-a', workflow_id: definition.id, agent_id: 'agent-a', status: 'completed', definition };
    const { c, props } = mount(run); await flush();
    props.definition = { ...legacy(), revision: 2, definition_hash: 'b'.repeat(64), steps: [...legacy().steps, { id: 'new', kind: 'agent', task: '新增任务' }] };
    await flush();
    expect(c.displayed.value).toEqual(run.definition); expect(c.displayed.value.steps.map(step => step.id)).toEqual(['collect']);
    expect(c.recentUpdate.value).toBeNull(); expect(c.updatedIds.value).toEqual([]); expect(c.canEdit.value).toBe(false);
  });

  it('temporarily hides the compact inspector behind execution details without losing local state', async () => {
    const { c, props } = mount(); await flush();
    c.select('collect'); c.setStep('task', 'Retain this unsaved node instruction.');
    const draft = JSON.stringify(c.draft.value);
    props.topInset = 120; await flush(); expect(c.canvasInsets.value.top).toBe(120);
    props.processOpen = true; await flush();
    expect(c.inspectorVisible.value).toBe(true);
    c.frameSize.value = { width: 700, height: 800 }; props.bottomInset = 360; await flush();
    expect(c.inspectorVisible.value).toBe(false); expect(c.inspectorOpen.value).toBe(true);
    expect(c.selectedId.value).toBe('collect'); expect(c.dirty.value).toBe(true);
    props.processOpen = false; await flush();
    expect(c.inspectorVisible.value).toBe(true); expect(JSON.stringify(c.draft.value)).toBe(draft);
    expect(host.call).not.toHaveBeenCalled(); expect(host.saveLayout).not.toHaveBeenCalled();
  });

  it('opens only the requested inspector and preserves unsaved edits and view state across floating panels', async () => {
    const positions = { collect: { x: 42, y: 84 } }, viewport = { x: 24, y: 80, zoom: .8 };
    host.getLayout.mockResolvedValue({ ok: true, layout: { workflow_id: 'flow-a', layout_revision: 1, definition_revision: 1, positions, viewport } });
    const { c, props } = mount(); await flush();
    expect(c.selectedId.value).toBe(''); expect(c.inspectorVisible.value).toBe(false);
    c.select('collect'); c.setStep('task', 'Keep the selected material and my local edit.');
    expect(c.inspectorVisible.value).toBe(true); expect(c.state()).toEqual({ dirty: true, saving: false, canSave: true });
    const draft = JSON.stringify(c.draft.value);
    props.conversationOpen = true; props.bottomInset = 340; await flush();
    expect(c.inspectorVisible.value).toBe(false); expect(c.inspectorOpen.value).toBe(true);
    // A movable floating conversation does not reserve a fixed right column.
    expect(c.canvasInsets.value.right).toBe(16); expect(c.canvasInsets.value.bottom).toBe(340);
    props.conversationOpen = false; await flush();
    c.closeInspector();
    expect(c.selectedId.value).toBe('collect'); expect(c.inspectorVisible.value).toBe(false);
    expect(JSON.stringify(c.draft.value)).toBe(draft); expect(c.positions.value).toEqual(positions); expect(c.viewport.value).toEqual(viewport);
    expect(host.call).not.toHaveBeenCalled(); expect(host.saveLayout).not.toHaveBeenCalled();
  });

  it('supports workflow settings and output inspection while Escape only closes the floating inspector', async () => {
    const { c } = mount(); await flush();
    c.focusStep('__input'); await flush();
    expect(c.selectedId.value).toBe('__input'); expect(c.inspectorVisible.value).toBe(true);
    const preventDefault = vi.fn(), stopPropagation = vi.fn();
    c.keydown({ key: 'Escape', defaultPrevented: false, preventDefault, stopPropagation } as unknown as KeyboardEvent);
    expect(preventDefault).toHaveBeenCalledOnce(); expect(stopPropagation).toHaveBeenCalledOnce();
    expect(c.inspectorVisible.value).toBe(false); expect(c.selectedId.value).toBe('__input');
    c.focusStep('__output'); await flush();
    expect(c.selectedId.value).toBe('__output'); expect(c.inspectorVisible.value).toBe(true);
    c.select('');
    expect(c.inspectorVisible.value).toBe(false); expect(c.dirty.value).toBe(false);
    expect(host.call).not.toHaveBeenCalled(); expect(host.saveLayout).not.toHaveBeenCalled();
  });

  it('exposes confirmation for either selected waiting node even while another branch runs', async () => {
    const definition = legacy(); definition.graph = upgradeWorkflowGraph(definition);
    insertWorkflowGraphGroup(definition, [{ id: 'fork', kind: 'parallel' }, { id: 'one', kind: 'confirm', prompt: 'Confirm one' }, { id: 'two', kind: 'confirm', prompt: 'Confirm two' }, { id: 'join', kind: 'join' }], '__input');
    const run: AgentWorkflowRun = { id: 'run-a', workflow_id: 'flow-a', agent_id: 'agent-a', definition, status: 'running', steps: [{ step_id: 'one', status: 'waiting_confirmation' }, { step_id: 'two', status: 'waiting_confirmation' }, { step_id: 'collect', status: 'running' }] };
    const { c, props, emit } = mount(run); await flush();
    expect(c.selectedId.value).toBe('one'); expect(c.waiting.value).toBe(true); expect(c.canEdit.value).toBe(false);
    c.select('two'); expect(c.waiting.value).toBe(true); expect(emit).toHaveBeenCalledWith('select', 'two');
    c.select('collect'); expect(c.waiting.value).toBe(false);
    c.select('two'); props.run!.status = 'cancelled'; await flush(); expect(c.waiting.value).toBe(false);
    await c.save(); expect(host.call).not.toHaveBeenCalled(); expect(host.saveLayout).not.toHaveBeenCalled();
  });

  it('saves an upgraded graph and configured condition in one ordinary patch request', async () => {
    const { c, emit } = mount(); await flush();
    c.upgradeGraph(); c.addGroup('condition');
    const control = c.draft.value.steps.find(step => step.kind === 'condition')!;
    c.setCondition('operator', 'gt'); c.setCondition('value', '1000');
    for (const step of c.draft.value.steps.filter(step => step.kind === 'agent' && !step.task)) { c.select(step.id); c.setStep('task', 'Read the selected sources.'); }
    expect(c.issues.value).toEqual([]); expect(c.dirty.value).toBe(true);
    host.call.mockImplementation(async () => ({ ok: true, workflow: { ...JSON.parse(JSON.stringify(c.draft.value)), revision: 2, definition_hash: 'b'.repeat(64) } }));
    await c.save();
    const [agent, group, body] = host.call.mock.calls[0]!;
    expect(agent).toBe('agent-a'); expect(group).toBe('edit'); expect(body.expected_revision).toBe(1); expect(body.source_session_id).toBe('source-a');
    expect(body.operations).toContainEqual({ op: 'set_graph', graph: c.draft.value.graph });
    expect(body.operations.find((op: { op: string; step?: { id: string } }) => op.op === 'add_step' && op.step?.id === control.id).step.condition).toEqual({ source: 'inputs.amount', operator: 'gt', value: '1000' });
    expect(c.dirty.value).toBe(false); expect(emit).toHaveBeenCalledWith('saved', expect.objectContaining({ revision: 2 }));
  });

  it('clears the hidden comparison value when switching to an empty-value rule', async () => {
    const { c } = mount(); await flush(); c.upgradeGraph(); c.addGroup('condition');
    c.setCondition('value', 'old value'); c.setCondition('operator', 'is_empty');
    expect(c.draft.value.steps.find(step => step.kind === 'condition')?.condition).toEqual({ source: 'inputs.amount', operator: 'is_empty', value: '' });
  });
});
