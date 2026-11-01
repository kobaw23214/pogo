export type LandingMotionLayout = "full" | "compact" | "mobile";

export const landingMotionPreferenceKey = "pogo:landing-motion:v1";

// Only a deliberate opt-out disables the experience; system preferences reduce
// its intensity instead. Unknown/old values retain the default-on behavior.
export function landingMotionPreference(value: string | null): boolean {
  return value !== "off";
}

export function landingMotionLayout(width: number, height: number): LandingMotionLayout {
  if (width <= 900) return "mobile";
  return height >= 700 ? "full" : "compact";
}

export function landingMotionScale(layout: LandingMotionLayout, reduced = false): number {
  const scale = layout === "mobile" ? 0.28 : layout === "compact" ? 0.55 : 1;
  return reduced ? scale * 0.45 : scale;
}

// Distances are relative to the artwork, not the document scroll distance.
// Front planes move farther while the reading plane stays still.
export const heroMotionLayers = [
  { selector: ".plate-archive", depth: "far", y: 30, x: -5 },
  { selector: ".art-iris", depth: "far", y: 38, x: -8 },
  { selector: ".art-leaf", depth: "far", y: 42, x: 8 },
  { selector: ".plate-blueprint", depth: "far", y: 34, x: -6 },
  { selector: ".art-shell", depth: "middle", y: 76, x: 10 },
  { selector: ".art-mountain", depth: "middle", y: 82, x: 9 },
  { selector: ".art-wave", depth: "middle", y: 90, x: -4 },
  { selector: ".plate-vellum", depth: "middle", y: 86, x: -8 },
  { selector: ".art-neuron", depth: "near", y: 142, x: 10 },
  { selector: ".plate-ticket", depth: "near", y: 156, x: -14 },
  { selector: ".plate-tag", depth: "near", y: 164, x: 14 },
] as const;

export function heroLayerTravel(distance: number, top: number, scale: number): number {
  // Allow a small bleed at the top, keeping planes out of the navigation.
  return Math.min(distance * scale, Math.max(16, top + 24));
}
