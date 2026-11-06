import { describe, expect, it } from "vitest";
import { buildTokens, type ThemeSeed } from "./build";
import { luminance, mix } from "./color";
import { THEME_SEEDS } from "./themes";

const keys = ["text", "muted", "live", "error", "warning", "link"] as const;
const ratio = (foreground: string, background: string) => {
  const a = luminance(foreground), b = luminance(background);
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
};
const surfaces = ["--el-bg-color", "--el-fill-color-light", "--el-fill-color-lighter"] as const;

describe("execution-layer foregrounds", () => {
  it.each(THEME_SEEDS)("keeps all small labels and state glyphs legible on $id", seed => {
    const tokens = buildTokens(seed);
    for (const key of keys) {
      for (const background of surfaces) {
        expect(ratio(tokens[`--pg-activity-${key}`], tokens[background]), `${seed.id}/${key}/${background}`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });

  it("leaves the existing palette and surfaces used by Todo and other UI unchanged", () => {
    for (const seed of THEME_SEEDS) {
      const tokens = buildTokens(seed);
      expect(tokens["--el-text-color-primary"]).toBe(seed.ink);
      expect(tokens["--el-text-color-regular"]).toBe(mix(seed.ink, seed.canvas, 0.15));
      expect(tokens["--el-text-color-secondary"]).toBe(seed.muted);
      expect(tokens["--el-color-primary"]).toBe(seed.accent);
      expect(tokens["--el-color-danger"]).toBe(seed.danger);
      expect(tokens["--el-color-warning"]).toBe(seed.warning);
      expect(tokens["--pg-link-color"]).toBe(mix(seed.mode === "dark" ? "#91c4f2" : "#205e96", seed.ink, 0.1));
      expect(tokens["--el-bg-color"]).toBe(seed.canvas);
      expect(tokens["--el-fill-color-light"]).toBe(mix(seed.canvas, seed.ink, 0.06));
      expect(tokens["--el-fill-color-lighter"]).toBe(mix(seed.canvas, seed.ink, 0.04));
    }
  });

  it("does not recolour a foreground that already meets the requirement", () => {
    for (const seed of THEME_SEEDS) {
      const tokens = buildTokens(seed);
      const original = { text: tokens["--el-text-color-regular"], muted: seed.muted, live: seed.accent, error: seed.danger, warning: seed.warning, link: tokens["--pg-link-color"] };
      for (const key of keys) {
        if (surfaces.every(background => ratio(original[key], tokens[background]) >= 4.5)) {
          expect(tokens[`--pg-activity-${key}`], `${seed.id}/${key}`).toBe(original[key]);
        }
      }
    }
  });

  it.each(["#ffffff", "#141414"])("has a bounded reachable fallback when the seed ink itself has no contrast on %s", canvas => {
    const seed: ThemeSeed = { ...THEME_SEEDS[0], canvas, ink: canvas, muted: canvas, accent: canvas, danger: canvas, warning: canvas };
    const tokens = buildTokens(seed);
    for (const key of keys) {
      for (const background of surfaces) expect(ratio(tokens[`--pg-activity-${key}`], tokens[background])).toBeGreaterThanOrEqual(4.5);
    }
  });
});
