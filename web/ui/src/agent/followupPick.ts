import type { StarterCard } from "@/api/types";
import { followupHaystack, matchFollowups, type FollowupGroup } from "./followups";
import type { TimelineItem, TurnItem } from "./timeline";

/**
 * The chip row for the latest turn, and where it came from. "generated" means
 * the server made the chips for that turn (a `followups` event); "rules" means
 * the skills' declarative matching did (see followups.ts). messageId is the
 * turn's own id either way — the view keys its shown-once-per-turn counter on
 * it, and message ids restart per session, so the view prefixes the session.
 */
export interface FollowupPick {
  chips: StarterCard[];
  source: "generated" | "rules";
  messageId: string;
}

/**
 * pickFollowups chooses the chip row under the latest turn, or null for none.
 *
 * Generated chips win over the rules, but only for the turn they were made
 * for: chips can trail the run's end while the next question is already
 * running, so the message_id guard — not arrival order — decides which turn
 * they describe. A stale or empty generated set falls through to the rules,
 * whose own rule covers the row as a whole: relevant or absent.
 */
export function pickFollowups(
  generated: { messageId: string | null; chips: StarterCard[] },
  items: TimelineItem[],
  groups: FollowupGroup[],
): FollowupPick | null {
  let lastTurn: TurnItem | null = null;
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i];
    if (item.kind === "turn") {
      lastTurn = item;
      break;
    }
  }
  if (!lastTurn) return null;
  if (generated.chips.length && generated.messageId === lastTurn.id) {
    return { chips: generated.chips, source: "generated", messageId: lastTurn.id };
  }
  const chips = matchFollowups(groups, followupHaystack(items));
  return chips.length ? { chips, source: "rules", messageId: lastTurn.id } : null;
}
