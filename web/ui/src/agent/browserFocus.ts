import type { AgentEvent } from "@/api/types";
import type { DockTab } from "@/components/dockSheets";
import type { BrowserEventDelivery } from "./browserView";
import { completeBrowserState } from "./browserSheets";
import type { NativeBrowserState } from "./nativeBrowser";

/** Display intent only. Selecting a Sheet neither changes the Agent's page nor
 * transfers its control lease. All identities come from a completed tool call. */
export interface BrowserFocusRequest {
  phase: "run" | "reset" | "start" | "complete";
  sessionId: string;
  runId?: string;
  callId?: string;
  seq: number;
  contextRef?: string;
  tabId?: string;
  generation?: number;
}

let focusHook: ((request: BrowserFocusRequest) => void) | null = null;
export function setBrowserFocusHook(fn: ((request: BrowserFocusRequest) => void) | null): () => void {
  focusHook = fn;
  return () => { if (focusHook === fn) focusHook = null; };
}

const contextPattern = /^ctx_[a-f0-9]{24}$/;
const tabPattern = /^t[1-9]\d*$/;
const validSeq = (seq: number) => Number.isSafeInteger(seq) && seq >= 0;
const nonempty = (value: unknown): value is string => typeof value === "string" && value.length > 0;
function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}
function tabIntent(value: unknown): boolean {
  const args = record(value);
  if (typeof args?.action !== "string" || args.action.trim() !== "tab") return false;
  if (args.tab_action === "new") return args.target === undefined || args.target === "";
  return (args.tab_action === undefined || args.tab_action === "" || args.tab_action === "switch")
    && typeof args.target === "string" && tabPattern.test(args.target);
}
interface FocusCall { action: "tab" | "batch"; firstStep: number; steps: number; claim?: { context: string; tab: string; epoch: number } }
function focusCall(value: unknown): FocusCall | undefined {
  const args = record(value);
  if (args?.action === "tab" && args.tab_action === "claim" && typeof args.target === "string" && tabPattern.test(args.target)
    && typeof args.context_ref === "string" && contextPattern.test(args.context_ref)
    && Number.isSafeInteger(args.control_epoch) && (args.control_epoch as number) >= 0) {
    return { action: "tab", firstStep: 0, steps: 1, claim: { context: args.context_ref, tab: args.target, epoch: args.control_epoch as number } };
  }
  if (tabIntent(args)) return { action: "tab", firstStep: 0, steps: 1 };
  if (typeof args?.action !== "string" || args.action.trim() !== "batch" || !Array.isArray(args.actions)
    || args.actions.length < 1 || args.actions.length > 12) return;
  const firstStep = args.actions.findIndex(tabIntent);
  if (firstStep >= 0) return { action: "batch", firstStep, steps: args.actions.length };
}
interface ObservedSession {
  seq: number;
  runId?: string;
  seen: Set<string>;
  inflight: Map<string, FocusCall>;
}

/** Replays restore identity but never focus. Only a fresh main-run tab action
 * with its matching successful completion can request a different Sheet. */
export function createBrowserFocusObserver() {
  let selectedSession: string | null = null;
  const sessions = new Map<string, ObservedSession>();
  const sessionFor = (sid: string): ObservedSession => {
    let session = sessions.get(sid);
    if (!session) { session = { seq: -1, seen: new Set(), inflight: new Map() }; sessions.set(sid, session); }
    return session;
  };
  return {
    selectSession(sessionId: string | null): void {
      if (sessionId === selectedSession) return;
      if (selectedSession) sessionFor(selectedSession).inflight.clear();
      selectedSession = sessionId;
      if (sessionId) {
        const session = sessionFor(sessionId);
        session.inflight.clear();
        focusHook?.({ phase: "reset", sessionId, runId: session.runId, seq: Math.max(0, session.seq) });
      }
    },
    observe(event: AgentEvent, delivery: BrowserEventDelivery): void {
      const sid = delivery.sessionId;
      if (!sid || sid !== selectedSession || !validSeq(event.seq)) return;
      const session = sessionFor(sid);
      if (event.type === "snapshot") {
        session.seq = event.seq;
        const snapshot = event.snapshot;
        const runId = snapshot?.run?.active ? snapshot.run.run_id ?? snapshot.live?.run_id : undefined;
        if (runId !== session.runId) session.seen.clear();
        session.runId = nonempty(runId) ? runId : undefined;
        session.inflight.clear();
        focusHook?.({ phase: "reset", sessionId: sid, runId: session.runId, seq: event.seq });
        return;
      }
      if (event.seq <= session.seq) return;
      session.seq = event.seq;
      if (event.type === "run_start") {
        if (!nonempty(event.run_id) || event.run_id === session.runId) return;
        session.runId = event.run_id;
        session.seen.clear();
        session.inflight.clear();
        // A restored run can later make a genuinely new live call, but its
        // backlog must first discard any display intent from the old stream.
        focusHook?.({ phase: delivery.replay ? "reset" : "run", sessionId: sid, runId: session.runId, seq: event.seq });
        return;
      }
      if (event.type === "run_end") {
        if (event.run_id !== undefined && event.run_id !== session.runId) return;
        session.runId = undefined;
        session.inflight.clear();
        // A completed request may still await Sheet discovery after run_end.
        return;
      }
      if (!session.runId || event.name !== "browser" || !nonempty(event.call_id)
        || event.task_id !== undefined || event.frame !== undefined || event.frames !== undefined
        || (event.run_id !== undefined && event.run_id !== session.runId)) return;
      if (event.type === "tool_start") {
        if (session.seen.has(event.call_id)) return;
        session.seen.add(event.call_id);
        const call = focusCall(event.args);
        if (!call || delivery.replay) return;
        session.inflight.set(event.call_id, call);
        focusHook?.({ phase: "start", sessionId: sid, runId: session.runId, callId: event.call_id, seq: event.seq });
        return;
      }
      if (event.type !== "tool_end") return;
      const call = session.inflight.get(event.call_id);
      session.inflight.delete(event.call_id);
      const details = record(event.details);
      if (!call || delivery.replay || (event.is_error !== undefined && event.is_error !== false)
        || details?.ok !== true || details.backend !== "native" || details.action !== call.action
        || details.content_source === "pogo_bookmarks" || typeof details.context_ref !== "string"
        || !contextPattern.test(details.context_ref) || typeof details.active_tab !== "string"
        || !tabPattern.test(details.active_tab) || typeof details.version !== "string") return;
      // Claim transfers only the explicitly observed Page, never a sibling or
      // reincarnated context returned by a delayed completion.
      if (call.claim && (details.context_ref !== call.claim.context || details.active_tab !== call.claim.tab
        || details.control !== "agent" || !Number.isSafeInteger(details.control_epoch)
        || (details.control_epoch as number) <= call.claim.epoch)) return;
      // A batch can stop at a dialog before reaching its requested tab action.
      if (call.action === "batch" && (!Number.isSafeInteger(details.steps) || (details.steps as number) <= call.firstStep
        || (details.steps as number) > call.steps)) return;
      const version = /^([1-9]\d*):(t[1-9]\d*):/.exec(details.version);
      const generation = version ? Number(version[1]) : NaN;
      if (!version || version[2] !== details.active_tab || !Number.isSafeInteger(generation)) return;
      focusHook?.({ phase: "complete", sessionId: sid, runId: session.runId, callId: event.call_id, seq: event.seq,
        contextRef: details.context_ref, tabId: details.active_tab, generation });
    },
  };
}

const focusLifetime = 15_000;
interface PendingFocus extends BrowserFocusRequest { runId: string; callId: string; contextRef: string; tabId: string; generation: number; expires: number; epoch: number }

/** Explicit Agent tab actions follow once. Polls and popup observations alone
 * preserve the user's selection; manual selection cancels an unfinished action. */
export class BrowserFocusPolicy {
  private sessionId?: string;
  private runId?: string;
  private seq = -1;
  private epoch = 0;
  private inflight = new Map<string, { epoch: number; seq: number }>();
  private pending?: PendingFocus;

  reset(): void {
    this.epoch++;
    this.sessionId = this.runId = undefined;
    this.seq = -1;
    this.inflight.clear();
    this.pending = undefined;
  }
  manual(): void {
    this.epoch++;
    this.inflight.clear();
    this.pending = undefined;
  }
  observe(request: BrowserFocusRequest, now = Date.now()): void {
    if (!nonempty(request.sessionId) || !validSeq(request.seq)) return;
    if (request.phase === "reset") {
      this.reset();
      this.sessionId = request.sessionId;
      this.runId = request.runId;
      this.seq = request.seq;
      return;
    }
    if (request.phase === "run") {
      if (!nonempty(request.runId) || (request.sessionId === this.sessionId && request.seq <= this.seq)) return;
      this.reset();
      this.sessionId = request.sessionId;
      this.runId = request.runId;
      this.seq = request.seq;
      return;
    }
    if (request.sessionId !== this.sessionId || request.runId !== this.runId || !nonempty(request.runId)
      || !nonempty(request.callId) || request.seq <= this.seq) return;
    this.seq = request.seq;
    if (request.phase === "start") {
      this.pending = undefined;
      this.inflight.set(request.callId, { epoch: this.epoch, seq: request.seq });
      return;
    }
    if (request.phase !== "complete") return;
    const started = this.inflight.get(request.callId);
    this.inflight.delete(request.callId);
    if (!started || started.epoch !== this.epoch || request.seq <= started.seq
      || typeof request.contextRef !== "string" || !contextPattern.test(request.contextRef)
      || typeof request.tabId !== "string" || !tabPattern.test(request.tabId)
      || !Number.isSafeInteger(request.generation) || request.generation! < 1 || !Number.isFinite(now)) return;
    this.pending = { ...request, runId: request.runId, callId: request.callId, contextRef: request.contextRef,
      tabId: request.tabId, generation: request.generation!, expires: now + focusLifetime, epoch: this.epoch };
  }
  selection(tabs: readonly DockTab[], sid: string | null, state: NativeBrowserState, now = Date.now()): string | undefined {
    const pending = this.pending;
    if (!pending) return;
    if (pending.epoch !== this.epoch || pending.sessionId !== sid || pending.runId !== this.runId
      || !Number.isFinite(now) || now >= pending.expires) { this.pending = undefined; return; }
    if (!completeBrowserState(state)) return;
    if (state.context_ref !== pending.contextRef || state.generation !== pending.generation || state.reset) {
      this.pending = undefined;
      return;
    }
    if (state.active_tab !== pending.tabId || !state.tabs.some(page => page.id === pending.tabId)) return;
    const sheet = tabs.find(tab => tab.kind === "browser" && !tab.browserUnavailable
      && tab.browserBinding?.sessionId === sid && tab.browserBinding.context_ref === pending.contextRef
      && tab.browserBinding.tab_id === pending.tabId);
    if (!sheet) return;
    this.pending = undefined;
    return sheet.id;
  }
}
