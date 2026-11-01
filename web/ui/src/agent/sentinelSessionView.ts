import { shallowRef } from "vue";
import type { SentinelSessionDetail } from "@/api/sentinelSessions";
import { normalizeMessages } from "@/api/messageContent";

export interface SentinelSheetBinding {
  sentinelId: string;
  parentSession?: string;
  agentId?: string;
  visible: boolean;
}

const bindingKey = (value: SentinelSheetBinding) => JSON.stringify([value.sentinelId, value.parentSession || "", value.agentId || ""]);

/** Check even successful responses before displaying or approving anything.
 * The API is authoritative; the guard also prevents late requests from
 * crossing the retained tab's selected parent/Agent identity. */
export function validSentinelSessionDetail(value: SentinelSessionDetail, binding: SentinelSheetBinding): boolean {
  const watch = value?.watch;
  if (!watch || watch.id !== binding.sentinelId || watch.task_id !== `sentinel:${binding.sentinelId}` ||
      binding.agentId && watch.agent_id !== binding.agentId ||
      binding.parentSession && watch.parent_session_id !== binding.parentSession) return false;
  if (value.session === null) return !watch.session_id;
  const session = value.session;
  return !!session && !!watch.session_id && session.id === watch.session_id && typeof session.cwd === "string" && session.cwd.startsWith("/") &&
    !!session.snapshot && Array.isArray(session.snapshot.messages) && !!session.snapshot.results &&
    !!session.snapshot.live && Array.isArray(session.snapshot.live.pending_gates) && !!session.snapshot.run;
}

export function sentinelSessionPollDelay(value: SentinelSessionDetail | null): number {
  return value?.watch.enabled || value?.watch.busy || value?.session?.snapshot.run.active ? 2000 : 10_000;
}

export function sentinelSessionState(value: SentinelSessionDetail): string {
  if (value.session?.snapshot.live.pending_gates.length) return "waiting_human";
  if (value.session?.snapshot.run.active || value.watch.busy) return "running";
  const state = value.watch.state;
  return ["listening", "paused", "expired", "completed", "error", "blocked", "waiting_capacity", "unavailable", "removed"].includes(state)
    ? state : value.watch.enabled ? "listening" : "paused";
}

/** One fetch at a time, bounded by visibility. Hidden tabs retain their last
 * view but stop their timer and abort in-flight reads. No read creates or
 * resumes a background execution. */
export function createSentinelSessionFeed(options: {
  binding(): SentinelSheetBinding;
  fetch(id: string, signal: AbortSignal): Promise<SentinelSessionDetail>;
  errorMessage(): string;
}) {
  const detail = shallowRef<SentinelSessionDetail | null>(null);
  const error = shallowRef("");
  const loading = shallowRef(false);
  let epoch = 0;
  let key = "";
  let controller: AbortController | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let disposed = false;

  function stop() {
    ++epoch;
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
    controller?.abort();
    controller = null;
    loading.value = false;
  }

  function schedule() {
    if (disposed || !options.binding().visible) return;
    timer = setTimeout(() => { timer = undefined; void refresh(); }, sentinelSessionPollDelay(detail.value));
  }

  async function refresh() {
    stop();
    const binding = { ...options.binding() };
    const nextKey = bindingKey(binding);
    if (key !== nextKey) { detail.value = null; error.value = ""; key = nextKey; }
    if (disposed || !binding.visible || !binding.sentinelId) return;
    const mine = epoch;
    const active = new AbortController();
    controller = active;
    loading.value = true;
    const current = () => !disposed && mine === epoch && !active.signal.aborted && options.binding().visible && bindingKey(options.binding()) === nextKey;
    try {
      const value = await options.fetch(binding.sentinelId, active.signal);
      if (!current()) return;
      if (!validSentinelSessionDetail(value, binding)) {
        // An identity mismatch invalidates old content too. A network failure
        // may retain it, but another worker's view must never be displayed.
        detail.value = null;
        throw new Error("Invalid Sentinel session binding");
      }
      detail.value = value.session ? {
        ...value,
        session: { ...value.session, snapshot: { ...value.session.snapshot, messages: normalizeMessages(value.session.snapshot.messages) } },
      } : value;
      error.value = "";
    } catch {
      if (current()) error.value = options.errorMessage();
    } finally {
      if (mine === epoch) {
        controller = null;
        loading.value = false;
        schedule();
      }
    }
  }

  function dispose() { disposed = true; stop(); }
  return { detail, error, loading, refresh, sync: refresh, stop, dispose };
}
