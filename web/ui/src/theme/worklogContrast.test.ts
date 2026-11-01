import { describe, expect, it } from "vitest";
import { luminance, mix } from "./color";
import { buildTokens } from "./build";
import { THEME_SEEDS } from "./themes";

/**
 * The work log's rail glyphs carry category colour on every skin, so the
 * colours have to survive every skin. 3:1 against the canvas is the WCAG bar
 * for non-text UI — and these glyphs are exactly that (16px strokes, with
 * shape as the second encoding). A token that dipped under on one skin would
 * be invisible there, and nobody would think to look.
 *
 * The values measured here are the derived ones: WorkLog.vue mixes the raw
 * tokens 55/45 towards the ink (canvas and ink are a skin's extremes, so the
 * mix always moves away from the canvas, in both modes). The formula is
 * pinned here so the SCSS and this test cannot drift apart silently.
 */
function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

describe("work-log category colours", () => {
  it("hold 3:1 against the canvas on every skin", () => {
    const failures: string[] = [];
    for (const seed of THEME_SEEDS) {
      const tokens = buildTokens(seed);
      const canvas = tokens["--el-bg-color"];
      const ink = tokens["--el-text-color-primary"];
      // Keep in sync with the --wl-cat-* definitions in WorkLog.vue.
      const derived: Record<string, string> = {
        thinking: mix(tokens["--pg-syn-type"], ink, 0.45),
        explore: mix(tokens["--el-color-primary"], ink, 0.45),
        change: mix(tokens["--el-color-warning"], ink, 0.45),
        command: tokens["--el-text-color-regular"],
        failure: tokens["--el-color-danger"],
      };
      for (const [category, color] of Object.entries(derived)) {
        const ratio = contrast(color, canvas);
        if (ratio < 3) failures.push(`${seed.id}/${category}: ${ratio.toFixed(2)} (${color})`);
      }
    }
    expect(failures).toEqual([]);
  });
});
