import { computed, onScopeDispose, ref, watch } from "vue";
import { api, authHeaders, streamURL } from "@/api/client";
import type { AgentEvent, Snapshot } from "@/api/types";
import { matchesAgentWorkflowExecution, validAgentWorkflowId } from "./agentWorkflowNavigation";
import { parseFrame } from "./sse";
import { normalizeTaskSnapshot, projectTaskSessionSnapshot, reduceTaskSessionEvent } from "./taskSessionSnapshot";

export interface TaskSessionStreamScope {
  agentId: string; sessionId: string; visible: boolean;
  workflowId?: string; workflowRunId?: string; activeRunId?: string;
}
export type TaskSessionConnection = "idle" | "validating" | "connecting" | "live" | "reconnecting" | "error";
function matchesScope(result: Awaited<ReturnType<typeof api.session>>, selected: TaskSessionStreamScope) {
  const background = result.background_task;
  const workflow = !!selected.workflowId && !!selected.workflowRunId && matchesAgentWorkflowExecution(result.agent_workflow, selected.agentId, selected.workflowId, selected.workflowRunId);
  const schedule = !selected.workflowId && !selected.workflowRunId && background?.source === "schedule" && background.agent_id === selected.agentId && !!background.source_id && background.task_id === `schedule:${background.source_id}`;
  return result.session_id === selected.sessionId && (workflow || schedule);
}

/** Read-only execution subscription. It never starts a run or publishes chat/browser UI intents. */
export function useTaskSessionStream(source: () => TaskSessionStreamScope) {
  const raw = ref<Snapshot | null>(null), verifiedSession = ref<string | null>(null);
  const connection = ref<TaskSessionConnection>("idle"), error = ref(""), authEpoch = ref(0), checkingScope = ref(false);
  const projection = computed(() => projectTaskSessionSnapshot(raw.value, source().activeRunId));
  const snapshot = computed(() => projection.value.snapshot), isolationMissing = computed(() => projection.value.isolationMissing);
  // A legacy node may share the preceding nodes' prompt history. Once the
  // latest Run marker proves attribution, estimates need that actual full
  // request history, not the smaller transcript projected for presentation.
  const contextSnapshot = computed(() => projection.value.snapshot && !projection.value.snapshot.task_context_unavailable ? raw.value : null);
  const loading = computed(() => connection.value === "validating" || connection.value === "connecting");
  const canDecide = computed(() => source().visible && !checkingScope.value && connection.value === "live" && verifiedSession.value === source().sessionId && !!snapshot.value);
  const identity = () => { const value = source(); return JSON.stringify([value.agentId, value.sessionId, value.visible, value.workflowId, value.workflowRunId, value.activeRunId, authEpoch.value]); };
  let disposed = false, generation = 0, failures = 0;
  let controller: AbortController | undefined, retry: ReturnType<typeof setTimeout> | undefined, watchdog: ReturnType<typeof setTimeout> | undefined, scopeTimer: ReturnType<typeof setTimeout> | undefined;

  function interrupt() {
    ++generation; controller?.abort(); clearTimeout(retry); clearTimeout(watchdog); clearTimeout(scopeTimer); checkingScope.value = false; verifiedSession.value = null;
  }
  function close() { interrupt(); raw.value = null; error.value = ""; connection.value = "idle"; }
  function reconnect(mine: number, key: string, terminal = false) {
    if (disposed || mine !== generation || key !== identity() || !source().visible) return;
    clearTimeout(watchdog); clearTimeout(scopeTimer); checkingScope.value = false; controller?.abort(); verifiedSession.value = null;
    error.value = terminal ? "unavailable" : "disconnected";
    connection.value = terminal ? "error" : "reconnecting";
    if (terminal) { raw.value = null; return; }
    const delay = Math.min(10000, 500 * 2 ** Math.min(failures++, 5));
    retry = setTimeout(() => { void connect(true); }, delay);
  }
  function scheduleScopeCheck(requestController: AbortController, mine: number, key: string) {
    clearTimeout(scopeTimer);
    scopeTimer = setTimeout(async () => {
      const current = () => !disposed && mine === generation && key === identity() && !requestController.signal.aborted;
      if (!current()) return;
      checkingScope.value = true;
      const deadline = setTimeout(() => { reconnect(mine, key); }, 10000);
      try {
        const result = await api.session(source().sessionId, requestController.signal);
        if (!current()) return;
        if (!matchesScope(result, source())) { reconnect(mine, key, true); error.value = "scopeChanged"; return; }
        checkingScope.value = false; scheduleScopeCheck(requestController, mine, key);
      } catch (issue) {
        if (current() && (issue as Error)?.name !== "AbortError") reconnect(mine, key, [401, 403, 404].includes((issue as { status?: number })?.status || 0));
      } finally { clearTimeout(deadline); }
    }, 15000);
  }
  async function consume(response: Response, requestController: AbortController, mine: number, key: string) {
    const current = () => !disposed && mine === generation && key === identity() && !requestController.signal.aborted;
    let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
    let receivedSnapshot = false;
    const heartbeat = () => {
      clearTimeout(watchdog);
      watchdog = setTimeout(() => { reconnect(mine, key); }, 45000);
    };
    try {
      reader = response.body!.getReader();
      const decoder = new TextDecoder(); let buffer = "";
      heartbeat();
      while (current()) {
        const result = await reader.read(); if (!current()) return;
        if (result.done) break;
        heartbeat(); buffer += decoder.decode(result.value, { stream: true });
        // Accept CRLF and LF frames, including boundaries split between reads.
        let separator: RegExpExecArray | null;
        while ((separator = /\r?\n\r?\n/.exec(buffer))) {
          const frame = buffer.slice(0, separator.index); buffer = buffer.slice(separator.index + separator[0].length);
          const event = parseFrame(frame); if (!event || !current()) continue;
          if (event.type === "rewound" || event.type === "compacted") { void connect(true); return; }
          if (event.type === "snapshot") {
            if (!event.snapshot) continue;
            raw.value = reduceTaskSessionEvent(raw.value, event);
            receivedSnapshot = true; failures = 0; error.value = "";
            verifiedSession.value = source().sessionId; connection.value = "live";
            scheduleScopeCheck(requestController, mine, key);
          } else if (receivedSnapshot) raw.value = reduceTaskSessionEvent(raw.value, event as AgentEvent);
        }
        if (buffer.length > 8 * 1024 * 1024) throw new Error("Stream frame limit");
      }
      if (current()) reconnect(mine, key);
    } catch (issue) {
      if (current() && (issue as Error)?.name !== "AbortError") reconnect(mine, key);
    } finally {
      if (mine === generation) clearTimeout(watchdog);
      try { await reader?.cancel(); } catch { /* The request may already be aborted. */ }
      reader?.releaseLock();
    }
  }
  async function connect(recovering = false): Promise<void> {
    interrupt();
    if (disposed || !source().visible) { connection.value = "idle"; return; }
    const selected = { ...source() }, key = identity(), mine = generation;
    if (!validAgentWorkflowId(selected.agentId) || !validAgentWorkflowId(selected.sessionId) ||
      selected.workflowId && !validAgentWorkflowId(selected.workflowId) || selected.workflowRunId && !validAgentWorkflowId(selected.workflowRunId) ||
      selected.activeRunId && !validAgentWorkflowId(selected.activeRunId)) {
      raw.value = null; error.value = "scopeChanged"; connection.value = "error"; return;
    }
    const requestController = new AbortController(); controller = requestController;
    const current = () => !disposed && mine === generation && key === identity() && !requestController.signal.aborted;
    connection.value = recovering ? "reconnecting" : "validating";
    if (!recovering) error.value = "";
    watchdog = setTimeout(() => { reconnect(mine, key); }, 10000);
    try {
      // Session identity is authenticated independently of the transcript. Recheck it after every outage.
      const result = await api.session(selected.sessionId, requestController.signal);
      if (!current()) return;
      if (!matchesScope(result, selected)) {
        clearTimeout(watchdog); raw.value = null; error.value = "scopeChanged"; connection.value = "error"; return;
      }
      raw.value = normalizeTaskSnapshot(result.snapshot);
      verifiedSession.value = result.session_id;
      if (!recovering) connection.value = "connecting";
      // Omitting from forces an authoritative snapshot. Incremental replay omits token/tool deltas.
      const response = await fetch(streamURL(selected.sessionId), { headers: authHeaders(), signal: requestController.signal });
      if (!current()) return;
      if (!response.ok || !response.body) { reconnect(mine, key, [401, 403, 404].includes(response.status)); return; }
      void consume(response, requestController, mine, key);
    } catch (issue) {
      if (!current() || (issue as Error)?.name === "AbortError") return;
      reconnect(mine, key, [401, 403, 404].includes((issue as { status?: number })?.status || 0));
    }
  }
  async function refresh() { failures = 0; await connect(); }
  watch(identity, () => { close(); failures = 0; if (source().visible) void connect(); }, { immediate: true, flush: "sync" });
  const authenticationChanged = () => { ++authEpoch.value; };
  if (typeof window !== "undefined") window.addEventListener("pogo:auth-changed", authenticationChanged);
  onScopeDispose(() => { disposed = true; close(); if (typeof window !== "undefined") window.removeEventListener("pogo:auth-changed", authenticationChanged); });
  return { snapshot, contextSnapshot, verifiedSession, connection, error, loading, canDecide, isolationMissing, refresh, close };
}
