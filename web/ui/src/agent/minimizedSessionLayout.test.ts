import { describe, expect, it } from "vitest";
import { fitMinimizedSession } from "./minimizedSessionLayout";

describe("compact conversation placement", () => {
  it("keeps the two-line bar compact and starts clear of screen edges", () => {
    expect(fitMinimizedSession(null, { width: 1280, height: 800 })).toEqual({ x: 936, y: 716, width: 332, height: 72 });
  });
  it("supports arbitrary corners and clamps after viewport rotation or keyboard", () => {
    for (const viewport of [{ width: 390, height: 844 }, { width: 844, height: 390 }, { width: 320, height: 220 }, { width: 250, height: 150 }]) {
      for (const saved of [{ x: -500, y: -500 }, { x: 5000, y: 5000 }]) {
        const bar = fitMinimizedSession(saved, viewport);
        expect(bar.x).toBeGreaterThan(0); expect(bar.y).toBeGreaterThan(0);
        expect(bar.x + bar.width).toBeLessThan(viewport.width); expect(bar.y + bar.height).toBeLessThan(viewport.height);
      }
    }
  });
  it("keeps a notched device's safe edges clear", () => {
    const bar = fitMinimizedSession({ x: 900, y: 0 }, { width: 844, height: 390, safeLeft: 44, safeRight: 44, safeTop: 20, safeBottom: 34 });
    expect(bar.x).toBeGreaterThanOrEqual(44);
    expect(bar.x + bar.width).toBeLessThanOrEqual(800);
    expect(bar.y).toBe(20);
  });
  it("does not trust malformed saved coordinates", () => {
    expect(fitMinimizedSession({ x: NaN, y: Infinity }, { width: 1280, height: 800 })).toEqual(fitMinimizedSession(null, { width: 1280, height: 800 }));
  });
});
