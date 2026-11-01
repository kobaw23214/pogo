import { describe, expect, it } from "vitest";
import { anatomyFocusContent, type CapabilityKey } from "../../components/studio/studioLandingContent";

describe("anatomy focus content", () => {
  it("covers every capability fold exactly once", () => {
    const keys: CapabilityKey[] = ["agent", "model", "skill", "mcp"];
    expect(Object.keys(anatomyFocusContent).sort()).toEqual([...keys].sort());
  });

  it("provides a caption, a two-line card and a layer tag for each focus", () => {
    for (const entry of Object.values(anatomyFocusContent)) {
      expect(entry.caption).toMatch(/^ANATOMY — 0\d \//);
      expect(entry.lines).toHaveLength(2);
      expect(entry.lines[0].length).toBeGreaterThan(0);
      expect(entry.tag).toContain("·");
    }
  });
});
