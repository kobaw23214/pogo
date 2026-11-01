import type { AgentEvent } from "@/api/types";

export interface BrowserRevealRequest {
  sessionId: string;
  runId: string;
  callId?: string;
  seq: number;
}
export interface BrowserEventDelivery {
  sessionId: string | null;
  replay: boolean;
}

function browserMetadata(args: unknown): boolean {
  return !!args && typeof args === "object" && !Array.isArray(args)
    && typeof (args as { action?: unknown }).action === "string"
    && (args as { action: string }).action.trim() === "bookmarks";
}

// UI observation only. Browser execution stays in the server-side Chromium session.
let ensurePaneHook: ((request: BrowserRevealRequest) => void) | null = null;
export function setEnsurePaneHook(fn: ((request: BrowserRevealRequest) => void) | null): () => void {
  ensurePaneHook = fn;
  return () => { if (ensurePaneHook === fn) ensurePaneHook = null; };
}

/** Observe the first browser call in a run. Replayed calls count as already
 * revealed but cannot move the UI. A snapshot only restores run identity. */
export function createBrowserReveal() {
  let selectedSession: string | null = null;
  const sessions = new Map<string, { runId?: string; seq: number }>();
  const revealed = new Set<string>();
  return {
    selectSession(sessionId: string | null) { selectedSession = sessionId; },
    observe(event: AgentEvent, delivery: BrowserEventDelivery) {
      const sid = delivery.sessionId;
      if (!sid || sid !== selectedSession) return;
      const session = sessions.get(sid) ?? { seq: -1 };
      sessions.set(sid, session);
      if (!Number.isSafeInteger(event.seq) || event.seq < 0) return;
      if (event.type === "snapshot") {
        // A current subscription's snapshot can restart the sequence after a
        // server restart. Preserve run deduplication, but adopt its new cursor.
        session.seq = event.seq;
        const snapshot = event.snapshot;
        session.runId = snapshot?.run?.active ? snapshot.run.run_id ?? snapshot.live?.run_id : undefined;
        if (session.runId && snapshot?.live?.pending_tools.some(tool => tool.name === "browser" && !browserMetadata(tool.args))) revealed.add(JSON.stringify([sid, session.runId]));
        return;
      }
      if (event.seq <= session.seq) return;
      session.seq = event.seq;
      if (event.type === "run_start") { session.runId = event.run_id; return; }
      if (event.type === "run_end") { session.runId = undefined; return; }
      if (event.type !== "tool_start" || event.name !== "browser" || !session.runId) return;
      if (browserMetadata(event.args)) return;
      const id = JSON.stringify([sid, session.runId]);
      if (revealed.has(id)) return;
      revealed.add(id);
      if (!delivery.replay) ensurePaneHook?.({ sessionId: sid, runId: session.runId, callId: event.call_id, seq: event.seq });
    },
  };
}

/** The view may need to wait for a real browser Sheet before entering.
 * Re-check authority at that time, and respect every manual exit in this run. */
export class BrowserImmersionPolicy {
  private requests = new Set<string>();
  private dismissed = new Set<string>();
  private key(sessionId: string, runId: string) { return JSON.stringify([sessionId, runId]); }
  request(request: BrowserRevealRequest): boolean {
    const key = this.key(request.sessionId, request.runId);
    if (this.requests.has(key) || this.dismissed.has(key)) return false;
    this.requests.add(key);
    return true;
  }
  isAllowed(request: BrowserRevealRequest, currentSession: string | null, currentRun: string | undefined): boolean {
    const key = this.key(request.sessionId, request.runId);
    return request.sessionId === currentSession && request.runId === currentRun && this.requests.has(key) && !this.dismissed.has(key);
  }
  manualExit(sessionId: string | null, runId: string | undefined): void {
    if (sessionId && runId) this.dismissed.add(this.key(sessionId, runId));
  }
}

/** Backlog and subscription are cut atomically on the server. Missing headers
 * fail closed for automatic reveals while the normal event fold still works. */
export function browserStreamBacklogThrough(value: string | null): number {
  if (value === null || !/^\d+$/.test(value)) return Infinity;
  const cutoff = Number(value);
  return Number.isSafeInteger(cutoff) ? cutoff : Infinity;
}
