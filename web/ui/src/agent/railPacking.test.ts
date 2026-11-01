import { describe, expect, it } from "vitest";
import { packRail, type RailLikeDot } from "./railPacking";

const dot = (id: string, marker: RailLikeDot["marker"], topPct: number): RailLikeDot => ({
  id,
  marker,
  topPct,
});

describe("packRail — overlapping subagent runs collapse", () => {
  const GAP = 3;

  it("merges consecutive notices that land within one dot of each other", () => {
    const out = packRail([dot("a", "notice", 40), dot("b", "notice", 41.5), dot("c", "notice", 42.9)], GAP);
    expect(out).toHaveLength(1);
    expect(out[0].dot.id).toBe("a");
    expect(out[0].cluster?.map((d) => d.id)).toEqual(["a", "b", "c"]);
  });

  it("keeps far-apart notices separate", () => {
    const out = packRail([dot("a", "notice", 10), dot("b", "notice", 30)], GAP);
    expect(out).toHaveLength(2);
    expect(out[0].cluster).toBeUndefined();
    expect(out[1].cluster).toBeUndefined();
  });

  it("never merges questions or skills, even overlapping ones", () => {
    const out = packRail(
      [dot("q1", "question", 40), dot("q2", "question", 40.5), dot("s", "skill", 41)],
      GAP,
    );
    expect(out.map((e) => e.dot.id)).toEqual(["q1", "q2", "s"]);
  });

  it("a question between two notices breaks the run", () => {
    const out = packRail(
      [dot("a", "notice", 40), dot("q", "question", 40.5), dot("b", "notice", 41)],
      GAP,
    );
    expect(out.map((e) => e.dot.id)).toEqual(["a", "q", "b"]);
    expect(out[0].cluster).toBeUndefined();
  });

  it("the run's anchor is its first dot, so a click lands at its top", () => {
    const out = packRail([dot("a", "notice", 50), dot("b", "notice", 51)], GAP);
    expect(out[0].dot.topPct).toBe(50);
  });
});
