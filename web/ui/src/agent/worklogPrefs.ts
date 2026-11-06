import { ref } from "vue";

/**
 * The work-log escape hatch, and the counters that say whether it is needed.
 *
 * worklogExpandAll answers the complaint that sank Claude Code's forced fold
 * and chased Cursor's users into their forum: a fold you cannot switch off is
 * a feature some people will route around. When it is on, every work log
 * opens by default; flipping it applies to every mounted log at once, and a
 * card folded by hand afterwards stays folded until the next flip.
 *
 * The counters are local and coarse on purpose — this is a single-user local
 * tool, so "telemetry" means enough to answer one question later: of the work
 * logs shown, how many did the reader actually open? Read it from the dev
 * console:
 *
 *   JSON.parse(localStorage.getItem("pogo:telemetry"))
 */
const PREF_KEY = "pogo:worklog-open";
const TELEMETRY_KEY = "pogo:telemetry";

export const worklogExpandAll = ref<boolean>(readPref());

export type ExecutionDetailMode = "summary" | "full";
const DETAIL_KEY = "pogo:execution-detail";
function readDetailMode(): ExecutionDetailMode {
  try {
    const saved = localStorage.getItem(DETAIL_KEY);
    if (saved === "full" || saved === "summary") return saved;
  } catch { /* Private mode keeps the preference in memory. */ }
  return worklogExpandAll.value ? "full" : "summary";
}
export const executionDetailMode = ref<ExecutionDetailMode>(readDetailMode());
export function setExecutionDetailMode(mode: ExecutionDetailMode): void {
  executionDetailMode.value = mode;
  try { localStorage.setItem(DETAIL_KEY, mode); } catch { /* Session-only. */ }
}

// A one-shot "open this one log" signal, set by search-hit locating when the
// message it jumps to lives inside a fold. Unlike the global escape hatch it
// changes no preference: the named log opens, everything else keeps its fold.
export const worklogRevealId = ref("");

// The row-level half of the same signal: a long-prose row's body renders only
// when open, so a jump landing inside one opens that row too — otherwise the
// located text would stay hidden behind its first line.
export const worklogRevealRow = ref("");

function readPref(): boolean {
  try {
    return localStorage.getItem(PREF_KEY) === "1";
  } catch {
    // Private mode; the choice is then session-only.
    return false;
  }
}

export function setWorklogExpandAll(v: boolean): void {
  worklogExpandAll.value = v;
  try {
    localStorage.setItem(PREF_KEY, v ? "1" : "0");
  } catch {
    // Private mode — session-only, same as the skin.
  }
}

export type WorklogEvent =
  /** A folded log appeared on screen. The denominator of the expansion rate. */
  | "worklog.shown"
  /** The reader opened a folded log by hand. The numerator. */
  | "worklog.manualOpen"
  /** The reader drilled into a layer inside one. */
  | "worklog.rowOpen";

export function trackWorklog(event: WorklogEvent): void {
  try {
    const all = JSON.parse(localStorage.getItem(TELEMETRY_KEY) ?? "{}") as Record<string, number>;
    all[event] = (all[event] ?? 0) + 1;
    localStorage.setItem(TELEMETRY_KEY, JSON.stringify(all));
  } catch {
    // Counters are a convenience, never a reason to break the view.
  }
}
