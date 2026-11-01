export type SceneLayout = "desktop" | "mobile" | "compact";
export function sceneProgress(scroll: number, start: number, distance: number): number {
  if (!Number.isFinite(scroll) || !Number.isFinite(start) || !Number.isFinite(distance) || distance <= 0) return 0;
  return Math.max(0, Math.min(1, (scroll - start) / distance));
}

export function sceneLayout(width: number, height: number): SceneLayout {
  if (height < 560) return "compact";
  return width <= 760 ? "mobile" : "desktop";
}

export function sceneTravel(enabled: boolean, layout: SceneLayout): number {
  if (!enabled || layout === "compact") return 0;
  return layout === "mobile" ? 0.5 : 1;
}

// Travel is a fraction of the viewport, so disassembly remains legible at
// different resolutions. Near planes clear the lens; distant ones linger.
export const cinemaPlanes = [
  { id: "archive", kind: "archive", x: 17, y: 18, w: 25, r: -9, dx: -0.36, dy: -0.3, spin: -18, z: 1 },
  { id: "iris", kind: "image", x: 28, y: 2, w: 27, r: 4, dx: -0.16, dy: -0.48, spin: 14, z: 2 },
  { id: "leaf", kind: "image", x: 57, y: 8, w: 28, r: 9, dx: 0.28, dy: -0.38, spin: 16, z: 3 },
  { id: "shell", kind: "image", x: 74, y: 24, w: 22, r: -6, dx: 0.46, dy: -0.1, spin: 24, z: 4 },
  { id: "blueprint", kind: "blueprint", x: 7, y: 35, w: 27, r: 3, dx: -0.49, dy: -0.02, spin: -16, z: 5 },
  { id: "mountain", kind: "image", x: 57, y: 29, w: 31, r: 5, dx: 0.35, dy: 0.33, spin: 18, z: 6 },
  { id: "wave", kind: "image", x: 37, y: 17, w: 31, r: -4, dx: 0.05, dy: -0.42, spin: -8, z: 7 },
  { id: "vellum", kind: "vellum", x: 29, y: 39, w: 28, r: -5, dx: -0.32, dy: 0.38, spin: -21, z: 8 },
  { id: "neuron", kind: "image", x: 49, y: 35, w: 25, r: 7, dx: 0.32, dy: 0.56, spin: 28, z: 9 },
  { id: "ticket", kind: "ticket", x: 16, y: 72, w: 28, r: -5, dx: -0.52, dy: 0.48, spin: -24, z: 10 },
  { id: "tag", kind: "tag", x: 75, y: 71, w: 19, r: 10, dx: 0.6, dy: 0.24, spin: 30, z: 11 },
] as const;
