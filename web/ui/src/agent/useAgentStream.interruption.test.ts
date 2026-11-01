import { afterEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type AgentEvent } from "@/api/types";
import { gt } from "@/i18n";
import { useAgentStream } from "./useAgentStream";

describe("unfinished model runs", () => {
  const streams: ReturnType<typeof useAgentStream>[] = [];
  afterEach(() => {
    streams.splice(0).forEach(stream => stream.disconnect());
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });
  function create() {
    const stream = useAgentStream({ attachments: false });
    streams.push(stream);
    return stream;
  }

  it("shows an output-limit notice even if the provider returned no error or answer", () => {
    const stream = create();
    stream.apply({ type: "run_start", seq: 1, ts: 1, run_id: "first" });
    stream.apply({ type: "run_end", seq: 2, ts: 2, end_reason: "max_tokens", stop_reason: "max_tokens" });
    expect(stream.run.value.active).toBe(false);
    expect(stream.streamError.value).toBe(gt("stream.outputLimited"));
    expect(stream.messages.value).toHaveLength(0);
  });

  it("explains transport interruption, preserves detail and does not start a replacement run", () => {
    const stream = create();
    stream.apply({ type: "run_end", seq: 1, ts: 1, end_reason: "transport_error", error: "response timed out" });
    expect(stream.streamError.value).toBe(`${gt("stream.responseInterrupted")}\nresponse timed out`);
    expect(stream.run.value.active).toBe(false);
    expect(stream.messages.value).toHaveLength(0);
  });

  it("uses the run's actual stopping reason over a previous provider output limit", () => {
    const stream = create();
    stream.apply({ type: "run_end", seq: 1, ts: 1, end_reason: "token_budget", stop_reason: "max_tokens", error: "budget exceeded" });
    expect(stream.streamError.value).toBe("budget exceeded");
  });

  it("clears the interruption notice when the user starts a continuation", () => {
    const stream = create();
    stream.apply({ type: "run_end", seq: 1, ts: 1, end_reason: "max_tokens" });
    stream.apply({ type: "run_start", seq: 2, ts: 2, run_id: "continuation" });
    expect(stream.streamError.value).toBeNull();
    expect(stream.run.value).toMatchObject({ active: true, run_id: "continuation" });
  });

  it("does not label normal completion or cancellation as output truncation", () => {
    const stream = create();
    stream.apply({ type: "run_end", seq: 1, ts: 1, end_reason: "completed" });
    expect(stream.streamError.value).toBeNull();
    stream.apply({ type: "run_end", seq: 2, ts: 2, end_reason: "aborted", stop_reason: "aborted" });
    expect(stream.streamError.value).toBeNull();
  });

  function snapshot(seq: number, run: { active: boolean; end_reason?: string; error?: string }): AgentEvent {
    return { type: "snapshot", seq, ts: seq, snapshot: {
      seq, messages: [], results: {}, live: emptyLive(), run,
      policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0,
    } };
  }

  it("restores terminal notices from a full snapshot, and replaces them with the current session truth", () => {
    const stream = create();
    stream.apply(snapshot(1, { active: false, end_reason: "max_tokens" }));
    expect(stream.streamError.value).toBe(gt("stream.outputLimited"));
    stream.apply(snapshot(2, { active: false, end_reason: "transport_error", error: "interrupted" }));
    expect(stream.streamError.value).toBe(`${gt("stream.responseInterrupted")}\ninterrupted`);
    stream.apply(snapshot(3, { active: true }));
    expect(stream.streamError.value).toBeNull();
    stream.apply(snapshot(4, { active: false, end_reason: "completed" }));
    expect(stream.streamError.value).toBeNull();
  });

  it("retains a terminal notice after SSE reconnect with no run_end replay, and clears it on session switch", async () => {
    vi.useFakeTimers();
    function wire() {
      let controller!: ReadableStreamDefaultController<Uint8Array>;
      const body = new ReadableStream<Uint8Array>({ start(value) { controller = value; } });
      return { response: new Response(body),
        send(e: AgentEvent) { controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(e)}\n\n`)); },
        close() { controller.close(); },
      };
    }
    const first = wire(), second = wire(), next = wire();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(first.response).mockResolvedValueOnce(second.response).mockResolvedValueOnce(next.response));
    const stream = create(), original = stream.connect("first");
    first.send(snapshot(0, { active: false }));
    first.send({ type: "run_end", seq: 1, ts: 1, end_reason: "max_tokens" });
    await vi.advanceTimersByTimeAsync(1);
    expect(stream.streamError.value).toBe(gt("stream.outputLimited"));
    const resumed = stream.retryNow();
    await vi.advanceTimersByTimeAsync(1);
    expect(stream.connected.value).toBe(true);
    expect(stream.streamError.value).toBe(gt("stream.outputLimited"));
    const switched = stream.connect("next");
    await vi.advanceTimersByTimeAsync(1);
    expect(stream.streamError.value).toBeNull();
    next.send(snapshot(0, { active: false }));
    await vi.advanceTimersByTimeAsync(1);
    expect(stream.streamError.value).toBeNull();
    stream.disconnect(); first.close(); second.close(); next.close();
    await Promise.all([original, resumed, switched]);
  });
});
