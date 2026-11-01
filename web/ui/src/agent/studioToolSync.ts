import { ref, shallowRef } from "vue";
import type { StudioSessionCapabilities, StudioToolConfigSync } from "./studioSessionCapabilities";

export interface StudioToolState {
  sessionId: string;
  revision: number;
  desiredRevision: number;
  pending: boolean;
  tools: string[];
  error: string;
}

export function validStudioToolConfig(value: unknown): value is StudioToolConfigSync {
  if (!value || typeof value !== "object") return false;
  const v = value as StudioToolConfigSync;
  return typeof v.changed === "boolean" && typeof v.deferred === "boolean"
    && Number.isSafeInteger(v.revision) && v.revision >= 0
    && Number.isSafeInteger(v.desired_revision) && v.desired_revision >= v.revision
    && Array.isArray(v.tools) && v.tools.every(name => typeof name === "string" && !!name);
}

/** Only current-session, monotonic host receipts may change effective tools.
 * A notice never grants a desired tool early, and receiving an SSE never sends
 * another sync request. The caller chooses focus/idle/reconnect boundaries.
 */
export function createStudioToolSync(request: (sessionId: string) => Promise<StudioToolConfigSync>) {
  const state = shallowRef<StudioToolState | null>(null);
  const syncing = ref(false);
  const catalogVersion = ref(0);
  let sessionId: string | null = null, epoch = 0, observation = 0;
  let inFlight: Promise<void> | null = null;

  function select(sid: string | null) {
    if (sessionId === sid) return;
    sessionId = sid; epoch++; observation++; inFlight = null;
    state.value = null; syncing.value = false;
  }

  function accept(sid: string, value: StudioToolConfigSync): boolean {
    if (sid !== sessionId || !validStudioToolConfig(value)) return false;
    const old = state.value;
    if (old && (value.revision < old.revision
      || value.revision === old.revision && value.desired_revision < old.desiredRevision)) return false;
    const tools = [...new Set(value.tools)].sort();
    const next: StudioToolState = { sessionId: sid, revision: value.revision, desiredRevision: value.desired_revision,
      pending: value.deferred, tools, error: "" };
    if (!old || old.revision !== next.revision || JSON.stringify(old.tools) !== JSON.stringify(tools)) catalogVersion.value++;
    if (JSON.stringify(old) !== JSON.stringify(next)) { observation++; state.value = next; }
    return true;
  }

  function observeCapabilities(sid: string, caps: StudioSessionCapabilities) {
    if (!caps.tools_sync || sid !== sessionId) return;
    accept(sid, { changed: false, deferred: caps.tools_pending === true, tools: caps.tools ?? [],
      revision: caps.tools_revision ?? 0, desired_revision: caps.tools_desired_revision ?? caps.tools_revision ?? 0 });
  }

  function sync(): Promise<void> {
    if (!sessionId || !state.value) return Promise.resolve(); // Older server: do not guess an endpoint.
    if (inFlight) return inFlight;
    const sid = sessionId, generation = epoch, observed = observation;
    syncing.value = true;
    const work = (async () => {
      try {
        const response = await Promise.resolve().then(() => request(sid));
        if (generation !== epoch || sid !== sessionId) return;
        if (!validStudioToolConfig(response)) throw new Error("Invalid tool configuration receipt");
        accept(sid, response);
      } catch (error) {
        if (generation === epoch && sid === sessionId && observed === observation && state.value) {
          state.value = { ...state.value, error: error instanceof Error ? error.message : String(error) };
        }
      } finally {
        if (generation === epoch) { syncing.value = false; inFlight = null; }
      }
    })();
    inFlight = work;
    return work;
  }
  return { state, syncing, catalogVersion, select, accept, observeCapabilities, sync };
}
