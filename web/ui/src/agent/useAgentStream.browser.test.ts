import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type AgentEvent } from "@/api/types";
import { useAgentStream } from "./useAgentStream";
import { setEnsurePaneHook } from "./browserView";
import { setBrowserFocusHook } from "./browserFocus";
import { observeBrowserDownloads } from "./browserDownloads";

function event(type: AgentEvent["type"], seq: number, extra: Partial<AgentEvent> = {}): AgentEvent {
  return { type, seq, ts: Date.now(), ...extra };
}
function snapshot(seq: number): AgentEvent {
  return event("snapshot", seq, { snapshot: {
    seq, messages: [], results: {}, live: emptyLive(), run: { active: false },
    policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0,
  } });
}
function transport(cutoff: string | null = "0") {
  let controller!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({ start(value) { controller = value; } });
  const response = new Response(body, { headers: cutoff === null ? {} : { "X-Pogo-Stream-Backlog-Through": cutoff } });
  return {
    response,
    send(...events: AgentEvent[]) { controller.enqueue(new TextEncoder().encode(events.map(e => `data: ${JSON.stringify(e)}\n\n`).join(""))); },
    end() { controller.close(); },
  };
}

describe("stream-driven browser reveals", () => {
  const streams: ReturnType<typeof useAgentStream>[] = [];
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    for (const stream of streams.splice(0)) stream.disconnect();
    setEnsurePaneHook(null);
    setBrowserFocusHook(null);
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });
  function store() { const stream = useAgentStream({ attachments: false }); streams.push(stream); return stream; }

  it("delivers downloads through the existing stream without revealing a browser or crossing conversations", async () => {
    const wire = transport(), first = vi.fn(), other = vi.fn(), show = vi.fn();
    const stopFirst = observeBrowserDownloads("s1", first), stopOther = observeBrowserDownloads("s2", other);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(wire.response));
    setEnsurePaneHook(show);
    const stream = store(), connection = stream.connect("s1");
    const downloads = { downloads: [{ id: "download-1", filename: "订单.xlsx", state: "saved" as const, path: "uploads/browser/example/订单.xlsx", received_bytes: 1024, total_bytes: 1024, created_at: "2026-10-13T00:00:00Z" }] };
    wire.send(snapshot(0), event("browser_downloads", 1, { browser_downloads: downloads }));
    await vi.advanceTimersByTimeAsync(1);
    expect(first).toHaveBeenCalledExactlyOnceWith(downloads); expect(other).not.toHaveBeenCalled(); expect(show).not.toHaveBeenCalled();
    stopFirst(); stopOther(); stream.disconnect(); wire.end(); await connection;
  });

  it("publishes one live tab result with its trusted context and ignores manual replay", async () => {
    const wire = transport(), focus = vi.fn();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(wire.response));
    setBrowserFocusHook(focus);
    const stream = store();
    const connection = stream.connect("s1");
    const start = event("tool_start", 2, { name: "browser", call_id: "new-page", args: { action: "tab", tab_action: "new" } });
    const end = event("tool_end", 3, { name: "browser", call_id: "new-page", details: { action: "tab", ok: true, backend: "native", context_ref: "ctx_123456789012345678901234", active_tab: "t2", version: "1:t2:1:3" } });
    wire.send(snapshot(0), event("run_start", 1, { run_id: "r1" }), start, end, event("run_end", 4));
    await vi.advanceTimersByTimeAsync(1);
    expect(focus.mock.calls.map(([request]) => request.phase).filter(phase => phase === "complete")).toEqual(["complete"]);
    expect(focus.mock.calls.find(([request]) => request.phase === "complete")?.[0]).toMatchObject({ sessionId: "s1", runId: "r1", callId: "new-page", contextRef: "ctx_123456789012345678901234", tabId: "t2", generation: 1 });
    stream.apply(event("run_start", 5, { run_id: "replayed" })); stream.apply({ ...start, seq: 6 }); stream.apply({ ...end, seq: 7 });
    expect(focus.mock.calls.filter(([request]) => request.phase === "complete")).toHaveLength(1);
    stream.disconnect(); wire.end(); await connection;
  });

  it("folds a historical browser run while revealing only the later live run", async () => {
    const wire = transport("3"), show = vi.fn();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(wire.response));
    setEnsurePaneHook(show);
    const stream = store();
    const connection = stream.connect("s1");
    wire.send(event("run_start", 1, { run_id: "historical" }), event("tool_start", 2, { name: "browser", call_id: "old" }), event("run_end", 3));
    await vi.advanceTimersByTimeAsync(1);
    expect(show).not.toHaveBeenCalled();
    expect(stream.live.value.pending_tools).toHaveLength(0);
    wire.send(event("run_start", 4, { run_id: "live" }), event("tool_start", 5, { name: "browser", call_id: "new" }));
    await vi.advanceTimersByTimeAsync(1);
    expect(show).toHaveBeenCalledExactlyOnceWith({ sessionId: "s1", runId: "live", callId: "new", seq: 5 });
    stream.disconnect();
    expect(stream.connected.value).toBe(false);
    wire.end(); await connection;
  });

  it("fences discarded SSE bytes after switching sessions", async () => {
    const old = transport(), next = transport(), show = vi.fn();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(old.response).mockResolvedValueOnce(next.response));
    setEnsurePaneHook(show);
    const stream = store();
    const firstConnection = stream.connect("s1");
    old.send(snapshot(0));
    await vi.advanceTimersByTimeAsync(1);
    const nextConnection = stream.connect("s2");
    old.send(event("run_start", 1, { run_id: "discarded" }), event("tool_start", 2, { name: "browser" }));
    next.send(snapshot(0), event("run_start", 1, { run_id: "current" }), event("tool_start", 2, { name: "browser" }));
    await vi.advanceTimersByTimeAsync(1);
    expect(show).toHaveBeenCalledTimes(1);
    expect(show.mock.calls[0][0]).toMatchObject({ sessionId: "s2", runId: "current" });
    expect(stream.run.value.run_id).toBe("current");
    stream.disconnect(); old.end(); next.end(); await Promise.all([firstConnection, nextConnection]);
  });

  it("does not infer automatic entry from manually replayed frames or a server without a boundary", async () => {
    const wire = transport(null), show = vi.fn();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(wire.response));
    setEnsurePaneHook(show);
    const stream = store();
    const connection = stream.connect("s1");
    wire.send(event("run_start", 1, { run_id: "old-server" }), event("tool_start", 2, { name: "browser" }));
    await vi.advanceTimersByTimeAsync(1);
    stream.apply(event("run_start", 3, { run_id: "manual-replay" }));
    stream.apply(event("tool_start", 4, { name: "browser" }));
    expect(show).not.toHaveBeenCalled();
    expect(stream.live.value.pending_tools).toHaveLength(1);
    stream.disconnect(); wire.end(); await connection;
  });
});
