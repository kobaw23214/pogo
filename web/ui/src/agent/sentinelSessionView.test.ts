import { afterEach, describe, expect, it, vi } from "vitest";
import { emptyLive } from "@/api/types";
import type { SentinelSessionDetail } from "@/api/sentinelSessions";
import { createSentinelSessionFeed, sentinelSessionPollDelay, sentinelSessionState, validSentinelSessionDetail, type SentinelSheetBinding } from "./sentinelSessionView";

const original: SentinelSheetBinding = { sentinelId: "watch-a", parentSession: "parent-a", agentId: "agent-a", visible: true };
function record(overrides: Partial<SentinelSessionDetail["watch"]> = {}): SentinelSessionDetail {
  return {
    watch: { id: "watch-a", task_id: "sentinel:watch-a", agent_id: "agent-a", parent_session_id: "parent-a", session_id: "worker-a", title: "A source", state: "listening", enabled: true, pending_events: 0, ...overrides },
    session: { id: "worker-a", cwd: "/work/agent-a", model: "test", snapshot: { seq: 1, messages: [], results: {}, live: emptyLive(), run: { active: false }, policy: { mode: "strict" }, usage: { input: 0, output: 0 }, context_tokens: 0 } },
  };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { resolve, promise };
}
function fixture() {
  let binding = { ...original };
  const fetch = vi.fn<(_: string, signal: AbortSignal) => Promise<SentinelSessionDetail>>();
  const feed = createSentinelSessionFeed({ binding: () => binding, fetch, errorMessage: () => "Unable to load" });
  return { feed, fetch, update: (change: Partial<SentinelSheetBinding>) => { binding = { ...binding, ...change }; } };
}
afterEach(() => vi.useRealTimers());

describe("Sentinel session view binding", () => {
  it("accepts the exact host binding and rejects foreign Watch, parent, Agent or worker identities", () => {
    expect(validSentinelSessionDetail(record(), original)).toBe(true);
    for (const change of [{ id: "watch-b" }, { task_id: "schedule:watch-a" }, { parent_session_id: "parent-b" }, { agent_id: "agent-b" }, { session_id: "worker-b" }]) {
      expect(validSentinelSessionDetail(record(change), original), JSON.stringify(change)).toBe(false);
    }
    const wrongWorker = record(); wrongWorker.session!.id = "other-worker";
    expect(validSentinelSessionDetail(wrongWorker, original)).toBe(false);
  });

  it("allows registration before the first worker exists without manufacturing a conversation", () => {
    const empty: SentinelSessionDetail = { watch: { ...record().watch, session_id: undefined }, session: null };
    expect(validSentinelSessionDetail(empty, original)).toBe(true);
    expect(validSentinelSessionDetail({ ...empty, watch: record().watch }, original)).toBe(false);
    expect(validSentinelSessionDetail({ ...empty, watch: { ...empty.watch, parent_session_id: undefined } }, original)).toBe(false);
    expect(validSentinelSessionDetail({ ...empty, watch: { ...empty.watch, parent_session_id: undefined } }, { ...original, parentSession: undefined })).toBe(true);
  });

  it("follows live approvals and processing state, and slows polling after pause or expiry", () => {
    const value = record();
    expect(sentinelSessionState(value)).toBe("listening");
    expect(sentinelSessionPollDelay(value)).toBe(2000);
    value.watch.enabled = false; value.watch.state = "expired";
    expect(sentinelSessionState(value)).toBe("expired");
    expect(sentinelSessionPollDelay(value)).toBe(10_000);
    value.session!.snapshot.run.active = true;
    expect(sentinelSessionState(value)).toBe("running");
    expect(sentinelSessionPollDelay(value)).toBe(2000);
    value.session!.snapshot.live.pending_gates = [{ gate_id: "gate-a" } as never];
    expect(sentinelSessionState(value)).toBe("waiting_human");
  });
});

describe("Sentinel session feed lifecycle", () => {
  it("normalizes legacy message content only after verifying the worker binding", async () => {
    const f = fixture(), value = record();
    value.session!.snapshot.messages = [
      { id: "question", role: "user", content: "旧版问题", ts: 100, run_id: "run-a" },
      { id: "empty", role: "assistant", content: null },
      { id: "answer", role: "assistant", content: { type: "text", text: "继续回答" } },
    ] as never;
    f.fetch.mockResolvedValue(value);
    await f.feed.sync();
    expect(f.feed.detail.value?.session?.snapshot.messages).toEqual([
      { id: "question", role: "user", content: [{ type: "text", text: "旧版问题" }], ts: 100, run_id: "run-a" },
      { id: "empty", role: "assistant", content: [] },
      { id: "answer", role: "assistant", content: [{ type: "text", text: "继续回答" }] },
    ]);
    expect(f.feed.detail.value?.session?.id).toBe("worker-a");
    expect(value.session!.snapshot.messages[0]!.content).toBe("旧版问题");
    f.fetch.mockResolvedValueOnce({ ...value, watch: { ...value.watch, agent_id: "foreign-agent" } });
    await f.feed.refresh();
    expect(f.feed.detail.value).toBeNull();
    expect(f.feed.error.value).toBe("Unable to load");
    f.feed.dispose();
  });

  it("fetches and refreshes only while the sheet is visible, retaining the last view when hidden", async () => {
    vi.useFakeTimers();
    const f = fixture(); f.fetch.mockResolvedValue(record());
    await f.feed.sync();
    expect(f.feed.detail.value?.session?.id).toBe("worker-a");
    expect(f.fetch).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(2000);
    expect(f.fetch).toHaveBeenCalledTimes(2);
    f.update({ visible: false }); await f.feed.sync();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(f.fetch).toHaveBeenCalledTimes(2);
    expect(f.feed.detail.value?.session?.id).toBe("worker-a");
    f.feed.dispose();
  });

  it("aborts a hidden in-flight read and ignores its eventual response", async () => {
    vi.useFakeTimers();
    const f = fixture(), query = deferred<SentinelSessionDetail>();
    f.fetch.mockReturnValue(query.promise);
    const pending = f.feed.sync();
    const signal = f.fetch.mock.calls[0]![1];
    f.update({ visible: false }); await f.feed.sync();
    expect(signal.aborted).toBe(true);
    query.resolve(record()); await pending;
    expect(f.feed.detail.value).toBeNull();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(f.fetch).toHaveBeenCalledTimes(1);
    f.feed.dispose();
  });

  it("does not let a slow old Watch response replace a newly selected Watch", async () => {
    const f = fixture(), old = deferred<SentinelSessionDetail>();
    const next = record({ id: "watch-b", task_id: "sentinel:watch-b" });
    f.fetch.mockReturnValueOnce(old.promise).mockResolvedValueOnce(next);
    const pending = f.feed.sync();
    f.update({ sentinelId: "watch-b" }); await f.feed.sync();
    old.resolve(record()); await pending;
    expect(f.feed.detail.value?.watch.id).toBe("watch-b");
    expect(f.feed.error.value).toBe("");
    f.feed.dispose();
  });

  it("retains the current content during network failures but clears a mismatched binding", async () => {
    const f = fixture();
    f.fetch.mockResolvedValueOnce(record()).mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce(record({ parent_session_id: "wrong-parent" }));
    await f.feed.sync(); await f.feed.refresh();
    expect(f.feed.detail.value?.watch.id).toBe("watch-a");
    expect(f.feed.error.value).toBe("Unable to load");
    await f.feed.refresh();
    expect(f.feed.detail.value).toBeNull();
    expect(f.feed.error.value).toBe("Unable to load");
    f.feed.dispose();
  });

  it("refreshing before an earlier request settles leaves one latest polling timer", async () => {
    vi.useFakeTimers();
    const f = fixture(), old = deferred<SentinelSessionDetail>();
    f.fetch.mockReturnValueOnce(old.promise).mockResolvedValue(record());
    const pending = f.feed.sync();
    const signal = f.fetch.mock.calls[0]![1];
    await f.feed.refresh();
    expect(signal.aborted).toBe(true);
    old.resolve(record()); await pending;
    expect(vi.getTimerCount()).toBe(1);
    await vi.advanceTimersByTimeAsync(2000);
    expect(f.fetch).toHaveBeenCalledTimes(3);
    expect(vi.getTimerCount()).toBe(1);
    f.feed.dispose();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("makes no request for an invisible initial tab and will never restart after disposal", async () => {
    vi.useFakeTimers();
    const f = fixture(); f.update({ visible: false });
    await f.feed.sync();
    expect(f.fetch).not.toHaveBeenCalled();
    f.feed.dispose(); f.update({ visible: true });
    await f.feed.refresh();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(f.fetch).not.toHaveBeenCalled();
  });
});
