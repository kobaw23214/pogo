import { describe, expect, it } from "vitest";
import { adjustFloatingChat, fitFloatingChat } from "./immersiveLayout";

describe("floating conversation bounds", () => {
  it("keeps an oversized or offscreen saved panel reachable after a resize", () => {
    const panel = fitFloatingChat({ x: 1700, y: -100, width: 1400, height: 1200 }, { width: 900, height: 600 });
    expect(panel.x).toBeGreaterThanOrEqual(12);
    expect(panel.y).toBeGreaterThanOrEqual(88);
    expect(panel.x + panel.width).toBeLessThanOrEqual(888);
    expect(panel.y + panel.height).toBeLessThanOrEqual(588);
  });
  it("keeps narrow screens and soft-keyboard viewports usable", () => {
    for (const viewport of [{ width: 390, height: 844 }, { width: 390, height: 300 }, { width: 250, height: 150 }]) {
      const panel = fitFloatingChat(null, viewport);
      expect(panel.x).toBeGreaterThan(0);
      expect(panel.y).toBeGreaterThan(0);
      expect(panel.x + panel.width).toBeLessThan(viewport.width);
      expect(panel.y + panel.height).toBeLessThan(viewport.height);
    }
  });
  it("constrains drag and resize without losing the panel controls", () => {
    const viewport = { width: 1400, height: 900 };
    const initial = fitFloatingChat(null, viewport);
    const moved = adjustFloatingChat(initial, -5000, 5000, false, viewport);
    expect(moved.x).toBe(12);
    expect(moved.y + moved.height).toBe(888);
    const resized = adjustFloatingChat(initial, -5000, -5000, true, viewport);
    expect(resized.width).toBe(360);
    expect(resized.height).toBe(300);
  });
});
