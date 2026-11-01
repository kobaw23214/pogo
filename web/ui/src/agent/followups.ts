import type { StarterCard } from "@/api/types";
import type { TimelineItem } from "./timeline";

// Follow-up chips are the next step offered once a turn has finished.
//
// Two decisions shape this module, and both are about not being annoying:
//
//  1. Generated first, rules as the fallback. The server asks a model for
//     chips after each run — asynchronously, after the answer has fully
//     rendered, so a suggestion never delays what it builds on — and pushes
//     them as a `followups` event. What lives here is the deterministic
//     fallback for when generation is off, fails, or stays silent: a skill
//     declares its chips and the condition for them, and matching is a
//     substring search over text already on screen.
//  2. Relevant or absent. Matching is against the *last* turn only, and no match
//     shows nothing. A fixed row after every turn would suggest decompiling
//     something when the user just asked about a family profile, and a
//     suggestion that is wrong half the time trains people to ignore the row.
export interface FollowupGroup {
  when: string[];
  chips: StarterCard[];
}

// HAYSTACK_LIMIT keeps matching cheap and bounded. Tool arguments can be a whole
// file's contents (a write call), and a `when` needle worth matching — a command
// name, a tool name — appears early or not at all.
const HAYSTACK_LIMIT = 4000;

/**
 * followupHaystack renders what the last turn did into one lowercased string:
 * the assistant's reply plus each tool call's name and arguments.
 *
 * Only the last turn, deliberately. Over the whole conversation, one appearance
 * of a command would keep matching for the rest of the session, so the chips
 * would stop describing what just happened.
 */
export function followupHaystack(items: TimelineItem[]): string {
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i];
    if (item.kind !== "turn") continue;
    const parts: string[] = [item.text];
    for (const call of item.calls) {
      parts.push(call.name);
      try {
        parts.push(JSON.stringify(call.args) ?? "");
      } catch {
        // Args that will not serialise (cycles) are simply not matchable.
      }
    }
    return parts.join("\n").slice(0, HAYSTACK_LIMIT).toLowerCase();
  }
  return "";
}

// The chip row's ceiling, mirroring the server's skills.MaxFollowupChips: the
// row wraps at two lines of roughly four chips.
const FOLLOWUP_CHIP_CAP = 8;

// The dedup key: two chips asking the same question under different titles are
// the same suggestion, and so are two titled differently but worded identically.
function chipKey(c: StarterCard): string {
  return (c.prompt ?? c.title).toLowerCase().replace(/[\s\p{P}\p{S}]/gu, "");
}

/**
 * matchFollowups returns the chips of every group whose condition holds, merged.
 *
 * A turn that ran two commands is about both, so one match would hide the other
 * half's next steps; merging with dedup lets the row fill out toward the cap.
 * The file's order is still the author's statement of priority — earlier groups
 * come first in the row.
 */
export function matchFollowups(groups: FollowupGroup[], haystack: string): StarterCard[] {
  if (!haystack) return [];
  const chips: StarterCard[] = [];
  const seen = new Set<string>();
  for (const g of groups) {
    if (!g.when.some((w) => w && haystack.includes(w.toLowerCase()))) continue;
    for (const c of g.chips) {
      if (chips.length === FOLLOWUP_CHIP_CAP) return chips;
      const key = chipKey(c);
      if (!key || seen.has(key)) continue;
      seen.add(key);
      chips.push(c);
    }
  }
  return chips;
}
