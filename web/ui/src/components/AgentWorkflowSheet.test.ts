import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, ref, type Ref } from "vue";
import type { TaskContextReading, TaskContextScope } from "@/agent/taskContextUsage";
import type { AgentWorkflowUpdate } from "@/agent/agentWorkflowUpdates";
import type { AgentWorkflowCapabilities, AgentWorkflowDefinition, AgentWorkflowResponse, AgentWorkflowRun, AgentWorkflowVersionSummary } from "@/api/agentWorkflows";
import type { AgentWorkflowRunStatus } from "@/agent/useAgentWorkflowRunStatus";

const host = vi.hoisted(() => ({
  list: vi.fn(), call: vi.fn(), confirm: vi.fn(), serial: 0,
  mounted: [] as (() => void)[], cleanup: [] as (() => void)[],
  updates: null as Ref<AgentWorkflowUpdate | null> | null,
  overview: null as AgentWorkflowRunStatus | null,
}));
vi.mock("vue", async original => ({
  ...await original<typeof import("vue")>(),
  onMounted: (fn: () => void) => host.mounted.push(fn),
  onBeforeUnmount: (fn: () => void) => host.cleanup.push(fn),
  provide: () => {},
  inject: (key: unknown, fallback: unknown) => typeof key === "symbol" && key.description === "agentWorkflowRunStatus" ? host.overview || fallback : host.updates || fallback,
  useSSRContext: () => ({ modules: new Set() }),
}));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key, locale: { value: "zh-CN" } }) }));
vi.mock("element-plus", () => ({ ElMessageBox: { confirm: host.confirm } }));
vi.mock("@/api/agentWorkflows", () => ({ agentWorkflowsApi: { list: host.list, call: host.call } }));
vi.mock("@/agent/useAgentWorkflowActivity", () => ({ useAgentWorkflowActivity: () => ({ snapshot: ref(null), connection: ref('idle'), connected: ref(false), error: ref(''), refresh: vi.fn() }) }));
vi.mock("@/agent/useAgentWorkflowResources", () => ({ useAgentWorkflowResources: () => ({ catalog: ref(null), loading: ref(false), error: ref(''), refresh: vi.fn().mockResolvedValue(undefined) }) }));
vi.mock("@/utils/uuid", () => ({ randomUUID: () => `${String(++host.serial).padStart(8, "0")}-0000-4000-8000-000000000000` }));
vi.mock("./MarkdownView.vue", () => ({ default: {} }));
vi.mock("./TaskSessionResult.vue", () => ({ default: {} }));
vi.mock("./agent-workflow/AgentWorkflowEditor.vue", () => ({ default: {} }));
import AgentWorkflowSheet from "./AgentWorkflowSheet.vue";

interface Controller {
  workflows: Ref<AgentWorkflowDefinition[]>;
  visibleWorkflows: Ref<AgentWorkflowDefinition[]>;
  showDeleted: Ref<boolean>;
  deletingFlowId: Ref<string>;
  canDelete(flow: AgentWorkflowDefinition): boolean;
  deleteWorkflow(flow?: AgentWorkflowDefinition): Promise<void>;
  setDeletedView(value: boolean): Promise<void>;
  workflow: Ref<AgentWorkflowDefinition | null>;
  workflowId: Ref<string>;
  runs: Ref<AgentWorkflowRun[]>;
  run: Ref<AgentWorkflowRun | null>;
  runId: Ref<string>;
  nodeContextReadings: Ref<Record<string, TaskContextReading>>;
  observedContextReading: Ref<TaskContextReading | null>;
  setNodeContext(stepId: string, reading: TaskContextReading | null, source?: TaskContextScope): void;
  editor: Ref<{ prepareClose(): Promise<boolean>; focusStep(id: string): void } | null>;
  editorDirty: Ref<boolean>;
  capabilities: Ref<AgentWorkflowCapabilities>;
  workflowActions: Ref<{ command: string; disabled: boolean }[]>;
  workflowAction(command: string): Promise<void>;
  inputMode: Ref<"trial" | "start">;
  inputOpen: Ref<boolean>;
  inputLoading: Ref<boolean>;
  inputFields: Ref<AgentWorkflowDefinition["inputs"]>;
  runInputNotice: Ref<string>;
  runInputApprovalMode: Ref<string | null>;
  inputs: Ref<Record<string, string>>;
  files: Ref<string>;
  feedback: Ref<string>;
  selectedStepId: Ref<string>;
  confirmationFeedback: Ref<Record<string, string>>;
  waitingSteps: Ref<AgentWorkflowDefinition['steps']>;
  processSessionId: Ref<string>;
  processActiveRunId: Ref<string | undefined>;
  processSteps: Ref<{ id: string; parentStepId: string; sessionId: string; activeRunId?: string; status: string }[]>;
  nodeProcessOpen: Ref<boolean>;
  nodeProcessStepId: Ref<string>;
  nodeProcessChanged(open: boolean, id?: string): void;
  chooseProcessStep(id: string): void;
  artifactOpen: Ref<boolean>;
  artifactText: Ref<string>;
  artifactPath: Ref<string>;
  createOpen: Ref<boolean>;
  createName: Ref<string>;
  createTask: Ref<string>;
  createOutput: Ref<string>;
  error: Ref<string>;
  notice: Ref<string>;
  showProcess: Ref<boolean>;
  controlOpen: Ref<boolean>;
  previewDefinition: Ref<AgentWorkflowDefinition | null>;
  previewVersion: Ref<AgentWorkflowVersionSummary | null>;
  versions: Ref<AgentWorkflowVersionSummary[]>;
  versionsError: Ref<string>;
  canRestore: Ref<boolean>;
  openControl(tab?: "overview" | "versions" | "runs"): void;
  loadVersions(append?: boolean): Promise<void>;
  previewSavedVersion(version: AgentWorkflowVersionSummary): Promise<void>;
  restoreSavedVersion(): Promise<void>;
  toggleConversation(): void;
  toggleProcess(): void;
  refresh(): Promise<void>;
  chooseWorkflow(id: string, selectedRun?: string): Promise<void>;
  chooseRun(id: string): Promise<void>;
  back(): Promise<void>;
  artifact(id: string): Promise<void>;
  prepareRun(mode: "trial" | "start"): Promise<void>;
  start(): Promise<void>;
  createWorkflow(): Promise<void>;
  control(action: "cancel" | "retry"): Promise<void>;
  confirm(stepId?: string): Promise<void>;
  setFeedback(stepId: string, value: string): void;
  selectStep(stepId: string): void;
  testCodeNode(value: { stepId: string; inputs: Record<string, unknown>; expectedRevision: number; expectedHash: string }): Promise<void>;
}

const caps = { read: true, edit: true, run: true, enable: true, confirm: true };
const flow = (agentId = "agent-a", id = "flow-one"): AgentWorkflowDefinition => ({
  id, agent_id: agentId, name: "资料整理", revision: 1, definition_hash: "a".repeat(64),
  inputs: [{ key: "topic", label: "主题", required: true }],
  steps: [{ id: "review", kind: "confirm", title: "本人确认", prompt: "核对资料" }],
});
const run = (agentId = "agent-a", workflowId = "flow-one", id = "run-one"): AgentWorkflowRun => ({
  id, agent_id: agentId, workflow_id: workflowId, status: "completed", definition: flow(agentId, workflowId),
  steps: [{ step_id: "review", status: "completed" }],
});
const version = (value: AgentWorkflowDefinition): AgentWorkflowVersionSummary => ({ version_id: value.definition_hash, revision: value.revision, name: value.name, updated_at: 1, step_count: value.steps.length, is_current_draft: false, is_enabled: false, sources: ["saved"] });
const scopes: ReturnType<typeof effectScope>[] = [];
const pending = <T>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
};
const flush = async () => { for (let i = 0; i < 10; i++) await Promise.resolve(); await nextTick(); };

function response(agentId: string, _group: string, body: Record<string, unknown>): Promise<AgentWorkflowResponse> {
  const workflowId = String(body.workflow_id || "flow-one");
  if (body.action === "get") return Promise.resolve({ ok: true, workflow: flow(agentId, workflowId), capabilities: caps });
  if (body.action === "runs") return Promise.resolve({ ok: true, runs: [run(agentId, workflowId)] });
  if (body.action === "versions") return Promise.resolve({ ok: true, versions: [version(flow(agentId, workflowId))], enabled_version: { ...version(flow(agentId, workflowId)), is_enabled: true }, has_more: false });
  if (body.action === "version") return Promise.resolve({ ok: true, workflow: flow(agentId, workflowId), version: { ...version(flow(agentId, workflowId)), is_enabled: true } });
  if (body.action === "run") return Promise.resolve({ ok: true, run: run(agentId, workflowId, String(body.run_id)), capabilities: caps });
  throw new Error(`Unexpected fixture action ${String(body.action)}`);
}

function mount() {
  const props = reactive({ agentId: "agent-a", sessionId: "session-a", visible: true, conversationOpen: false, immersive: false, toolRevision: 0 });
  const scope = effectScope(), emit = vi.fn();
  scopes.push(scope);
  const c = scope.run(() => (AgentWorkflowSheet as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  for (const mounted of host.mounted.splice(0)) mounted();
  return { c, props, emit };
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal("window", new EventTarget());
  host.serial = 0;
  host.updates = ref(null);
  host.overview = null;
  host.list.mockReset().mockImplementation((aid: string) => Promise.resolve({ ok: true, workflows: [flow(aid)], capabilities: caps }));
  host.call.mockReset().mockImplementation(response);
  host.confirm.mockReset().mockResolvedValue("confirm");
});
afterEach(() => {
  for (const cleanup of host.cleanup.splice(0)) cleanup();
  for (const scope of scopes.splice(0)) scope.stop();
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("internal workflow workspace lifetime", () => {
  it('keeps context usage separate for parallel node sessions and rejects stale run and Agent readings', async () => {
    const { c, props } = mount(); await flush();
    const definition = { ...flow(), steps: [{ id: 'left', kind: 'agent' as const, task: 'Left' }, { id: 'right', kind: 'agent' as const, task: 'Right' }], graph: { version: 1 as const, edges: [] } };
    c.workflowId.value = definition.id; c.workflow.value = definition;
    c.run.value = { ...run(), definition, status: 'running', steps: [{ step_id: 'left', status: 'running', session_id: 'left-session', active_run_id: 'left-turn' }, { step_id: 'right', status: 'running', session_id: 'right-session', active_run_id: 'right-turn' }] };
    const reading: TaskContextReading = { sessionId: 'left-session', activeRunId: 'left-turn', used: 250, window: 1000, percent: 25, free: 750, source: 'measured' };
    c.selectStep('left'); c.setNodeContext('left', reading); expect(c.observedContextReading.value?.percent).toBe(25);
    c.selectStep('right'); expect(c.observedContextReading.value).toBeNull();
    c.setNodeContext('right', reading); expect(c.observedContextReading.value).toBeNull();
    c.setNodeContext('right', { ...reading, sessionId: 'right-session', activeRunId: 'outdated' }); expect(c.observedContextReading.value).toBeNull();
    c.setNodeContext('right', { ...reading, sessionId: 'right-session', activeRunId: 'right-turn', used: 700, percent: 70, free: 300 }); expect(c.observedContextReading.value?.percent).toBe(70);
    c.selectStep('left'); expect(c.observedContextReading.value?.percent).toBe(25);
    c.setNodeContext('left', null, { agentId: 'agent-a', workflowRunId: 'run-one', sessionId: 'right-session', activeRunId: 'right-turn' }); expect(c.observedContextReading.value?.percent).toBe(25);
    c.setNodeContext('left', null, { agentId: 'agent-a', workflowRunId: 'another-run', sessionId: 'left-session', activeRunId: 'left-turn' }); expect(c.observedContextReading.value?.percent).toBe(25);
    c.setNodeContext('left', null, { agentId: 'agent-a', workflowRunId: 'run-one', sessionId: 'left-session', activeRunId: 'left-turn', unmounted: true }); expect(c.observedContextReading.value?.percent).toBe(25); expect(c.observedContextReading.value?.stale).toBe(true);
    c.setNodeContext('left', null, { agentId: 'agent-a', workflowRunId: 'run-one', sessionId: 'left-session', activeRunId: 'left-turn' }); expect(c.observedContextReading.value).toBeNull();
    c.setNodeContext('left', reading);
    c.run.value = { ...c.run.value!, id: 'different-run' }; expect(c.observedContextReading.value).toBeNull();
    c.setNodeContext('left', reading); props.agentId = 'agent-b'; await flush(); expect(c.nodeContextReadings.value).toEqual({});
  });

  it("deletes only the confirmed list item and retains read-only history in the deleted view", async () => {
    const first = flow(), second = { ...flow('agent-a', 'flow-two'), name: '独立测试流程', revision: 7 };
    const deleted = { ...second, deleted: true, archived: true, enabled_version: '' };
    let removed = false;
    host.list.mockImplementation(() => Promise.resolve({ ok: true, workflows: removed ? [first] : [first, second], capabilities: caps }));
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => {
      if (body.action === 'delete') { removed = true; return Promise.resolve({ ok: true, workflow: deleted, capabilities: caps }); }
      if (body.action === 'list') return Promise.resolve({ ok: true, workflows: [first, deleted], capabilities: caps });
      if (body.action === 'get' && body.workflow_id === second.id) return Promise.resolve({ ok: true, workflow: deleted, capabilities: caps });
      return response(aid, group, body);
    });
    const { c } = mount(); await flush();
    await c.deleteWorkflow(second); await flush();
    expect(host.confirm).toHaveBeenCalledWith('agentWorkflow.deleteQuestion', 'agentWorkflow.delete', expect.any(Object));
    expect(host.call.mock.calls.find(([, , body]) => body.action === 'delete')).toMatchObject(['agent-a', 'edit', { action: 'delete', workflow_id: 'flow-two', expected_revision: 7, source_session_id: 'session-a', request_id: expect.any(String) }]);
    expect(c.workflowId.value).toBe('');
    expect(c.visibleWorkflows.value.map(item => item.id)).toEqual(['flow-one']);
    await c.setDeletedView(true); await flush();
    expect(host.call).toHaveBeenCalledWith('agent-a', 'read', { action: 'list', include_deleted: true }, expect.any(AbortSignal));
    expect(c.visibleWorkflows.value).toEqual([deleted]);
    await c.chooseWorkflow('flow-two', 'run-one'); await flush();
    expect(c.workflow.value?.deleted).toBe(true); expect(c.runs.value).toHaveLength(1); expect(c.run.value?.id).toBe('run-one');
    expect(c.workflowActions.value.map(item => item.command)).toEqual(['settings']);
    c.previewVersion.value = version(first);
    expect(c.canRestore.value).toBe(false);
    const count = host.call.mock.calls.length;
    await c.control('retry'); await c.prepareRun('trial');
    expect(c.inputOpen.value).toBe(false); expect(host.call).toHaveBeenCalledTimes(count);
  });
  it("does not delete when confirmation is cancelled or the account scope changes", async () => {
    const { c, props } = mount(); await flush();
    host.confirm.mockRejectedValueOnce('cancel');
    await c.deleteWorkflow(flow());
    expect(host.call.mock.calls.some(([, , body]) => body.action === 'delete')).toBe(false);
    const gate = pending<string>(); host.confirm.mockReturnValueOnce(gate.promise);
    const operation = c.deleteWorkflow(flow()); await flush();
    expect(c.deletingFlowId.value).toBe('flow-one');
    const confirmations = host.confirm.mock.calls.length;
    await c.deleteWorkflow(flow());
    expect(host.confirm).toHaveBeenCalledTimes(confirmations);
    props.agentId = 'agent-b'; await flush(); gate.resolve('confirm'); await operation; await flush();
    expect(host.call.mock.calls.some(([, , body]) => body.action === 'delete')).toBe(false);
    expect(c.deletingFlowId.value).toBe(''); expect(c.showDeleted.value).toBe(false);
  });
  it("keeps a workflow in the list when deletion is rejected and blocks known unfinished runs", async () => {
    const { c } = mount(); await flush();
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => body.action === 'delete' ? Promise.reject(new Error('version conflict')) : response(aid, group, body));
    await c.deleteWorkflow(flow());
    expect(c.visibleWorkflows.value).toHaveLength(1); expect(c.error.value).toBe('version conflict');
    await c.chooseWorkflow('flow-one'); await flush();
    c.runs.value = [{ ...run(), status: 'waiting_confirmation' }];
    expect(c.canDelete(flow())).toBe(false);
    const count = host.call.mock.calls.length; await c.deleteWorkflow(flow());
    expect(host.call).toHaveBeenCalledTimes(count);
    c.runs.value = [{ ...run(), status: 'running', cancel_requested: true }];
    expect(c.canDelete(flow())).toBe(false);
    c.runs.value = [];
    c.capabilities.value = { ...caps, edit: false };
    expect(c.canDelete(flow())).toBe(false);
  });
  it.each(["trial", "start"] as const)("identifies Agent iteration bodies in the frozen %s input notice", async mode => {
    const definition: AgentWorkflowDefinition = { ...flow(), enabled_version: 'a'.repeat(64), steps: [{ id: 'batch', kind: 'iteration', iteration: { items: { type: 'array', value: ['one'] }, body: { id: 'body', kind: 'agent', task: 'Summarize {{iteration.item}}', skills: ['embedded:notes'] } } }] };
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => {
      if (body.action === 'get') return Promise.resolve({ ok: true, workflow: definition, capabilities: caps });
      if (body.action === 'versions') return Promise.resolve({ ok: true, enabled_version: { ...version(definition), is_enabled: true } });
      if (body.action === 'version') return Promise.resolve({ ok: true, workflow: definition, version: { ...version(definition), is_enabled: true } });
      return response(aid, group, body);
    });
    const { c } = mount(); await flush(); await c.chooseWorkflow('flow-one'); await flush();
    await c.prepareRun(mode); await flush();
    expect(c.inputOpen.value).toBe(true); expect(c.runInputNotice.value).toBe('agentWorkflow.realRun');
    expect(c.runInputApprovalMode.value).toBe('standard');
    c.workflow.value = { ...definition, revision: 2, tool_approval_mode: 'auto', steps: flow().steps };
    expect(c.runInputNotice.value).toBe('agentWorkflow.realRun');
    expect(c.runInputApprovalMode.value).toBe('standard');
    expect(host.call.mock.calls.filter(([, group]) => group === 'run')).toHaveLength(0);
  });
  it('automatically observes real iteration Agent sessions and keeps each selected item isolated', async () => {
    const definition: AgentWorkflowDefinition = { ...flow(), steps: [{ id: 'batch', title: 'Batch research', kind: 'iteration', iteration: { items: { type: 'array', value: ['one', 'two', 'three'] }, body: { id: 'body', kind: 'agent', task: 'Summarize {{iteration.item}}' } } }] };
    let current: AgentWorkflowRun = { ...run(), status: 'running', definition, execution_session_id: 'outer-conversation', steps: [{ step_id: 'batch', status: 'running', repeat: { kind: 'iteration', total: 3, items: [
      { index: 0, status: 'completed', session_id: 'item-one-session', active_run_id: 'item-one-engine' },
      { index: 1, status: 'running', session_id: 'item-two-session', active_run_id: 'item-two-engine' },
      { index: 2, status: 'pending' },
    ] } }] };
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => body.action === 'get' ? Promise.resolve({ ok: true, workflow: definition, capabilities: caps }) : body.action === 'run' ? Promise.resolve({ ok: true, run: current, capabilities: caps }) : response(aid, group, body));
    const { c, emit } = mount(); await flush(); await c.chooseWorkflow('flow-one', 'run-one'); await flush();
    expect(c.showProcess.value).toBe(true); expect(emit).toHaveBeenCalledWith('conversation', false);
    expect(c.processSteps.value).toHaveLength(2);
    expect(c.processSessionId.value).toBe('item-two-session'); expect(c.processActiveRunId.value).toBe('item-two-engine');
    c.selectStep('batch'); expect(c.processSessionId.value).toBe('item-two-session');
    c.chooseProcessStep('batch:0'); expect(c.processSessionId.value).toBe('item-one-session'); expect(c.processActiveRunId.value).toBe('item-one-engine');
    current = { ...current, steps: [{ ...current.steps![0]!, repeat: { ...current.steps![0]!.repeat!, items: current.steps![0]!.repeat!.items.map(item => ({ ...item, status: 'completed' })) } }] };
    await c.refresh(); await flush();
    expect(c.processSessionId.value).toBe('item-one-session'); expect(c.processActiveRunId.value).toBe('item-one-engine');
    expect(c.processSteps.value.every(item => item.sessionId !== 'outer-conversation')).toBe(true);
  });
  it('keeps Code-only iteration runs out of Agent process and model-call notices', async () => {
    const definition: AgentWorkflowDefinition = { ...flow(), steps: [{ id: 'batch', kind: 'iteration', iteration: { items: { type: 'array', value: [1] }, body: { id: 'body', kind: 'code', code: { language: 'python', source: 'def main(inputs):\n return {"result": 1}', inputs: {}, outputs: { result: { type: 'integer' } } } } } }] };
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => body.action === 'get' ? Promise.resolve({ ok: true, workflow: definition, capabilities: caps }) : body.action === 'run' ? Promise.resolve({ ok: true, run: { ...run(), definition, status: 'running', execution_session_id: 'outer-conversation' }, capabilities: caps }) : response(aid, group, body));
    const { c } = mount(); await flush(); await c.chooseWorkflow('flow-one'); await flush(); await c.prepareRun('trial');
    expect(c.runInputNotice.value).toBe('agentWorkflow.codeRun');
    await c.chooseRun('run-one'); await flush();
    expect(c.showProcess.value).toBe(false); expect(c.processSessionId.value).toBe(''); expect(c.processSteps.value).toEqual([]);
  });
  it('submits only the selected code node with the version frozen by its sample form', async () => {
    const definition = { ...flow(), steps: [{ id: 'calculate', kind: 'code' as const, code: { language: 'python' as const, source: "def main(inputs):\n    return {'total': 1}", inputs: {}, outputs: { total: { type: 'number' as const } } } }] };
    host.call.mockImplementation((aid:string,group:string,body:Record<string,unknown>) => body.action === 'get' ? Promise.resolve({ok:true, workflow:definition, capabilities:caps}) : group === 'run' ? Promise.resolve({ok:false}) : response(aid,group,body));
    const {c}=mount(); await flush(); await c.chooseWorkflow('flow-one'); await flush();
    const sample = {stepId:'calculate', inputs:{amount:12}, expectedRevision:1, expectedHash:'a'.repeat(64)};
    c.workflow.value = {...definition, revision:2, definition_hash:'b'.repeat(64)};
    await c.testCodeNode(sample);
    const request=host.call.mock.calls.find(([,group])=>group==='run')![2];
    expect(request).toMatchObject({action:'test_node',step_id:'calculate',node_inputs:{amount:12},expected_revision:1,expected_hash:'a'.repeat(64),source_session_id:'session-a'});
    expect(request.inputs).toBeUndefined(); expect(request.files).toBeUndefined();
    c.editorDirty.value=true; await c.testCodeNode(sample);
    expect(host.call.mock.calls.filter(([,group])=>group==='run')).toHaveLength(1);
  });
  it('opens the code test target without showing an unrelated Agent execution session', async () => {
    const {c}=mount(); await flush(); await c.chooseWorkflow('flow-one'); await flush();
    const focus=vi.fn(); c.editor.value={prepareClose:async()=>true,focusStep:focus};
    c.run.value={...run(),id:'code-test',status:'running',execution_session_id:'unrelated-old-session',node_test:{step_id:'calculate',inputs:{}},definition:{...flow(),steps:[{id:'calculate',kind:'code',code:{language:'javascript',source:'function main(inputs){return {value:1}}',inputs:{},outputs:{value:{type:'number'}}}}]},steps:[{step_id:'calculate',status:'running'}]};
    await flush(); expect(c.showProcess.value).toBe(false); expect(c.processSessionId.value).toBe(''); expect(focus).toHaveBeenCalledWith('calculate');
  });
  it('reuses an uncertain start only within its original source conversation',async()=>{
    host.call.mockImplementation((aid:string,group:string,body:Record<string,unknown>)=>{if(group==='run')return Promise.reject(new Error('response lost'));return response(aid,group,body);});
    const{c,props}=mount();await flush();await c.chooseWorkflow('flow-one');await flush();
    await c.prepareRun('trial');c.inputs.value.topic='same';c.files.value='source.md';await c.start();await c.start();
    props.sessionId='session-b';await flush();await c.prepareRun('trial');c.inputs.value.topic='same';c.files.value='source.md';await c.start();
    const requests=host.call.mock.calls.filter(([,group])=>group==='run').map(([, ,body])=>body);
    expect(requests).toHaveLength(3);expect(requests[0]!.request_id).toBe(requests[1]!.request_id);expect(requests[2]!.request_id).not.toBe(requests[0]!.request_id);
    expect(requests.map(body=>body.source_session_id)).toEqual(['session-a','session-a','session-b']);
  });
  it('submits the same unique file count as the picker and rejects more than sixteen unique files',async()=>{
    host.call.mockImplementation((aid:string,group:string,body:Record<string,unknown>)=>group==='run'?Promise.resolve({ok:false}):response(aid,group,body));
    const{c}=mount();await flush();await c.chooseWorkflow('flow-one');await flush();await c.prepareRun('trial');
    c.inputs.value.topic='files';c.files.value=Array.from({length:17},()=> 'source.md').join('\n');await c.start();
    const requests=host.call.mock.calls.filter(([,group])=>group==='run');expect(requests).toHaveLength(1);expect(requests[0]![2].files).toEqual(['source.md']);
    c.files.value=Array.from({length:17},(_,i)=>`${i}.md`).join('\n');await c.start();expect(host.call.mock.calls.filter(([,group])=>group==='run')).toHaveLength(1);expect(c.error.value).toBe('agentWorkflow.inputFilesTooMany');
  });
  it("uses declared defaults and keeps the reviewed draft schema/version while a trial form is open", async () => {
    const current = { ...flow(), inputs: [{ key: 'topic' }, { key: 'review', type: 'boolean' as const, default: 'yes' }, { key: 'audience', type: 'select' as const, default: 'team', options: [{ value: 'team', label: '同事' }] }] };
    host.call.mockImplementation((aid:string,group:string,body:Record<string,unknown>)=>body.action==='get'?Promise.resolve({ok:true,workflow:current,capabilities:caps}):group==='run'?Promise.resolve({ok:false}):response(aid,group,body));
    const {c}=mount();await flush();await c.chooseWorkflow('flow-one');await flush();await c.prepareRun('trial');
    expect(c.inputs.value).toEqual({topic:'',review:'yes',audience:'team'});c.inputs.value.topic='季度报告';
    c.workflow.value={...current,revision:2,definition_hash:'b'.repeat(64),inputs:[{key:'subject'}]};
    expect(c.inputFields.value?.map(field=>field.key)).toEqual(['topic','review','audience']);await c.start();
    const calls=host.call.mock.calls.filter(([,group])=>group==='run');expect(calls).toHaveLength(1);expect(calls[0]![2]).toMatchObject({action:'trial',expected_revision:1,expected_hash:'a'.repeat(64),inputs:{topic:'季度报告',review:'yes',audience:'team'}});
  });
  it("previews an exact saved version without replacing the current draft or starting a Run", async () => {
    const old = flow(), current = { ...flow(), revision: 3, definition_hash: "c".repeat(64), name: "Latest draft" };
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => body.action === "get" ? Promise.resolve({ ok: true, workflow: current, capabilities: caps }) : body.action === "version" ? Promise.resolve({ ok: true, workflow: old, version: version(old), capabilities: caps }) : response(aid, group, body));
    const { c } = mount(); await flush(); await c.chooseWorkflow("flow-one"); await flush();
    await c.previewSavedVersion(version(old)); await flush();
    expect(c.previewDefinition.value?.revision).toBe(1);
    expect(c.workflow.value?.revision).toBe(3);
    expect(c.canRestore.value).toBe(true);
    expect(c.runId.value).toBe("");
    expect(c.workflowActions.value.find(action => action.command === "enable")?.disabled).toBe(true);
    await c.refresh(); await flush();
    expect(c.previewDefinition.value?.definition_hash).toBe(old.definition_hash);
    expect(host.call.mock.calls.filter(([, group]) => group === "run" || group === "enable")).toHaveLength(0);
  });

  it("checks unsaved edits after the version read before switching the canvas", async () => {
    const reading = pending<AgentWorkflowResponse>(), current = { ...flow(), revision: 3, definition_hash: "c".repeat(64) };
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => body.action === "get" ? Promise.resolve({ ok: true, workflow: current, capabilities: caps }) : body.action === "version" ? reading.promise : response(aid, group, body));
    const { c } = mount(); await flush(); await c.chooseWorkflow("flow-one"); await flush();
    const prepareClose = vi.fn().mockResolvedValue(false); c.editor.value = { prepareClose, focusStep: vi.fn() };
    const preview = c.previewSavedVersion(version(flow())); c.editorDirty.value = true;
    reading.resolve({ ok: true, workflow: flow(), version: version(flow()) }); await preview; await flush();
    expect(prepareClose).toHaveBeenCalledTimes(1);
    expect(c.previewDefinition.value).toBeNull(); expect(c.editorDirty.value).toBe(true);
  });

  it("restores a preview into a new draft with exact revision/hash and leaves activation explicit", async () => {
    const old = flow(); let current = { ...flow(), revision: 3, definition_hash: "c".repeat(64), enabled_version: "b".repeat(64), name: "Latest draft" };
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => {
      if (body.action === "get") return Promise.resolve({ ok: true, workflow: current, capabilities: caps });
      if (body.action === "version") return Promise.resolve({ ok: true, workflow: old, version: version(old) });
      if (group === "edit" && body.action === "restore") { current = { ...current, revision: 4, definition_hash: "d".repeat(64), name: old.name }; return Promise.resolve({ ok: true, workflow: current }); }
      return response(aid, group, body);
    });
    const { c } = mount(); await flush(); await c.chooseWorkflow("flow-one"); await flush();
    await c.previewSavedVersion(version(old)); await flush(); await c.restoreSavedVersion(); await flush();
    const restores = host.call.mock.calls.filter(([, group, body]) => group === "edit" && body.action === "restore");
    expect(restores).toHaveLength(1); expect(restores[0]![2]).toMatchObject({ workflow_id: "flow-one", expected_revision: 3, version_id: old.definition_hash, target_revision: 1 });
    expect(c.previewDefinition.value).toBeNull(); expect(c.workflow.value?.revision).toBe(4);
    expect(c.workflow.value?.enabled_version).toBe("b".repeat(64));
    expect(host.call.mock.calls.filter(([, group]) => group === "run" || group === "enable")).toHaveLength(0);
  });

  it("requires another review if the draft changes while restore confirmation is open", async () => {
    const old = flow(), current = { ...flow(), revision: 3, definition_hash: "c".repeat(64) };
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => body.action === "get" ? Promise.resolve({ ok: true, workflow: current, capabilities: caps }) : body.action === "version" ? Promise.resolve({ ok: true, workflow: old, version: version(old) }) : response(aid, group, body));
    const { c } = mount(); await flush(); await c.chooseWorkflow("flow-one"); await flush(); await c.previewSavedVersion(version(old)); await flush();
    const confirmation = pending<string>(); host.confirm.mockReturnValue(confirmation.promise);
    const restoring = c.restoreSavedVersion(); c.workflow.value = { ...current, revision: 4, definition_hash: "d".repeat(64) };
    confirmation.resolve("confirm"); await restoring; await flush();
    expect(c.versionsError.value).toBe("agentWorkflow.restoreChanged");
    expect(host.call.mock.calls.filter(([, group]) => group === "edit")).toHaveLength(0);
  });
  it("refreshes the selected canvas immediately on a live save without replacing the editor", async () => {
    const { c } = mount();
    await flush(); await c.chooseWorkflow("flow-one"); await flush();
    const editor = { prepareClose: vi.fn(), focusStep: vi.fn() };
    c.editor.value = editor; const mountedEditor = c.editor.value;
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => body.action === "get" ? Promise.resolve({ ok: true, capabilities: caps, workflow: { ...flow(aid), revision: 2, name: "实时修改", definition_hash: "b".repeat(64) } }) : response(aid, group, body));
    host.updates!.value = { sessionId: "session-a", agentId: "agent-a", workflowId: "flow-one", action: "patch", revision: 2, seq: 10 };
    await flush();
    expect(c.workflow.value?.revision).toBe(2);
    expect(c.workflow.value?.name).toBe("实时修改");
    expect(c.editor.value).toBe(mountedEditor);
    expect(editor.prepareClose).not.toHaveBeenCalled();
  });

  it("does not switch a historical Run to its edited draft or accept another Agent's event", async () => {
    const { c } = mount(); await flush(); await c.chooseWorkflow("flow-one", "run-one"); await flush();
    const previous = c.run.value?.definition;
    const calls = host.call.mock.calls.length;
    host.updates!.value = { sessionId: "session-a", agentId: "agent-other", workflowId: "flow-one", action: "patch", seq: 10 };
    await flush(); expect(host.call.mock.calls).toHaveLength(calls);
    host.updates!.value = { sessionId: "another-session", agentId: "agent-a", workflowId: "flow-one", action: "patch", seq: 11 };
    await flush(); expect(host.call.mock.calls).toHaveLength(calls);
    host.updates!.value = { sessionId: "session-a", agentId: "agent-a", workflowId: "flow-one", action: "patch", seq: 12 };
    await flush();
    expect(c.runId.value).toBe("run-one");
    expect(c.run.value?.definition).toEqual(previous);
  });

  it("shows a newly created workflow from the list and keeps an existing draft selected", async () => {
    const { c } = mount(); await flush();
    host.updates!.value = { sessionId: "session-a", agentId: "agent-a", workflowId: "flow-new", action: "create", seq: 10 };
    await flush(); expect(c.workflowId.value).toBe("flow-new");
    host.updates!.value = { sessionId: "session-a", agentId: "agent-a", workflowId: "flow-other", action: "create", seq: 11 };
    await flush(); expect(c.workflowId.value).toBe("flow-new");
  });
  it("opens the existing conversation without replacing an unsaved editor or sending a request", async () => {
    const { c, props, emit } = mount();
    await flush(); await c.chooseWorkflow("flow-one"); await flush();
    const prepareClose = vi.fn().mockResolvedValue(false), focusStep = vi.fn();
    const editor = { prepareClose, focusStep };
    c.editor.value = editor; c.editorDirty.value = true;
    const selectedEditor = c.editor.value;
    c.controlOpen.value = true; c.showProcess.value = true;
    const calls = host.call.mock.calls.length;
    c.toggleConversation();
    expect(emit).toHaveBeenCalledWith("conversation", true);
    expect(c.controlOpen.value).toBe(false);
    expect(c.showProcess.value).toBe(false);
    props.conversationOpen = true; await flush();
    c.selectStep("review");
    expect(emit).toHaveBeenCalledWith("conversation", false);
    expect(c.editor.value).toBe(selectedEditor);
    expect(c.editorDirty.value).toBe(true);
    expect(prepareClose).not.toHaveBeenCalled();
    expect(host.call.mock.calls).toHaveLength(calls);
  });

  it("closes the execution drawer without cancelling the current Run or changing its snapshot", async () => {
    const { c, emit } = mount();
    await flush(); await c.chooseWorkflow("flow-one", "run-one"); await flush();
    const original = c.run.value, calls = host.call.mock.calls.length;
    c.toggleProcess();
    expect(c.showProcess.value).toBe(true);
    expect(emit).toHaveBeenCalledWith("conversation", false);
    c.toggleProcess();
    expect(c.showProcess.value).toBe(false);
    expect(c.run.value).toBe(original);
    expect(c.runId.value).toBe("run-one");
    expect(host.call.mock.calls).toHaveLength(calls);
  });

  it("keeps draft testing and enabled-version execution distinct in the compact actions", async () => {
    const { c } = mount();
    await flush();
    await c.chooseWorkflow("flow-one");
    await flush();

    await c.workflowAction("start");
    expect(c.inputOpen.value).toBe(false);
    await c.workflowAction("trial");
    expect(c.inputOpen.value).toBe(true);
    expect(c.inputMode.value).toBe("trial");

    c.inputOpen.value = false;
    c.workflow.value!.enabled_version = "a".repeat(64);
    await c.workflowAction("start");
    expect(c.inputOpen.value).toBe(true);
    expect(c.inputMode.value).toBe("start");
    expect(host.call.mock.calls.some(([, group]) => group === "run")).toBe(false);
  });

  it("rechecks unsaved edits and revoked permissions before dispatching a menu action", async () => {
    const { c, emit } = mount();
    await flush();
    await c.chooseWorkflow("flow-one");
    await flush();
    c.workflow.value!.enabled_version = "a".repeat(64);
    const calls = host.call.mock.calls.length;

    c.editorDirty.value = true;
    for (const command of ["edit", "validate", "trial", "enable", "start"]) await c.workflowAction(command);
    expect(c.inputOpen.value).toBe(false);
    expect(host.call.mock.calls).toHaveLength(calls);
    expect(emit.mock.calls.some(([event]) => event === "intent")).toBe(false);

    c.editorDirty.value = false;
    c.capabilities.value = { read: true, edit: false, run: false, enable: false };
    for (const command of ["edit", "trial", "enable", "start", "disable", "archive"]) await c.workflowAction(command);
    expect(c.inputOpen.value).toBe(false);
    expect(host.call.mock.calls).toHaveLength(calls);
  });

  it("uses the enabled snapshot inputs and submits its frozen version when activation changes", async () => {
    const enabled = flow(), enabledSummary = { ...version(enabled), is_enabled: true };
    const current: AgentWorkflowDefinition = { ...flow(), revision: 3, tool_approval_mode: 'auto', definition_hash: "c".repeat(64), enabled_version: enabled.definition_hash, inputs: [{ key: "draft_only", label: "草稿参数" }] };
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => {
      if (body.action === "get") return Promise.resolve({ ok: true, workflow: current, capabilities: caps });
      if (body.action === "versions") return Promise.resolve({ ok: true, versions: [version(current), enabledSummary], enabled_version: enabledSummary });
      if (body.action === "version") return Promise.resolve({ ok: true, workflow: enabled, version: enabledSummary });
      if (group === "run") return Promise.reject(new Error("Enabled version changed; review and retry"));
      return response(aid, group, body);
    });
    const { c } = mount(); await flush(); await c.chooseWorkflow("flow-one"); await flush();
    await c.prepareRun("start");
    expect(c.inputFields.value).toEqual(enabled.inputs); expect(c.inputs.value).toEqual({ topic: "" });
    expect(c.runInputApprovalMode.value).toBe('standard');
    c.inputs.value.topic = "项目资料";
    c.workflow.value = { ...current, enabled_version: "b".repeat(64), inputs: [{ key: "new_draft", label: "新参数" }] };
    expect(c.inputFields.value).toEqual(enabled.inputs);
    expect(c.runInputApprovalMode.value).toBe('standard');
    await c.start();
    const starts = host.call.mock.calls.filter(([, group]) => group === "run");
    expect(starts).toHaveLength(1);
    expect(starts[0]![2]).toMatchObject({ action: "start", workflow_id: "flow-one", version_id: enabled.definition_hash, inputs: { topic: "项目资料" } });
    expect(c.error.value).toBe("Enabled version changed; review and retry");
    expect(c.inputOpen.value).toBe(true); expect(c.runId.value).toBe("");
  });

  it('shows the saved draft approval mode for a test independently of the enabled version', async () => {
    const { c } = mount(); await flush(); await c.chooseWorkflow('flow-one'); await flush();
    c.workflow.value = { ...flow(), revision: 2, tool_approval_mode: 'auto', definition_hash: 'b'.repeat(64), enabled_version: 'a'.repeat(64) };
    await c.prepareRun('trial');
    expect(c.runInputApprovalMode.value).toBe('auto');
    c.workflow.value = { ...c.workflow.value, tool_approval_mode: 'strict' };
    expect(c.runInputApprovalMode.value).toBe('auto');
  });

  it("aborts run preparation on navigation and session changes and ignores late snapshot reads", async () => {
    let reading = pending<AgentWorkflowResponse>(), signal: AbortSignal | undefined;
    const enabled = flow(), summary = { ...version(enabled), is_enabled: true }, current = { ...flow(), enabled_version: enabled.definition_hash };
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>, requestSignal?: AbortSignal) => {
      if (body.action === "get") return Promise.resolve({ ok: true, workflow: current, capabilities: caps });
      if (body.action === "versions") return Promise.resolve({ ok: true, versions: [summary], enabled_version: summary });
      if (body.action === "version") { signal = requestSignal; return reading.promise; }
      return response(aid, group, body);
    });
    const { c, props } = mount(); await flush(); await c.chooseWorkflow("flow-one"); await flush();
    const first = c.prepareRun("start"); await flush(); expect(c.inputLoading.value).toBe(true);
    await c.chooseRun("run-one"); expect(signal?.aborted).toBe(true);
    reading.resolve({ ok: true, workflow: enabled, version: summary }); await first; await flush();
    expect(c.inputOpen.value).toBe(false); expect(c.runId.value).toBe("run-one");
    await c.chooseRun(""); await flush();
    reading = pending<AgentWorkflowResponse>();
    const second = c.prepareRun("start"); await flush();
    props.sessionId = "session-b"; expect(signal?.aborted).toBe(true);
    reading.resolve({ ok: true, workflow: enabled, version: summary }); await second; await flush();
    expect(c.inputOpen.value).toBe(false); expect(c.inputs.value).toEqual({}); expect(c.inputLoading.value).toBe(false);
    await c.start(); expect(host.call.mock.calls.some(([, group]) => group === "run")).toBe(false);
  });

  it("keeps the selected canvas and unsaved state when the same workspace is hidden and reopened", async () => {
    const { c, props } = mount();
    await flush();
    await c.chooseWorkflow("flow-one");
    await flush();
    c.editorDirty.value = true;
    const prepareClose = vi.fn().mockResolvedValue(false);
    c.editor.value = { prepareClose, focusStep: vi.fn() };

    props.visible = false;
    await flush();
    const before = host.list.mock.calls.length;
    await vi.advanceTimersByTimeAsync(12000);
    expect(host.list).toHaveBeenCalledTimes(before);
    expect(c.workflowId.value).toBe("flow-one");
    expect(c.workflow.value?.id).toBe("flow-one");
    expect(c.editorDirty.value).toBe(true);

    props.visible = true;
    await flush();
    expect(c.workflowId.value).toBe("flow-one");
    expect(c.workflow.value?.id).toBe("flow-one");
    expect(c.editorDirty.value).toBe(true);
    expect(prepareClose).not.toHaveBeenCalled();
  });

  it("stays on a dirty draft when leaving for a Run or the list is declined", async () => {
    const { c, emit } = mount();
    await flush();
    await c.chooseWorkflow("flow-one");
    await flush();
    c.editorDirty.value = true;
    const prepareClose = vi.fn().mockResolvedValue(false);
    c.editor.value = { prepareClose, focusStep: vi.fn() };
    const reads = host.call.mock.calls.length;

    await c.chooseRun("run-one");
    await c.back();
    c.prepareRun("trial");
    expect(prepareClose).toHaveBeenCalledTimes(2);
    expect(c.workflowId.value).toBe("flow-one");
    expect(c.runId.value).toBe("");
    expect(c.editorDirty.value).toBe(true);
    expect(c.inputOpen.value).toBe(false);
    expect(host.call).toHaveBeenCalledTimes(reads);
    expect(emit).not.toHaveBeenCalledWith("expand", false);
  });

  it.each(["Agent", "account"])("clears private state on %s change and ignores old list and artifact responses", async change => {
    const { c, props } = mount();
    await flush();
    await c.chooseWorkflow("flow-one", "run-one");
    await flush();
    const oldList = pending<AgentWorkflowResponse>(), oldArtifact = pending<AgentWorkflowResponse>();
    host.list.mockReturnValueOnce(oldList.promise).mockResolvedValueOnce({ ok: true, workflows: [flow(change === "Agent" ? "agent-b" : "agent-a", "new-owner-flow")], capabilities: caps });
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => body.action === "artifact" ? oldArtifact.promise : response(aid, group, body));
    const readingList = c.refresh(), readingArtifact = c.artifact("review");
    c.inputs.value = { topic: "private input" };
    c.files.value = "private-source.md";
    c.inputOpen.value = true;
    c.feedback.value = "private confirmation";
    c.confirmationFeedback.value = { review: "private node feedback" };
    c.selectedStepId.value = "review";
    c.artifactText.value = "private report";
    c.artifactPath.value = "private-report.md";
    c.artifactOpen.value = true;
    c.createName.value = "private new flow";
    c.createTask.value = "private task";
    c.createOpen.value = true;
    c.editorDirty.value = true;

    if (change === "Agent") props.agentId = "agent-b";
    else window.dispatchEvent(new Event("pogo:auth-changed"));
    await flush();
    expect(c.workflow.value).toBeNull();
    expect(c.workflowId.value).toBe("");
    expect(c.run.value).toBeNull();
    expect(c.runId.value).toBe("");
    expect(c.runs.value).toEqual([]);
    expect(c.inputs.value).toEqual({});
    expect(c.files.value).toBe("");
    expect(c.feedback.value).toBe("");
    expect(c.confirmationFeedback.value).toEqual({});
    expect(c.selectedStepId.value).toBe("");
    expect(c.artifactText.value).toBe("");
    expect(c.artifactPath.value).toBe("");
    expect(c.inputOpen.value || c.artifactOpen.value || c.createOpen.value || c.editorDirty.value).toBe(false);
    expect(c.createName.value).toBe("");
    expect(c.createTask.value).toBe("");

    oldList.resolve({ ok: true, workflows: [flow("agent-a", "old-private-flow")], capabilities: caps });
    oldArtifact.resolve({ ok: true, text: "late private report", path: "late-report.md" });
    await Promise.all([readingList, readingArtifact]);
    await flush();
    expect(c.workflows.value.map(item => item.id)).toEqual(["new-owner-flow"]);
    expect(c.workflow.value).toBeNull();
    expect(c.artifactText.value).toBe("");
    expect(c.artifactOpen.value).toBe(false);
  });

  it("retries an uncertain create with the same step identity and request ID", async () => {
    const { c } = mount();
    await flush();
    let attempts = 0;
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => {
      if (body.action !== "create") return response(aid, group, body);
      if (++attempts === 1) return Promise.reject(new Error("connection lost after submission"));
      return Promise.resolve({ ok: true, workflow: flow(aid, "created-flow"), capabilities: caps });
    });
    c.createOpen.value = true;
    c.createName.value = "资料整理";
    c.createTask.value = "整理测试资料";
    c.createOutput.value = "notes.md";
    await c.createWorkflow();
    expect(c.createOpen.value).toBe(true);
    expect(c.error.value).toContain("connection lost");
    await c.createWorkflow();
    await flush();

    const creates = host.call.mock.calls.filter(([, , body]) => body.action === "create");
    expect(creates).toHaveLength(2);
    expect(creates[0]![2].request_id).toBeTruthy();
    expect(creates[0]![2].steps[0].id).toMatch(/^step_[A-Za-z0-9_]+$/);
    expect(creates[1]![2]).toEqual(creates[0]![2]);
    expect(c.workflowId.value).toBe("created-flow");
    expect(c.createOpen.value).toBe(false);
  });

  it("keeps the Agent canvas when changing source conversations and closes the previous trial inputs", async () => {
    const { c, props } = mount();
    await flush();
    await c.chooseWorkflow("flow-one");
    await flush();
    const prepareClose = vi.fn().mockResolvedValue(true);
    c.editor.value = { prepareClose, focusStep: vi.fn() };
    c.prepareRun("trial");
    c.inputs.value.topic = "first conversation input";
    c.files.value = "first-session-source.md";
    expect(c.inputOpen.value).toBe(true);
    const reads = host.call.mock.calls.length;
    props.sessionId = "session-b";
    await flush();

    expect(c.workflowId.value).toBe("flow-one");
    expect(c.workflow.value?.id).toBe("flow-one");
    expect(c.editor.value?.prepareClose).toBe(prepareClose);
    expect(c.inputOpen.value).toBe(false);
    expect(host.call).toHaveBeenCalledTimes(reads);
    expect(prepareClose).not.toHaveBeenCalled();
    c.prepareRun("trial");
    expect(c.inputs.value).toEqual({ topic: "" });
    expect(c.files.value).toBe("");
  });

  it.each(['running', 'waiting_confirmation'])("accepts cancellation of a %s Run and waits for its real terminal state", async status => {
    let current: AgentWorkflowRun = { ...run(), status, inputs: { topic: 'Original input' }, steps: [{ step_id: 'review', status: status === 'waiting_confirmation' ? status : 'running', summary: 'Keep prior work' }] };
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => {
      if (group === 'run' && body.action === 'cancel') { current = { ...current, cancel_requested: true }; return Promise.resolve({ ok: true, run: current, capabilities: caps }); }
      if (body.action === 'run') return Promise.resolve({ ok: true, run: current, capabilities: caps });
      if (body.action === 'runs') return Promise.resolve({ ok: true, runs: [current] });
      return response(aid, group, body);
    });
    const { c } = mount(); await flush(); await c.chooseWorkflow('flow-one', 'run-one'); await flush();
    await c.control('cancel'); await flush();
    expect(host.confirm).toHaveBeenCalledWith('agentWorkflow.cancelQuestion', 'agentWorkflow.cancelRun', expect.any(Object));
    expect(host.call.mock.calls.filter(([, group, body]) => group === 'run' && body.action === 'cancel')).toHaveLength(1);
    expect(host.call.mock.calls.find(([, group, body]) => group === 'run' && body.action === 'cancel')![2]).toMatchObject({ workflow_id: 'flow-one', run_id: 'run-one', source_session_id: 'session-a', request_id: expect.any(String) });
    expect(c.runId.value).toBe('run-one'); expect(c.run.value?.status).toBe(status); expect(c.run.value?.cancel_requested).toBe(true); expect(c.waitingSteps.value).toEqual([]);
    current = { ...current, status: 'cancelled' };
    await vi.advanceTimersByTimeAsync(2000); await flush();
    expect(c.run.value?.status).toBe('cancelled'); expect(c.run.value?.inputs).toEqual({ topic: 'Original input' }); expect(c.run.value?.steps?.[0]?.summary).toBe('Keep prior work');
  });
  it.each(['failed', 'cancelled', 'interrupted'])("opens the new Run after agreeing to retry a %s Run and retains the original history", async status => {
    const original: AgentWorkflowRun = { ...run(), status, inputs: { topic: 'Original input' }, steps: [{ step_id: 'review', status, summary: 'Historical result' }] };
    const retried: AgentWorkflowRun = { ...original, id: 'run-two', retry_of: original.id, status: 'queued', steps: [{ step_id: 'review', status: 'pending' }] };
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => {
      if (group === 'run' && body.action === 'retry') return Promise.resolve({ ok: true, run: retried, capabilities: caps });
      if (body.action === 'run') return Promise.resolve({ ok: true, run: body.run_id === 'run-two' ? retried : original, capabilities: caps });
      if (body.action === 'runs') return Promise.resolve({ ok: true, runs: [original, retried] });
      return response(aid, group, body);
    });
    const { c } = mount(); await flush(); await c.chooseWorkflow('flow-one', 'run-one'); await flush();
    await c.control('retry'); await flush();
    expect(host.confirm).toHaveBeenCalledWith('agentWorkflow.retryQuestion', 'agentWorkflow.retry', expect.any(Object));
    const calls = host.call.mock.calls.filter(([, group, body]) => group === 'run' && body.action === 'retry');
    expect(calls).toHaveLength(1); expect(calls[0]![2]).toMatchObject({ workflow_id: 'flow-one', run_id: 'run-one', source_session_id: 'session-a', request_id: expect.any(String) });
    expect(c.runId.value).toBe('run-two'); expect(c.run.value).toMatchObject({ id: 'run-two', retry_of: 'run-one', status: 'queued', inputs: original.inputs, definition: original.definition });
    expect(c.runs.value.find(item => item.id === 'run-one')).toEqual(original);
    await c.chooseRun('run-one'); await flush(); expect(c.run.value).toEqual(original); expect(c.run.value?.steps?.[0]?.summary).toBe('Historical result');
  });
  it("does not offer confirmation or submit a retry for a completed Run", async () => {
    const { c } = mount();
    await flush();
    await c.chooseWorkflow("flow-one", "run-one");
    await flush();
    expect(c.run.value?.status).toBe("completed");
    const requests = host.call.mock.calls.length;

    await c.control("retry");
    expect(host.confirm).not.toHaveBeenCalled();
    expect(host.call).toHaveBeenCalledTimes(requests);
  });

  it.each(["failed", "cancelled", "interrupted"])("asks before retrying a %s Run and leaves it unchanged when declined", async status => {
    const { c } = mount();
    await flush();
    await c.chooseWorkflow("flow-one", "run-one");
    await flush();
    c.run.value = { ...run(), status };
    host.confirm.mockRejectedValueOnce(new Error("user declined"));
    const requests = host.call.mock.calls.length;

    await c.control("retry");
    expect(host.confirm).toHaveBeenCalledWith("agentWorkflow.retryQuestion", "agentWorkflow.retry", expect.any(Object));
    expect(host.call).toHaveBeenCalledTimes(requests);
    expect(c.run.value?.status).toBe(status);
    expect(c.runId.value).toBe("run-one");
  });
});

describe("graph run node controls", () => {
  const graphRun = (): AgentWorkflowRun => {
    const definition: AgentWorkflowDefinition = {
      ...flow(), graph: { version: 1, edges: [
        { source: "__input", target: "fork" }, { source: "fork", target: "left" }, { source: "fork", target: "right" },
        { source: "left", target: "reviewA" }, { source: "right", target: "reviewB" },
        { source: "reviewA", target: "merge" }, { source: "reviewB", target: "merge" }, { source: "merge", target: "__output" },
      ] },
      steps: [
        { id: "fork", kind: "parallel" },
        { id: "left", kind: "agent", task: "整理资料" },
        { id: "reviewA", kind: "confirm", prompt: "确认A" },
        { id: "right", kind: "agent", task: "核对来源" },
        { id: "reviewB", kind: "confirm", prompt: "确认B" },
        { id: "merge", kind: "join" },
      ],
    };
    return { ...run(), status: "running", definition, current_step: 0, execution_session_id: "left-session", steps: [
      { step_id: "fork", status: "completed" },
      { step_id: "left", status: "completed", session_id: "left-session" },
      { step_id: "reviewA", status: "waiting_confirmation" },
      { step_id: "right", status: "running", session_id: "right-session" },
      { step_id: "reviewB", status: "waiting_confirmation" },
      { step_id: "merge", status: "queued" },
    ] };
  };
  it("confirms the selected waiting node while another branch runs, retaining other feedback", async () => {
    const { c } = mount();
    await flush();
    await c.chooseWorkflow("flow-one", "run-one");
    await flush();
    let current = graphRun();
    c.run.value = current;
    host.call.mockImplementation((aid: string, group: string, body: Record<string, unknown>) => {
      if (group === "confirm") {
        current = { ...current, steps: current.steps!.map(step => step.step_id === body.step_id ? { ...step, status: "completed", feedback: String(body.feedback) } : step) };
        return Promise.resolve({ ok: true, run: current, capabilities: caps });
      }
      if (body.action === "run") return Promise.resolve({ ok: true, run: current, capabilities: caps });
      return response(aid, group, body);
    });
    c.setFeedback("reviewA", "先核对A");
    c.setFeedback("reviewB", "B可以继续");
    expect(c.waitingSteps.value.map(step => step.id)).toEqual(["reviewA", "reviewB"]);
    await c.confirm("reviewB");
    await flush();
    const confirmations = host.call.mock.calls.filter(([, group]) => group === "confirm");
    expect(confirmations).toHaveLength(1);
    expect(confirmations[0]![2]).toMatchObject({ run_id: "run-one", step_id: "reviewB", feedback: "B可以继续" });
    expect(c.confirmationFeedback.value).toEqual({ reviewA: "先核对A" });
    expect(c.waitingSteps.value.map(step => step.id)).toEqual(["reviewA"]);
    await c.confirm("reviewB");
    await c.confirm("right");
    expect(host.call.mock.calls.filter(([, group]) => group === "confirm")).toHaveLength(1);
  });
  it("selects the actual node execution session and does not attach a session to a control node", async () => {
    const { c } = mount();
    await flush();
    await c.chooseWorkflow("flow-one", "run-one");
    await flush();
    c.run.value = graphRun();
    expect(c.processSessionId.value).toBe("right-session");
    c.selectStep("left");
    expect(c.processSessionId.value).toBe("left-session");
    c.selectStep("merge");
    expect(c.processSessionId.value).toBe("");
    c.selectStep("right");
    expect(c.processSessionId.value).toBe("right-session");
  });
  it("keeps a manually selected node while another parallel branch runs and opens only one detail surface", async () => {
    const { c } = mount(); await flush(); await c.chooseWorkflow("flow-one", "run-one"); await flush();
    const value = graphRun(); value.steps = value.steps!.map(step => ({ ...step, active_run_id: `attempt-${step.step_id}` })); c.run.value = value;
    c.showProcess.value = true; c.selectStep("left"); c.nodeProcessChanged(true, "left");
    expect(c.showProcess.value).toBe(false); expect(c.nodeProcessOpen.value).toBe(true);
    expect(c.processSessionId.value).toBe("left-session"); expect(c.processActiveRunId.value).toBe("attempt-left");
    c.run.value = { ...value, steps: value.steps!.map(step => ({ ...step, status: step.step_id === 'left' ? 'completed' : step.status })) };
    expect(c.processSessionId.value).toBe("left-session"); expect(c.selectedStepId.value).toBe("left");
    c.chooseProcessStep("right");
    expect(c.showProcess.value).toBe(true); expect(c.nodeProcessOpen.value).toBe(false);
    expect(c.processSessionId.value).toBe("right-session"); expect(c.processActiveRunId.value).toBe("attempt-right");
    c.chooseProcessStep("merge"); expect(c.processSessionId.value).toBe("right-session");
  });
  it("clears node detail selection when changing Agents", async () => {
    const { c, props } = mount(); await flush(); await c.chooseWorkflow("flow-one", "run-one"); await flush();
    c.run.value = graphRun(); c.selectStep("left"); c.nodeProcessChanged(true, "left");
    props.agentId = "agent-b"; await flush();
    expect(c.nodeProcessOpen.value).toBe(false); expect(c.nodeProcessStepId.value).toBe(""); expect(c.run.value).toBeNull();
  });
  it("uses node sessions for resource-bound sequential runs and keeps legacy session routing", async()=>{
    const {c}=mount();await flush();await c.chooseWorkflow('flow-one','run-one');await flush();
    const value=graphRun();delete value.definition.graph;
    value.resources={version:1,nodes:{left:{skills:[{id:'embedded:docs',name:'docs',version:'v1',path:'resources/left/skill/SKILL.md',sha256:'a'.repeat(64)}]}}};
    value.execution_session_id='left-session';c.run.value=value;c.selectStep('right');expect(c.processSessionId.value).toBe('right-session');
    delete value.resources;c.run.value={...value};expect(c.processSessionId.value).toBe('left-session');
  });
  it("opens actual execution details after selecting a confirmation or control node", async () => {
    const { c } = mount();
    await flush(); await c.chooseWorkflow("flow-one", "run-one"); await flush();
    c.run.value = graphRun();
    c.selectStep("reviewA");
    expect(c.processSessionId.value).toBe("");
    c.toggleProcess();
    expect(c.processSessionId.value).toBe("right-session");
    c.toggleProcess();
    c.run.value = { ...graphRun(), status: "completed", steps: graphRun().steps!.map(step => ({ ...step, status: "completed" })) };
    c.selectStep("merge");
    c.toggleProcess();
    expect(c.processSessionId.value).toBe("right-session");
  });
  it.each(["cancelled", "failed", "interrupted", "completed"])("does not allow a stale waiting-node confirmation after the graph is %s", async status => {
    const { c } = mount();
    await flush();
    await c.chooseWorkflow("flow-one", "run-one");
    await flush();
    c.run.value = { ...graphRun(), status };
    const requests = host.call.mock.calls.length;
    expect(c.waitingSteps.value).toEqual([]);
    await c.confirm("reviewA");
    expect(host.call).toHaveBeenCalledTimes(requests);
  });
});
