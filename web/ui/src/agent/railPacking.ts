// The turn rail packs overlapping runs: consecutive notice markers (subagent
// completions) that land within one dot-height of each other stack into a
// smear, so they collapse into a single entry carrying the run as a cluster.
// Questions and skills never merge — each is a separate place to jump to, and
// two user turns can share a pixel without losing their identity.

export type RailMarkerKind = "question" | "skill" | "notice";

export interface RailLikeDot {
  id: string;
  topPct: number;
  marker: RailMarkerKind;
}

export interface RailEntry<D extends RailLikeDot> {
  /** The rendered anchor: first dot of the run. */
  dot: D;
  /** All dots in the collapsed run, anchor included; absent when alone. */
  cluster?: D[];
}

export function packRail<D extends RailLikeDot>(dots: D[], gapPct: number): RailEntry<D>[] {
  const out: RailEntry<D>[] = [];
  for (const d of dots) {
    const prev = out[out.length - 1];
    if (
      prev &&
      prev.dot.marker === "notice" &&
      d.marker === "notice" &&
      d.topPct - prev.dot.topPct < gapPct
    ) {
      prev.cluster = [...(prev.cluster ?? [prev.dot]), d];
    } else {
      out.push({ dot: d });
    }
  }
  return out;
}
