import type { AgentEvent } from "@/api/types";

export type SessionProgressKind = "started" | "toolRunning" | "toolDone" | "toolFailed" | "approval" | "approved" | "denied" | "thinking" | "answering" | "retry" | "ended" | "stopped" | "limited" | "failed";
export interface SessionProgressEvent {
  id: string;
  kind: SessionProgressKind;
  ts: number;
  tool?: string;
}

const limit = 8;
const safeTool = (name?: string) => name && /^[\w.:-]{1,80}$/.test(name) ? name : undefined;

function endedKind(event: AgentEvent): SessionProgressKind {
  // The harness can stop because of its own budget while the provider's last
  // response still says end_turn. Its explicit reason is authoritative.
  const reason = event.end_reason || event.stop_reason;
  if (["turn_limit", "token_budget", "cost_budget", "time_budget", "context_overflow", "max_tokens"].includes(reason ?? "")) return "limited";
  if (["transport_error", "tool_protocol", "stagnation", "error"].includes(reason ?? "") || event.error) return "failed";
  if (reason === "aborted") return "stopped";
  // Even a completed run is described neutrally: a finished model run does
  // not independently prove that every requested business action succeeded.
  return "ended";
}

/** A small public activity feed. It never copies arguments, output, thinking,
 * URLs, error bodies or assistant text into the minimized surface. */
export function foldSessionProgress(previous: readonly SessionProgressEvent[], event: AgentEvent): SessionProgressEvent[] {
  if (event.type === "snapshot") {
    const live = event.snapshot?.live;
    if (!live) return [];
    const gate = live.pending_gates?.[live.pending_gates.length - 1];
    const tool = live.pending_tools?.[live.pending_tools.length - 1];
    const kind: SessionProgressKind | undefined = gate ? "approval" : tool ? "toolRunning" : live.active || event.snapshot?.run?.active ? "started" : undefined;
    // A reconnect only states what is true now. An idle snapshot cannot prove
    // the preceding task succeeded, and never reuses another run's event.
    return kind ? [{ id: `snapshot:${event.seq}`, kind, ts: tool?.started_at || event.ts, tool: safeTool(gate?.tool ?? tool?.name) }] : [];
  }
  let kind: SessionProgressKind | undefined;
  switch (event.type) {
    case "run_start": kind = "started"; previous = []; break;
    case "tool_start": kind = "toolRunning"; break;
    case "tool_end": kind = event.is_error ? "toolFailed" : "toolDone"; break;
    case "gate_request": kind = "approval"; break;
    case "gate_resolved": kind = event.allow === true ? "approved" : "denied"; break;
    case "thinking": kind = "thinking"; break;
    case "token": kind = "answering"; break;
    case "retry": kind = "retry"; break;
    case "error": kind = "failed"; break;
    case "run_end": kind = endedKind(event); break;
    case "rewound": case "compacted": return [];
    default: return previous as SessionProgressEvent[];
  }
  const tool = ["tool_start", "tool_end", "gate_request"].includes(event.type) ? safeTool(event.name) : undefined;
  const last = previous[previous.length - 1];
  // Token/thinking deltas describe one state, not hundreds of new events.
  if (last?.kind === kind && last.tool === tool && ["thinking", "answering", "retry"].includes(kind)) return previous as SessionProgressEvent[];
  const id = `${event.seq}:${event.type}:${event.call_id ?? event.gate_id ?? event.ts}`;
  if (previous.some(item => item.id === id)) return previous as SessionProgressEvent[];
  return [...previous, { id, kind, ts: event.ts, ...(tool ? { tool } : {}) }].slice(-limit);
}

export function sessionProgressTone(kind?: SessionProgressKind): "working" | "done" | "attention" | "error" | "idle" {
  if (["toolFailed", "failed"].includes(kind ?? "")) return "error";
  if (["approval", "denied", "limited", "stopped"].includes(kind ?? "")) return "attention";
  if (["toolDone", "approved"].includes(kind ?? "")) return "done";
  if (kind === "ended") return "idle";
  return kind ? "working" : "idle";
}
