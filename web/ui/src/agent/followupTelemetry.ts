/**
 * Follow-up chip counters, in the same local telemetry bucket as the work-log
 * ones (see worklogPrefs.ts): coarse and local on purpose, just enough to
 * answer two questions later — how often is a chip row shown, and of the
 * picks, how many went to the server's generated chips vs the rule-matched
 * ones, at which position. Read it from the dev console:
 *
 *   JSON.parse(localStorage.getItem("pogo:telemetry"))
 *
 * Keys carry only enums and ordinals; chip text is never recorded.
 */
const TELEMETRY_KEY = "pogo:telemetry";

/** Where the shown row came from: the server's `followups` event, or the skills' rules. */
export type FollowupSource = "generated" | "rules";

export type FollowupEvent =
  /** A non-empty chip row appeared. The view counts it once per turn per session. */
  | "followup.shown"
  /** A chip was picked. Buckets below add the source, then the position in the row. */
  | "followup.pick";

export function trackFollowup(event: FollowupEvent, detail?: { source: FollowupSource; index?: number }): void {
  try {
    const all = JSON.parse(localStorage.getItem(TELEMETRY_KEY) ?? "{}") as Record<string, number>;
    all[event] = (all[event] ?? 0) + 1;
    if (detail) {
      const bySource = `${event}.${detail.source}`;
      all[bySource] = (all[bySource] ?? 0) + 1;
      if (detail.index !== undefined) {
        const byIndex = `${bySource}.${detail.index}`;
        all[byIndex] = (all[byIndex] ?? 0) + 1;
      }
    }
    localStorage.setItem(TELEMETRY_KEY, JSON.stringify(all));
  } catch {
    // Counters are a convenience, never a reason to break the view.
  }
}
