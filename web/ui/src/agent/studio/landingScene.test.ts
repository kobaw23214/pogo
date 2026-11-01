import { describe, expect, it } from "vitest";
import { cinemaPlanes, sceneLayout, sceneProgress, sceneTravel } from "../../components/studio/landingSceneConfig";

describe("scroll position is the animation clock", () => {
  it("clamps outside the scene at the exact endpoints", () => {
    expect(sceneProgress(200, 500, 1200)).toBe(0);
    expect(sceneProgress(500, 500, 1200)).toBe(0);
    expect(sceneProgress(1700, 500, 1200)).toBe(1);
    expect(sceneProgress(4000, 500, 1200)).toBe(1);
  });
  it("revisits the same pose when scrolling backwards", () => {
    const positions = [0, 250, 500, 750, 1000, 750, 500, 250, 0];
    expect(positions.map(scroll => sceneProgress(scroll, 0, 1000))).toEqual([0, .25, .5, .75, 1, .75, .5, .25, 0]);
  });
  it("does not publish an invalid pose for an unmeasured layout", () => {
    for (const distance of [0, -1, Number.NaN, Infinity]) expect(sceneProgress(100, 0, distance)).toBe(0);
    expect(sceneProgress(Number.NaN, 0, 100)).toBe(0);
  });
  it("normalizes each scene independently of its document offset", () => {
    expect(sceneProgress(2750, 2000, 1500)).toBe(.5);
    expect(sceneProgress(750, 0, 1500)).toBe(.5);
  });
});

describe("layout and cinematic depth", () => {
  it("uses static scene layouts on short screens", () => {
    expect(sceneLayout(1280, 500)).toBe("compact");
    expect(sceneLayout(700, 400)).toBe("compact");
    expect(sceneLayout(390, 844)).toBe("mobile");
    expect(sceneLayout(1280, 720)).toBe("desktop");
  });
  it("reduces travel deliberately without disabling the narrative", () => {
    expect(sceneTravel(true, "desktop")).toBe(1);
    expect(sceneTravel(true, "mobile")).toBe(.5);
    expect(sceneTravel(false, "desktop")).toBe(0);
    expect(sceneTravel(true, "compact")).toBe(0);
  });
  it("moves foreground sheets far enough to clear the lens", () => {
    expect(new Set(cinemaPlanes.map(plane => plane.id)).size).toBe(11);
    const ticket = cinemaPlanes.find(plane => plane.id === "ticket")!;
    expect(Math.abs(ticket.dx) * 1280).toBeGreaterThan(600);
    expect(Math.abs(ticket.dy) * 720).toBeGreaterThan(300);
  });
});
