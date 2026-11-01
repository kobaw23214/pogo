import { describe, expect, it } from "vitest";
import { heroLayerTravel, heroMotionLayers, landingMotionLayout, landingMotionScale, landingMotionPreference } from "../../components/studio/landingMotionConfig";

describe("landing motion preference", () => {
  it("starts enabled on a first visit or with an unknown saved value", () => {
    expect(landingMotionPreference(null)).toBe(true);
    expect(landingMotionPreference("legacy")).toBe(true);
  });

  it("retains an explicit opt-out across visits", () => {
    expect(landingMotionPreference("off")).toBe(false);
    expect(landingMotionPreference("on")).toBe(true);
  });

  it("keeps parallax present but gentler with reduced motion", () => {
    for (const layout of ["full", "compact", "mobile"] as const) {
      expect(landingMotionScale(layout, true)).toBeGreaterThan(0);
      expect(landingMotionScale(layout, true)).toBeLessThan(landingMotionScale(layout));
    }
  });
});

describe("landing motion viewport constraints", () => {
  it("keeps narrow layouts unpinned regardless of height", () => {
    expect(landingMotionLayout(320, 900)).toBe("mobile");
    expect(landingMotionLayout(900, 1200)).toBe("mobile");
  });

  it("releases sticky scenes on short desktop screens", () => {
    expect(landingMotionLayout(1440, 699)).toBe("compact");
    expect(landingMotionLayout(1280, 720)).toBe("full");
  });

  it("reduces travel on phones and short screens", () => {
    expect(landingMotionScale("mobile")).toBeLessThan(landingMotionScale("compact"));
    expect(landingMotionScale("compact")).toBeLessThan(landingMotionScale("full"));
  });

  it("keeps near planes perceptibly faster than distant planes", () => {
    const far = heroMotionLayers.filter(layer => layer.depth === "far").map(layer => layer.y);
    const near = heroMotionLayers.filter(layer => layer.depth === "near").map(layer => layer.y);
    expect(Math.min(...near)).toBeGreaterThan(Math.max(...far) * 2);
    expect(new Set(heroMotionLayers.map(layer => layer.selector)).size).toBe(11);
  });

  it("limits upward movement when a plane is already near the header", () => {
    expect(heroLayerTravel(150, 10, 1)).toBeLessThanOrEqual(34);
    expect(heroLayerTravel(150, 200, 1)).toBe(150);
  });

  it("applies the mobile factor before checking the image boundary", () => {
    expect(heroLayerTravel(150, 200, 0.28)).toBeCloseTo(42);
    expect(heroLayerTravel(150, 0, 0.28)).toBe(24);
  });
});
