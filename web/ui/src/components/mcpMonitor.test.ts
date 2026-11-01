import { describe, expect, it, vi } from "vitest";
import {
  asMonitorBridge,
  asAgentMonitorBridge,
  createAgentBridgeLoaders,
  buildCallsQuery,
  createBridgeLoaders,
  createDefaultFilter,
  createGenerationGate,
  formatDurationMs,
  formatErrorRate,
  mergeCalls,
  normalizeMcpEntrypoint,
  normalizeMcpStatus,
  snapshotMonitorFilter,
  requireMonitorAgentId,
} from "./mcpMonitor";
import type { McpCall, McpMonitorApiBridge, McpAgentMonitorApiBridge } from "./mcpMonitor";

const NOW = 1_800_000_000_000;

function call(id: string, over: Partial<McpCall> = {}): McpCall {
  return {
    id,
    trace_id: "",
    parent_id: "",
    direction: "outbound",
    server: "srv",
    tool: "tool",
    wire_tool: "",
    transport: "streamable",
    client: "",
    mcp_session_id: "",
    request_id: "",
    session_id: "",
    run_id: "",
    task_id: "",
    owner_id: "",
    tool_call_id: "",
    started_at: NOW,
    finished_at: 0,
    duration_ms: 0,
    status: "ok",
    error_kind: "",
    ...over,
  };
}

function parsed(query: string): URLSearchParams {
  return new URLSearchParams(query);
}

describe("buildCallsQuery", () => {
  it("keeps one committed time window across stats, paging and unapplied input", () => {
    const draft = createDefaultFilter();
    const committed = snapshotMonitorFilter(draft, NOW);
    draft.tool = "not-applied";
    const first = parsed(buildCallsQuery(committed, { limit: 100 }));
    const later = parsed(buildCallsQuery(committed, { cursor: "page-two" }));
    expect(first.get("from")).toBe(later.get("from"));
    expect(first.get("to")).toBe(later.get("to"));
    expect(first.has("tool")).toBe(false);
    expect(parsed(buildCallsQuery(snapshotMonitorFilter(draft, NOW + 10000))).get("tool")).toBe("not-applied");
  });
  it("applies the selected time window as explicit from/to", () => {
    const f = { ...createDefaultFilter(), window: "1h" as const };
    const p = parsed(buildCallsQuery(f, { now: NOW }));
    expect(p.get("from")).toBe(String(NOW - 3_600_000));
    expect(p.get("to")).toBe(String(NOW));
  });

  it("serializes every active filter under its contract parameter name", () => {
    const f = {
      ...createDefaultFilter(),
      direction: "inbound" as const,
      status: "timeout" as const,
      server: " fs ",
      tool: "read",
      q: "crash",
      traceId: "t1",
      sessionId: "s1",
      runId: "r1",
      taskId: "k1",
      entrypoint: "cli" as const,
    };
    const p = parsed(buildCallsQuery(f, { now: NOW }));
    expect(p.get("direction")).toBe("inbound");
    expect(p.get("status")).toBe("timeout");
    expect(p.get("server")).toBe("fs"); // trimmed
    expect(p.get("tool")).toBe("read");
    expect(p.get("q")).toBe("crash");
    expect(p.get("trace_id")).toBe("t1");
    expect(p.get("session_id")).toBe("s1");
    expect(p.get("run_id")).toBe("r1");
    expect(p.get("task_id")).toBe("k1");
    expect(p.get("entrypoint")).toBe("cli");
  });

  it("omits empty filters so list and stats queries stay identical", () => {
    const p = parsed(buildCallsQuery(createDefaultFilter(), { now: NOW }));
    for (const key of ["direction", "status", "server", "tool", "q", "trace_id", "session_id", "run_id", "task_id", "client_id", "entrypoint", "before", "limit"]) {
      expect(p.has(key), key).toBe(false);
    }
  });

  it("serializes entrypoint identically for list, stats and later pages", () => {
    const f = { ...createDefaultFilter(), entrypoint: "cli" as const };
    const committed = snapshotMonitorFilter(f, NOW);
    const list = parsed(buildCallsQuery(committed, { limit: 100 }));
    const stats = parsed(buildCallsQuery(committed));
    const pageTwo = parsed(buildCallsQuery(committed, { cursor: "c2", limit: 100 }));
    for (const p of [list, stats, pageTwo]) {
      expect(p.get("entrypoint")).toBe("cli");
    }
  });

  it("maps the pagination cursor to before and passes the page size", () => {
    const p = parsed(buildCallsQuery(createDefaultFilter(), { now: NOW, cursor: "abc", limit: 100 }));
    expect(p.get("before")).toBe("abc");
    expect(p.get("limit")).toBe("100");
  });
});

describe("mergeCalls", () => {
  it("dedupes by id so shifted keyset pages never double-list a call", () => {
    const first = [call("a"), call("b")];
    const second = [call("b"), call("c")];
    expect(mergeCalls(first, second).map((c) => c.id)).toEqual(["a", "b", "c"]);
  });

  it("keeps the earlier occurrence so a fresh first page wins over stale rows", () => {
    const fresh = [call("x", { status: "ok", duration_ms: 42 })];
    const stale = [call("x", { status: "running" })];
    const merged = mergeCalls(fresh, stale);
    expect(merged).toHaveLength(1);
    expect(merged[0].status).toBe("ok");
  });
});

describe("normalizeMcpEntrypoint", () => {
  it("keeps the CLI entrypoint a first-class value, never unknown", () => {
    expect(normalizeMcpEntrypoint("pogo")).toBe("pogo");
    expect(normalizeMcpEntrypoint("mcp")).toBe("mcp");
    expect(normalizeMcpEntrypoint("cli")).toBe("cli");
    expect(normalizeMcpEntrypoint("unknown")).toBe("unknown");
    expect(normalizeMcpEntrypoint("sdk")).toBe("unknown");
    expect(normalizeMcpEntrypoint("")).toBe("unknown");
    expect(normalizeMcpEntrypoint(undefined)).toBe("unknown");
  });
});

describe("createGenerationGate", () => {
  it("invalidates in-flight requests when a newer generation starts", () => {
    const gate = createGenerationGate();
    const slow = gate.next();
    const newer = gate.next();
    expect(gate.isLatest(slow)).toBe(false);
    expect(gate.isLatest(newer)).toBe(true);
  });

  it("auto-refresh ticks share the current generation until a filter change", () => {
    const gate = createGenerationGate();
    gate.next();
    const tick = gate.current();
    expect(gate.isLatest(tick)).toBe(true);
    gate.next(); // user changed a filter mid-tick
    expect(gate.isLatest(tick)).toBe(false);
  });
});

describe("normalizeMcpStatus", () => {
  it("passes through the contract statuses", () => {
    for (const s of ["running", "ok", "error", "cancelled", "timeout", "interrupted"]) {
      expect(normalizeMcpStatus(s)).toBe(s);
    }
  });

  it("buckets anything else as unknown — never as success", () => {
    expect(normalizeMcpStatus("success")).toBe("unknown");
    expect(normalizeMcpStatus("")).toBe("unknown");
    expect(normalizeMcpStatus("done")).toBe("unknown");
  });
});

describe("formatters", () => {
  it("distinguishes a known zero duration from a missing value", () => {
    expect(formatDurationMs(0)).toBe("0 ms");
    expect(formatDurationMs(null)).toBeNull();
    expect(formatDurationMs(undefined)).toBeNull();
    expect(formatDurationMs(Number.NaN)).toBeNull();
  });

  it("formats sub-second and multi-second durations", () => {
    expect(formatDurationMs(12)).toBe("12 ms");
    expect(formatDurationMs(1500)).toBe("1.5 s");
  });

  it("keeps a null error rate null instead of inventing 0% or 100%", () => {
    expect(formatErrorRate(null)).toBeNull();
    expect(formatErrorRate(undefined)).toBeNull();
  });

  it("formats rates with at most one decimal", () => {
    expect(formatErrorRate(0)).toBe("0%");
    expect(formatErrorRate(1)).toBe("100%");
    expect(formatErrorRate(0.125)).toBe("12.5%");
  });
});

describe("asMonitorBridge", () => {
  it("rejects an api object without the monitor methods", () => {
    expect(() => asMonitorBridge({ mcpLogs: () => Promise.resolve() })).toThrow(/not wired/);
    expect(() => asMonitorBridge(null)).toThrow(/not wired/);
    expect(() => asMonitorBridge({ mcpCalls: () => Promise.resolve() })).toThrow(/not wired/);
  });

  it("accepts a fully wired bridge", () => {
    const bridge: McpMonitorApiBridge = {
      mcpCalls: vi.fn(),
      mcpCallStats: vi.fn(),
      mcpCall: vi.fn(),
    };
    expect(asMonitorBridge(bridge)).toBe(bridge);
  });
});

describe("createBridgeLoaders", () => {
  it("sends list and stats through the same serializer with matching filters", async () => {
    const queries: string[] = [];
    const bridge: McpMonitorApiBridge = {
      mcpCalls: vi.fn(async (q: string) => {
        queries.push(q);
        return { calls: [], next_cursor: "", storage: "sqlite" as const, dropped: 0, write_errors: 0 };
      }),
      mcpCallStats: vi.fn(async (q: string) => {
        queries.push(q);
        return {
          total: 0, running: 0, ok: 0, errors: 0, cancelled: 0, timeouts: 0, interrupted: 0,
          error_rate: null, p50_ms: null, p95_ms: null, max_ms: null,
          by_direction: { inbound: 0, outbound: 0 }, by_tool: [],
          storage: "sqlite" as const, dropped: 0, write_errors: 0,
        };
      }),
      mcpCall: vi.fn(async () => ({ call: call("d1") })),
    };
    const loaders = createBridgeLoaders(bridge, 100);
		const filter = snapshotMonitorFilter({ ...createDefaultFilter(), status: "error" as const }, NOW);
    await loaders.loadCalls(filter, "cur");
    await loaders.loadStats(filter);
    const listQ = parsed(queries[0]);
    const statsQ = parsed(queries[1]);
    expect(listQ.get("status")).toBe("error");
    expect(statsQ.get("status")).toBe("error");
    // Only pagination differs between the two queries.
    listQ.delete("before");
    listQ.delete("limit");
    expect(listQ.toString()).toBe(statsQ.toString());
  });

  it("unwraps the detail envelope", async () => {
    const bridge: McpMonitorApiBridge = {
      mcpCalls: vi.fn(),
      mcpCallStats: vi.fn(),
      mcpCall: vi.fn(async (id: string) => ({ call: call(id) })),
    };
    const detail = await createBridgeLoaders(bridge, 100).loadDetail("d9");
    expect(detail.id).toBe("d9");
    expect(bridge.mcpCall).toHaveBeenCalledWith("d9");
  });
});

describe("Agent MCP history bridge", () => {
  function bridge() {
    const value = call("own", { agent_id: "agent-a", agent_name: "原名称" });
    return {
      mcpAgentCalls: vi.fn<McpAgentMonitorApiBridge["mcpAgentCalls"]>().mockResolvedValue({ calls: [value], next_cursor: "page-2", storage: "sqlite", dropped: 0, write_errors: 0 }),
      mcpAgentCallStats: vi.fn<McpAgentMonitorApiBridge["mcpAgentCallStats"]>().mockResolvedValue({} as Awaited<ReturnType<McpAgentMonitorApiBridge["mcpAgentCallStats"]>>),
      mcpAgentCall: vi.fn<McpAgentMonitorApiBridge["mcpAgentCall"]>().mockResolvedValue({ call: value }),
    };
  }
  it("serializes an exact Manager Agent filter identically for list, stats and paging", () => {
    const f = snapshotMonitorFilter({ ...createDefaultFilter(), agentId: " agent-a " }, NOW);
    for (const opts of [{}, { limit: 100 }, { cursor: "page-2" }]) expect(parsed(buildCallsQuery(f, opts)).get("agent_id")).toBe("agent-a");
  });
  it("pins all three loaders to one Agent endpoint and leaves session unfiltered", async () => {
    const api = bridge(), loaders = createAgentBridgeLoaders(api, "agent-a", 50), f = snapshotMonitorFilter(createDefaultFilter(), NOW);
    await loaders.loadCalls(f); await loaders.loadStats(f); await loaders.loadCalls(f, "page-2"); await loaders.loadDetail("own");
    expect(api.mcpAgentCalls.mock.calls.map(row => row[0])).toEqual(["agent-a", "agent-a"]);
    expect(api.mcpAgentCall).toHaveBeenCalledWith("agent-a", "own");
    const list = parsed(api.mcpAgentCalls.mock.calls[0]![1]), stats = parsed(api.mcpAgentCallStats.mock.calls[0]![1]);
    expect(list.get("from")).toBe(stats.get("from")); expect(list.get("to")).toBe(stats.get("to"));
    expect(list.has("session_id")).toBe(false); expect(list.has("agent_id")).toBe(false); expect(list.get("limit")).toBe("50");
    expect(parsed(api.mcpAgentCalls.mock.calls[1]![1]).get("before")).toBe("page-2");
  });
  it.each(["", " ", "../agent", "agent/a", "agent?all=1"])("does not substitute a global API for unavailable Agent %s", id => {
    const api = bridge(); expect(() => requireMonitorAgentId(id)).toThrow(); expect(() => createAgentBridgeLoaders(api, id, 100)).toThrow(); expect(api.mcpAgentCalls).not.toHaveBeenCalled();
  });
  it("rejects a conflicting Agent filter before making a request", async () => {
    const api = bridge(), loaders = createAgentBridgeLoaders(api, "agent-a", 100), f = { ...createDefaultFilter(), agentId: "agent-b" };
    await expect(loaders.loadCalls(f)).rejects.toThrow("scope changed"); expect(() => loaders.loadStats(f)).toThrow("scope changed"); expect(api.mcpAgentCalls).not.toHaveBeenCalled(); expect(api.mcpAgentCallStats).not.toHaveBeenCalled();
  });
  it.each(["agent-b", "", undefined])("rejects foreign and historically unattributed response rows (%s)", async agentId => {
    const api = bridge(), loaders = createAgentBridgeLoaders(api, "agent-a", 100), value = call("foreign", { agent_id: agentId });
    api.mcpAgentCalls.mockResolvedValueOnce({ calls: [value], next_cursor: "", storage: "sqlite", dropped: 0, write_errors: 0 }); api.mcpAgentCall.mockResolvedValueOnce({ call: value });
    await expect(loaders.loadCalls(createDefaultFilter())).rejects.toThrow("another Agent"); await expect(loaders.loadDetail("foreign")).rejects.toThrow("another Agent");
  });
  it("requires the dedicated scoped bridge rather than borrowing global loaders", () => {
    expect(() => asAgentMonitorBridge({ mcpCalls: vi.fn(), mcpCallStats: vi.fn(), mcpCall: vi.fn() })).toThrow("not wired");
    const api = bridge(); expect(asAgentMonitorBridge(api)).toBe(api);
  });
});
