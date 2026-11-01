import { onBeforeUnmount, ref, type Ref } from "vue";
import { authHeaders } from "@/api/client";
import { platformApi } from "@/api/platform";
import { parseFrame } from "./sse";
import type { WorkflowRun } from "./workflowTypes";
import { pollIntervalFor, runLive, sortWorkflowRuns, streamEventRun, streamEventType, workflowRunCan } from "./workflowPresentation";

/**
 * Run observation for the workflow editor. The run record (run.json) is the
 * state authority and the stream carries it whole on every structural event,
 * so v2 heals straight from /runs/{rid}/stream: fetch + ReadableStream (the
 * Authorization header rules out EventSource), parseFrame per frame, adopt
 * the record, done. Dispatch-only frames trigger one throttled refetch. Any
 * stream failure — refused, errored, or a clean EOF with the run still live —
 * degrades to polling (tight while agents run, relaxed while only a person is
 * being waited on); a terminal record stops everything.
 */
export function useWorkflowRun(workflowId: Ref<string>) {
  const run = ref<WorkflowRun | null>(null);
  const history = ref<WorkflowRun[]>([]);
  const historyLoading = ref(false);
  const historyError = ref("");
  const starting = ref(false);
  const stopping = ref(false);
  const retrying = ref(false);
  const error = ref("");
  const accessError = ref<401 | 403 | 404 | null>(null);
  /** How the record is currently being followed — diagnostic, not chrome. */
  const mode = ref<"idle" | "sse" | "poll">("idle");

  let controller: AbortController | undefined;
  let pollTimer: number | undefined;
  let refetchTimer: number | undefined;
  let generation = 0;
  let disposed = false;
  let loadEpoch = 0;
  let historyEpoch = 0;

  function stopPolling() {
    if (pollTimer !== undefined) window.clearTimeout(pollTimer);
    pollTimer = undefined;
  }

  function closeStream() {
    generation++;
    controller?.abort();
    controller = undefined;
    if (mode.value === "sse") mode.value = "idle";
  }

  function stopAll() {
    closeStream();
    stopPolling();
    if (refetchTimer !== undefined) window.clearTimeout(refetchTimer);
    refetchTimer = undefined;
    mode.value = "idle";
  }

  function accessStatus(value: unknown): 401 | 403 | 404 | null {
    const status = (value as { status?: number } | null)?.status;
    return status === 401 || status === 403 || status === 404 ? status : null;
  }

  function invalidateAccess(id: string | null, failure: unknown, epoch: number): boolean {
    const status = accessStatus(failure);
    if (!status || disposed || epoch !== loadEpoch || (id && run.value && run.value.id !== id)) return false;
    stopAll();
    ++loadEpoch;
    ++historyEpoch; // A stale list must not put the revoked record back.
    historyLoading.value = false;
    run.value = null;
    history.value = status === 401 || !id ? [] : history.value.filter(item => item.id !== id);
    accessError.value = status;
    error.value = failure instanceof Error ? failure.message : "Run unavailable";
    return true;
  }

  function adopt(record: WorkflowRun) {
    if (disposed) return;
    // Structural SSE frames may contain only the durable engine record.
    if (!record.capabilities && run.value?.id === record.id && run.value.capabilities) {
      record = { ...record, capabilities: run.value.capabilities };
    }
    run.value = record;
    accessError.value = null;
    history.value = sortWorkflowRuns([record, ...history.value.filter((item) => item.id !== record.id)]);
    if (!runLive(record)) stopAll();
  }

  async function refresh() {
    const current = run.value;
    if (!current || disposed) return;
    const epoch = loadEpoch;
    try {
      const next = await platformApi.workflowRuns.get(workflowId.value, current.id);
      if (epoch !== loadEpoch || run.value?.id !== current.id) return;
      adopt(next);
    } catch (err) {
      if (invalidateAccess(current.id, err, epoch)) return;
      // Non-fatal: the canvas simply lags one cycle.
    }
  }

  function startPolling() {
    if (disposed || pollTimer !== undefined || !runLive(run.value)) return;
    closeStream();
    mode.value = "poll";
    const tick = async () => {
      pollTimer = undefined;
      if (disposed || document.hidden || !runLive(run.value)) {
        if (runLive(run.value)) pollTimer = window.setTimeout(tick, pollIntervalFor(run.value));
        return;
      }
      await refresh();
      if (runLive(run.value)) pollTimer = window.setTimeout(tick, pollIntervalFor(run.value));
    };
    pollTimer = window.setTimeout(tick, pollIntervalFor(run.value));
  }

  /** Dispatch frames carry the delegation, not the record; one throttled GET
      a beat later picks up the settled state (and everything else). */
  function scheduleRefetch() {
    if (refetchTimer !== undefined || disposed) return;
    refetchTimer = window.setTimeout(() => {
      refetchTimer = undefined;
      void refresh();
    }, 800);
  }

  async function connect(rid: string) {
    if (disposed) return;
    closeStream();
    const gen = generation;
    const epoch = loadEpoch;
    const ctrl = new AbortController();
    controller = ctrl;
    try {
      const res = await fetch(platformApi.workflowRuns.streamUrl(workflowId.value, rid), {
        credentials: "same-origin",
        cache: "no-store",
        headers: authHeaders(),
        signal: ctrl.signal,
      });
      if (disposed || gen !== generation || epoch !== loadEpoch) return;
      if (!res.ok || !res.body) {
        if (invalidateAccess(rid, { status: res.status }, epoch)) return;
        startPolling();
        return;
      }
      if (disposed || gen !== generation) return;
      mode.value = "sse";
      const reader = res.body.getReader();
      const decoder = new TextDecoder("utf-8");
      let buffer = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        let sep: number;
        while ((sep = buffer.indexOf("\n\n")) !== -1) {
          const frame = buffer.slice(0, sep);
          buffer = buffer.slice(sep + 2);
          const event = parseFrame(frame);
          if (!event || gen !== generation) continue;
          const record = streamEventRun(event);
          if (record) adopt(record);
          else if (streamEventType(event).startsWith("workflow_dispatch")) scheduleRefetch();
        }
      }
    } catch (e) {
      if ((e as Error)?.name === "AbortError") return;
      // Fall through to the polling fallback below.
    } finally {
      if (gen === generation && controller === ctrl) {
        controller = undefined;
        if (mode.value === "sse") mode.value = "idle";
      }
    }
    // A clean EOF with the run still going (server restart, a suspended run's
    // single-frame stream) heals by polling.
    if (!disposed && gen === generation && runLive(run.value)) startPolling();
  }

  async function start(): Promise<boolean> {
    if (starting.value || runLive(run.value) || run.value?.persistence_error) return false;
    starting.value = true;
    const epoch = ++loadEpoch;
    error.value = "";
    accessError.value = null;
    let startedId: string | null = null;
    try {
      const { id } = await platformApi.workflowRuns.start(workflowId.value);
      startedId = id;
      const record = await platformApi.workflowRuns.get(workflowId.value, id);
      if (disposed || epoch !== loadEpoch) return false;
      adopt(record);
      if (runLive(run.value)) void connect(id);
      return true;
    } catch (e) {
      if (epoch === loadEpoch && !disposed) {
        if (!invalidateAccess(startedId, e, epoch)) error.value = (e as Error).message;
      }
      return false;
    } finally {
      starting.value = false;
    }
  }

  async function stop(): Promise<void> {
    const current = run.value;
    if (!current || stopping.value || !runLive(current) || !workflowRunCan(current, "can_cancel")) return;
    stopping.value = true;
    error.value = "";
    const epoch = loadEpoch;
    try {
      await platformApi.workflowRuns.cancel(workflowId.value, current.id);
      if (disposed || epoch !== loadEpoch || run.value?.id !== current.id) return;
      await refresh();
    } catch (e) {
      if (epoch === loadEpoch && !disposed && !invalidateAccess(current.id, e, epoch)) error.value = (e as Error).message;
    } finally {
      stopping.value = false;
    }
  }

  async function loadHistory(): Promise<void> {
    const epoch = ++historyEpoch;
    const selectionEpoch = loadEpoch;
    historyLoading.value = true;
    historyError.value = "";
    try {
      const { runs } = await platformApi.workflowRuns.list(workflowId.value);
      if (disposed || epoch !== historyEpoch) return;
      history.value = sortWorkflowRuns(runs);
    } catch (err) {
      if (!disposed && epoch === historyEpoch) {
        if (selectionEpoch === loadEpoch && !invalidateAccess(null, err, selectionEpoch)) historyError.value = err instanceof Error ? err.message : String(err);
      }
    } finally {
      if (epoch === historyEpoch) historyLoading.value = false;
    }
  }

  async function retry(): Promise<boolean> {
    const current = run.value;
    if (!current || retrying.value || !workflowRunCan(current, "can_retry", false)) return false;
    retrying.value = true;
    error.value = "";
    const epoch = loadEpoch;
    try {
      const { id } = await platformApi.workflowRuns.retry(workflowId.value, current.id);
      if (disposed || epoch !== loadEpoch || run.value?.id !== current.id) return false;
      return await loadRun(id);
    } catch (err) {
      if (epoch === loadEpoch && !disposed && !invalidateAccess(current.id, err, epoch)) error.value = err instanceof Error ? err.message : String(err);
      return false;
    } finally {
      retrying.value = false;
    }
  }

  /** Opening the editor shows the latest run's state; a still-live run
      (started in another tab, or suspended on a person) resumes watching. */
  async function loadLatest(): Promise<void> {
    const epoch = ++loadEpoch;
    stopAll();
    try {
      const { runs } = await platformApi.workflowRuns.list(workflowId.value);
      if (disposed || epoch !== loadEpoch) return;
      history.value = sortWorkflowRuns(runs);
      const latest = history.value[0] ?? null;
      if (latest) adopt(latest);
      else run.value = null;
      if (runLive(latest)) void connect(latest!.id);
    } catch (err) {
      invalidateAccess(null, err, epoch);
      // Runs are optional furniture: the editor works without them.
    }
  }

  /** A task-center result opens its exact run, never whichever ran most recently. */
  async function loadRun(id: string): Promise<boolean> {
    const epoch = ++loadEpoch;
    stopAll();
    run.value = null;
    error.value = "";
    accessError.value = null;
    try {
      const record = await platformApi.workflowRuns.get(workflowId.value, id);
      if (disposed || epoch !== loadEpoch) return false;
      adopt(record);
      if (runLive(record)) void connect(record.id);
      return true;
    } catch (err) {
      if (!disposed && epoch === loadEpoch && !invalidateAccess(id, err, epoch)) error.value = err instanceof Error ? err.message : String(err);
      return false;
    }
  }

  onBeforeUnmount(() => {
    disposed = true;
    ++loadEpoch;
    stopAll();
  });

  return { run, history, historyLoading, historyError, starting, stopping, retrying, error, accessError, mode, start, stop, retry, loadHistory, loadLatest, loadRun, refresh };
}
