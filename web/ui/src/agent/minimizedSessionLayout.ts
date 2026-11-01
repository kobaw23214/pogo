import type { FloatingRect, ViewportSize } from "./immersiveLayout";

export interface MinimizedViewport extends ViewportSize {
  safeTop?: number; safeRight?: number; safeBottom?: number; safeLeft?: number;
}

export function fitMinimizedSession(saved: Pick<FloatingRect, "x" | "y"> | null, viewport: MinimizedViewport): FloatingRect {
  const vw = Math.max(1, viewport.width), vh = Math.max(1, viewport.height);
  const gap = Math.min(12, vw / 24, vh / 24);
  const left = Math.min(vw / 4, Math.max(gap, viewport.safeLeft ?? 0));
  const right = Math.min(vw / 4, Math.max(gap, viewport.safeRight ?? 0));
  const top = Math.min(vh / 4, Math.max(gap, viewport.safeTop ?? 0));
  const bottom = Math.min(vh / 4, Math.max(gap, viewport.safeBottom ?? 0));
  const width = Math.min(332, vw - left - right);
  const height = Math.min(72, vh - top - bottom);
  return {
    x: Math.min(vw - right - width, Math.max(left, Number.isFinite(saved?.x) ? saved!.x : vw - right - width)),
    y: Math.min(vh - bottom - height, Math.max(top, Number.isFinite(saved?.y) ? saved!.y : vh - bottom - height)),
    width, height,
  };
}
