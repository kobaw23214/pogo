import { describe, expect, it } from "vitest";
import { fitScale, shouldReencode, SMALL_ENOUGH } from "./attachImage";

describe("fitScale", () => {
  it("shrinks the long edge to the ceiling, never upscales", () => {
    expect(fitScale(3136, 1568)).toBeCloseTo(0.5);
    expect(fitScale(800, 600)).toBe(1);
    expect(fitScale(600, 800)).toBe(1);
  });

  it("keeps the aspect ratio on portrait images", () => {
    const s = fitScale(1000, 3000);
    expect(Math.round(1000 * s)).toBeLessThanOrEqual(1568);
    expect(Math.round(3000 * s)).toBe(1568);
  });

  it("ignores nonsense dimensions rather than scaling by zero", () => {
    expect(fitScale(0, 0)).toBe(1);
    expect(fitScale(-10, 5)).toBe(1);
  });
});

describe("shouldReencode", () => {
  it("passes small files through untouched", () => {
    expect(shouldReencode(3000, 2000, SMALL_ENOUGH)).toBe(false);
  });

  it("re-encodes oversized files and oversized pixels alike", () => {
    expect(shouldReencode(3136, 1568, 3_000_000)).toBe(true);
    expect(shouldReencode(800, 600, 2_500_000)).toBe(true);
  });

  it("keeps a right-sized file of moderate weight", () => {
    expect(shouldReencode(1200, 800, 900_000)).toBe(false);
  });
});
