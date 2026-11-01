// Shared types, query serialization and small pure helpers for the skill
// monitor panel (SkillMonitorPanel.vue). The field shapes mirror the Go
// skillobserve contract (skillobserve/event.go, web/skillmonitor.go): only
// metadata — never skill file contents, tool arguments, outputs or raw
// error text.
//
// Stage semantics, per the backend contract: "discovery" (the skill was
// surfaced to the agent), "load" (its instructions were read) and "execute"
// (a tool the skill declares ran). These are separate observations of one
// skill, never interchangeable successes — a discovered or loaded skill says
// nothing about whether any execution worked, and one ok execution says
// nothing about the skill as a whole.
//
// API wiring: the main agent adds three methods to `api` in api/client.ts —
//   skillEvents(query: string)     -> GET /api/skills/events?<query>       (SkillEventsPage)
//   skillEventStats(query: string) -> GET /api/skills/events/stats?<query> (SkillEventStats)
//   skillEvent(id: string)         -> GET /api/skills/events/<id>          ({ event })
// importing SkillEventsPage/SkillEventStats/SkillEvent and buildEventsQuery
// from here, so the serializer and the HTTP layer never drift apart.
import {
  createGenerationGate,
  formatDurationMs,
  formatErrorRate,
} from "@/components/mcpMonitor";

export { createGenerationGate, formatDurationMs, formatErrorRate };

export type SkillEventStage = "discovery" | "load" | "execute";

export const SKILL_EVENT_STAGES: readonly SkillEventStage[] = [
  "discovery",
  "load",
  "execute",
];

// Anything outside the contract set is shown as unknown — an unrecognized
// stage must never be read as evidence of skill usage.
export function normalizeSkillStage(stage: string): SkillEventStage | "unknown" {
  return (SKILL_EVENT_STAGES as readonly string[]).includes(stage)
    ? (stage as SkillEventStage)
    : "unknown";
}

// Who triggered the operation: the agent itself ("internal") or an external
// client driving Pogo ("external"). Distinct from the entrypoint below.
export type SkillEventCaller = "internal" | "external";

export const SKILL_EVENT_CALLERS: readonly SkillEventCaller[] = ["internal", "external"];

export function normalizeSkillCaller(caller: string): SkillEventCaller | "unknown" {
  return (SKILL_EVENT_CALLERS as readonly string[]).includes(caller)
    ? (caller as SkillEventCaller)
    : "unknown";
}

// Which surface the originating run came through: a Pogo session ("pogo"),
// an inbound MCP task ("mcp") or the local CLI ("cli"). "unknown" is a
// honest value, not an error — and "cli" must never be bucketed into it.
export type SkillEventEntrypoint = "pogo" | "mcp" | "cli";

export const SKILL_EVENT_ENTRYPOINTS: readonly SkillEventEntrypoint[] = ["pogo", "mcp", "cli"];

export function normalizeSkillEntrypoint(entrypoint: string): SkillEventEntrypoint | "unknown" {
  return (SKILL_EVENT_ENTRYPOINTS as readonly string[]).includes(entrypoint)
    ? (entrypoint as SkillEventEntrypoint)
    : "unknown";
}

// How the operation was triggered. "runtime" is a packaged runtime skill's
// contract tool executing (stage "execute"); anything outside the contract
// set is unknown.
export function skillMechanismLabelKey(mechanism: string): string {
  switch (mechanism) {
    case "catalog":
      return "skillMonitor.mechanismCatalog";
    case "file_read":
      return "skillMonitor.mechanismFileRead";
    case "explicit":
      return "skillMonitor.mechanismExplicit";
    case "manifest":
      return "skillMonitor.mechanismManifest";
    case "runtime":
      return "skillMonitor.mechanismRuntime";
    default:
      return "skillMonitor.mechanismUnknown";
  }
}

// Where the skill came from. "bundle" is a packaged runtime bundle, distinct
// from the built-in/uploaded directory skills; anything outside the contract
// set is unknown.
export function skillSourceLabelKey(source: string): string {
  switch (source) {
    case "user":
      return "skillMonitor.sourceUser";
    case "project":
      return "skillMonitor.sourceProject";
    case "path":
      return "skillMonitor.sourcePath";
    case "embedded":
      return "skillMonitor.sourceEmbedded";
    case "bundle":
      return "skillMonitor.sourceBundle";
    default:
      return "skillMonitor.sourceUnknown";
  }
}

export type SkillEventStatus =
  | "running"
  | "ok"
  | "error"
  | "cancelled"
  | "timeout"
  | "interrupted";

export const SKILL_EVENT_STATUSES: readonly SkillEventStatus[] = [
  "running",
  "ok",
  "error",
  "cancelled",
  "timeout",
  "interrupted",
];

// Same outcome vocabulary as the MCP monitor; anything else is unknown and
// must never fall into a "success" bucket.
export function normalizeSkillStatus(status: string): SkillEventStatus | "unknown" {
  return (SKILL_EVENT_STATUSES as readonly string[]).includes(status)
    ? (status as SkillEventStatus)
    : "unknown";
}

// The event has a terminal outcome whose duration is meaningful. Running and
// interrupted events keep an unknown duration (a real 0 ms stays a real value).
export function hasKnownDuration(event: Pick<SkillEvent, "status">): boolean {
  const s = normalizeSkillStatus(event.status);
  return s !== "running" && s !== "interrupted" && s !== "unknown";
}

export interface SkillEvent {
  id: string;
  trace_id: string;
  parent_id?: string;
  // W3C span correlation fields (observability completion contract);
  // optional so events recorded before the schema upgrade stay valid.
  span_id?: string;
  parent_span_id?: string;
  trace_flags?: string;
  // Authenticated local caller identity (trusted); never the remote's
  // self-reported client name.
  client_id?: string;
  skill_id: string;
  skill_name: string;
  skill_source: string;
  stage: string;
  caller: string;
  entrypoint: string;
  mechanism: string;
  tool?: string;
  wire_tool?: string;
  session_id?: string;
  run_id?: string;
  task_id?: string;
  owner_id?: string;
  tool_call_id?: string;
  started_at: number;
  finished_at?: number;
  duration_ms: number;
  status: string;
  error_kind?: string;
}

export type SkillMonitorStorage = "sqlite" | "memory";

export interface SkillEventsPage {
  events: SkillEvent[];
  next_cursor: string;
  storage: SkillMonitorStorage;
  dropped: number;
  write_errors: number;
}

// One by_skill aggregate row (top 20 by executions, then total). Latency and
// the error counter describe declared-tool executions of that skill only.
export interface SkillEventStatsSkillRow {
  skill_id: string;
  skill_name: string;
  skill_source: string;
  discoveries: number;
  loads: number;
  executions: number;
  execution_errors: number;
  p95_ms: number | null;
}

// Caller split over all matching events, every stage included.
export interface SkillEventStatsByCaller {
  internal: number;
  external: number;
  unknown: number;
}

// Aggregates over every retained event matching the filter, independent of
// the list page size. Stage counters cover all three stages. The outcome and
// latency fields describe declared-tool executions (stage "execute") only:
// error_rate = (errors + timeouts) / (ok + errors + timeouts) over those
// executions, null without completed samples — they never claim a whole
// skill succeeded.
export interface SkillEventStats {
  total: number;
  discoveries: number;
  loads: number;
  executions: number;
  execution_running: number;
  execution_ok: number;
  execution_errors: number;
  execution_cancelled: number;
  execution_timeouts: number;
  execution_interrupted: number;
  error_rate: number | null;
  p50_ms: number | null;
  p95_ms: number | null;
  max_ms: number | null;
  by_caller: SkillEventStatsByCaller;
  by_skill: SkillEventStatsSkillRow[];
  storage: SkillMonitorStorage;
  dropped: number;
  write_errors: number;
}

export type SkillMonitorWindow = "1h" | "24h" | "7d";

export const SKILL_WINDOW_MS: Record<SkillMonitorWindow, number> = {
  "1h": 60 * 60 * 1000,
  "24h": 24 * 60 * 60 * 1000,
  "7d": 7 * 24 * 60 * 60 * 1000,
};

export interface SkillMonitorFilter {
  // Empty string = unfiltered ("all").
  stage: "" | SkillEventStage;
  caller: "" | SkillEventCaller | "unknown";
  entrypoint: "" | SkillEventEntrypoint | "unknown";
  status: "" | SkillEventStatus;
  skillName: string;
  skillId: string;
  q: string;
  tool: string;
  traceId: string;
  sessionId: string;
  window: SkillMonitorWindow;
  /** Fixed upper bound shared by this query's list, stats and later pages. */
  at?: number;
}

export function snapshotSkillFilter(filter: SkillMonitorFilter, now = Date.now()): SkillMonitorFilter {
  return { ...filter, at: now };
}

export function createDefaultSkillFilter(): SkillMonitorFilter {
  return {
    stage: "",
    caller: "",
    entrypoint: "",
    status: "",
    skillName: "",
    skillId: "",
    q: "",
    tool: "",
    traceId: "",
    sessionId: "",
    window: "24h",
  };
}

export interface SkillEventsQueryOptions {
  cursor?: string;
  limit?: number;
  // Injectable clock so tests are deterministic; from = now - window.
  now?: number;
}

// Serializes a filter into the query string shared by the list and the stats
// endpoint — the contract requires list and aggregate filters to be
// identical, so both call this one function. An explicit from/to window is
// always sent so the stats cards and the list describe the same range.
export function buildEventsQuery(
  filter: SkillMonitorFilter,
  opts: SkillEventsQueryOptions = {},
): string {
  const p = new URLSearchParams();
  const now = opts.now ?? filter.at ?? Date.now();
  p.set("from", String(now - SKILL_WINDOW_MS[filter.window]));
  p.set("to", String(now));
  if (filter.stage) p.set("stage", filter.stage);
  if (filter.caller) p.set("caller", filter.caller);
  if (filter.entrypoint) p.set("entrypoint", filter.entrypoint);
  if (filter.status) p.set("status", filter.status);
  if (filter.skillName.trim()) p.set("skill_name", filter.skillName.trim());
  if (filter.skillId) p.set("skill_id", filter.skillId);
  if (filter.q.trim()) p.set("q", filter.q.trim());
  if (filter.tool.trim()) p.set("tool", filter.tool.trim());
  if (filter.traceId) p.set("trace_id", filter.traceId);
  if (filter.sessionId) p.set("session_id", filter.sessionId);
  if (opts.cursor) p.set("before", opts.cursor);
  if (opts.limit && opts.limit > 0) p.set("limit", String(Math.floor(opts.limit)));
  return p.toString();
}

// Appends a keyset page to the list, dropping rows whose id already appears —
// a slow auto-refresh and a manual "load more" can otherwise surface the same
// event twice when rows shift between requests. Earlier arguments win for
// duplicates, so a fresh first page replaces stale snapshots in place.
export function mergeEvents(first: SkillEvent[], second: SkillEvent[]): SkillEvent[] {
  const seen = new Set<string>();
  const out: SkillEvent[] = [];
  for (const event of [...first, ...second]) {
    if (seen.has(event.id)) continue;
    seen.add(event.id);
    out.push(event);
  }
  return out;
}

// The bridge the main agent adds to `api` in api/client.ts. Kept structural
// (no import of client.ts) so client.ts can import this module without a
// circular dependency.
export interface SkillMonitorApiBridge {
  skillEvents(query: string): Promise<SkillEventsPage>;
  skillEventStats(query: string): Promise<SkillEventStats>;
  skillEvent(id: string): Promise<{ event: SkillEvent }>;
}

export interface SkillMonitorLoaders {
  loadEvents(filter: SkillMonitorFilter, cursor?: string): Promise<SkillEventsPage>;
  loadStats(filter: SkillMonitorFilter): Promise<SkillEventStats>;
  loadDetail(id: string): Promise<SkillEvent>;
}

// Narrows the untyped `api` object to the bridge, with an explicit error
// instead of an undefined-call TypeError while the wiring is pending.
export function asSkillMonitorBridge(candidate: unknown): SkillMonitorApiBridge {
  const a = candidate as Partial<SkillMonitorApiBridge> | null | undefined;
  if (
    a &&
    typeof a.skillEvents === "function" &&
    typeof a.skillEventStats === "function" &&
    typeof a.skillEvent === "function"
  ) {
    return a as SkillMonitorApiBridge;
  }
  throw new Error("skill monitor api not wired: api.skillEvents/skillEventStats/skillEvent missing");
}

// Default loaders over the bridge; the panel uses these when no loaders are
// injected via props (tests and embedding always inject).
export function createSkillBridgeLoaders(
  bridge: SkillMonitorApiBridge,
  pageSize: number,
): SkillMonitorLoaders {
  return {
    loadEvents: (filter, cursor) =>
      bridge.skillEvents(buildEventsQuery(filter, { cursor, limit: pageSize })),
    loadStats: (filter) => bridge.skillEventStats(buildEventsQuery(filter)),
    loadDetail: async (id) => (await bridge.skillEvent(id)).event,
  };
}
