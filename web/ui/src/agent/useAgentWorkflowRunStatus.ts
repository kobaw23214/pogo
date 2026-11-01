import { computed, inject, onScopeDispose, ref, watch } from "vue";
import type { InjectionKey } from "vue";
import { agentWorkflowsApi } from "@/api/agentWorkflows";
import type { AgentWorkflowRunStatusItem } from "@/api/agentWorkflows";
import { validAgentWorkflowId } from "./agentWorkflowNavigation";
import { agentWorkflowUpdatesKey } from "./agentWorkflowUpdates";

export interface AgentWorkflowRunCounts { active: number; waiting: number; running: number; queued: number; cancelling: number }
export interface AgentWorkflowFlowStatus extends AgentWorkflowRunCounts { preferred: AgentWorkflowRunStatusItem }
const emptyCounts = (): AgentWorkflowRunCounts => ({ active: 0, waiting: 0, running: 0, queued: 0, cancelling: 0 });

function count(value: AgentWorkflowRunCounts, item: AgentWorkflowRunStatusItem) {
  value.active++;
  if (item.cancel_requested) value.cancelling++;
  else if (item.status === "waiting_confirmation") value.waiting++;
  else if (item.status === "running") value.running++;
  else value.queued++;
}
function priority(item: AgentWorkflowRunStatusItem) {
  return item.cancel_requested ? 2 : item.status === "waiting_confirmation" ? 0 : item.status === "running" ? 1 : 3;
}
function validItem(item: AgentWorkflowRunStatusItem): boolean {
  return !!item && validAgentWorkflowId(item.id) && validAgentWorkflowId(item.workflow_id)
    && typeof item.name === "string" && ["queued", "running", "waiting_confirmation"].includes(item.status)
    && typeof item.cancel_requested === "boolean" && Number.isFinite(item.created_at) && Number.isFinite(item.updated_at);
}

/** One Agent-scoped status source lives with the dock, including while its
 * workflow sheet has never been opened. Children share it through injection. */
export function useAgentWorkflowRunStatus(context: () => { agentId?: string; revision?: number }) {
  const snapshot = ref<AgentWorkflowRunStatusItem[] | null>(null);
  const connection = ref<"idle" | "loading" | "live" | "stale" | "denied">("idle");
  const authEpoch = ref(0), authenticated = ref(true);
  const liveUpdate = inject(agentWorkflowUpdatesKey, ref(null));
  let disposed = false, epoch = 0, controller: AbortController | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const stale = computed(() => connection.value === "stale" || connection.value === "denied");
  const counts = computed(() => {
    const result = emptyCounts();
    for (const item of snapshot.value || []) count(result, item);
    return result;
  });
  const byWorkflow = computed(() => {
    const result: Record<string, AgentWorkflowFlowStatus> = {};
    for (const item of snapshot.value || []) {
      const current = result[item.workflow_id];
      if (!current) result[item.workflow_id] = { ...emptyCounts(), preferred: item };
      const value = result[item.workflow_id]!;
      count(value, item);
      if (priority(item) < priority(value.preferred)) value.preferred = item;
    }
    return result;
  });
  function stop() { ++epoch; clearTimeout(timer); controller?.abort(); controller = undefined; }
  function canPoll() {
    return !disposed && authenticated.value && validAgentWorkflowId(context().agentId)
      && (typeof document === "undefined" || document.visibilityState !== "hidden");
  }
  async function load(mine: number) {
    if (!canPoll() || mine !== epoch) return;
    const agentId = context().agentId!;
    const abort = new AbortController(); controller = abort;
    const timeout = setTimeout(() => abort.abort(), 10_000);
    const current = () => !disposed && mine === epoch && agentId === context().agentId && authenticated.value;
    let denied = false;
    if (snapshot.value === null) connection.value = "loading";
    try {
      const result = await agentWorkflowsApi.call(agentId, "read", { action: "run_status" }, abort.signal);
      if (!current()) return;
      if (result.ok === false || result.agent_id !== agentId || !Array.isArray(result.run_status) || !result.run_status.every(validItem)) throw new Error("Invalid status scope");
      snapshot.value = result.run_status; connection.value = "live";
    } catch (issue) {
      if (!current()) return;
      const error = issue as { status?: number; code?: number };
      denied = [401, 403, 404].includes(Number(error?.status ?? error?.code));
      if (denied) { snapshot.value = null; connection.value = "denied"; }
      else connection.value = "stale";
    } finally {
      clearTimeout(timeout);
      if (current()) {
        controller = undefined;
        if (!denied && canPoll()) timer = setTimeout(() => void load(mine), counts.value.active ? 3000 : 10_000);
      }
    }
  }
  function refresh() {
    stop();
    if (canPoll()) void load(epoch);
  }
  function scopeChanged() {
    stop(); snapshot.value = null; connection.value = "idle";
    if (canPoll()) void load(epoch);
  }
  function authChanged(event: Event) {
    authenticated.value = (event as CustomEvent<{ authenticated?: boolean }>).detail?.authenticated !== false;
    authEpoch.value++;
  }
  function visibilityChanged() {
    if (typeof document !== "undefined" && document.visibilityState === "hidden") { stop(); return; }
    refresh();
  }
  watch(() => [context().agentId, context().revision, authEpoch.value], scopeChanged, { immediate: true, flush: "sync" });
  watch(liveUpdate, update => { if (update?.agentId === context().agentId) refresh(); });
  if (typeof window !== "undefined") {
    window.addEventListener("pogo:auth-changed", authChanged);
    window.addEventListener("focus", visibilityChanged);
    document.addEventListener("visibilitychange", visibilityChanged);
  }
  onScopeDispose(() => {
    disposed = true; stop(); snapshot.value = null;
    if (typeof window !== "undefined") {
      window.removeEventListener("pogo:auth-changed", authChanged);
      window.removeEventListener("focus", visibilityChanged);
      document.removeEventListener("visibilitychange", visibilityChanged);
    }
  });
  return { snapshot, counts, byWorkflow, stale, connection, refresh };
}

export type AgentWorkflowRunStatus = ReturnType<typeof useAgentWorkflowRunStatus>;
export const agentWorkflowRunStatusKey: InjectionKey<AgentWorkflowRunStatus> = Symbol("agentWorkflowRunStatus");
