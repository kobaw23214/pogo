import { nextTick } from "vue";
import { worklogRevealId, worklogRevealRow } from "@/agent/worklogPrefs";

const callSelector = (label: string) => `.call[data-cite="${CSS.escape(label)}"]`;
const rowSelector = (label: string) => `[data-rid][data-cite-labels~="${CSS.escape(label)}"]`;
const inside = (scope: ParentNode, element: Node) => scope instanceof Node && scope.contains(element);

/** A folded row keeps its source labels even before its ToolCall is mounted. */
function findTarget(label: string, scope: ParentNode): HTMLElement | null {
  if (!label || /\s/.test(label)) return null;
  return scope.querySelector<HTMLElement>(callSelector(label)) ?? scope.querySelector<HTMLElement>(rowSelector(label));
}

/** Availability is scoped to the calling conversation, subagent or share view. */
export function canJumpToCite(label: string, scope: ParentNode = document): boolean {
  return findTarget(label, scope) !== null;
}

/**
 * Reveal an existing execution record, including lazily mounted WorkLog rows.
 * The synchronous result accepts/rejects the local target; Vue mounts its body
 * before the eventual scroll. This never invokes or repeats a tool call.
 */
export function jumpToCite(label: string, scope: ParentNode = document): boolean {
  const original = findTarget(label, scope);
  if (!original) return false;
  const candidateRow = original.closest<HTMLElement>("[data-rid][data-cite-labels]");
  const row = candidateRow && inside(scope, candidateRow) ? candidateRow : null;
  const candidateLog = original.closest<HTMLElement>(".worklog[data-xid]");
  const log = candidateLog && inside(scope, candidateLog) ? candidateLog : null;
  const unfolding = log !== null && !log.classList.contains("open");
  const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  // New WorkLogs expose a mounted-instance id so duplicate exchange/row ids
  // in another pane do not react to this citation's reveal signals.
  const revealId = log?.dataset.revealId || log?.dataset.xid || "";
  const rowId = row?.dataset.rid ?? "";

  void (async () => {
    await Promise.all([
      reveal(worklogRevealId, revealId),
      reveal(worklogRevealRow, rowId ? (log?.dataset.revealId ? `${revealId}:${rowId}` : rowId) : ""),
    ]);
    await nextTick();
    // Legacy logs animate their fold; conversation-first logs keep prose in
    // place and need only the render flush above.
    if (unfolding && log?.querySelector(".fold") && !reduced) await new Promise<void>((done) => window.setTimeout(done, 420));
    if (!original.isConnected || !inside(scope, original)) return;
    const destination = row
      ? row.querySelector<HTMLElement>(callSelector(label)) ?? (row.matches(callSelector(label)) ? row : row.matches(rowSelector(label)) ? row : null)
      : original.matches(callSelector(label)) ? original : null;
    if (!destination || !inside(scope, destination)) return;
    // Reveal the exact technical record and any containing activity group.
    // This is a local UI event; it never runs a tool or changes browser control.
    destination.dispatchEvent(new CustomEvent("pogo-reveal-tool", { bubbles: true }));
    await nextTick();
    if (!destination.isConnected || !inside(scope, destination)) return;
    destination.scrollIntoView({ behavior: reduced ? "auto" : "smooth", block: "center" });
    destination.classList.add("cite-focused");
    window.setTimeout(() => destination.classList.remove("cite-focused"), 1500);
  })();
  return true;
}

// Re-arm the same signal after a manual re-fold. Awaiting the blank value's
// flush makes the subsequent id observable before the caller measures the row.
async function reveal(signal: { value: string }, id: string): Promise<void> {
  if (!id) return;
  if (signal.value === id) {
    signal.value = "";
    await nextTick();
  }
  signal.value = id;
}
