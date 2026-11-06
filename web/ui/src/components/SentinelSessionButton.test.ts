import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import type { SentinelSessionList, SentinelSessionSummary } from "@/api/sentinelSessions";

const host = vi.hoisted(() => ({ list: vi.fn(), cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({
  ...await original<typeof import("vue")>(),
  onMounted: (fn: () => void) => fn(),
  onBeforeUnmount: (fn: () => void) => host.cleanup.push(fn),
  useSSRContext: () => ({ modules: new Set() }),
}));
vi.mock("vue-i18n", async original => ({ ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/api/sentinelSessions", () => ({ sentinelSessionsApi: { list: host.list } }));
import SentinelSessionButton from "./SentinelSessionButton.vue";

interface Controller {
  watches: Ref<SentinelSessionSummary[]>;
  expanded: Ref<boolean>;
  loading: Ref<boolean>;
  error: Ref<boolean>;
  hasActive: Ref<boolean>;
  load(): Promise<void>;
  open(item: SentinelSessionSummary): void;
  visibilityChanged(): void;
}
const scopes: ReturnType<typeof effectScope>[] = [];
const record = (extra: Partial<SentinelSessionSummary> = {}): SentinelSessionSummary => ({
  id: "watch-one", task_id: "sentinel:watch-one", agent_id: "agent-a", parent_session_id: "main-a",
  title: "目录监听", state: "listening", enabled: true, pending_events: 0, ...extra,
});
function pending<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done; }); return { promise, resolve }; }
async function flush() { for (let n = 0; n < 7; n++) await Promise.resolve(); await nextTick(); }
function mount(extra: Partial<{ parentSession: string | null; agentId: string; enabled: boolean }> = {}) {
  const props = reactive({ parentSession: "main-a" as string | null, agentId: "agent-a", enabled: true, ...extra });
  const emit = vi.fn(), scope = effectScope(); scopes.push(scope);
  const c = scope.run(() => (SentinelSessionButton as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  return { c, props, emit };
}
beforeEach(() => { vi.useFakeTimers(); host.list.mockReset().mockResolvedValue({ watches: [record()] }); });
afterEach(() => { for (const fn of host.cleanup.splice(0)) fn(); for (const scope of scopes.splice(0)) scope.stop(); vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("parent Sentinel access", () => {
  it.each([{ parentSession: null }, { agentId: "" }, { enabled: false }])("does not request a list without an available parent/Agent context: %o", async extra => {
    const { c } = mount(extra); await flush();
    expect(host.list).not.toHaveBeenCalled(); expect(c.watches.value).toEqual([]);
  });

  it("keeps paused and expired history while rejecting unknown, foreign and mismatched bindings", async () => {
    const paused = record({ id: "paused", task_id: "sentinel:paused", state: "paused", enabled: false });
    const expired = record({ id: "expired", task_id: "sentinel:expired", state: "expired", enabled: false });
    host.list.mockResolvedValueOnce({ watches: [paused, expired, record({ agent_id: "foreign" }), record({ parent_session_id: "foreign" }), record({ parent_session_id: undefined }), record({ id: "../bad" }), record({ task_id: "sentinel:other" })] });
    const { c } = mount(); await flush();
    expect(new Set(c.watches.value.map(item => item.id))).toEqual(new Set(["paused", "expired"]));
    expect(c.hasActive.value).toBe(false);
  });

  it("opens only an already listed, same-parent Sentinel through the Sheet event", async () => {
    const { c, emit } = mount(); await flush();
    c.open(record());
    expect(emit).toHaveBeenCalledWith("open", "watch-one", "目录监听", "agent-a", "main-a");
    c.open(record({ parent_session_id: "other" }));
    c.open(record({ id: "unknown", task_id: "sentinel:unknown" }));
    expect(emit).toHaveBeenCalledTimes(1);
  });

  it("clears old counts and drops late results when the main conversation changes", async () => {
    const old = pending<SentinelSessionList>(), fresh = pending<SentinelSessionList>();
    host.list.mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise);
    const { c, props } = mount();
    const signal = host.list.mock.calls[0]![1] as AbortSignal;
    props.parentSession = "main-b"; await flush();
    expect(signal.aborted).toBe(true); expect(c.watches.value).toEqual([]);
    fresh.resolve({ watches: [record({ parent_session_id: "main-b" })] }); await flush();
    old.resolve({ watches: [record()] }); await flush();
    expect(c.watches.value).toHaveLength(1);
    expect(c.watches.value[0]!.parent_session_id).toBe("main-b");
  });

  it("retains last data after a refresh failure only within the same context", async () => {
    const { c, props, emit } = mount(); await flush();
    host.list.mockRejectedValueOnce(new Error("offline")); await c.load();
    expect(c.error.value).toBe(true); expect(c.watches.value).toHaveLength(1);
    c.open(c.watches.value[0]!); expect(emit).toHaveBeenCalledTimes(1);
    host.list.mockRejectedValueOnce(new Error("offline"));
    props.agentId = "agent-b"; props.parentSession = "main-b"; await flush();
    expect(c.watches.value).toEqual([]);
    c.open(record()); expect(emit).toHaveBeenCalledTimes(1);
  });

  it("polls every five seconds and stops immediately when disabled", async () => {
    const { c, props } = mount(); await flush();
    expect(host.list).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(5000); expect(host.list).toHaveBeenCalledTimes(2);
    props.enabled = false; await flush();
    expect(c.watches.value).toEqual([]);
    await vi.advanceTimersByTimeAsync(20000); expect(host.list).toHaveBeenCalledTimes(2);
  });

  it("suspends polling while the document is hidden and refreshes on return", async () => {
    const doc = { hidden: false, addEventListener: vi.fn(), removeEventListener: vi.fn() };
    vi.stubGlobal("document", doc);
    const { c } = mount(); await flush();
    doc.hidden = true; c.visibilityChanged();
    await vi.advanceTimersByTimeAsync(20000); expect(host.list).toHaveBeenCalledTimes(1);
    doc.hidden = false; c.visibilityChanged(); await flush();
    expect(host.list).toHaveBeenCalledTimes(2);
  });

  it("aborts an unmounted request and never adopts its late response", async () => {
    const response = pending<SentinelSessionList>(); host.list.mockReturnValueOnce(response.promise);
    const { c } = mount(); const signal = host.list.mock.calls[0]![1] as AbortSignal;
    for (const fn of host.cleanup.splice(0)) fn();
    expect(signal.aborted).toBe(true);
    response.resolve({ watches: [record()] }); await flush();
    expect(c.watches.value).toEqual([]);
    await vi.advanceTimersByTimeAsync(20000); expect(host.list).toHaveBeenCalledTimes(1);
  });
});
