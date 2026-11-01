import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import { emptyLive, type Message, type ShareEntry, type Snapshot } from "@/api/types";
import type { Exchange } from "@/agent/exchanges";

const host = vi.hoisted(() => ({
  session: vi.fn(), transcript: vi.fn(), retainedSubagentTranscript: vi.fn(), shareView: vi.fn(),
  mounted: [] as (() => unknown)[], cleanup: [] as (() => void)[],
}));
vi.mock("vue", async original => ({
  ...await original<typeof import("vue")>(),
  onMounted: (fn: () => unknown) => host.mounted.push(fn),
  onBeforeUnmount: (fn: () => void) => host.cleanup.push(fn),
  provide: () => {},
  useSSRContext: () => ({ modules: new Set() }),
}));
vi.mock("vue-i18n", async original => ({ ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/api/client", () => ({ api: { session: host.session, transcript: host.transcript, retainedSubagentTranscript: host.retainedSubagentTranscript, shareView: host.shareView }, authHeaders: () => ({}), streamURL: (id: string) => `/api/sessions/${id}/stream` }));
vi.mock("./RollingLine.vue", () => ({ default: {} }));
vi.mock("./TurnCard.vue", () => ({ default: {} }));
vi.mock("./SteeringMessage.vue", () => ({ default: {} }));
vi.mock("./OrderedMessageContent.vue", () => ({ default: {} }));
vi.mock("./WorkLog.vue", () => ({ default: {} }));
vi.mock("./ApprovalBar.vue", () => ({ default: {} }));
vi.mock("./AnswerText.vue", () => ({ default: {} }));
vi.mock("./Logo.vue", () => ({ default: {} }));
import SubagentSheet from "./SubagentSheet.vue";
import TaskSessionResult from "./TaskSessionResult.vue";
import ShareView from "@/views/ShareView.vue";

const scopes: ReturnType<typeof effectScope>[] = [];
function setup<T>(component: unknown, props: Record<string, unknown> = {}): T {
  const scope = effectScope(); scopes.push(scope);
  return scope.run(() => (component as { setup(props: unknown, context: unknown): T }).setup(reactive(props), { emit() {}, expose() {} }))!;
}
async function flush() { for (let i = 0; i < 12; i++) await Promise.resolve(); await nextTick(); }
async function mounted() { for (const hook of host.mounted.splice(0)) await hook(); await flush(); }
function snapshot(): Snapshot {
  return {
    seq: 1,
    messages: [
      { id: "q", role: "user", content: "旧版问题", ts: 1 },
      { id: "empty", role: "assistant", content: null },
      { id: "a", role: "assistant", content: { type: "text", text: "可继续阅读的回答" }, ts: 3 },
    ] as unknown as Message[],
    results: {}, live: emptyLive(), run: { active: false }, policy: { mode: "standard" },
    usage: { input: 0, output: 0 }, context_tokens: 0,
  };
}
const normalized = () => [
  { id: "q", role: "user", content: [{ type: "text", text: "旧版问题" }], ts: 1 },
  { id: "empty", role: "assistant", content: [] },
  { id: "a", role: "assistant", content: [{ type: "text", text: "可继续阅读的回答" }], ts: 3 },
];
beforeEach(() => {
  vi.useFakeTimers();
  host.session.mockReset(); host.transcript.mockReset(); host.retainedSubagentTranscript.mockReset(); host.shareView.mockReset();
  vi.stubGlobal("location", { pathname: "/share/public-a" });
  vi.stubGlobal("document", { title: "", createElement: () => ({}), head: { appendChild() {} } });
  vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(new Response(new ReadableStream({ start(controller) {
    controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ type: "snapshot", seq: 1, ts: 1, snapshot: snapshot() })}\n\n`));
  } }), { status: 200 }))));
});
afterEach(() => {
  for (const hook of host.cleanup.splice(0)) hook();
  host.mounted.length = 0;
  for (const scope of scopes.splice(0)) scope.stop();
  vi.useRealTimers(); vi.unstubAllGlobals();
});

describe("retained transcript message compatibility", () => {
  it("opens a retained subagent transcript with legacy content without breaking its footer or answer", async () => {
    host.retainedSubagentTranscript.mockResolvedValue({ session_id: "child-a", snapshot: snapshot() });
    const c = setup<{ messages: Ref<Message[]>; settled: Ref<boolean>; action: Ref<string | null>; exchanges: Ref<Exchange[]> }>(SubagentSheet, { childSession: "child-a", parentSession: "parent-a", agentId: "agent-a", live: false });
    await mounted();
    expect(host.retainedSubagentTranscript).toHaveBeenCalledWith("parent-a", "agent-a");
    expect(c.messages.value).toEqual(normalized());
    expect(c.settled.value).toBe(true); expect(c.action.value).toBeNull();
    expect(c.exchanges.value[0]?.answer?.text).toBe("可继续阅读的回答");
  });

  it("normalizes a bound background result while keeping its verified identity", async () => {
    host.session.mockResolvedValue({ session_id: "task-a", background_task: { source: "schedule", task_id: "schedule:job-a", source_id: "job-a", agent_id: "agent-a" }, snapshot: snapshot() });
    const c = setup<{ snapshot: Ref<Snapshot | null>; verifiedSession: Ref<string | null>; exchanges: Ref<Exchange[]> }>(TaskSessionResult, { sessionId: "task-a", agentId: "agent-a", visible: true });
    await flush();
    expect(c.verifiedSession.value).toBe("task-a"); expect(c.snapshot.value?.messages).toEqual(normalized());
    expect(c.exchanges.value[0]?.answer?.text).toBe("可继续阅读的回答");
  });

  it("renders a shared transcript with legacy content and retains old question-answer-only shares", async () => {
    host.shareView.mockResolvedValueOnce({ id: "public-a", answer: "fallback", created_at: 1, messages: snapshot().messages, results: {} });
    const c = setup<{ entry: Ref<ShareEntry | null>; exchange: Ref<Exchange | null> }>(ShareView);
    await mounted();
    expect(c.entry.value?.messages).toEqual(normalized());
    expect(c.exchange.value?.answer?.text).toBe("可继续阅读的回答");
    host.shareView.mockResolvedValueOnce({ id: "old", question: "旧问题", answer: "旧回答", created_at: 1 });
    const old = setup<{ entry: Ref<ShareEntry | null>; exchange: Ref<Exchange | null> }>(ShareView);
    await mounted();
    expect(old.entry.value?.answer).toBe("旧回答"); expect(old.entry.value).not.toHaveProperty("messages"); expect(old.exchange.value).toBeNull();
  });
});
