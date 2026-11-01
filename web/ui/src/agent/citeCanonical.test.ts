import { describe, expect, it } from "vitest";
import { canonicalCiteLabel, numberCitations, planCitations, rewriteCitations } from "./cite";

describe("registered citation identity", () => {
  it("resolves mixed-case references to the same recorded source and copy number", () => {
    const text = "结论。 [1](#cite:s7) [1](#cite:S7) [1](#cite:s2)";
    const known = new Set(["S2", "S7"]);
    const plan = planCitations(text, known);
    expect(plan.map(item => item.label)).toEqual(["S7", "S7", "S2"]);
    expect(plan.map(item => item.size)).toEqual([2, 2, 2]);
    const numbering = numberCitations(text, known);
    expect(numbering.unknown).toEqual([]);
    expect([...numbering.nums]).toEqual([["S7", 1], ["S2", 2]]);
    expect(rewriteCitations(text, numbering.nums)).toBe("结论。 [1] [1] [2]");
  });

  it("does not create evidence from code examples, ordinary links or unknown IDs", () => {
    const text = "`[1](#cite:s1)` [site](https://example.test/#cite:s1)\n~~~md\n[1](#cite:s2)\n~~~\n未知。 [1](#cite:s9)";
    expect(numberCitations(text, new Set(["S1", "S2"]))).toEqual({ nums: new Map(), unknown: ["S9"] });
    for (const id of ["s10000", "file1", "Ss1", " S1", "../s1"]) expect(canonicalCiteLabel(id)).toBe(id);
  });

  it("does not count a malformed wrapper as a displayed citation", () => {
    expect(numberCitations("说明。 [1](#cite:S1]", new Set(["S1"]))).toEqual({ nums: new Map(), unknown: [] });
  });
});
