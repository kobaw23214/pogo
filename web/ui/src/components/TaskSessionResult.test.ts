import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { computed, createSSRApp, effectScope, reactive, ref, type Ref } from "vue";
import { renderToString } from "vue/server-renderer";
import { emptyLive, type Snapshot } from "@/api/types";
import { approvalUiKey, type ApprovalUiState } from "@/agent/approvalState";

const host = vi.hoisted(() => ({ decide: vi.fn(), freeze: vi.fn(), thaw: vi.fn(), cancel: vi.fn(), refresh: vi.fn(),
  provided: new Map<unknown, unknown>(), stream: null as null | { snapshot: Ref<Snapshot | null>; contextSnapshot: Ref<Snapshot | null>; verifiedSession: Ref<string | null>; loading: Ref<boolean>; error: Ref<string>; connection: Ref<string>; canDecide: Ref<boolean>; isolationMissing: Ref<boolean>; refresh: () => Promise<void> } }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(),
  provide: (key: unknown, value: unknown) => host.provided.set(key, value), onBeforeUnmount: () => {}, useSSRContext: () => ({ modules: new Set() }),
}));
vi.mock("vue-i18n", async original => ({ ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/api/client", () => ({ api: { decideGate: host.decide, freezeGate: host.freeze, thawGate: host.thaw, cancel: host.cancel } }));
vi.mock("@/agent/useTaskSessionStream", () => ({ useTaskSessionStream: () => host.stream }));
vi.mock("./WorkLog.vue", () => ({ default: { render: () => null } }));
vi.mock("./ApprovalBar.vue", () => ({ default: { render: () => null } }));
vi.mock("./OrderedMessageContent.vue", () => ({ default: { render: () => null } }));
vi.mock("./SubagentSheet.vue", () => ({ default: { render: () => null } }));
import TaskSessionResult from "./TaskSessionResult.vue";

const scopes: ReturnType<typeof effectScope>[] = [];
const props = () => ({ sessionId: "node-one", agentId: "agent-a", workflowId: "flow-one", workflowRunId: "run-one", visible: true });
const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
beforeEach(() => {
  for (const operation of [host.decide, host.freeze, host.thaw, host.cancel, host.refresh]) operation.mockReset().mockResolvedValue({});
  host.provided.clear();
  host.stream = { snapshot: ref({ seq: 1, messages: [], results: {}, live: { ...emptyLive(), active: true, pending_gates: [{ gate_id: "g1", call_id: "c1", tool: "bash", deadline: 10000 }] },
    run: { active: true }, policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0 }), contextSnapshot: computed(() => host.stream?.snapshot.value || null), verifiedSession: ref("node-one"), loading: ref(false), error: ref(""), connection: ref("live"), canDecide: ref(false), isolationMissing: ref(false), refresh: host.refresh };
});
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); });

describe("task session process presentation", () => {
  it("publishes only the currently verified execution reading and clears it when the selected scope changes", async () => {
    host.stream!.snapshot.value = { ...host.stream!.snapshot.value!, messages: [{ id: 'u', role: 'user', run_id: 'engine-one', content: [{ type: 'text', text: 'input' }] }],
      context_tokens: 12000, run: { active: false, model: 'node-model', context_window: 100000 }, usage: { input: 300000, output: 10000 } };
    const scope = effectScope(); scopes.push(scope);
    const selected = reactive({ ...props(), activeRunId: 'engine-one' }), emit = vi.fn();
    const c = scope.run(() => (TaskSessionResult as unknown as { setup: (props: unknown, context: unknown) => { contextReading: Ref<unknown> } }).setup(selected, { emit, expose() {} }))!;
    expect(c.contextReading.value).toMatchObject({ sessionId: 'node-one', activeRunId: 'engine-one', used: 12000, window: 100000, percent: 12 });
    expect(emit).toHaveBeenLastCalledWith('context', expect.objectContaining({ used: 12000 }), expect.objectContaining({ agentId: 'agent-a', sessionId: 'node-one', activeRunId: 'engine-one', workflowRunId: 'run-one' }));
    selected.sessionId = 'node-two'; await flush();
    expect(c.contextReading.value).toBeNull(); expect(emit).toHaveBeenLastCalledWith('context', null, expect.objectContaining({ sessionId: 'node-two' }));
    host.stream!.verifiedSession.value = 'node-two'; selected.visible = false; await flush();
    expect(c.contextReading.value).toBeNull();
  });

  it("blocks all approval controls until verified live and preserves allow, freeze and thaw when connected", async () => {
    const scope = effectScope(); scopes.push(scope);
    const c = scope.run(() => (TaskSessionResult as unknown as { setup: (props: unknown, context: unknown) => { decide(value: { gateId: string; allow: boolean }): void; gateClock(id: string, frozen: boolean): void } }).setup(reactive(props()), { emit() {}, expose() {} }))!;
    const approval = host.provided.get(approvalUiKey) as ApprovalUiState;
    c.decide({ gateId: "g1", allow: true }); c.gateClock("g1", true); c.gateClock("g1", false); await flush();
    expect(approval.canDecide).toBe(false); expect(approval.activeGateIds.value.size).toBe(0);
    expect(host.decide).not.toHaveBeenCalled(); expect(host.freeze).not.toHaveBeenCalled(); expect(host.thaw).not.toHaveBeenCalled();
    host.stream!.canDecide.value = true;
    expect(approval.canDecide).toBe(true); expect(approval.activeGateIds.value.has("g1")).toBe(true);
    c.decide({ gateId: "g1", allow: true }); await flush(); expect(host.decide).toHaveBeenCalledWith("node-one", "g1", true, { args: undefined, remember: undefined });
    c.gateClock("g1", true); await flush(); expect(host.freeze).toHaveBeenCalledWith("node-one", "g1");
    c.gateClock("g1", false); await flush(); expect(host.thaw).toHaveBeenCalledWith("node-one", "g1");
    host.stream!.canDecide.value = false; c.decide({ gateId: "g1", allow: false }); await flush(); expect(host.decide).toHaveBeenCalledTimes(1);
  });

  it("keeps compact live status and reconnection visible while removing duplicate headings", async () => {
    host.stream!.connection.value = "reconnecting"; host.stream!.error.value = "disconnected";
    const compact = await renderToString(createSSRApp(TaskSessionResult, { ...props(), compact: true }));
    expect(compact).toContain("task-session-connection"); expect(compact).toContain("taskSessionResult.reconnect");
    expect(compact).toContain('data-testid="task-context-ring"');
    expect(compact).not.toContain('class="tr-header"'); expect(compact).not.toContain("taskSessionResult.readOnly");
    const full = await renderToString(createSSRApp(TaskSessionResult, props()));
    expect(full).toContain('class="tr-header"'); expect(full).toContain("taskSessionResult.readOnly");
    const external = await renderToString(createSSRApp(TaskSessionResult, { ...props(), compact: true, hideContextRing: true }));
    expect(external).not.toContain('data-testid="task-context-ring"');
  });
});
