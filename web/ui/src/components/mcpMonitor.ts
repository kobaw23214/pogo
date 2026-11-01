// Shared types, query serialization and small pure helpers for the MCP call
// monitor panel (McpMonitorPanel.vue). The field shapes mirror the Go
// mcpobserve contract (see the phase-2 contract doc): only metadata, never
// arguments, outputs, credentials or raw error text. Error diagnostics are
// bounded/redacted by the server and still rendered as untrusted text.
//
// API wiring: the main agent adds three methods to `api` in api/client.ts —
//   mcpCalls(query: string)      -> GET /api/mcp/calls?<query>       (McpCallsPage)
//   mcpCallStats(query: string)  -> GET /api/mcp/calls/stats?<query> (McpCallStats)
//   mcpCall(id: string)          -> GET /api/mcp/calls/<id>          ({ call })
// importing McpCallsPage/McpCallStats/McpCall and buildCallsQuery from here,
// so the serializer and the HTTP layer never drift apart.

export type McpCallDirection = "inbound" | "outbound";

export type McpCallStatus =
  | "running"
  | "ok"
  | "error"
  | "cancelled"
  | "timeout"
  | "interrupted";

export const MCP_CALL_STATUSES: readonly McpCallStatus[] = [
  "running",
  "ok",
  "error",
  "cancelled",
  "timeout",
  "interrupted",
];

// Anything outside the contract set is shown as unknown — notably it must
// never fall into a "success" bucket.
export function normalizeMcpStatus(status: string): McpCallStatus | "unknown" {
  return (MCP_CALL_STATUSES as readonly string[]).includes(status)
    ? (status as McpCallStatus)
    : "unknown";
}

export interface McpCall {
  id: string;
  trace_id: string;
  parent_id?: string;
  // W3C span correlation fields (observability completion contract). Optional
  // so fixtures and events recorded before the schema upgrade stay valid.
  span_id?: string;
  parent_span_id?: string;
  trace_flags?: string;
  // Authenticated local caller identity — trusted, unlike `client` below,
  // which is the remote's self-reported name and never an identity.
  client_id?: string;
  // Which surface the call came through; "cli" is the local CLI, never
  // bucketed as unknown.
  entrypoint?: string;
  direction: string;
  server: string;
  tool: string;
  wire_tool?: string;
  transport?: string;
  client?: string;
  mcp_session_id?: string;
  request_id?: string;
  session_id?: string;
  run_id?: string;
  task_id?: string;
  owner_id?: string;
  /** Trusted Agent identity/name captured when the call began. Older and
      non-Agent calls deliberately leave these empty. */
  agent_id?: string;
  agent_name?: string;
  tool_call_id?: string;
  started_at: number;
  finished_at?: number;
  duration_ms: number;
  status: string;
  error_kind?: string;
  /** Server-redacted diagnostic, never raw arguments/results. Legacy calls
      leave this empty; error_kind alone must not be presented as a cause. */
  error_summary?: string;
  error_code?: string;
  /** Detail-only diagnostic recovered from a strictly joined saved tool result.
      Never implies that the list or stored historical row was backfilled. */
  error_source?: "session";
}

export type McpMonitorStorage = "sqlite" | "memory";

export interface McpCallsPage {
  calls: McpCall[];
  next_cursor: string;
  storage: McpMonitorStorage;
  dropped: number;
  write_errors: number;
}

export interface McpCallStatsToolRow {
  direction: string;
  server: string;
  tool: string;
  total: number;
  errors: number;
  p95_ms: number | null;
}

export interface McpCallStats {
  total: number;
  running: number;
  ok: number;
  errors: number;
  cancelled: number;
  timeouts: number;
  interrupted: number;
  error_rate: number | null;
  p50_ms: number | null;
  p95_ms: number | null;
  max_ms: number | null;
  by_direction: { inbound: number; outbound: number };
  by_tool: McpCallStatsToolRow[];
  storage: McpMonitorStorage;
  dropped: number;
  write_errors: number;
}

export type McpMonitorWindow = "1h" | "24h" | "7d";

// Entrypoint vocabulary: pogo session, inbound MCP, the local CLI, or an
// honest unknown. Anything else also normalizes to unknown.
export type McpEntrypoint = "pogo" | "mcp" | "cli" | "unknown";

export const MCP_ENTRYPOINTS: readonly McpEntrypoint[] = ["pogo", "mcp", "cli", "unknown"];

export function normalizeMcpEntrypoint(entrypoint: string | undefined): McpEntrypoint {
  return (MCP_ENTRYPOINTS as readonly string[]).includes(entrypoint ?? "")
    ? (entrypoint as McpEntrypoint)
    : "unknown";
}

export const MCP_WINDOW_MS: Record<McpMonitorWindow, number> = {
  "1h": 60 * 60 * 1000,
  "24h": 24 * 60 * 60 * 1000,
  "7d": 7 * 24 * 60 * 60 * 1000,
};

export interface McpMonitorFilter {
  // Empty string = unfiltered ("all").
  direction: "" | McpCallDirection;
  status: "" | McpCallStatus;
  server: string;
  tool: string;
  q: string;
  traceId: string;
  sessionId: string;
  runId: string;
  taskId: string;
  entrypoint: "" | McpEntrypoint;
  /** Manager-only exact Agent filter; Agent views use their endpoint scope. */
  agentId?: string;
  window: McpMonitorWindow;
  /** Fixed upper bound shared by this query's list, stats and later pages. */
  at?: number;
}

export function snapshotMonitorFilter(filter: McpMonitorFilter, now = Date.now()): McpMonitorFilter {
  return { ...filter, at: now };
}

export function createDefaultFilter(): McpMonitorFilter {
  return {
    direction: "",
    status: "",
    server: "",
    tool: "",
    q: "",
    traceId: "",
    sessionId: "",
    runId: "",
    taskId: "",
    entrypoint: "",
    agentId: "",
    window: "24h",
  };
}

export interface McpCallsQueryOptions {
  cursor?: string;
  limit?: number;
  // Injectable clock so tests are deterministic; from = now - window.
  now?: number;
}

// Serializes a filter into the query string shared by the list and the stats
// endpoint — the contract requires list and aggregate filters to be
// identical, so both call this one function. The server applies a default
// 24h window when `from` is absent; we always send an explicit window so the
// stats cards and the list describe the same range.
export function buildCallsQuery(
  filter: McpMonitorFilter,
  opts: McpCallsQueryOptions = {},
): string {
  const p = new URLSearchParams();
  const now = opts.now ?? filter.at ?? Date.now();
  p.set("from", String(now - MCP_WINDOW_MS[filter.window]));
  p.set("to", String(now));
  if (filter.direction) p.set("direction", filter.direction);
  if (filter.status) p.set("status", filter.status);
  if (filter.server.trim()) p.set("server", filter.server.trim());
  if (filter.tool.trim()) p.set("tool", filter.tool.trim());
  if (filter.q.trim()) p.set("q", filter.q.trim());
  if (filter.traceId) p.set("trace_id", filter.traceId);
  if (filter.sessionId) p.set("session_id", filter.sessionId);
  if (filter.runId) p.set("run_id", filter.runId);
  if (filter.taskId) p.set("task_id", filter.taskId);
  if (filter.entrypoint) p.set("entrypoint", filter.entrypoint);
  if (filter.agentId?.trim()) p.set("agent_id", filter.agentId.trim());
  if (opts.cursor) p.set("before", opts.cursor);
  if (opts.limit && opts.limit > 0) p.set("limit", String(Math.floor(opts.limit)));
  return p.toString();
}

// Appends a keyset page to the list, dropping rows whose id already appears —
// a slow auto-refresh and a manual "load more" can otherwise surface the same
// call twice when rows shift between requests. Earlier arguments win for
// duplicates, so a fresh first page replaces stale snapshots in place.
export function mergeCalls(first: McpCall[], second: McpCall[]): McpCall[] {
  const seen = new Set<string>();
  const out: McpCall[] = [];
  for (const call of [...first, ...second]) {
    if (seen.has(call.id)) continue;
    seen.add(call.id);
    out.push(call);
  }
  return out;
}

// Generation counter guarding against slow responses overwriting newer
// filters: every filter change mints a new generation, responses from older
// generations are discarded on arrival.
export function createGenerationGate() {
  let generation = 0;
  return {
    next(): number {
      generation += 1;
      return generation;
    },
    current(): number {
      return generation;
    },
    isLatest(g: number): boolean {
      return g === generation;
    },
  };
}

// "12 ms" / "1.2 s"; known zero durations remain valid. The caller hides
// duration for running/interrupted calls, whose elapsed time is unknown.
export function formatDurationMs(ms: number | null | undefined): string | null {
  if (ms === null || ms === undefined || !Number.isFinite(ms) || ms < 0) return null;
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(1)} s`;
}

// 0..1 ratio -> percentage with one decimal, trimmed; null stays null so the
// UI can show "—" instead of a misleading "0%" or "100%".
export function formatErrorRate(rate: number | null | undefined): string | null {
  if (rate === null || rate === undefined || !Number.isFinite(rate)) return null;
  const pct = rate * 100;
  const rounded = Math.round(pct * 10) / 10;
  return `${Number.isInteger(rounded) ? rounded.toFixed(0) : rounded.toFixed(1)}%`;
}

// The bridge the main agent adds to `api` in api/client.ts. Kept structural
// (no import of client.ts) so client.ts can import this module without a
// circular dependency.
export interface McpMonitorApiBridge {
  mcpCalls(query: string): Promise<McpCallsPage>;
  mcpCallStats(query: string): Promise<McpCallStats>;
  mcpCall(id: string): Promise<{ call: McpCall }>;
}

export interface McpMonitorLoaders {
  loadCalls(filter: McpMonitorFilter, cursor?: string): Promise<McpCallsPage>;
  loadStats(filter: McpMonitorFilter): Promise<McpCallStats>;
  loadDetail(id: string): Promise<McpCall>;
}

export interface McpAgentMonitorApiBridge {
  mcpAgentCalls(agentId: string, query: string): Promise<McpCallsPage>;
  mcpAgentCallStats(agentId: string, query: string): Promise<McpCallStats>;
  mcpAgentCall(agentId: string, id: string): Promise<{ call: McpCall }>;
}

export function requireMonitorAgentId(agentId: string): string {
  if (!/^[A-Za-z0-9_-]{1,160}$/.test(agentId)) throw new Error("MCP history requires an explicit Agent");
  return agentId;
}

export function asAgentMonitorBridge(candidate: unknown): McpAgentMonitorApiBridge {
  const api = candidate as Partial<McpAgentMonitorApiBridge> | null | undefined;
  if (api && typeof api.mcpAgentCalls === "function" && typeof api.mcpAgentCallStats === "function" && typeof api.mcpAgentCall === "function") return api as McpAgentMonitorApiBridge;
  throw new Error("Agent MCP monitor api not wired");
}

/** A missing Agent can never fall back to the administrator's global API. */
export function createAgentBridgeLoaders(bridge: McpAgentMonitorApiBridge, agentId: string, pageSize: number): McpMonitorLoaders {
  const scope = requireMonitorAgentId(agentId);
  const query = (filter: McpMonitorFilter, cursor?: string, limit?: number) => {
    if (filter.agentId && filter.agentId !== scope) throw new Error("MCP history Agent scope changed");
    return buildCallsQuery({ ...filter, agentId: "" }, { cursor, limit });
  };
  const callInScope = (call: McpCall) => {
    if (call.agent_id !== scope) throw new Error("MCP history response belongs to another Agent");
    return call;
  };
  return {
    loadCalls: async (filter, cursor) => {
      const page = await bridge.mcpAgentCalls(scope, query(filter, cursor, pageSize));
      page.calls.forEach(callInScope);
      return page;
    },
    loadStats: filter => bridge.mcpAgentCallStats(scope, query(filter)),
    loadDetail: async id => callInScope((await bridge.mcpAgentCall(scope, id)).call),
  };
}

// Narrows the untyped `api` object to the bridge, with an explicit error
// instead of an undefined-call TypeError while the wiring is pending.
export function asMonitorBridge(candidate: unknown): McpMonitorApiBridge {
  const a = candidate as Partial<McpMonitorApiBridge> | null | undefined;
  if (
    a &&
    typeof a.mcpCalls === "function" &&
    typeof a.mcpCallStats === "function" &&
    typeof a.mcpCall === "function"
  ) {
    return a as McpMonitorApiBridge;
  }
  throw new Error("mcp monitor api not wired: api.mcpCalls/mcpCallStats/mcpCall missing");
}

// Default loaders over the bridge; the panel uses these when no loaders are
// injected via props (tests and embedding always inject).
export function createBridgeLoaders(
  bridge: McpMonitorApiBridge,
  pageSize: number,
): McpMonitorLoaders {
  return {
    loadCalls: (filter, cursor) =>
      bridge.mcpCalls(buildCallsQuery(filter, { cursor, limit: pageSize })),
    loadStats: (filter) => bridge.mcpCallStats(buildCallsQuery(filter)),
    loadDetail: async (id) => (await bridge.mcpCall(id)).call,
  };
}
