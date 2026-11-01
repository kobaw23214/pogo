import { describe, expect, it, vi } from "vitest";
import skillMonitorMessages from "@/i18n/messages/skillMonitor";
import {
  asSkillMonitorBridge,
  buildEventsQuery,
  createDefaultSkillFilter,
  createSkillBridgeLoaders,
  hasKnownDuration,
  mergeEvents,
  normalizeSkillCaller,
  normalizeSkillEntrypoint,
  normalizeSkillStage,
  normalizeSkillStatus,
  skillMechanismLabelKey,
  skillSourceLabelKey,
  snapshotSkillFilter,
} from "./skillMonitor";
import type {
  SkillEvent,
  SkillEventStats,
  SkillMonitorApiBridge,
} from "./skillMonitor";

const NOW = 1_800_000_000_000;

function event(id: string, over: Partial<SkillEvent> = {}): SkillEvent {
  return {
    id,
    trace_id: "",
    skill_id: "skill-1",
    skill_name: "demo",
    skill_source: "project",
    stage: "execute",
    caller: "internal",
    entrypoint: "pogo",
    mechanism: "manifest",
    started_at: NOW,
    duration_ms: 0,
    status: "ok",
    ...over,
  };
}

// A stats payload in the exact contract shape: flat stage counters,
// execution_* outcome counters, by_caller and per-skill rows. If the backend
// shape drifts the type checker fails here, not in the browser.
function stats(over: Partial<SkillEventStats> = {}): SkillEventStats {
  return {
    total: 0,
    discoveries: 0,
    loads: 0,
    executions: 0,
    execution_running: 0,
    execution_ok: 0,
    execution_errors: 0,
    execution_cancelled: 0,
    execution_timeouts: 0,
    execution_interrupted: 0,
    error_rate: null,
    p50_ms: null,
    p95_ms: null,
    max_ms: null,
    by_caller: { internal: 0, external: 0, unknown: 0 },
    by_skill: [],
    storage: "sqlite",
    dropped: 0,
    write_errors: 0,
    ...over,
  };
}

function parsed(query: string): URLSearchParams {
  return new URLSearchParams(query);
}

describe("buildEventsQuery", () => {
  it("keeps catalog identity distinct when skills share a display name", () => {
    const f = { ...createDefaultSkillFilter(), skillName: "same-name", skillId: "catalog-a", entrypoint: "mcp" as const, caller: "internal" as const, q: "trace-fragment" };
    const q = parsed(buildEventsQuery(f, { now: NOW }));
    expect(q.get("skill_id")).toBe("catalog-a");
    expect(q.get("skill_name")).toBe("same-name");
    expect(q.get("entrypoint")).toBe("mcp");
    expect(q.get("caller")).toBe("internal");
    expect(q.get("q")).toBe("trace-fragment");
  });
  it("keeps one committed time window across stats, paging and unapplied input", () => {
    const draft = createDefaultSkillFilter();
    const committed = snapshotSkillFilter(draft, NOW);
    draft.skillName = "not-applied";
    const first = parsed(buildEventsQuery(committed, { limit: 100 }));
    const later = parsed(buildEventsQuery(committed, { cursor: "page-two" }));
    expect(first.get("from")).toBe(later.get("from"));
    expect(first.get("to")).toBe(later.get("to"));
    expect(first.has("skill_name")).toBe(false);
    expect(parsed(buildEventsQuery(snapshotSkillFilter(draft, NOW + 10000))).get("skill_name")).toBe("not-applied");
  });

  it("applies the selected window as an explicit from/to range", () => {
    const f = { ...createDefaultSkillFilter(), window: "1h" as const };
    const q = parsed(buildEventsQuery(f, { now: NOW }));
    expect(q.get("to")).toBe(String(NOW));
    expect(q.get("from")).toBe(String(NOW - 60 * 60 * 1000));
    const week = parsed(buildEventsQuery({ ...f, window: "7d" }, { now: NOW }));
    expect(week.get("from")).toBe(String(NOW - 7 * 24 * 60 * 60 * 1000));
  });

  it("defaults to all stages and all callers with a 24h window", () => {
    const f = createDefaultSkillFilter();
    expect(f.stage).toBe("");
    expect(f.caller).toBe("");
    expect(f.status).toBe("");
    expect(f.window).toBe("24h");
    const q = parsed(buildEventsQuery(f, { now: NOW }));
    expect(q.has("stage")).toBe(false);
    expect(q.has("caller")).toBe(false);
    expect(q.has("status")).toBe(false);
    expect(q.get("from")).toBe(String(NOW - 24 * 60 * 60 * 1000));
  });

  it("serializes exact filters, correlation ids, cursor and limit", () => {
    const f = {
      ...createDefaultSkillFilter(),
      stage: "execute" as const,
      caller: "external" as const,
      status: "timeout" as const,
      skillName: "  pdf-tools  ",
      tool: "run_pdf",
      traceId: "trace-1",
      sessionId: "session-1",
    };
    const q = parsed(buildEventsQuery(f, { now: NOW, cursor: "cur", limit: 100 }));
    expect(q.get("stage")).toBe("execute");
    expect(q.get("caller")).toBe("external");
    expect(q.get("status")).toBe("timeout");
    expect(q.get("skill_name")).toBe("pdf-tools");
    expect(q.get("tool")).toBe("run_pdf");
    expect(q.get("trace_id")).toBe("trace-1");
    expect(q.get("session_id")).toBe("session-1");
    expect(q.get("before")).toBe("cur");
    expect(q.get("limit")).toBe("100");
  });

  it("serializes the CLI entrypoint identically for list and stats", () => {
    const f = { ...createDefaultSkillFilter(), entrypoint: "cli" as const };
    const list = parsed(buildEventsQuery(f, { now: NOW, limit: 100 }));
    const stats = parsed(buildEventsQuery(f, { now: NOW }));
    for (const q of [list, stats]) {
      expect(q.get("entrypoint")).toBe("cli");
    }
    // Caller credentials are not part of the monitoring query.
    const none = parsed(buildEventsQuery(createDefaultSkillFilter(), { now: NOW }));
    expect(none.has("client_id")).toBe(false);
  });
});

describe("mergeEvents", () => {
  it("appends a keyset page without duplicating shifted rows", () => {
    const merged = mergeEvents(
      [event("a"), event("b")],
      [event("b", { status: "error" }), event("c")],
    );
    expect(merged.map((e) => e.id)).toEqual(["a", "b", "c"]);
    // The first page's copy wins so a fresh reload replaces stale rows.
    expect(merged[1].status).toBe("ok");
  });
});

describe("normalizers", () => {
  it("passes through the contract stages and buckets the rest as unknown", () => {
    expect(normalizeSkillStage("discovery")).toBe("discovery");
    expect(normalizeSkillStage("load")).toBe("load");
    expect(normalizeSkillStage("execute")).toBe("execute");
    expect(normalizeSkillStage("success")).toBe("unknown");
    expect(normalizeSkillStage("")).toBe("unknown");
  });

  it("keeps caller, entrypoint and status vocabularies honest", () => {
    expect(normalizeSkillCaller("internal")).toBe("internal");
    expect(normalizeSkillCaller("external")).toBe("external");
    expect(normalizeSkillCaller("nested")).toBe("unknown");
    expect(normalizeSkillEntrypoint("pogo")).toBe("pogo");
    expect(normalizeSkillEntrypoint("mcp")).toBe("mcp");
    expect(normalizeSkillEntrypoint("cli")).toBe("cli");
    expect(normalizeSkillEntrypoint("sdk")).toBe("unknown");
    for (const s of ["running", "ok", "error", "cancelled", "timeout", "interrupted"]) {
      expect(normalizeSkillStatus(s)).toBe(s);
    }
    expect(normalizeSkillStatus("done")).toBe("unknown");
  });
});

describe("hasKnownDuration", () => {
  it("treats a real finished 0 ms as known, running and interrupted as unknown", () => {
    expect(hasKnownDuration(event("e", { status: "ok", duration_ms: 0 }))).toBe(true);
    expect(hasKnownDuration(event("e", { status: "error" }))).toBe(true);
    expect(hasKnownDuration(event("e", { status: "timeout" }))).toBe(true);
    expect(hasKnownDuration(event("e", { status: "cancelled" }))).toBe(true);
    expect(hasKnownDuration(event("e", { status: "running" }))).toBe(false);
    expect(hasKnownDuration(event("e", { status: "interrupted" }))).toBe(false);
    expect(hasKnownDuration(event("e", { status: "weird" }))).toBe(false);
  });
});

describe("asSkillMonitorBridge", () => {
  it("rejects an api object without the skill monitor methods", () => {
    expect(() => asSkillMonitorBridge({ skills: () => Promise.resolve() })).toThrow(/not wired/);
    expect(() => asSkillMonitorBridge(null)).toThrow(/not wired/);
    expect(() => asSkillMonitorBridge({ skillEvents: () => Promise.resolve() })).toThrow(/not wired/);
  });

  it("accepts a fully wired bridge", () => {
    const bridge: SkillMonitorApiBridge = {
      skillEvents: vi.fn(),
      skillEventStats: vi.fn(),
      skillEvent: vi.fn(),
    };
    expect(asSkillMonitorBridge(bridge)).toBe(bridge);
  });
});

describe("createSkillBridgeLoaders", () => {
  function bridgeWith(queries: string[]): SkillMonitorApiBridge {
    return {
      skillEvents: vi.fn(async (q: string) => {
        queries.push(q);
        return { events: [], next_cursor: "", storage: "sqlite" as const, dropped: 0, write_errors: 0 };
      }),
      skillEventStats: vi.fn(async (q: string) => {
        queries.push(q);
        return stats();
      }),
      skillEvent: vi.fn(async (id: string) => ({ event: event(id) })),
    };
  }

  it("sends list and stats through the same serializer with matching filters", async () => {
    const queries: string[] = [];
    const bridge = bridgeWith(queries);
    const loaders = createSkillBridgeLoaders(bridge, 100);
    const filter = { ...snapshotSkillFilter(createDefaultSkillFilter(), NOW), stage: "load" as const };
    await loaders.loadEvents(filter, "cur");
    await loaders.loadStats(filter);
    const listQ = parsed(queries[0]);
    const statsQ = parsed(queries[1]);
    expect(listQ.get("stage")).toBe("load");
    expect(statsQ.get("stage")).toBe("load");
    // Only pagination differs between the two queries.
    listQ.delete("before");
    listQ.delete("limit");
    expect(listQ.toString()).toBe(statsQ.toString());
  });

  it("unwraps the detail envelope", async () => {
    const bridge = bridgeWith([]);
    const detail = await createSkillBridgeLoaders(bridge, 100).loadDetail("d9");
    expect(detail.id).toBe("d9");
    expect(bridge.skillEvent).toHaveBeenCalledWith("d9");
  });

  it("accepts contract stats without inventing missing samples", async () => {
    const queries: string[] = [];
    const bridge = bridgeWith(queries);
    // stage=load: execution metrics are intentionally null/zero per contract.
    const s = stats({ loads: 3, total: 3, by_caller: { internal: 3, external: 0, unknown: 0 } });
    expect(s.error_rate).toBeNull();
    expect(s.p50_ms).toBeNull();
    expect(s.executions).toBe(0);
    const got = await createSkillBridgeLoaders(bridge, 100).loadStats(createDefaultSkillFilter());
    expect(got.by_caller.internal).toBe(0);
    expect(got.by_skill).toEqual([]);
  });
});

describe("mechanism/source label keys", () => {
  it("maps every contract value to its own label key, unknowns included", () => {
    expect(skillMechanismLabelKey("catalog")).toBe("skillMonitor.mechanismCatalog");
    expect(skillMechanismLabelKey("file_read")).toBe("skillMonitor.mechanismFileRead");
    expect(skillMechanismLabelKey("explicit")).toBe("skillMonitor.mechanismExplicit");
    expect(skillMechanismLabelKey("manifest")).toBe("skillMonitor.mechanismManifest");
    expect(skillMechanismLabelKey("runtime")).toBe("skillMonitor.mechanismRuntime");
    expect(skillMechanismLabelKey("nested")).toBe("skillMonitor.mechanismUnknown");
    expect(skillSourceLabelKey("user")).toBe("skillMonitor.sourceUser");
    expect(skillSourceLabelKey("project")).toBe("skillMonitor.sourceProject");
    expect(skillSourceLabelKey("path")).toBe("skillMonitor.sourcePath");
    expect(skillSourceLabelKey("embedded")).toBe("skillMonitor.sourceEmbedded");
    expect(skillSourceLabelKey("bundle")).toBe("skillMonitor.sourceBundle");
    expect(skillSourceLabelKey("weird")).toBe("skillMonitor.sourceUnknown");
  });

  it("ships zh-CN and en copy for the packaged runtime skill enums", () => {
    const messages = skillMonitorMessages.messages;
    expect(messages["zh-CN"].mechanismRuntime).toBe("运行时契约");
    expect(messages.en.mechanismRuntime).toBe("Runtime contract");
    expect(messages["zh-CN"].sourceBundle).toBe("运行时包");
    expect(messages.en.sourceBundle).toBe("Runtime package");
  });
});
