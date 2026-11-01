import { gt } from "@/i18n";
import { summarizeArgs } from "./timeline";
import type { Live } from "@/api/types";

// Vue-free, like the other projections: one line saying what the run is doing
// right now, unit tested without a component.

/**
 * liveActivity is the live work header's second half — the "正在执行 go build"
 * next to the ticking clock.
 *
 * The order is a priority list, not a sequence: an approval outranks the tool
 * it is waiting on, because the run is blocked on the person, not on the call.
 * The tool reads its one-line summary (the same identifiers the folded rows
 * carry — a bare "running bash" is the no-scent summary the research warns
 * about). Plain states come last and are allowed to be vague, because by the
 * time nothing else is on screen, vague is the truth.
 */
export function liveActivity(live: Live): string {
  if (live.pending_gates.length > 0) return gt("workLog.live.awaitingApproval");

  const tool = live.pending_tools[live.pending_tools.length - 1];
  if (tool) {
    const detail = summarizeArgs(tool.name, tool.args);
    return detail
      ? gt("workLog.live.runningTool", { detail })
      : gt("workLog.live.runningToolBare", { name: tool.name });
  }

  const incoming = live.incoming?.[live.incoming.length - 1];
  if (incoming) {
    return incoming.name
      ? gt("workLog.live.preparing", { name: incoming.name })
      : gt("workLog.live.drafting");
  }

  // Thinking remains accumulated after prose begins; output is the newer
  // observable phase rather than a reason to keep displaying "thinking".
  if ((live.text ?? "") !== "") return gt("workLog.live.answering");
  if ((live.thinking ?? "") !== "") return gt("workLog.live.thinking");
  return gt("workLog.live.waiting");
}
