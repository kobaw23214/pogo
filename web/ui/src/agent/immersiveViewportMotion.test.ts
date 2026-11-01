import { describe, expect, it } from "vitest";
import { acceptsImmersivePaneLayout, type ImmersiveLayoutDetail } from "./immersiveViewportMotion";

const event = (phase: ImmersiveLayoutDetail["phase"], transitionId = "current"): ImmersiveLayoutDetail => ({ phase, transitionId, animated: true, direction: "entering", duration: 420 });
describe("browser pane immersive lifecycle fencing", () => {
  it("lets a hidden pane clean up its matching finish while rejecting new work", () => {
    expect(acceptsImmersivePaneLayout(false, "current", event("finish"))).toBe(true);
    for (const phase of ["start", "commit"] as const)
      expect(acceptsImmersivePaneLayout(false, "current", event(phase))).toBe(false);
  });
  it("cannot revive a cleared transition when the pane is shown again", () => {
    for (const visible of [false, true]) for (const phase of ["commit", "finish"] as const)
      expect(acceptsImmersivePaneLayout(visible, undefined, event(phase))).toBe(false);
    expect(acceptsImmersivePaneLayout(true, undefined, event("start", "new"))).toBe(true);
  });
  it("rejects the previous target's late completion after a new motion starts", () => {
    for (const phase of ["commit", "finish"] as const)
      expect(acceptsImmersivePaneLayout(true, "new", event(phase, "old"))).toBe(false);
    expect(acceptsImmersivePaneLayout(true, "new", event("finish", "new"))).toBe(true);
  });
});
