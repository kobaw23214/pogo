import { nextTick, ref } from "vue";

/**
 * The one live region the conversation gets.
 *
 * The transcript itself is deliberately not `role="log"`: a log announces
 * every addition, and a streaming answer adds text dozens of times a second —
 * a screen reader would read shreds of sentences forever. What a non-visual
 * reader actually needs is the milestones: the run finished, how long it
 * took. Those are announced here, through a single `role="status"` region
 * rendered in AgentView.
 */
export const announcement = ref("");

// Clearing first forces a DOM change even when the same milestone repeats —
// two consecutive "已完成，用时 2s" must both be announced, and an unchanged
// text node announces nothing.
export function announce(message: string): void {
  announcement.value = "";
  void nextTick(() => (announcement.value = message));
}
